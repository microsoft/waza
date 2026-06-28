package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	SkipDirs     []string
	EnvAllowList []string
	Policy       *Policy
}

// Capture builds a Snapshot from input. The returned snapshot's redaction
// counters reflect everything scrubbed during this call; the caller may
// inspect input.Policy.MatchCount() / MatchedRules() after.
//
// Capture does NOT write to disk; see Writer.Write for that.
func Capture(in CaptureInput) (*Snapshot, error) {
	if in.Task == nil {
		return nil, fmt.Errorf("snapshot: capture requires Task")
	}
	if in.Run == nil {
		return nil, fmt.Errorf("snapshot: capture requires Run")
	}
	policy := in.Policy
	if policy == nil {
		policy = DefaultPolicy()
	}
	policy.ResetCounters()

	// Reset matched-rule statistics so this snapshot reports only its own
	// matches, not the running totals across previous captures.

	snap := &Snapshot{
		SchemaVersion: CurrentSchemaVersion,
		Kind:          Kind,
		WazaVersion:   in.WazaVersion,
		CreatedAt:     time.Now().UTC(),
		EvalID:        in.EvalID,
		EvalName:      in.EvalName,
		Skill:         in.Skill,
		Task: SnapshotTask{
			TestID:      in.Task.TestID,
			DisplayName: in.Task.DisplayName,
			Golden:      in.Task.Golden,
			Tags:        append([]string(nil), in.Task.Tags...),
			RunNumber:   in.Run.RunNumber,
		},
		Engine: in.Engine,
	}

	// Prompt: copy with redaction. Context values may contain user-supplied
	// strings (e.g., interpolated tokens), so route through RedactAny.
	if in.Request != nil {
		snap.Prompt.Message = policy.RedactString(in.Request.Message)
		if len(in.Request.Context) > 0 {
			redactedCtx := policy.RedactAny(in.Request.Context)
			if m, ok := redactedCtx.(map[string]any); ok {
				snap.Prompt.Context = m
			}
		}
		if len(in.Request.Instructions) > 0 {
			snap.Prompt.Instructions = make([]InstructionEntry, len(in.Request.Instructions))
			for i, instr := range in.Request.Instructions {
				snap.Prompt.Instructions[i] = InstructionEntry{
					Path:   filepath.ToSlash(instr.Path),
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
			snap.ToolEvents[i] = redactToolEvent(ev, policy)
		}
	}

	// Fixtures
	if in.FixturesRoot != "" {
		digests, err := HashFixturesExcluding(in.FixturesRoot, in.SkipDirs)
		if err != nil {
			return nil, fmt.Errorf("snapshot: hash fixtures: %w", err)
		}
		snap.Fixtures = digests
	}

	// Env capture: default-deny allow-list with redaction. Always populate
	// AllowList so consumers know whether capture was intentionally empty
	// vs. configured-with-no-matches.
	snap.Env = CaptureEnv(in.EnvAllowList, policy)

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
		Validations: in.Run.Validations,
	}

	// Final-pass: recompute the redaction summary because RedactString
	// calls in the Result/Prompt sections may have added more matches
	// after the Env phase.
	snap.Redaction.AppliedRules = policy.MatchedRules()
	snap.Redaction.RedactionCount = policy.MatchCount()
	return snap, nil
}

// redactToolEvent returns a copy of ev with every captured string value
// passed through the policy. The tool_call_id, tool_name, turn, sequence,
// success, and duration_ms fields are preserved verbatim because they are
// required keys for replay correlation; only the user-controlled args /
// result payload and error message are scrubbed.
func redactToolEvent(ev models.ToolEvent, policy *Policy) models.ToolEvent {
	out := ev
	out.Args = policy.RedactAny(ev.Args)
	out.Result = policy.RedactAny(ev.Result)
	out.Error = policy.RedactString(ev.Error)
	return out
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
