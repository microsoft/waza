package runnercheckpoint

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func TestCheckpointActualCaptureFacadePreservesProtectedOriginals(t *testing.T) {
	for _, control := range []string{"success", "failed-checks", "canceled"} {
		t.Run(control, func(t *testing.T) {
			store := backend(t)
			wire := fixture(t)
			private := []byte("checkpoint-private-conversation-control")
			changed := bytes.Replace(wire, []byte("prompt: hello"), append([]byte("prompt: hello "), private...), 1)
			if bytes.Equal(changed, wire) {
				t.Fatal("capture control did not change the actual prompt")
			}
			wire = changed
			wantCode := 0
			if control == "failed-checks" {
				changed = bytes.Replace(wire, []byte("- hello"), []byte("- absent-output-control"), 1)
				if bytes.Equal(changed, wire) {
					t.Fatal("failed-check control did not change actual expectations")
				}
				wire, wantCode = changed, 1
			}
			factories, observed := 0, 0
			service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
				factories++
				if control == "canceled" {
					return nil, context.Canceled
				}
				return execution.NewMockEngine(config.ModelID), nil
			})
			if control == "canceled" {
				wantCode = 3
			}
			protected, err := ownedsubmission.CaptureHooks(runnerunit.SubmissionHooks{
				Terminal: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator,
					_ executionledger.Reservation, terminal runnerterminal.Envelope,
				) error {
					observed++
					for _, diagnostic := range terminal.Diagnostics {
						if diagnostic.Message != "" && diagnostic.Message != "[REDACTED]" {
							t.Fatal("joined checkpoint observer received restricted original text")
						}
					}
					if terminal.Observation != nil && terminal.Observation.ErrorMsg != "" &&
						terminal.Observation.ErrorMsg != "[REDACTED]" {
						t.Fatal("joined checkpoint observer received restricted observation text")
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			hooks := Hooks{Submission: protected}
			result, err := service.Submit(context.Background(), wire, store, hooks)
			if err != nil || result.ExitCode != wantCode || observed != 1 || factories != 1 ||
				result.Submission.Completion == nil || result.FinalizedDigest == "" || len(result.Submission.Report) != 0 {
				t.Fatalf("actual checkpoint capture facade did not finalize original outcome: %+v, %v", result, err)
			}
			owner, err := executionledger.New(store)
			if err != nil {
				t.Fatal(err)
			}
			source := executionattempt.Source{
				ExecutionID:    result.Submission.Reservation.ExecutionID,
				ManifestDigest: result.Submission.Completion.ManifestDigest,
			}
			completed, err := owner.ReadCompleted(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			names := make([]string, 0, len(completed.Publication.Manifest.Artifacts))
			category := false
			for _, artifact := range completed.Publication.Manifest.Artifacts {
				names = append(names, artifact.Name)
				category = category || artifact.Name == "execution-evidence-capture.json"
				if artifact.Name == "report.json" {
					t.Fatal("checkpoint publication stored the raw rich report")
				}
			}
			if !category {
				t.Fatal("actual facade did not publish its category evidence")
			}
			completed, err = owner.ReadCompleted(context.Background(), source, names...)
			if err != nil {
				t.Fatal(err)
			}
			for name, data := range completed.Publication.ArtifactBytes {
				if bytes.Contains(data, private) || bytes.Contains(data, []byte(`"prompt"`)) ||
					bytes.Contains(data, []byte(`"transcript"`)) {
					t.Fatalf("restricted conversation persisted in %s", name)
				}
			}
			destination := bytes.Replace(wire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
			if bytes.Equal(destination, wire) {
				t.Fatal("restore control did not change destination ordinal")
			}
			restored, err := service.Resume(context.Background(), destination, store, result.FinalizedDigest, hooks)
			if err != nil || restored.ExitCode != wantCode || restored.Submission.EngineCalls != 0 ||
				factories != 1 || observed != 1 || restored.Submission.Terminal.Selection.Attempt != 2 {
				t.Fatalf("protected checkpoint restore reran or relabeled original: %+v, %v", restored, err)
			}
			terminal, err := runnerterminal.Encode(*restored.Submission.Terminal, result.Submission.Reservation.Binding.TerminalBinding())
			if err != nil || !bytes.Equal(terminal, completed.Publication.ArtifactBytes["terminal.json"]) {
				t.Fatalf("protected checkpoint restore changed original terminal bytes: %v", err)
			}
			if _, err := EncodeResult(restored); err != nil {
				t.Fatalf("actual protected restore contradicted result profile: %v", err)
			}
		})
	}
}

func TestCheckpointAdapterRetainsIndependentCapturePublicationValidation(t *testing.T) {
	store := backend(t)
	factories, observed := 0, 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	protected, err := ownedsubmission.CaptureHooks(runnerunit.SubmissionHooks{
		Terminal: func(context.Context, *executionledger.Ledger, executionledger.Creator,
			executionledger.Reservation, runnerterminal.Envelope,
		) error {
			observed++
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepare := protected.PreparePublication
	tampered := false
	protected.PreparePublication = func(ctx context.Context, owner *executionledger.Ledger, creator executionledger.Creator,
		snapshot runnerunit.SubmissionSnapshot,
	) (runnerunit.SubmissionPublication, error) {
		publication, err := prepare(ctx, owner, creator, snapshot)
		if err != nil {
			return publication, err
		}
		for i := range publication.Artifacts {
			if publication.Artifacts[i].Name == "execution-evidence-capture.json" {
				publication.Artifacts[i].Data = append(publication.Artifacts[i].Data, ' ')
				tampered = true
				break
			}
		}
		return publication, nil
	}
	result, err := service.Submit(context.Background(), fixture(t), store, Hooks{Submission: protected})
	if err == nil || !tampered || factories != 1 || observed != 0 || result.BeforeDigest == "" ||
		result.FinalizedDigest != "" || result.Submission.Reservation == nil || result.Submission.Completion != nil {
		t.Fatalf("checkpoint adapter lost original publication validation: %+v, %v", result, err)
	}
	owner, err := executionledger.New(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.ReadCompletion(context.Background(), result.Submission.Reservation.ExecutionID); !errors.Is(err, artifactmanifest.ErrMissing) {
		t.Fatalf("rejected category publication acquired completion: %v", err)
	}
}

func TestCheckpointActualCaptureFacadeRejectsRestrictedBindingBeforeReservation(t *testing.T) {
	store := backend(t)
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	wire := fixture(t)
	restricted := "ghp_" + strings.Repeat("A", 36)
	changed := bytes.Replace(wire, []byte("id: selected"), []byte("id: "+restricted), 1)
	if bytes.Equal(changed, wire) {
		t.Fatal("restricted binding control did not change actual task ID")
	}
	wire = changed
	changed = bytes.Replace(wire, []byte(`"task":"selected"`), []byte(`"task":"`+restricted+`"`), 1)
	if bytes.Equal(changed, wire) {
		t.Fatal("restricted binding control did not preserve matching selection")
	}
	protected, err := ownedsubmission.CaptureHooks(runnerunit.SubmissionHooks{
		Reserved: func(context.Context, *executionledger.Ledger, executionledger.Creator, executionledger.Reservation) error {
			t.Fatal("restricted original binding reached persisted checkpoint reservation")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Submit(context.Background(), changed, store, Hooks{Submission: protected})
	if err == nil || result.Submission == nil || result.Submission.Reservation != nil ||
		factories != 0 || result.BeforeDigest != "" || result.FinalizedDigest != "" {
		t.Fatalf("checkpoint capture admission leaked reservation or execution: %+v, %v", result, err)
	}
	if len(result.Submission.Diagnostics) == 0 || result.Submission.Diagnostics[0].Code != "capture_admission_failed" {
		t.Fatalf("control failed outside actual capture admission: %+v", result.Submission.Diagnostics)
	}
	namespace, err := artifactmanifest.NewNamespace(store, artifactmanifest.ExecutionLedgerNamespaceV1)
	if err != nil {
		t.Fatal(err)
	}
	keys, truncated, err := namespace.ListUnverified(context.Background())
	if err != nil || truncated || len(keys) != 0 {
		t.Fatalf("restricted binding left execution records: %v, %t, %v", keys, truncated, err)
	}
}
