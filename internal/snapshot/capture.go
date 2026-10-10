package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"time"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

// CaptureInput is everything Capture needs to build a Snapshot. It is
// assembled by the runner inside executeRun once the RunResult is finalized.
type CaptureInput struct {
	WazaVersion string
	EvalID      string
	EvalName    string
	Skill       string

	Task    *models.TestCase
	Request *execution.ExecutionRequest
	Run     *models.RunResult

	Engine       SnapshotEngine
	FixturesRoot string
	// SkipDirs is an optional list of absolute directories under
	// FixturesRoot whose contents will be omitted from the captured
	// fixture digests. The runner uses this to exclude the configured
	// snapshot output directory, which prevents previously-emitted
	// snapshots from perturbing the fixture hash on re-runs.
	SkipDirs           []string
	EnvAllowList       []string
	Policy             *Policy
	Spec               *models.EvalSpec
	ExecutionMode      string
	DiagnosticCategory string
	WorkspacePaths     []string
	EvaluatorOnly      []string
	WorkspaceLimits    WorkspaceLimits
}

// Capture builds a Snapshot from input. The returned snapshot's redaction
// counters reflect everything scrubbed during this call. Policy configuration
// is copied so concurrent captures do not share mutable counters.
//
// Capture does NOT write to disk; see Writer.Write for that.
func Capture(in CaptureInput) (*Snapshot, error) {
	if in.Task == nil {
		return nil, fmt.Errorf("snapshot: capture requires Task")
	}
	if in.Run == nil {
		return nil, fmt.Errorf("snapshot: capture requires Run")
	}
	policy, err := in.Policy.forCapture()
	if err != nil {
		return nil, err
	}
	for _, identity := range []struct{ value, field string }{
		{in.Task.TestID, "task ID"},
		{in.EvalID, "eval ID"},
		{in.Engine.Type, "engine type"},
		{in.Engine.ModelID, "model ID"},
	} {
		if err := policy.checkIdentity(identity.value, identity.field); err != nil {
			return nil, err
		}
	}

	snap := &Snapshot{
		SchemaVersion: CurrentSchemaVersion,
		Kind:          Kind,
		WazaVersion:   policy.RedactString(in.WazaVersion),
		CreatedAt:     time.Now().UTC(),
		EvalID:        in.EvalID,
		EvalName:      policy.RedactString(in.EvalName),
		Skill:         policy.RedactString(in.Skill),
		Task: SnapshotTask{
			TestID:      in.Task.TestID,
			DisplayName: policy.RedactString(in.Task.DisplayName),
			Golden:      in.Task.Golden,
			Tags:        policy.RedactStringSlice(in.Task.Tags),
			RunNumber:   in.Run.RunNumber,
		},
		Engine: in.Engine,
	}
	snap.Engine.JudgeModel = policy.RedactString(snap.Engine.JudgeModel)

	// Prompt: copy with redaction. Context values may contain user-supplied
	// strings (e.g., interpolated tokens), so route through RedactAny.
	if in.Request != nil {
		snap.Prompt.Message = policy.RedactString(in.Request.Message)
		if len(in.Request.Context) > 0 {
			redactedCtx, err := policy.redactJSON(in.Request.Context)
			if err != nil {
				return nil, fmt.Errorf("snapshot: prompt context: %w", err)
			}
			if m, ok := redactedCtx.(map[string]any); ok {
				snap.Prompt.Context = m
			}
		}
		if len(in.Request.Instructions) > 0 {
			snap.Prompt.Instructions = make([]InstructionEntry, len(in.Request.Instructions))
			for i, instr := range in.Request.Instructions {
				path := filepath.ToSlash(instr.Path)
				if err := policy.checkIdentity(path, "instruction path"); err != nil {
					return nil, err
				}
				snap.Prompt.Instructions[i] = InstructionEntry{
					Path:   path,
					SHA256: shaBytes(instr.Content),
				}
			}
		}
	}
	if len(in.Task.Stimulus.FollowUps) > 0 {
		// Follow-ups are stored on the task's stimulus before execution;
		// capture the static list as configured. Live multi-turn responder
		// traffic is recoverable from ToolEvents.Turn fields below.
		snap.Prompt.FollowUps = policy.RedactStringSlice(in.Task.Stimulus.FollowUps)
	}

	// Tool events: redact strings inside Args/Result/Error map structures.
	if len(in.Run.ToolEvents) > 0 {
		snap.ToolEvents = make([]models.ToolEvent, len(in.Run.ToolEvents))
		for i, ev := range in.Run.ToolEvents {
			redacted, err := redactToolEvent(ev, policy)
			if err != nil {
				return nil, fmt.Errorf("snapshot: tool event %d: %w", i+1, err)
			}
			snap.ToolEvents[i] = redacted
		}
	}

	// Fixtures
	if in.FixturesRoot != "" {
		digests, err := HashFixturesExcluding(in.FixturesRoot, in.SkipDirs)
		if err != nil {
			return nil, fmt.Errorf("snapshot: hash fixtures: %s", policy.RedactString(err.Error()))
		}
		snap.Fixtures = digests
		for i := range snap.Fixtures {
			if err := policy.checkIdentity(snap.Fixtures[i].Path, "fixture path"); err != nil {
				return nil, err
			}
		}
	}

	// Env capture: default-deny allow-list with redaction. Always populate
	// AllowList so consumers know whether capture was intentionally empty
	// vs. configured-with-no-matches.
	snap.Env = CaptureEnv(in.EnvAllowList, policy)
	for _, key := range append(append([]string(nil), snap.Env.AllowList...), snap.Env.DeniedKeys...) {
		if err := policy.checkIdentity(key, "environment variable name"); err != nil {
			return nil, err
		}
	}
	for key := range snap.Env.Captured {
		if err := policy.checkIdentity(key, "environment variable name"); err != nil {
			return nil, err
		}
		if policy.IsSensitiveKey(key) {
			policy.recordSensitiveMatch()
		}
	}

	// Redaction summary — always present so users can confirm rules ran.
	snap.Redaction = SnapshotRedaction{
		Policy:         policy.Label(),
		AppliedRules:   policy.MatchedRules(),
		RedactionCount: policy.MatchCount(),
	}

	// Result subset
	snap.Result = SnapshotResult{
		Status:      in.Run.Status,
		FinalOutput: policy.RedactString(in.Run.FinalOutput),
		ErrorMsg:    policy.RedactString(in.Run.ErrorMsg),
		DurationMs:  in.Run.DurationMs,
	}
	if in.Run.Validations != nil {
		snap.Result.Validations = make(map[string]models.GraderResults, len(in.Run.Validations))
		for key, result := range in.Run.Validations {
			if err := policy.checkIdentity(key, "grader reference"); err != nil {
				return nil, err
			}
			if err := policy.checkIdentity(result.Name, "grader identifier"); err != nil {
				return nil, err
			}
			if err := policy.checkIdentity(string(result.Type), "grader type"); err != nil {
				return nil, err
			}
			result.Feedback = policy.RedactString(result.Feedback)
			if result.Details != nil {
				details, err := policy.redactJSON(result.Details)
				if err != nil {
					return nil, fmt.Errorf("snapshot: grader details: %w", err)
				}
				normalized, ok := details.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("snapshot: grader details must be a JSON object")
				}
				result.Details = normalized
			}
			snap.Result.Validations[key] = result
		}
	}
	snap.CommandInvocations, err = redactTyped(in.Run.CommandInvocations, policy)
	if err != nil {
		return nil, fmt.Errorf("snapshot: command invocations: %w", err)
	}
	snap.Checkpoints, err = redactTyped(in.Run.Checkpoints, policy)
	if err != nil {
		return nil, fmt.Errorf("snapshot: checkpoints: %w", err)
	}
	var workspaceErr error
	var workspaceRedaction SnapshotRedaction
	if len(in.WorkspacePaths) > 0 {
		snap.WorkspaceFiles, workspaceRedaction, workspaceErr = CaptureWorkspaceWithAccounting(in.Run.WorkspaceDir, in.WorkspacePaths, in.EvaluatorOnly, policy, in.WorkspaceLimits)
	}

	// Final-pass: recompute the redaction summary because RedactString
	// calls in the Result/Prompt sections may have added more matches
	// after the Env phase.
	snap.Redaction.AppliedRules = append(policy.MatchedRules(), workspaceRedaction.AppliedRules...)
	slices.Sort(snap.Redaction.AppliedRules)
	snap.Redaction.AppliedRules = slices.Compact(snap.Redaction.AppliedRules)
	snap.Redaction.RedactionCount = policy.MatchCount() + workspaceRedaction.RedactionCount
	snap.Evidence, err = buildEvidence(in, snap)
	if err != nil {
		return nil, err
	}
	if len(in.WorkspacePaths) > 0 {
		for i := range snap.Evidence.Artifacts {
			if snap.Evidence.Artifacts[i].ID == "workspace" {
				snap.Evidence.Artifacts[i].Availability = "unavailable"
				snap.Evidence.Artifacts[i].Completeness = "partial"
				snap.Evidence.Artifacts[i].Reason = "Only explicitly selected files were requested; a complete workspace is not preserved."
			}
		}
		for i, file := range snap.WorkspaceFiles {
			digest, err := evidence.JSONDigest(file)
			if err != nil {
				return nil, err
			}
			snap.Evidence.Artifacts = append(snap.Evidence.Artifacts, models.EvidenceArtifact{
				ID: "workspace-file/" + file.Path, Kind: "workspace_file", Availability: "captured",
				Completeness: "complete", Document: "snapshot", Pointer: fmt.Sprintf("/workspaceFiles/%d", i),
				ContentDigest: digest, Redacted: file.Redacted,
			})
		}
		captured := make(map[string]bool, len(snap.WorkspaceFiles))
		for _, file := range snap.WorkspaceFiles {
			captured[file.Path] = true
		}
		for i, name := range in.WorkspacePaths {
			if !captured[name] {
				snap.Evidence.Artifacts = append(snap.Evidence.Artifacts, models.EvidenceArtifact{
					ID: fmt.Sprintf("workspace-request/%d", i+1), Kind: "workspace_file",
					Availability: "unavailable", Completeness: "unknown",
					Reason: "This requested file was not preserved; review the capture diagnostic and explicit allowlist.",
				})
			}
		}
		if workspaceErr != nil {
			snap.Evidence.Diagnostics = append(snap.Evidence.Diagnostics, models.EvidenceDiagnostic{
				Category: "capture", Code: "workspace_partial", Message: workspaceErr.Error(),
			})
		}
		if err := evidence.Seal(snap.Evidence); err != nil {
			return nil, err
		}
	}
	return snap, nil
}

