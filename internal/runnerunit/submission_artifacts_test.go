package runnerunit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func TestSubmitBindsDetachedObserverBytesInOriginalFinalizedManifest(t *testing.T) {
	ctx := context.Background()
	backend := submissionBackend(t)
	var expected []byte
	calls := 0
	hooks := SubmissionHooks{Artifacts: func(ctx context.Context, ledger *executionledger.Ledger, creator executionledger.Creator, snapshot SubmissionSnapshot) ([]SubmissionArtifact, error) {
		calls++
		if err := ledger.ValidateCreator(ctx, creator); err != nil {
			return nil, err
		}
		var report Report
		if err := json.Unmarshal(snapshot.Report, &report); err != nil {
			return nil, err
		}
		if report.Calls != 1 || report.Observation == nil || snapshot.Terminal.EngineCalls != 1 ||
			snapshot.Terminal.Observation.Status != string(report.Observation.Status) ||
			snapshot.Reservation.Digest != artifactmanifest.Hash(snapshot.Reservation.Bytes) {
			t.Fatal("artifact observer did not receive original joined report/reservation")
		}
		var err error
		expected, err = json.Marshal(struct {
			ExecutionID       string `json:"execution_id"`
			ReservationDigest string `json:"reservation_digest"`
			DeclaredOnly      bool   `json:"declared_only"`
		}{snapshot.Reservation.Attempt.ExecutionID, snapshot.Reservation.Digest, true})
		if err != nil {
			return nil, err
		}
		snapshot.Report[0] = 'X'
		snapshot.Reservation.Bytes[0] = 'Y'
		snapshot.Terminal.Identity.Run = "callback-mutated"
		snapshot.Terminal.Observation.Status = "failed"
		return []SubmissionArtifact{{Name: "observer.json", MediaType: "application/json", Data: expected}}, nil
	}}
	service := NewService(nil)
	result, err := service.Submit(ctx, submissionWire(t, fixture(t), nil), backend, hooks)
	if err != nil || result.Status != "completed" || result.ExitCode != 0 || calls != 1 ||
		result.Terminal.Outcome != runnerterminal.Success || !json.Valid(result.Report) {
		t.Fatalf("observer mutated original evidence or prevented completion: %+v %v", result, err)
	}
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
	completed, err := ledger.ReadCompleted(ctx, source, "terminal.json", "report.json", "observer.json")
	if err != nil || !bytes.Equal(completed.Publication.ArtifactBytes["observer.json"], expected) ||
		len(completed.Publication.Manifest.Artifacts) != 3 ||
		completed.Publication.Manifest.Binding.Identity != result.Reservation.Binding.Identity {
		t.Fatalf("observer bytes were not bound through actual original completed source: %+v %v", completed, err)
	}
	reused, err := service.Submit(ctx, submissionWire(t, fixture(t), &SubmissionPolicy{Mode: "reuse", Source: &source}), backend, hooks)
	if err != nil || reused.Status != "reused" || calls != 1 || reused.EngineCalls != 0 {
		t.Fatalf("reuse ran observer again: %+v calls=%d %v", reused, calls, err)
	}
}

func TestSubmitArtifactObserverFailuresCannotComplete(t *testing.T) {
	for _, control := range []string{"observer-error", "duplicate-terminal", "unsafe-name", "invalid-media", "too-many", "closed-provider"} {
		t.Run(control, func(t *testing.T) {
			directory := t.TempDir()
			backend, err := artifactmanifest.OpenLocal(directory)
			if err != nil {
				t.Fatal(err)
			}

			closed := false
			t.Cleanup(func() {
				if !closed {
					if err := backend.Close(); err != nil {
						t.Error(err)
					}
				}
			})
			hooks := SubmissionHooks{Artifacts: func(context.Context, *executionledger.Ledger, executionledger.Creator, SubmissionSnapshot) ([]SubmissionArtifact, error) {
				artifact := SubmissionArtifact{Name: "observer.json", MediaType: "application/json", Data: []byte(`{}`)}
				switch control {
				case "observer-error":
					return nil, errors.New("observer control failure")
				case "duplicate-terminal":
					artifact.Name = "terminal.json"
				case "unsafe-name":
					artifact.Name = "../observer.json"
				case "invalid-media":
					artifact.MediaType = ""
				case "too-many":
					return make([]SubmissionArtifact, MaxSubmissionExtraArtifacts+1), nil
				case "closed-provider":
					if err := backend.Close(); err != nil {
						return nil, err
					}
					closed = true
				}
				return []SubmissionArtifact{artifact}, nil
			}}
			result, err := NewService(nil).Submit(context.Background(), submissionWire(t, fixture(t), nil), backend, hooks)
			if err == nil || result.Status != "submission_error" || result.ExitCode != 2 || result.Completion != nil ||
				result.EngineCalls != 1 || result.Terminal.Outcome != runnerterminal.Success {
				t.Fatalf("publication failure became successful completion or lost executed evidence: %+v %v", result, err)
			}
			if closed {
				backend, err = artifactmanifest.OpenLocal(directory)
				if err != nil {
					t.Fatal(err)
				}
				closed = false
			}
			ledger, err := executionledger.New(backend)
			if err != nil {
				t.Fatal(err)
			}
			if record, err := ledger.ReadCompletion(context.Background(), result.Reservation.ExecutionID); record != nil || !errors.Is(err, artifactmanifest.ErrMissing) {
				t.Fatalf("failed publication left completed claim: %+v %v", record, err)
			}
		})
	}
}
