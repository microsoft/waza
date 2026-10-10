// Package snapshot implements waza's per-task snapshot/replay artifact.
//
// A snapshot is a versioned record of observations from one task execution:
// prompt sequence, every tool call (name/args/result/timing), engine/model
// config, fixture file hashes, and an env-var allow-list capture. Snapshots
// are written under `--output-dir` when `waza run --snapshot` is passed and
// referenced from `results.json` (additive in outcome schema 1.2).
//
// `waza replay <snapshot.json>` checks stored event sequencing and grader
// score/pass consistency without model calls, or compares two snapshots.
// It does not reconstruct workspace state or re-run graders. Live replay
// is not implemented.
//
// The snapshot wire format is its own MAJOR.MINOR schema independent of the
// results.json schema. Additions are MINOR bumps; renames/removals are MAJOR.
// Readers MUST refuse to load a snapshot whose MAJOR does not match
// CurrentSchemaVersion (issue #368 policy).
package snapshot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
)

// Kind identifies this artifact in tooling that may also handle results.json
// or eval specs. Always "task-snapshot" for snapshots produced by `waza run`.
const Kind = "task-snapshot"

// CurrentSchemaVersion is the MAJOR.MINOR schema version of this package's
// wire format.
//
// 1.0 — initial snapshot format (issue #367).
const CurrentSchemaVersion = "1.1"

// Snapshot is the on-disk record of a single task run that `waza replay` can
// inspect for internal consistency or compare against another snapshot.
//
// Fixture and instruction digests do not preserve source contents. Unknown
// same-major fields are ignored by typed readers.
type Snapshot struct {
	// SchemaVersion is the MAJOR.MINOR version of the snapshot wire format.
	SchemaVersion string `json:"schemaVersion"`

	// Kind is always "task-snapshot" so a single tool inspecting JSON files
	// can route by kind.
	Kind string `json:"kind"`

	// WazaVersion is the version of waza that produced this snapshot.
	// Captured for diagnostics; not used for compatibility decisions
	// (SchemaVersion is the source of truth).
	WazaVersion string `json:"wazaVersion,omitempty"`

	// CreatedAt is the UTC timestamp at which this snapshot was written.
	CreatedAt time.Time `json:"createdAt"`

	// EvalID, EvalName, and Skill mirror the outcome fields so a standalone
	// snapshot can be attributed to its parent eval without re-reading
	// results.json.
	EvalID   string `json:"evalId,omitempty"`
	EvalName string `json:"evalName,omitempty"`
	Skill    string `json:"skill,omitempty"`

	// Task describes the task that was executed.
	Task SnapshotTask `json:"task"`

	// Engine records the engine/model configuration so a live replay can
	// reproduce the same request shape.
	Engine SnapshotEngine `json:"engine"`

	// Prompt holds the inputs the engine was given. FollowUps is the static
	// follow-up list when configured; responder-driven multi-turn is
	// captured via ToolEvents (which include turn boundaries).
	Prompt SnapshotPrompt `json:"prompt"`

	// ToolEvents is the canonical replay tape: the ordered, normalised
	// tool-call record exactly as it appears in
	// EvaluationOutcome.RunResult.ToolEvents (schema 1.1+). It is the
	// input inspected by replay model-replay mode for sequence consistency.
	ToolEvents []models.ToolEvent `json:"toolEvents,omitempty"`

	// Fixtures records sha256 digests of source fixture/resource files.
	// These hashes do not preserve their contents or final workspace state.
	Fixtures []FixtureDigest `json:"fixtures,omitempty"`

	// Env captures the env-var allow-list and (for auditing) the names of
	// vars present in os.Environ() that were denied capture.
	Env SnapshotEnv `json:"env"`

	// Redaction documents what was scrubbed before serialization.
	Redaction SnapshotRedaction `json:"redaction"`

	// Result holds the outcome of the captured run (status / final output /
	// grader results) so model-replay can inspect stored score/pass consistency.
	Result SnapshotResult `json:"result"`

	CommandInvocations []models.CommandInvocation `json:"commandInvocations,omitempty"`
	Checkpoints        []models.CheckpointOutcome `json:"checkpoints,omitempty"`
	Evidence           *models.EvidenceManifest   `json:"evidence,omitempty"`
	WorkspaceFiles     []WorkspaceFile            `json:"workspaceFiles,omitempty"`
	rawWorkspaceFiles  []json.RawMessage
}

// SnapshotTask carries identifying task metadata.
type SnapshotTask struct {
	TestID      string   `json:"testId"`
	DisplayName string   `json:"displayName,omitempty"`
	Group       string   `json:"group,omitempty"`
	Golden      bool     `json:"golden,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	RunNumber   int      `json:"runNumber"`
}

// SnapshotEngine records engine/model configuration. Fields that the engine
// does not surface (e.g., seed for the Copilot SDK) are simply left empty —
// the issue calls them out as best-effort metadata.
type SnapshotEngine struct {
	Type        string   `json:"type"`
	ModelID     string   `json:"modelId"`
	JudgeModel  string   `json:"judgeModel,omitempty"`
	TimeoutSec  int      `json:"timeoutSec,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`
}

