package orchestration

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
)

// checkpointRunner manages per-turn checkpoint evaluation during a multi-turn
// run. It owns the list of pending checkpoints for a task and exposes a
// single entry point that the multi-turn drivers (executeFollowUps and
// executeResponderLoop) call after each turn.
//
// The cumulative ExecutionResponse passed at each turn boundary already
// aggregates events, tool calls, skill invocations, usage, and final output
// through the just-completed turn, so we build a turn-scoped graders.Context
// directly from it without slicing.
type checkpointRunner struct {
	runner *EvalRunner
	tc     *models.TestCase
	// pending maps after_turn -> *Checkpoint for fast turn-boundary lookup
	// and to detect whether any checkpoints are configured.
	pending map[int]*models.Checkpoint
	// outcomes accumulates per-turn checkpoint results in turn order.
	outcomes []models.CheckpointOutcome
	// stopped is true once any "on_failure: stop" checkpoint has failed,
	// at which point the multi-turn driver should break out of its loop.
	stopped bool
}

// newCheckpointRunner builds a checkpointRunner from a TestCase. Returns nil
// when the task has no checkpoints, but all methods on *checkpointRunner are
// nil-safe — callers can invoke runForTurn/results on the returned value
// directly without a nil guard.
func newCheckpointRunner(runner *EvalRunner, tc *models.TestCase) *checkpointRunner {
	if tc == nil || len(tc.Checkpoints) == 0 {
		return nil
	}
	cr := &checkpointRunner{
		runner:  runner,
		tc:      tc,
		pending: make(map[int]*models.Checkpoint, len(tc.Checkpoints)),
	}
	for i := range tc.Checkpoints {
		cp := &tc.Checkpoints[i]
		cr.pending[cp.AfterTurn] = cp
	}
	return cr
}

// runForTurn evaluates the checkpoint (if any) scheduled for the given
// 1-based turn number. It returns true when the multi-turn loop should stop
// (i.e., an "on_failure: stop" checkpoint failed). When stop is requested,
// it also sets resp.ErrorMsg so the run is recorded as failed.
//
// Safe to call on a nil receiver, in which case it is a no-op returning false.
func (cr *checkpointRunner) runForTurn(ctx context.Context, turn int, resp *execution.ExecutionResponse) bool {
	if cr == nil {
		return false
	}
	cp, ok := cr.pending[turn]
	if !ok {
		return false
	}
	// Remove from pending so duplicate-turn callers (none expected, but
	// defensive) cannot re-run the same checkpoint.
	delete(cr.pending, turn)

	// Honor --skip-graders: if the user asked to skip grading, skip
	// checkpoint graders too. We do not record a CheckpointOutcome in that
	// case (mirroring how skipped runs omit final-pass validations).
	if cr.runner.skipGraders {
		return false
	}

	gCtx := cr.runner.buildGraderContext(cr.tc, resp, copilotevents.ToSDK(resp.Events))

	// Run only the checkpoint's graders. Reuse graders.RunAll by passing a
	// synthetic TestCase whose Validators field carries the checkpoint
	// graders. We do NOT want task-level expectation strings (output_contains
	// etc.) or task-level validators to run again here — those belong to the
	// final pass. Using a stripped TestCase ensures only the checkpoint
	// graders fire at this boundary.
	stub := &models.TestCase{
		TestID:      cr.tc.TestID,
		DisplayName: cr.tc.DisplayName,
		Validators:  cp.Graders,
	}
	results, err := graders.RunAll(
		ctx,
		nil, // no spec-level graders at checkpoint boundaries
		stub,
		gCtx,
		cr.runner.cfg.Spec().Config.JudgeModel,
		cr.runner.updateSnapshots,
	)
	outcome := models.CheckpointOutcome{
		AfterTurn:   turn,
		Status:      models.StatusPassed,
		Validations: results,
	}
	if err != nil {
		// Synthesize a single failed-grader entry so downstream consumers
		// can still see what went wrong, mirroring how runGraders surfaces
		// errors via resp.ErrorMsg.
		if outcome.Validations == nil {
			outcome.Validations = make(map[string]models.GraderResults)
		}
		outcome.Validations["_checkpoint_error"] = models.GraderResults{
			Name:     "_checkpoint_error",
			Type:     models.GraderKind("checkpoint_error"),
			Score:    0,
			Passed:   false,
			Feedback: err.Error(),
			Weight:   1.0,
		}
		outcome.Status = models.StatusError
	}

	failed := outcome.Status != models.StatusPassed
	if !failed {
		for _, gr := range outcome.Validations {
			if !gr.Passed {
				outcome.Status = models.StatusFailed
				failed = true
				break
			}
		}
	}

	// Surface checkpoint grader results as progress events for verbose CLI
	// users, mirroring the final-pass output.
	if cr.runner.verbose {
		names := make([]string, 0, len(outcome.Validations))
		for n := range outcome.Validations {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			gr := outcome.Validations[n]
			cr.runner.notifyProgress(ProgressEvent{
				EventType:  EventGraderResult,
				TestName:   cr.tc.DisplayName,
				DurationMs: gr.DurationMs,
				Details: map[string]any{
					"grader":      n,
					"grader_type": gr.Type,
					"passed":      gr.Passed,
					"score":       gr.Score,
					"feedback":    gr.Feedback,
					"checkpoint":  turn,
				},
			})
		}
	}

	if failed && cp.EffectiveOnFailure() == models.CheckpointStop {
		outcome.Stopped = true
		cr.stopped = true
		failedNames := failedGraderNames(outcome.Validations)
		msg := fmt.Sprintf("checkpoint after_turn=%d failed (on_failure: stop)", turn)
		if len(failedNames) > 0 {
			msg += ": " + strings.Join(failedNames, ", ")
		}
		// Preserve any prior error rather than overwriting silently.
		if resp.ErrorMsg == "" {
			resp.ErrorMsg = msg
		} else {
			resp.ErrorMsg = resp.ErrorMsg + "; " + msg
		}
	}

	cr.outcomes = append(cr.outcomes, outcome)
	return outcome.Stopped
}

// results returns the accumulated per-turn outcomes (sorted by turn).
func (cr *checkpointRunner) results() []models.CheckpointOutcome {
	if cr == nil || len(cr.outcomes) == 0 {
		return nil
	}
	out := make([]models.CheckpointOutcome, len(cr.outcomes))
	copy(out, cr.outcomes)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].AfterTurn < out[j].AfterTurn
	})
	return out
}

func failedGraderNames(m map[string]models.GraderResults) []string {
	var names []string
	for n, gr := range m {
		if !gr.Passed {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}