func buildEvidence(in CaptureInput, snap *Snapshot) (*models.EvidenceManifest, error) {
	mode := in.ExecutionMode
	if mode == "" {
		mode = "unknown"
	}
	policyMode := "unknown"
	if in.Request != nil && in.Request.ToolPolicy != nil {
		policyMode = string(in.Request.ToolPolicy.Mode)
	}
	manifest := &models.EvidenceManifest{
		Origin: models.EvidenceOrigin{
			EvalID: in.EvalID, TaskID: in.Task.TestID, RunNumber: in.Run.RunNumber,
			AttemptCount: in.Run.Attempts, PriorAttempts: "not_preserved",
		},
		Runtime: models.EvidenceRuntime{
			WazaVersion: snap.WazaVersion, RequestedEngine: snap.Engine.Type,
			RequestedModel: snap.Engine.ModelID, ExecutionMode: mode, DependencyMode: "unknown",
			RequestedPolicy: policyMode, VerifiedEnforcement: "unknown",
			GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
			SDKVersion: sdkVersion(), NativeSkillControl: "unknown",
		},
		Redaction: models.EvidenceRedaction{
			Policy: snap.Redaction.Policy, AppliedRules: snap.Redaction.AppliedRules,
			MatchCount:  snap.Redaction.RedactionCount,
			Limitations: []string{"Pattern redaction is not a confidentiality proof.", "Source digests do not preserve contents."},
		},
	}
	if in.Request != nil {
		noSkills := in.Request.NoSkills
		manifest.Runtime.NoSkills = &noSkills
		switch mode {
		case "live":
			manifest.Runtime.NativeSkillControl = "sdk_default"
			if noSkills {
				manifest.Runtime.NativeSkillControl = "requested_sdk_disable"
			}
		case "mock":
			manifest.Runtime.NativeSkillControl = "mock_not_applicable"
		}
	}
	switch in.Run.Attempts {
	case 1:
		manifest.Origin.PriorAttempts = "none"
	case 0:
		manifest.Origin.PriorAttempts = "unknown"
	}
	completeness, reason := "unknown", "Collector completeness is not certified."
	if in.Run.Status == models.StatusError {
		completeness, reason = "partial", "The run recorded an operational error."
		category := in.DiagnosticCategory
		if category == "" {
			category = "unknown"
		}
		manifest.Diagnostics = append(manifest.Diagnostics, models.EvidenceDiagnostic{
			Category: category, Code: "run_error", Message: "The run recorded an operational error; no agent cause is inferred.",
		})
	}
	for _, item := range []struct {
		id, kind, pointer string
		value             any
	}{
		{"prompt", "prompt", "/prompt", snap.Prompt},
		{"tool-events", "tool_events", "/toolEvents", snap.ToolEvents},
		{"command-invocations", "command_invocations", "/commandInvocations", snap.CommandInvocations},
		{"validations", "grader_results", "/result/validations", snap.Result.Validations},
		{"checkpoints", "checkpoint_results", "/checkpoints", snap.Checkpoints},
		{"result", "run_result", "/result", snap.Result},
		{"environment", "environment_capture", "/env", snap.Env},
		{"engine-config", "engine_configuration", "/engine", snap.Engine},
	} {
		data, err := json.Marshal(item.value)
		if err != nil {
			return nil, fmt.Errorf("snapshot: evidence content cannot be encoded")
		}
		if string(data) == "null" {
			manifest.Artifacts = append(manifest.Artifacts, models.EvidenceArtifact{
				ID: item.id, Kind: item.kind, Availability: "unavailable", Completeness: "unknown",
				Reason: "No content was recorded for this artifact.",
			})
			continue
		}
		digest, err := evidence.JSONDigest(item.value)
		if err != nil {
			return nil, err
		}
		manifest.Artifacts = append(manifest.Artifacts, models.EvidenceArtifact{
			ID: item.id, Kind: item.kind, Availability: "captured", Completeness: completeness,
			Document: "snapshot", Pointer: item.pointer, ContentDigest: digest, Reason: reason,
			Redacted: snap.Redaction.RedactionCount > 0,
		})
	}
	for _, item := range []struct {
		id, kind string
		value    any
	}{
		{"task-config", "task_configuration", in.Task},
		{"grader-config", "grader_configuration", in.Spec},
		{"fixtures", "fixture_inventory", snap.Fixtures},
		{"instructions", "instruction_inventory", snap.Prompt.Instructions},
	} {
		data, err := json.Marshal(item.value)
		if err != nil {
			return nil, fmt.Errorf("snapshot: evidence source cannot be encoded")
		}
		if string(data) == "null" {
			manifest.Artifacts = append(manifest.Artifacts, models.EvidenceArtifact{
				ID: item.id, Kind: item.kind, Availability: "unavailable", Completeness: "unknown",
				Reason: "Source identity is unavailable.",
			})
			continue
		}
		digest, err := evidence.JSONDigest(item.value)
		if err != nil {
			return nil, err
		}
		manifest.Artifacts = append(manifest.Artifacts, models.EvidenceArtifact{
			ID: item.id, Kind: item.kind, Availability: "digest_only", Completeness: "unknown",
			SourceDigest: digest, Reason: "Source/configuration inventory identity only; file contents are not preserved.",
		})
	}
	manifest.Artifacts = append(manifest.Artifacts,
		models.EvidenceArtifact{ID: "workspace", Kind: "workspace_state", Availability: "not_requested", Completeness: "unknown", Reason: "Workspace contents are not preserved by ordinary snapshots."},
		models.EvidenceArtifact{ID: "external-state", Kind: "external_state", Availability: "unavailable", Completeness: "unknown", Reason: "No authoritative external-state verification is recorded."},
		models.EvidenceArtifact{ID: "grader-implementation", Kind: "implementation_version", Availability: "unavailable", Completeness: "unknown", Reason: "Per-grader implementation and executable dependency versions are not certified."},
		models.EvidenceArtifact{ID: "rubric-version", Kind: "rubric_version", Availability: "unavailable", Completeness: "unknown", Reason: "Resolved external rubric/version provenance is not certified."},
		models.EvidenceArtifact{ID: "lockfile", Kind: "lockfile", Availability: "unavailable", Completeness: "unknown", Reason: "An actual lockfile was not preserved or fingerprinted by this capture."},
	)
	if err := evidence.Seal(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func sdkVersion() *string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	for _, dep := range info.Deps {
		if dep.Path != "github.com/github/copilot-sdk/go" {
			continue
		}
		if dep.Replace != nil {
			dep = dep.Replace
		}
		if dep.Version != "" && dep.Version != "(devel)" {
			version := dep.Version
			return &version
		}
	}
	return nil
}

// redactToolEvent rejects unsafe correlation identifiers rather than changing
// their meaning, and normalizes arbitrary JSON payloads before redaction.
func redactToolEvent(ev models.ToolEvent, policy *Policy) (models.ToolEvent, error) {
	out := ev
	for _, identity := range []struct{ value, field string }{
		{ev.ToolCallID, "tool call ID"},
		{ev.ToolName, "tool name"},
	} {
		if err := policy.checkIdentity(identity.value, identity.field); err != nil {
			return models.ToolEvent{}, err
		}
	}
	var err error
	out.Args, err = policy.redactJSON(ev.Args)
	if err != nil {
		return models.ToolEvent{}, fmt.Errorf("arguments: %w", err)
	}
	out.Result, err = policy.redactJSON(ev.Result)
	if err != nil {
		return models.ToolEvent{}, fmt.Errorf("result: %w", err)
	}
	out.Error = policy.RedactString(ev.Error)
	return out, nil
}

func shaBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Writer writes captured snapshots to <root>/<test_id>-run<N>.json. Writer
// is concurrency-safe: callers may invoke Write from multiple goroutines.
type Writer struct {
	root string
}

// NewWriter constructs a Writer rooted at dir. Write creates dir on demand,
// so the caller does not need to pre-create it.
func NewWriter(dir string) *Writer {
	return &Writer{root: dir}
}

// Root returns the directory the writer writes to.
func (w *Writer) Root() string {
	if w == nil {
		return ""
	}
	return w.root
}

// Write serializes snap to <root>/<test_id>-run<N>.json and returns the
// path written. The path is suitable for embedding in a results.json
// `runs[].snapshot_path` field.
func (w *Writer) Write(snap *Snapshot) (string, error) {
	if w == nil {
		return "", fmt.Errorf("snapshot: nil writer")
	}
	if snap == nil {
		return "", fmt.Errorf("snapshot: nil snapshot")
	}
	if len(snap.WorkspaceFiles) > 0 {
		data, err := json.MarshalIndent(snap, "", "  ")
		if err != nil {
			return "", fmt.Errorf("snapshot: workspace evidence cannot be encoded")
		}
		return writePrivateSnapshot(w.root, data)
	}
	if err := os.MkdirAll(w.root, 0o755); err != nil {
		return "", fmt.Errorf("snapshot: create dir %s: %w", w.root, err)
	}
	name := snapshotFilename(snap.Task.TestID, snap.Task.RunNumber)
	path := filepath.Join(w.root, name)

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", fmt.Errorf("snapshot: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("snapshot: write %s: %w", path, err)
	}
	return path, nil
}

// snapshotFilename produces a stable filename for the snapshot of a single
// task-run. Test IDs are sanitized to a filesystem-safe slug.
func snapshotFilename(testID string, run int) string {
	slug := sanitizeSlug(testID)
	if slug == "" {
		slug = "task"
	}
	return fmt.Sprintf("%s-run%d.json", slug, run)
}

func sanitizeSlug(s string) string {
	if s == "" {
		return ""
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
