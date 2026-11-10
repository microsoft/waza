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
	var engine *dispatchObservedEngine
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		engine = &dispatchObservedEngine{AgentEngine: execution.NewMockEngine(config.ModelID), check: func(ctx context.Context) error {
			if ledger == nil {
				return errors.New("runtime started before reservation")
			}
			started, err := ledger.HasDistinctDecision(ctx, executionID)
			if err != nil || !started {
				return fmt.Errorf("runtime started without persisted dispatch decision: %v", err)
			}
			return nil
		}}
		return engine, nil
	})
	result, err := service.Submit(t.Context(), fixture(t), backend(t), Hooks{
		Submission: runnerunit.SubmissionHooks{Reserved: func(_ context.Context, owner *executionledger.Ledger,
			_ executionledger.Creator, reservation executionledger.Reservation,
		) error {
			ledger, executionID = owner, reservation.Attempt.ExecutionID
			return nil
		}},
	})
	if err != nil || result.Submission == nil || result.Submission.Completion == nil || engine == nil || engine.calls != 2 {
		t.Fatalf("actual initialization/execution did not observe dispatch: %+v engine=%+v %v", result, engine, err)
	}
}
