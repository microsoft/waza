package runnercheckpoint

import (
	"context"
	"errors"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func TestBeforeCheckpointRejectsOriginalDispatchWithoutCompletion(t *testing.T) {
	ctx := context.Background()
	store := backend(t)
	factories := 0
	var source Reference
	service := adapter(t, func(models.Config) (execution.AgentEngine, error) {
		factories++
		return nil, errors.New("original dispatch reached runtime construction")
	})
	original, err := service.Submit(ctx, fixture(t), store, Hooks{
		Published: func(_ context.Context, ref Reference) error {
			if ref.Boundary == executioncheckpoint.BeforeUnit {
				source = ref
			}
			return nil
		},
		Submission: runnerunit.SubmissionHooks{
			Terminal: func(context.Context, *executionledger.Ledger, executionledger.Creator, executionledger.Reservation, runnerterminal.Envelope) error {
				return errors.New("completion publication interrupted after dispatch")
			},
		},
	})
	if err == nil || factories != 1 || source.Digest == "" || original.Submission.Completion != nil {
		t.Fatalf("fixture did not reach actual dispatch without completion: %+v %v", original, err)
	}
	ledger, err := executionledger.New(store)
	if err != nil {
		t.Fatal(err)
	}
	if completed, err := ledger.ReadCompletion(ctx, original.Submission.Reservation.ExecutionID); completed != nil ||
		!errors.Is(err, artifactmanifest.ErrMissing) {
		t.Fatalf("fixture unexpectedly retained a completion: %+v %v", completed, err)
	}
	recovered, err := service.Resume(ctx, fixture(t), store, source.Digest, Hooks{})
	if !errors.Is(err, executioncheckpoint.ErrStale) || factories != 1 || recovered.Submission != nil ||
		recovered.BeforeDigest != "" || recovered.FinalizedDigest != "" {
		t.Fatalf("missing completion silently reran possibly executed work: %+v factories=%d %v", recovered, factories, err)
	}
}
