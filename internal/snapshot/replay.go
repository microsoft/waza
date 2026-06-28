package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
)

// Mode controls how `waza replay` re-runs a snapshot.
type Mode string

const (
	// ModeModelReplay is the default mode: replay the captured tool_events
	// without any LLM/engine call. Graders that consume tool_events
	// (tool_calls, tool_constraint, etc.) verify byte-for-byte against the
	// snapshot, and the snapshot's grader results are re-checked against
	// the saved status. No tokens are spent. Diverges only if the snapshot
	// is internally inconsistent.
	ModeModelReplay Mode = "model-replay"

	// ModeLive re-runs the task against the real engine using the same
	// prompt + fixtures, then compares the resulting tool_events to the
	// snapshot's. Live mode is intentionally fuzzy: non-deterministic
	// fields (durations, raw model output text) are ignored; only the
	// ordered tool name + args fingerprint is compared.
	ModeLive Mode = "live"
)

// DivergenceKind classifies a single discrepancy reported by Compare.
type DivergenceKind string

const (
	DivLengthMismatch DivergenceKind = "length_mismatch"
	DivToolName       DivergenceKind = "tool_name"
	DivToolArgs       DivergenceKind = "tool_args"
	DivToolSuccess    DivergenceKind = "tool_success"
	DivToolResult     DivergenceKind = "tool_result"
	DivFinalStatus    DivergenceKind = "final_status"
	DivValidation     DivergenceKind = "validation"
)

// Divergence is a single discrepancy between two snapshots (or between a
// snapshot and a freshly-captured run in live mode). FirstDivergentIndex is
// the 0-based position in the tool_events slice; for non-tool-event
// divergences it is -1.
type Divergence struct {
	Kind                DivergenceKind `json:"kind"`
	FirstDivergentIndex int            `json:"first_divergent_index"`
	BaselineValue       string         `json:"baseline_value,omitempty"`
	ReplayedValue       string         `json:"replayed_value,omitempty"`
	Description         string         `json:"description"`
}

// CompareResult is the full report from Compare.
type CompareResult struct {
	Match        bool         `json:"match"`
	Divergences  []Divergence `json:"divergences"`
	BaselinePath string       `json:"baseline,omitempty"`
	ReplayedPath string       `json:"replayed,omitempty"`
}

// LoadSnapshotFile reads and parses a snapshot from disk.
func LoadSnapshotFile(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read snapshot %s: %w", path, err)
	}
	return ParseSnapshot(data, path)
}

// Compare returns the divergence report between baseline and replay. When
// strictResult is true, baseline.Result.Status and validations are also
// compared; live mode generally wants false because the new run produces
// its own grader results that should not be expected to match byte-for-byte.
//
// The comparison walks tool events by index (0..min(len(a),len(b))). Args
// and Result are compared via deep-equal of the canonical JSON form so
// `map[string]any{"a":1}` matches `map[string]any{"a":1.0}` after a JSON
// round-trip (no false positives on numeric kinding).
func Compare(baseline, replayed *Snapshot, strictResult bool) CompareResult {
	res := CompareResult{Match: true}
	if baseline == nil || replayed == nil {
		res.Match = false
		res.Divergences = append(res.Divergences, Divergence{
			Kind:                DivLengthMismatch,
			FirstDivergentIndex: -1,
			Description:         "one snapshot is nil",
		})
		return res
	}

	a, b := baseline.ToolEvents, replayed.ToolEvents
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i].ToolName != b[i].ToolName {
			res.Match = false
			res.Divergences = append(res.Divergences, Divergence{
				Kind:                DivToolName,
				FirstDivergentIndex: i,
				BaselineValue:       a[i].ToolName,
				ReplayedValue:       b[i].ToolName,
				Description:         fmt.Sprintf("tool name diverges at index %d", i),
			})
			return res
		}
		if !canonicalEqual(a[i].Args, b[i].Args) {
			res.Match = false
			res.Divergences = append(res.Divergences, Divergence{
				Kind:                DivToolArgs,
				FirstDivergentIndex: i,
				BaselineValue:       canonicalJSON(a[i].Args),
				ReplayedValue:       canonicalJSON(b[i].Args),
				Description:         fmt.Sprintf("tool args diverge at index %d (tool=%s)", i, a[i].ToolName),
			})
			return res
		}
		if strictResult {
			if a[i].Success != b[i].Success {
				res.Match = false
				res.Divergences = append(res.Divergences, Divergence{
					Kind:                DivToolSuccess,
					FirstDivergentIndex: i,
					BaselineValue:       fmt.Sprintf("%t", a[i].Success),
					ReplayedValue:       fmt.Sprintf("%t", b[i].Success),
					Description:         fmt.Sprintf("tool success diverges at index %d", i),
				})
				return res
			}
			if !canonicalEqual(a[i].Result, b[i].Result) {
				res.Match = false
				res.Divergences = append(res.Divergences, Divergence{
					Kind:                DivToolResult,
					FirstDivergentIndex: i,
					BaselineValue:       canonicalJSON(a[i].Result),
					ReplayedValue:       canonicalJSON(b[i].Result),
					Description:         fmt.Sprintf("tool result diverges at index %d", i),
				})
				return res
			}
		}
	}
	if len(a) != len(b) {
		res.Match = false
		res.Divergences = append(res.Divergences, Divergence{
			Kind:                DivLengthMismatch,
			FirstDivergentIndex: n,
			BaselineValue:       fmt.Sprintf("%d events", len(a)),
			ReplayedValue:       fmt.Sprintf("%d events", len(b)),
			Description:         "snapshots have different tool_event counts",
		})
		return res
	}

	if strictResult {
		if baseline.Result.Status != replayed.Result.Status {
			res.Match = false
			res.Divergences = append(res.Divergences, Divergence{
				Kind:                DivFinalStatus,
				FirstDivergentIndex: -1,
				BaselineValue:       string(baseline.Result.Status),
				ReplayedValue:       string(replayed.Result.Status),
				Description:         "final run status diverges",
			})
		}
		for name, ag := range baseline.Result.Validations {
			bg, ok := replayed.Result.Validations[name]
			if !ok {
				res.Match = false
				res.Divergences = append(res.Divergences, Divergence{
					Kind:                DivValidation,
					FirstDivergentIndex: -1,
					BaselineValue:       fmt.Sprintf("passed=%t", ag.Passed),
					ReplayedValue:       "missing",
					Description:         fmt.Sprintf("grader %q missing in replay", name),
				})
				continue
			}
			if ag.Passed != bg.Passed {
				res.Match = false
				res.Divergences = append(res.Divergences, Divergence{
					Kind:                DivValidation,
					FirstDivergentIndex: -1,
					BaselineValue:       fmt.Sprintf("passed=%t", ag.Passed),
					ReplayedValue:       fmt.Sprintf("passed=%t", bg.Passed),
					Description:         fmt.Sprintf("grader %q outcome diverges", name),
				})
			}
		}
	}
	return res
}

