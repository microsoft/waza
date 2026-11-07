package runnerunit

import (
	"context"
	"errors"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

func TestSubmitCaptureAdmissionPrecedesReservationAndFactory(t *testing.T) {
	backend := submissionBackend(t)
	service := NewService(nil)
	hooks := SubmissionHooks{Admission: func(context.Context, artifactmanifest.Binding) error {
		return errors.New("capture policy denied original binding")
	}}
	result, err := service.Submit(context.Background(), submissionWire(t, fixture(t), nil), backend, hooks)
	if err == nil || result.Reservation != nil || result.Decision != nil || result.Completion != nil ||
		result.EngineCalls != 0 || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "capture_admission_failed" {
		t.Fatalf("capture denial persisted or executed original binding: %+v %v", result, err)
	}
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	if err != nil {
		t.Fatal(err)
	}
	keys, truncated, err := namespace.ListUnverified(context.Background())
	if err != nil || truncated || len(keys) != 0 {
		t.Fatalf("pre-reservation capture denial left records: %v truncated=%v %v", keys, truncated, err)
	}
	hooks.Admission = func(_ context.Context, binding artifactmanifest.Binding) error {
		if len(binding.GraderIDs) == 0 {
			t.Fatal("capture mutation control requires an original grader identifier")
		}
		binding.GraderIDs[0] = "callback-mutated"
		return nil
	}
	accepted, err := service.Submit(context.Background(), submissionWire(t, fixture(t), nil), backend, hooks)
	if err != nil || accepted.Status != "completed" || accepted.Reservation.Binding.GraderIDs[0] == "callback-mutated" {
		t.Fatalf("capture callback mutated reserved original binding: %+v %v", accepted, err)
	}
}
