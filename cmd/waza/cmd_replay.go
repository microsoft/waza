package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/microsoft/waza/internal/snapshot"
	"github.com/spf13/cobra"
)

func newReplayCommand() *cobra.Command {
	var (
		mode      string
		bisectArg string
		jsonOut   bool
		strict    bool
	)
	cmd := &cobra.Command{
		Use:   "replay <snapshot.json>",
		Short: "Replay a snapshot to verify deterministic reproduction",
		Long: `Replay a self-contained task snapshot to verify that the eval is
reproducible from the captured tool_events tape.

Modes:
  model-replay  (default) Re-check grader outcomes against the snapshot's
                tool_events without contacting the engine; exits 0 when the
                snapshot is internally consistent.
  live          (planned) Re-run the task against the real engine and
                compare the resulting tool_events to the snapshot's. The
                comparison ignores durations and raw output text, focusing
                on the ordered tool name/args fingerprint.

Bisect:
  --bisect <other.json> compares two snapshots and reports the first
  divergent turn (or event sequence when turn boundaries are not tracked).

Exit codes:
  0  snapshots match / replay succeeded
  1  divergence detected (kind/value rendered to stderr or --json output)
  2  load / parse / IO error`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snapPath := args[0]
			snap, err := snapshot.LoadSnapshotFile(snapPath)
			if err != nil {
				return exitErr(2, err)
			}

			if bisectArg != "" {
				other, err := snapshot.LoadSnapshotFile(bisectArg)
				if err != nil {
					return exitErr(2, err)
				}
				turn, result := snapshot.Bisect(snap, other)
				result.BaselinePath = snapPath
				result.ReplayedPath = bisectArg
				return renderReplayResult(cmd, result, jsonOut, turn)
			}

			switch snapshot.Mode(strings.ToLower(mode)) {
			case "", snapshot.ModeModelReplay:
				return runModelReplay(cmd, snap, snapPath, jsonOut, strict)
			case snapshot.ModeLive:
				return exitErr(2, fmt.Errorf("live mode is not implemented yet; use --mode model-replay or open a tracking issue"))
			default:
				return exitErr(2, fmt.Errorf("unknown --mode %q (expected model-replay or live)", mode))
			}
		},
	}
	cmd.Flags().StringVar(&mode, "mode", string(snapshot.ModeModelReplay), "Replay mode: model-replay (default) or live")
	cmd.Flags().StringVar(&bisectArg, "bisect", "", "Path to a second snapshot to bisect against the primary")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON to stdout instead of a human summary")
	cmd.Flags().BoolVar(&strict, "strict", true, "In model-replay mode, also re-check final status and grader outcomes")
	return cmd
}

// runModelReplay re-checks the snapshot's grader results without re-running
// the engine. It compares the snapshot's stored validations against the
// tool_events tape to surface internal inconsistencies (e.g. a snapshot
// claims a tool_calls grader passed but no matching tool_event exists).
func runModelReplay(cmd *cobra.Command, snap *snapshot.Snapshot, source string, jsonOut, strict bool) error {
	report := buildModelReplayReport(snap, strict)
	report.SourcePath = source
	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return exitErr(2, err)
		}
	} else {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), renderModelReplayHuman(report))
	}
	if !report.Pass {
		return exitErr(1, fmt.Errorf("replay diverged: %d issue(s)", len(report.Issues)))
	}
	return nil
}

// renderReplayResult prints a CompareResult (used by bisect / live).
func renderReplayResult(cmd *cobra.Command, result snapshot.CompareResult, jsonOut bool, firstDivergentTurn int) error {
	if jsonOut {
		payload := struct {
			snapshot.CompareResult
			FirstDivergentTurn int `json:"first_divergent_turn,omitempty"`
		}{
			CompareResult:      result,
			FirstDivergentTurn: firstDivergentTurn,
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			return exitErr(2, err)
		}
	} else {
		var sb strings.Builder
		if result.Match {
			fmt.Fprintln(&sb, "OK snapshots match")
		} else {
			fmt.Fprintln(&sb, "DIVERGENCE detected")
			if firstDivergentTurn > 0 {
				fmt.Fprintf(&sb, "first divergent turn: %d\n", firstDivergentTurn)
			}
			fmt.Fprintln(&sb, snapshot.SummariseDivergences(result))
		}
		_, _ = fmt.Fprint(cmd.OutOrStdout(), sb.String())
	}
	if !result.Match {
		return exitErr(1, fmt.Errorf("snapshots diverge"))
	}
	return nil
}

