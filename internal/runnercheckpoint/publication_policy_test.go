package runnercheckpoint

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestFinalizedResumePinsHistoricalSidecarsThroughReuseDecision(t *testing.T) {
	ctx := t.Context()
	directory := t.TempDir()
	store, err := artifactmanifest.OpenLocal(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	wire := fixture(t)
	produced, err := service.Submit(ctx, wire, store, Hooks{})
	if err != nil || produced.Submission.Completion == nil || factories != 1 {
		t.Fatalf("actual terminal producer failed: %+v %v", produced, err)
	}
	owner, err := executionledger.New(store)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := owner.Reserve(ctx, produced.Submission.Reservation.Binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := owner.Reservation(creator)
	if err != nil {
		t.Fatal(err)
	}
	publications := artifactmanifest.NewStore(store)
	publication, err := publications.ReadCompleted(ctx, produced.Submission.Completion.ManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy manifests may omit the optional execution-attempt artifact.
	artifacts := []artifactmanifest.Artifact{}
	for _, artifact := range publication.Manifest.Artifacts {
		if artifact.Name != "execution-attempt.json" {
			artifacts = append(artifacts, artifact)
		}
	}
	if len(artifacts) != len(publication.Manifest.Artifacts)-1 {
		t.Fatal("legacy fixture did not remove the actual reservation artifact")
	}
	publication.Manifest.Artifacts = artifacts
	manifest, err := artifactmanifest.Encode(publication.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := publications.Create(ctx, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publications.Finalize(ctx, digest); err != nil {
		t.Fatal(err)
	}
	completed, err := owner.Complete(ctx, creator, digest)
	if err != nil {
		t.Fatal(err)
	}
	request, err := runnerunit.ParseSubmission(wire)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := executioncheckpoint.PublishFinalized(ctx, store, owner, creator, request.Request, service.revision, digest)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := owner.Reserve(ctx, reserved.Attempt.Binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	parentReservation, err := owner.Reservation(parent)
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := artifactmanifest.NewNamespace(store, artifactmanifest.ExecutionLedgerNamespaceV1)
	if err != nil {
		t.Fatal(err)
	}
	before, truncated, err := namespace.ListUnverified(ctx)
	if err != nil || truncated {
		t.Fatal("fixture cannot inspect complete decision inventory", err)
	}
	admissions := 0
	result, err := service.Resume(ctx, wire, store, checkpoint, Hooks{Submission: runnerunit.SubmissionHooks{
		ReuseAdmission: func(ctx context.Context, source *executionledger.CompletedExecution) error {
			admissions++
			changed := source.Reservation.Attempt
			changed.Parent = &executionattempt.Parent{ExecutionID: parentReservation.Attempt.ExecutionID, Relationship: "retry"}
			reservation, err := executionattempt.EncodeAttempt(changed)
			if err != nil {
				return err
			}
			updated := source.Completion
			updated.ReservationDigest = artifactmanifest.Hash(reservation)
			completion, err := executionledger.EncodeCompletion(updated)
			if err != nil {
				return err
			}
			for kind, data := range map[string][]byte{"reservation": reservation, "completion": completion} {
				path := filepath.Join(directory, artifactmanifest.ExecutionLedgerNamespaceV1+"-"+kind+"-"+reserved.Attempt.ExecutionID)
				if err := os.Chmod(path, 0o600); err != nil {
					return err
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					return err
				}
			}
			changedSource, err := owner.ReadCompleted(ctx, executionattempt.Source{ExecutionID: reserved.Attempt.ExecutionID, ManifestDigest: digest})
			if err != nil || bytes.Equal(changedSource.CompletionBytes, completed.CompletionBytes) {
				t.Fatal("mutation did not preserve valid but changed historical sidecars", err)
			}
			return nil
		},
	}})
	if err == nil || admissions != 1 || factories != 1 || result.Status != "submission_error" ||
		result.RestoredUnits != nil || result.RemainingWork != nil || result.Submission == nil ||
		result.Submission.Decision != nil {
		t.Fatalf("changed source authorized a recovery decision or success: %+v admissions=%d %v", result, admissions, err)
	}
	after, truncated, err := namespace.ListUnverified(ctx)
	if err != nil || truncated || len(after) != len(before) {
		t.Fatalf("rejected recovery changed immutable decision inventory: %v %v", after, err)
	}
}

func TestCheckpointAdapterRetainsActualProtectedPublicationAndReuseAdmission(t *testing.T) {
	store := backend(t)
	factories, prepared, observed := 0, 0, 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	wire := fixture(t)
	changed := bytes.Replace(wire, []byte("- hello"), []byte("- impossible-output-control"), 1)
	if bytes.Equal(changed, wire) {
		t.Fatal("protected terminal control did not select failed checks")
	}
	hooks := Hooks{Submission: runnerunit.SubmissionHooks{
		PreparePublication: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator,
			snapshot runnerunit.SubmissionSnapshot,
		) (runnerunit.SubmissionPublication, error) {
			prepared++
			if len(snapshot.Report) == 0 || snapshot.Terminal.Outcome != runnerterminal.EvaluationFailure {
				t.Fatal("actual protected publication did not receive original invocation evidence")
			}
			snapshot.Terminal.Diagnostics[0].Message = "[REDACTED]"
			return runnerunit.SubmissionPublication{Terminal: snapshot.Terminal}, nil
		},
		Terminal: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator,
			_ executionledger.Reservation, terminal runnerterminal.Envelope,
		) error {
			observed++
			if prepared != 1 || terminal.Diagnostics[0].Message != "[REDACTED]" {
				t.Fatal("checkpoint wrapper exposed unprepared terminal text to joined observer")
			}
			return nil
		},
	}}
	result, err := service.Submit(context.Background(), changed, store, hooks)
	if err != nil || factories != 1 || prepared != 1 || observed != 1 ||
		result.Submission.Terminal.Diagnostics[0].Message != "[REDACTED]" || len(result.Submission.Report) != 0 {
		t.Fatalf("checkpoint creation bypassed actual protected publication: %+v, %v", result, err)
	}
	reused, err := service.Resume(context.Background(), changed, store, result.FinalizedDigest, hooks)
	if err != nil || factories != 1 || prepared != 1 || observed != 1 ||
		reused.Submission.Terminal.Diagnostics[0].Message != "[REDACTED]" ||
		reused.Submission.EngineCalls != 0 || len(reused.Submission.Report) != 0 {
		t.Fatalf("checkpoint restore rewrote protected originals or bypassed source preservation: %+v, %v", reused, err)
	}
	denial := errors.New("current source disclosure policy rejects reuse")
	hooks.Submission.ReuseAdmission = func(context.Context, *executionledger.CompletedExecution) error { return denial }
	rejected, err := service.Resume(context.Background(), changed, store, result.FinalizedDigest, hooks)
	if !errors.Is(err, denial) || rejected.Submission.Decision != nil || rejected.Submission.Terminal != nil ||
		rejected.RestoredUnits != nil || rejected.RemainingWork != nil || factories != 1 {
		t.Fatalf("checkpoint wrapper bypassed actual source disclosure admission: %+v, %v", rejected, err)
	}
}

func TestCheckpointCaptureAdmissionDenialPrecedesActualReservationAndFactory(t *testing.T) {
	store := backend(t)
	factories, admitted := 0, 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	denial := errors.New("current capture policy rejects this original binding")
	result, err := service.Submit(context.Background(), fixture(t), store, Hooks{
		Submission: runnerunit.SubmissionHooks{
			Admission: func(context.Context, artifactmanifest.Binding) error {
				admitted++
				return denial
			},
		},
	})
	if !errors.Is(err, denial) || admitted != 1 || factories != 0 || result.BeforeDigest != "" ||
		result.Submission.Reservation != nil || result.Submission.Completion != nil {
		t.Fatalf("checkpoint operation reserved or executed after actual capture denial: %+v, %v", result, err)
	}
	namespace, err := artifactmanifest.NewNamespace(store, artifactmanifest.ExecutionLedgerNamespaceV1)
	if err != nil {
		t.Fatal(err)
	}
	keys, truncated, err := namespace.ListUnverified(context.Background())
	if err != nil || truncated || len(keys) != 0 {
		t.Fatalf("capture denial left execution namespace records: %v, %t, %v", keys, truncated, err)
	}
}

func TestOriginalCompletionDuringFreshChildReservationBlocksBeforeFactory(t *testing.T) {
	store := backend(t)
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	var originalOwner *executionledger.Ledger
	var originalCreator executionledger.Creator
	original, err := service.Submit(context.Background(), fixture(t), store, Hooks{
		Submission: runnerunit.SubmissionHooks{
			Reserved: func(_ context.Context, owner *executionledger.Ledger, creator executionledger.Creator,
				_ executionledger.Reservation,
			) error {
				originalOwner, originalCreator = owner, creator
				return nil
			},
		},
		Published: func(context.Context, Reference) error { return errors.New("pause after original before checkpoint") },
	})
	if err == nil || original.BeforeDigest == "" || factories != 0 {
		t.Fatalf("control did not preserve original live creator before execution: %+v, %v", original, err)
	}
	// A separate real producer supplies matching native terminal content.
	// Bind a new manifest to the original reservation, not the other execution.
	other, err := service.Submit(context.Background(), fixture(t), store, Hooks{})
	if err != nil || other.Submission.Completion == nil || factories != 1 {
		t.Fatalf("independent native content control did not complete: %+v, %v", other, err)
	}
	originalReservation, err := originalOwner.Reservation(originalCreator)
	if err != nil {
		t.Fatal(err)
	}
	publications := artifactmanifest.NewStore(store)
	publication, err := publications.ReadCompleted(context.Background(), other.Submission.Completion.ManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	rebound := false
	for i, artifact := range publication.Manifest.Artifacts {
		if artifact.Name == "execution-attempt.json" {
			artifact.SHA256 = originalReservation.Digest
			artifact.SizeBytes = int64(len(originalReservation.Bytes))
			if err := publications.Put(context.Background(), artifact, originalReservation.Bytes); err != nil {
				t.Fatal(err)
			}
			publication.Manifest.Artifacts[i] = artifact
			rebound = true
		}
	}
	if !rebound {
		t.Fatal("actual producer did not declare its reservation artifact")
	}
	manifest, err := artifactmanifest.Encode(publication.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := publications.Create(context.Background(), manifest)
	if err != nil || digest == other.Submission.Completion.ManifestDigest {
		t.Fatalf("original-bound completion fixture did not change manifest: %s %v", digest, err)
	}
	if _, err := publications.Finalize(context.Background(), digest); err != nil {
		t.Fatal(err)
	}
	rejected, err := service.Resume(context.Background(), fixture(t), store, original.BeforeDigest, Hooks{
		Submission: runnerunit.SubmissionHooks{
			Reserved: func(ctx context.Context, _ *executionledger.Ledger, _ executionledger.Creator,
				reservation executionledger.Reservation,
			) error {
				if reservation.Attempt.Parent == nil || reservation.Attempt.Parent.ExecutionID != original.Submission.Reservation.ExecutionID {
					t.Fatal("control did not occur in the exact fresh retry child reservation")
				}
				completed, err := originalOwner.Complete(ctx, originalCreator, digest)
				if err != nil {
					return err
				}
				if completed.Reservation.Attempt.ExecutionID != original.Submission.Reservation.ExecutionID {
					t.Fatal("completion control changed another source rather than original parent")
				}
				return nil
			},
		},
	})
	if !errors.Is(err, executioncheckpoint.ErrStale) || factories != 1 ||
		rejected.Submission.Reservation == nil || rejected.Submission.Completion != nil ||
		rejected.BeforeDigest != "" || rejected.RestoredUnits != nil {
		t.Fatalf("original completion during fresh-child reservation bypassed pre-factory source recheck: %+v, %v", rejected, err)
	}
}