// Bisect identifies the first divergent turn between baseline and failing.
// Returns the 1-based turn number of the first event that diverges, or 0
// when the snapshots match. The same Divergence slice that Compare emits
// is returned so callers can render details.
//
// Bisect prefers tool_events.Turn when populated (engines that track turn
// boundaries); when Turn is 0 it falls back to the event sequence index.
//
// Bisect runs Compare in non-strict mode: it ignores tool result payloads
// and compares only the ordered (name, args) sequence so the reported
// "first divergent turn" reflects where the agent took a different action,
// not where downstream side effects differed.
func Bisect(baseline, failing *Snapshot) (turn int, result CompareResult) {
	result = Compare(baseline, failing, false)
	if result.Match || len(result.Divergences) == 0 {
		return 0, result
	}
	first := result.Divergences[0]
	idx := first.FirstDivergentIndex
	switch {
	case idx < 0:
		return 0, result
	case idx < len(baseline.ToolEvents) && baseline.ToolEvents[idx].Turn > 0:
		return baseline.ToolEvents[idx].Turn, result
	default:
		return idx + 1, result
	}
}

// canonicalEqual reports whether two JSON-like values are equivalent after a
// JSON round-trip. Avoids false negatives on int vs float64 representations
// that arise from JSON unmarshalling.
func canonicalEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	ca, errA := json.Marshal(a)
	cb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return reflect.DeepEqual(a, b)
	}
	var oa, ob any
	if err := json.Unmarshal(ca, &oa); err != nil {
		return reflect.DeepEqual(a, b)
	}
	if err := json.Unmarshal(cb, &ob); err != nil {
		return reflect.DeepEqual(a, b)
	}
	return reflect.DeepEqual(oa, ob)
}

func canonicalJSON(v any) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	// Keep error messages from running wider than a terminal.
	s := string(data)
	const max = 200
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// SummariseDivergences returns a short, human-friendly summary of a
// CompareResult. The summary is intended for CLI output, not analytics.
func SummariseDivergences(r CompareResult) string {
	if r.Match {
		return "snapshots match"
	}
	if len(r.Divergences) == 0 {
		return "snapshots do not match (no divergence details)"
	}
	d := r.Divergences[0]
	var sb strings.Builder
	fmt.Fprintf(&sb, "first divergence: %s", d.Kind)
	if d.FirstDivergentIndex >= 0 {
		fmt.Fprintf(&sb, " at event #%d", d.FirstDivergentIndex+1)
	}
	if d.BaselineValue != "" || d.ReplayedValue != "" {
		fmt.Fprintf(&sb, "\n  baseline: %s\n  replayed: %s", d.BaselineValue, d.ReplayedValue)
	}
	if d.Description != "" {
		fmt.Fprintf(&sb, "\n  %s", d.Description)
	}
	return sb.String()
}
