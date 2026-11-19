package runnercheckpoint

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func TestFreshCheckpointSubmitBeyondDiscoveryLimitDoesNotAuthorizeRecovery(t *testing.T) {
	store := backend(t)
	namespace, err := artifactmanifest.NewNamespace(store, artifactmanifest.ExecutionLedgerNamespaceV1)
	if err != nil {
		t.Fatal(err)
	}
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	paused, err := service.Submit(t.Context(), fixture(t), store, Hooks{
		Submission: runnerunit.SubmissionHooks{Reserved: func(ctx context.Context, _ *executionledger.Ledger,
			_ executionledger.Creator, original executionledger.Reservation,
		) error {
			for index := 0; index < 1001; index++ {
				attempt := original.Attempt
				attempt.ExecutionID = fmt.Sprintf("exec-%064x", index)
				data, err := executionattempt.EncodeAttempt(attempt)
				if err != nil {
					return err
				}
				if _, err := namespace.Create(ctx, "reservation/"+attempt.ExecutionID, data); err != nil {
					return err
				}
			}
			return nil
		}},
		Published: func(context.Context, Reference) error { return errors.New("stop before dispatch") },
	})
	if err == nil || paused.BeforeDigest == "" || paused.Submission == nil ||
		paused.Submission.Decision != nil || factories != 0 {
		t.Fatalf("fresh checkpoint was blocked by unrelated records or dispatched: %+v factories=%d %v", paused, factories, err)
	}
	if _, truncated, err := namespace.ListUnverified(t.Context()); err != nil || !truncated {
		t.Fatalf("fixture did not exceed actual provider inventory limit: truncated=%v %v", truncated, err)
	}
	refused, err := service.Resume(t.Context(), fixture(t), store, paused.BeforeDigest, Hooks{})
	if err == nil || !errors.Is(err, executioncheckpoint.ErrContext) || refused.Submission != nil || factories != 0 {
		t.Fatalf("fresh publication was misrepresented as recoverable: %+v factories=%d %v", refused, factories, err)
	}
	completed, err := service.Submit(t.Context(), fixture(t), store, Hooks{})
	if err != nil || completed.Submission == nil || completed.Submission.Completion == nil ||
		completed.BeforeDigest == "" || completed.FinalizedDigest == "" || factories != 1 {
		t.Fatalf("fresh actual submission failed beyond discovery limit: %+v factories=%d %v", completed, factories, err)
	}
}

type dispatchObservedEngine struct {
	execution.AgentEngine
	check func(context.Context) error
	calls int
}

func (e *dispatchObservedEngine) Initialize(ctx context.Context) error {
	e.calls++
	if err := e.check(ctx); err != nil {
		return err
	}
	return e.AgentEngine.Initialize(ctx)
}

func (e *dispatchObservedEngine) Execute(ctx context.Context, request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	if err := e.check(ctx); err != nil {
		return nil, err
	}
	return e.AgentEngine.Execute(ctx, request)
}

func TestCheckpointEngineStartsOnlyAfterDispatchDecision(t *testing.T) {
	var ledger *executionledger.Ledger
	var executionID string
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		if ledger == nil {
			return nil, errors.New("runtime constructed before reservation")
		}
		started, err := ledger.HasDistinctDecision(t.Context(), executionID)
		if err != nil || !started {
			return nil, fmt.Errorf("runtime constructed without persisted dispatch decision: %v", err)
		}
		// The profile requires a concrete mock. Its successful Execute also
		// requires Initialize, both after this factory observed dispatch.
		return execution.NewMockEngine(config.ModelID), nil
	})
	result, err := service.Submit(t.Context(), fixture(t), backend(t), Hooks{
		Submission: runnerunit.SubmissionHooks{Reserved: func(_ context.Context, owner *executionledger.Ledger,
			_ executionledger.Creator, reservation executionledger.Reservation,
		) error {
			ledger, executionID = owner, reservation.Attempt.ExecutionID
			return nil
		}},
	})
	raw, encodeErr := EncodeResult(result)
	if err != nil || encodeErr != nil || result.ExitCode != 0 || result.Submission == nil ||
		result.Submission.Completion == nil || result.Submission.EngineCalls != 1 || factories != 1 ||
		result.Submission.Terminal == nil || result.Submission.Terminal.Outcome != runnerterminal.Success ||
		result.Submission.Terminal.EngineCalls != 1 || result.Submission.Terminal.Observation == nil {
		t.Fatalf("actual initialized execution did not follow dispatch: result=%s factories=%d err=%v encode=%v", raw, factories, err, encodeErr)
	}
}

func TestCheckpointWrappedMockIsRejectedBeforeLifecycle(t *testing.T) {
	var engine *dispatchObservedEngine
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		engine = &dispatchObservedEngine{AgentEngine: execution.NewMockEngine(config.ModelID), check: func(context.Context) error {
			t.Error("rejected wrapper unexpectedly reached lifecycle")
			return errors.New("wrapper lifecycle must not run")
		}}
		return engine, nil
	})
	store := backend(t)
	result, err := service.Submit(t.Context(), fixture(t), store, Hooks{})
	raw, encodeErr := EncodeResult(result)
	if err != nil || encodeErr != nil || engine == nil || engine.calls != 0 || result.Submission == nil ||
		result.Submission.Completion == nil || result.Submission.EngineCalls != 0 || result.ExitCode != 2 {
		t.Fatalf("wrapper admission control did not finalize zero-call startup failure: %s err=%v encode=%v", raw, err, encodeErr)
	}
	publication, err := artifactmanifest.NewStore(store).ReadCompleted(t.Context(),
		result.Submission.Completion.ManifestDigest, "terminal.json")
	if err != nil {
		t.Fatal(err)
	}
	saved := publication.ArtifactBytes["terminal.json"]
	envelope, err := runnerterminal.Validate(saved, result.Submission.Reservation.Binding.TerminalBinding())
	if err != nil || envelope.Outcome != runnerterminal.InfrastructureFailure || envelope.FailureBoundary != "startup" ||
		envelope.EngineCalls != 0 || len(envelope.Diagnostics) != 1 ||
		envelope.Diagnostics[0].Message != "admitting runtime: inline.mock-text requires a non-nil concrete *execution.MockEngine; factory returned *runnercheckpoint.dispatchObservedEngine" {
		t.Fatalf("actual saved wrapper diagnostic differs: %s %v", saved, err)
	}
	t.Logf("actual saved rejected-wrapper terminal: %s", saved)
}