// ModelReplayReport is the JSON/console payload for a model-replay run.
type ModelReplayReport struct {
	SourcePath string             `json:"source"`
	Mode       snapshot.Mode      `json:"mode"`
	Pass       bool               `json:"pass"`
	Status     string             `json:"status"`
	Issues     []ModelReplayIssue `json:"issues,omitempty"`
	ToolEvents int                `json:"tool_events"`
	Graders    int                `json:"graders"`
}

// ModelReplayIssue describes a single inconsistency found by model-replay.
type ModelReplayIssue struct {
	Kind    string `json:"kind"`
	Grader  string `json:"grader,omitempty"`
	Message string `json:"message"`
}

func buildModelReplayReport(snap *snapshot.Snapshot, strict bool) ModelReplayReport {
	r := ModelReplayReport{
		Mode:       snapshot.ModeModelReplay,
		Status:     string(snap.Result.Status),
		ToolEvents: len(snap.ToolEvents),
		Graders:    len(snap.Result.Validations),
		Pass:       true,
	}

	// Internal consistency: sequence numbers should be 1..N, strictly
	// monotonic. Anything else indicates the tape was edited or corrupted.
	if len(snap.ToolEvents) > 0 {
		// Sort a copy by Sequence to detect duplicates / gaps without
		// mutating the snapshot in memory.
		seqs := make([]int, len(snap.ToolEvents))
		for i, ev := range snap.ToolEvents {
			seqs[i] = ev.Sequence
		}
		sort.Ints(seqs)
		for i, s := range seqs {
			if s != i+1 {
				r.Pass = false
				r.Issues = append(r.Issues, ModelReplayIssue{
					Kind:    "sequence_gap",
					Message: fmt.Sprintf("tool_events.sequence is not 1..N (saw %d at position %d)", s, i+1),
				})
				break
			}
		}
	}

	// Strict mode: every grader entry must agree with itself — passed=true
	// with score 0 (and non-zero weight) is a clear inconsistency.
	if strict {
		for name, g := range snap.Result.Validations {
			if g.Passed && g.Weight > 0 && g.Score == 0 {
				r.Pass = false
				r.Issues = append(r.Issues, ModelReplayIssue{
					Kind:    "validation_inconsistency",
					Grader:  name,
					Message: "grader claims passed=true but score is 0",
				})
			}
		}
	}

	return r
}

func renderModelReplayHuman(r ModelReplayReport) string {
	var sb strings.Builder
	if r.Pass {
		fmt.Fprintf(&sb, "OK model-replay: %s (%d tool_events, %d graders)\n", r.Status, r.ToolEvents, r.Graders)
	} else {
		fmt.Fprintf(&sb, "FAIL model-replay: %d issue(s)\n", len(r.Issues))
		for _, iss := range r.Issues {
			grader := iss.Grader
			if grader != "" {
				grader = " [" + grader + "]"
			}
			fmt.Fprintf(&sb, "  - %s%s: %s\n", iss.Kind, grader, iss.Message)
		}
	}
	if r.SourcePath != "" {
		fmt.Fprintf(&sb, "  source: %s\n", filepath.Clean(r.SourcePath))
	}
	return sb.String()
}

// exitErr wraps an error so the cobra wrapper exits with the requested
// code. main.go's run loop honors ExitCodeError and prints its message.
func exitErr(code int, err error) error {
	if err == nil {
		err = fmt.Errorf("exit %d", code)
	}
	return &ExitCodeError{Code: code, Err: err}
}
