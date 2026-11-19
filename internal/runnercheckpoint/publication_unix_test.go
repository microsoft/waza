//go:build !windows

package runnercheckpoint

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func publicationBackend(t *testing.T) (*artifactmanifest.Local, string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permission-denial controls require a non-root process")
	}
	directory := t.TempDir()
	store, err := artifactmanifest.OpenLocal(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Error(err)
		}
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store, directory
}

func TestBeforeBlobWriteFailurePreventsActualFactory(t *testing.T) {
	store, directory := publicationBackend(t)
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	reserved := false
	result, err := service.Submit(context.Background(), fixture(t), store, Hooks{
		Submission: runnerunit.SubmissionHooks{
			Reserved: func(ctx context.Context, ledger *executionledger.Ledger,
				creator executionledger.Creator, _ executionledger.Reservation,
			) error {
				if err := ledger.ValidateCreator(ctx, creator); err != nil {
					return err
				}

				reserved = true
				return os.Chmod(directory, 0o500)
			},
		},
	})
	if !reserved || !errors.Is(err, os.ErrPermission) || factories != 0 || result.Submission.EngineCalls != 0 ||
		result.Submission.Reservation == nil || result.Submission.Completion != nil ||
		result.FinalizedDigest != "" || result.Status != "submission_error" {
		t.Fatalf("real before blob write failure bypassed pre-factory admission: reserved=%t factories=%d result=%+v error=%v",
			reserved, factories, result, err)
	}
}

func TestJoinedTerminalArtifactWriteFailureCannotPublishCompletionOrFinalizedCheckpoint(t *testing.T) {
	store, directory := publicationBackend(t)
	factories, joined := 0, 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	result, err := service.Submit(context.Background(), fixture(t), store, Hooks{
		Submission: runnerunit.SubmissionHooks{
			Terminal: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator,
				_ executionledger.Reservation, terminal runnerterminal.Envelope,
			) error {
				if factories != 1 || terminal.EngineCalls != 1 || terminal.Outcome != runnerterminal.Success {
					t.Fatalf("actual joined terminal control did not reach the production boundary: %+v", terminal)
				}
				joined++
				return os.Chmod(directory, 0o500)
			},
		},
	})
	if joined != 1 || !errors.Is(err, os.ErrPermission) || result.Status != "submission_error" ||
		result.BeforeDigest == "" || result.FinalizedDigest != "" || result.Submission.Completion != nil ||
		result.Submission.Terminal == nil || result.Submission.EngineCalls != 1 || result.RemainingWork != nil {
		t.Fatalf("real post-join artifact write failure became completed evidence: %+v, %v", result, err)
	}
	ledger, err := executionledger.New(store)
	if err != nil {
		t.Fatal(err)
	}
	if record, err := ledger.ReadCompletion(context.Background(), result.Submission.Reservation.ExecutionID); record != nil || !errors.Is(err, artifactmanifest.ErrMissing) {
		t.Fatalf("failed terminal write recorded a completion: %+v, %v", record, err)
	}
	if checkpoint, err := executioncheckpoint.Load(context.Background(), store, result.BeforeDigest); checkpoint == nil || err != nil {
		t.Fatalf("write failure deleted original before checkpoint evidence: %+v, %v", checkpoint, err)
	}
}
