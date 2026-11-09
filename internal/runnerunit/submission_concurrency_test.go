package runnerunit

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
)

func TestConcurrentDuplicateSubmissionsBindDistinctOriginalAttempts(t *testing.T) {
	ctx := context.Background()
	backend := submissionBackend(t)
	wire := submissionWire(t, fixture(t), nil)
	const count = 8
	type outcome struct {
		result Submission
		err    error
	}
	results := make(chan outcome, count)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			<-start
			result, err := NewService(nil).Submit(ctx, wire, backend, SubmissionHooks{})
			results <- outcome{result, err}
		})
	}
	close(start)
	workers.Wait()
	close(results)
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	executions, manifests := map[string]bool{}, map[string]bool{}
	var logical executionattempt.Logical
	for outcome := range results {
		result := outcome.result
		if outcome.err != nil || result.Status != "completed" || result.Completion == nil ||
			result.Reservation == nil || result.EngineCalls != 1 {
			t.Fatalf("concurrent production submission did not complete: %+v %v", result, outcome.err)
		}
		if len(executions) == 0 {
			logical = result.Reservation.Logical
		}
		if result.Reservation.Logical != logical || executions[result.Reservation.ExecutionID] ||
			manifests[result.Completion.ManifestDigest] {
			t.Fatal("duplicate collapsed logical work or distinct immutable execution evidence")
		}
		executions[result.Reservation.ExecutionID] = true
		manifests[result.Completion.ManifestDigest] = true
		source := executionattempt.Source{
			ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest,
		}
		completed, err := ledger.ReadCompleted(ctx, source, "execution-attempt.json")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(completed.Reservation.Bytes, completed.Publication.ArtifactBytes["execution-attempt.json"]) {
			t.Fatal("finalized manifest is not bound to its exact original reservation")
		}
		before := bytes.Clone(completed.CompletionBytes)
		reused, err := NewService(nil).Submit(ctx, submissionWire(t, fixture(t), &SubmissionPolicy{
			Mode: "reuse", Source: &source,
		}), backend, SubmissionHooks{})
		if err != nil || reused.Status != "reused" || reused.EngineCalls != 0 ||
			reused.Reservation.ExecutionID != source.ExecutionID {
			t.Fatalf("verified duplicate reuse ran new work or lost its source: %+v %v", reused, err)
		}
		after, err := ledger.ReadCompletion(ctx, source.ExecutionID)
		if err != nil || !bytes.Equal(before, after.Bytes) {
			t.Fatal("duplicate reuse rewrote the original completion")
		}
	}
	if len(executions) != count || len(manifests) != count {
		t.Fatal("concurrent fixture did not verify every execution and manifest")
	}
}

func TestSubmissionRejectsReplacementAttemptArtifactBeforePublication(t *testing.T) {
	backend := submissionBackend(t)
	hooks := SubmissionHooks{Artifacts: func(context.Context, *executionledger.Ledger, executionledger.Creator, SubmissionSnapshot) ([]SubmissionArtifact, error) {
		return []SubmissionArtifact{{Name: "execution-attempt.json", MediaType: "application/json", Data: []byte("{}")}}, nil
	}}
	result, err := NewService(nil).Submit(context.Background(), submissionWire(t, fixture(t), nil), backend, hooks)
	if err == nil || result.Status != "submission_error" || result.Completion != nil ||
		len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "artifact_observer_invalid" {
		t.Fatalf("attempt replacement became immutable evidence: %+v %v", result, err)
	}
	catalog, err := artifactmanifest.NewStore(backend).Catalog(context.Background())
	if err != nil || len(catalog.ManifestSHA256) != 0 {
		t.Fatalf("invalid attempt artifact was published before admission: %+v %v", catalog, err)
	}
}