// SnapshotPrompt is the input side of the run.
type SnapshotPrompt struct {
	Message      string             `json:"message"`
	FollowUps    []string           `json:"followUps,omitempty"`
	Context      map[string]any     `json:"context,omitempty"`
	Instructions []InstructionEntry `json:"instructions,omitempty"`
}

// InstructionEntry records an instruction file applied to the agent.
// The body itself is NOT captured (instructions can be large and they are
// already hashed via Fixtures when they live in the workspace); the path
// and digest are sufficient for replay drift detection.
type InstructionEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

// FixtureDigest is the sha256 of a single fixture file at snapshot time.
type FixtureDigest struct {
	Path   string `json:"path"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256"`
}

// SnapshotEnv documents env-var capture under the default-deny / allow-list
// policy described in the issue.
type SnapshotEnv struct {
	// AllowList is the configured allow-list applied at capture time.
	// Empty means default-deny captured nothing.
	AllowList []string `json:"allowList,omitempty"`

	// Captured holds the actually-captured KEY=VALUE pairs after the
	// allow-list and redaction were applied. Values may be redacted
	// placeholders if a Redaction rule matched.
	Captured map[string]string `json:"captured,omitempty"`

	// DeniedKeys lists the names (NOT values) of env vars present in the
	// process environment that were not in AllowList. Useful for auditing
	// what would have been redacted under a stricter policy. Empty when
	// the process environment is small (<= 0 entries) or the writer chose
	// to omit the audit list.
	DeniedKeys []string `json:"deniedKeys,omitempty"`
}

// SnapshotRedaction documents what was scrubbed before serialization.
type SnapshotRedaction struct {
	// Policy is "default", "default+custom", or "custom" depending on
	// whether the shipped defaults were applied, augmented, or replaced.
	Policy string `json:"policy"`

	// AppliedRules lists the rule names that matched at least once during
	// this capture, sorted for stable diffing.
	AppliedRules []string `json:"appliedRules,omitempty"`

	// RedactionCount is the total number of replacements made across the
	// captured payload.
	RedactionCount int `json:"redactionCount,omitempty"`
}

// SnapshotResult mirrors the relevant subset of models.RunResult so a
// snapshot can be replayed without also loading results.json.
type SnapshotResult struct {
	Status      models.Status                   `json:"status"`
	FinalOutput string                          `json:"finalOutput,omitempty"`
	ErrorMsg    string                          `json:"errorMsg,omitempty"`
	DurationMs  int64                           `json:"durationMs,omitempty"`
	Validations map[string]models.GraderResults `json:"validations,omitempty"`
}

// MarshalJSON ensures SchemaVersion and Kind are always populated.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	type alias Snapshot
	if s.SchemaVersion == "" {
		s.SchemaVersion = CurrentSchemaVersion
	}
	if s.Kind == "" {
		s.Kind = Kind
	}
	return json.Marshal(alias(s))
}

func (s *Snapshot) UnmarshalJSON(data []byte) error {
	if err := models.ValidateNativeJSONKeys(data, Snapshot{}); err != nil {
		return err
	}
	type alias Snapshot
	var decoded alias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoded.Evidence != nil {
		if err := models.ValidateNativeEvidenceProfile(decoded.Evidence); err != nil {
			return err
		}
	}
	*s = Snapshot(decoded)
	return nil
}

// ParseSnapshot decodes a snapshot from bytes and validates its schema
// version. The source argument is included in error messages for diagnostics.
func ParseSnapshot(data []byte, source string) (*Snapshot, error) {
	var header struct {
		SchemaVersion  string            `json:"schemaVersion"`
		Kind           string            `json:"kind"`
		Evidence       json.RawMessage   `json:"evidence"`
		WorkspaceFiles []json.RawMessage `json:"workspaceFiles"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", source, err)
	}
	if len(header.Evidence) > 0 && string(header.Evidence) != "null" {
		if _, err := jsonutil.Parse(data); err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", source, err)
		}
	}
	if header.Kind != "" && header.Kind != Kind {
		return nil, fmt.Errorf("snapshot %s: kind %q is not %q", source, header.Kind, Kind)
	}

	version := header.SchemaVersion
	if strings.TrimSpace(version) == "" {
		version = CurrentSchemaVersion
	}
	major, _, err := parseVersion(version)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: invalid schemaVersion %q: %w", source, version, err)
	}
	curMajor, _, _ := parseVersion(CurrentSchemaVersion)
	if major != curMajor {
		return nil, fmt.Errorf("snapshot %s: schemaVersion %q (major %d) is not compatible with waza's snapshot schema major %d (current %s)",
			source, version, major, curMajor, CurrentSchemaVersion)
	}

	var snap Snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&snap); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", source, err)
	}
	if snap.SchemaVersion == "" {
		snap.SchemaVersion = version
	}
	if snap.Kind == "" {
		snap.Kind = Kind
	}
	snap.rawWorkspaceFiles = header.WorkspaceFiles
	return &snap, nil
}

func parseVersion(v string) (major, minor int, err error) {
	parts := strings.Split(v, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, fmt.Errorf("expected MAJOR.MINOR")
	}
	major, err = strconv.Atoi(parts[0])
	if err != nil || major < 0 {
		return 0, 0, fmt.Errorf("major must be a non-negative integer")
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil || minor < 0 {
		return 0, 0, fmt.Errorf("minor must be a non-negative integer")
	}
	return major, minor, nil
}
