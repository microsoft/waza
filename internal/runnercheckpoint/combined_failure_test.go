package runnercheckpoint

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercheckpoint/testfixture"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func TestCapturedReusePinnedReadFailureCloseProjection(t *testing.T) {
	directory := t.TempDir()
	store, err := artifactmanifest.OpenLocal(directory)
	if err != nil {
		t.Fatal(err)
	}
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	created, err := service.SubmitCaptured(t.Context(), fixture(t), store, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	failing, transport := testfixture.NewPinnedReadBackend(t, directory)
	approved := false
	partial, err := service.ResumeCaptured(t.Context(), fixture(t), failing, created.FinalizedDigest, Hooks{
		Submission: runnerunit.SubmissionHooks{
			ReuseAdmission: func(context.Context, *executionledger.CompletedExecution) error {
				approved = true
				return nil
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), testfixture.PinnedReadFailure) || !approved || transport.Reads() != 4 ||
		partial.Source == nil || partial.Submission == nil || partial.Submission.Reservation != nil ||
		partial.Submission.Completion != nil {
		t.Fatalf("pinned read did not fail after source admission: %+v %v", partial, err)
	}
	if _, err := EncodeResult(partial); err != nil {
		t.Fatalf("ordinary captured failure should remain valid v1.0: %v", err)
	}
	closed := WithOperationError(partial, ErrorCodeStorageCloseFailed)
	if closed.Source != nil || !closed.SourceWithheld || closed.Submission != partial.Submission {
		t.Fatalf("close projection did not withhold only unlinked source: %+v", closed)
	}
	if _, err := EncodeResult(closed); err != nil {
		t.Fatalf("close projection did not encode: %v", err)
	}
}

func TestCapturedBeforeResumePartialAndClosePreservesLinkedSource(t *testing.T) {
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	store := backend(t)
	before, err := service.SubmitCaptured(t.Context(), fixture(t), store, Hooks{
		Published: func(context.Context, Reference) error { return errors.New("stop before dispatch") },
	})
	if err == nil || before.BeforeDigest == "" {
		t.Fatalf("before fixture did not stop after publication: %+v %v", before, err)
	}
	partial, err := service.ResumeCaptured(t.Context(), fixture(t), store, before.BeforeDigest, Hooks{
		Submission: runnerunit.SubmissionHooks{
			Terminal: func(context.Context, *executionledger.Ledger, executionledger.Creator,
				executionledger.Reservation, runnerterminal.Envelope) error {
				return errors.New("stop after capture approval")
			},
		},
	})
	if err == nil || partial.Source == nil || partial.Submission == nil ||
		partial.Submission.Reservation == nil || partial.Submission.Completion != nil ||
		partial.BeforeDigest == "" {
		t.Fatalf("before restore did not retain approved partial evidence: %+v %v", partial, err)
	}
	closed := WithOperationError(partial, ErrorCodeStorageCloseFailed)
	if closed.Source == nil || closed.SourceWithheld || closed.Submission != partial.Submission ||
		closed.BeforeDigest != partial.BeforeDigest {
		t.Fatalf("linked source or partial evidence was not preserved: %+v", closed)
	}
	if _, err := EncodeResult(closed); err != nil {
		t.Fatalf("before restore close projection did not encode: %v", err)
	}
}

func TestOperationErrorWithholdsMismatchedSourceWithoutHidingInvalidSource(t *testing.T) {
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	store := backend(t)
	created, err := service.SubmitCaptured(t.Context(), fixture(t), store, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := service.ResumeCaptured(t.Context(), fixture(t), store, created.FinalizedDigest, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	changed := *reused.Source
	changed.CompletionDigest = strings.Repeat("e", 64)
	reused.Source = &changed
	closed := WithOperationError(reused, ErrorCodeStorageCloseFailed)
	if closed.Source != nil || !closed.SourceWithheld || closed.Submission != reused.Submission {
		t.Fatalf("mismatched source was not withheld independently: %+v", closed)
	}
	if _, err := EncodeResult(closed); err != nil {
		t.Fatal(err)
	}
	changed.Boundary = "invalid"
	invalid := WithOperationError(reused, ErrorCodeStorageCloseFailed)
	if invalid.Source == nil {
		t.Fatal("invalid source was silently hidden")
	}
	if _, err := EncodeResult(invalid); err == nil {
		t.Fatal("invalid source was accepted")
	}
}
