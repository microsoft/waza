package runnerunit

import (
	"bytes"
	"context"
	"errors"
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
	for _, control := range []string{"invalid-bytes", "another-execution", "wrong-media", "identical-original"} {
		t.Run(control, func(t *testing.T) {
			ctx := context.Background()
			backend := submissionBackend(t)
			wire := submissionWire(t, fixture(t), nil)
			original, err := NewService(nil).Submit(ctx, wire, backend, SubmissionHooks{})
			if err != nil || original.Completion == nil {
				t.Fatalf("original publication control failed: %+v %v", original, err)
			}
			ledger, err := executionledger.New(backend)
			if err != nil {
				t.Fatal(err)
			}
			source := executionattempt.Source{
				ExecutionID: original.Reservation.ExecutionID, ManifestDigest: original.Completion.ManifestDigest,
			}
			before, err := ledger.ReadCompleted(ctx, source, "terminal.json", "execution-attempt.json")
			if err != nil || len(before.Publication.ArtifactBytes) != 2 {
				t.Fatalf("original immutable artifact control failed: %+v %v", before, err)
			}
			hooks := SubmissionHooks{Artifacts: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, snapshot SubmissionSnapshot) ([]SubmissionArtifact, error) {
				artifact := SubmissionArtifact{
					Name: "execution-attempt.json", MediaType: "application/json", Data: bytes.Clone(snapshot.Reservation.Bytes),
				}
				switch control {
				case "invalid-bytes":
					artifact.Data = []byte("{}")
				case "another-execution":
					artifact.Data = bytes.Clone(before.Reservation.Bytes)
				case "wrong-media":
					artifact.MediaType = "text/plain"
				}
				return []SubmissionArtifact{artifact}, nil
			}}
			result, err := NewService(nil).Submit(ctx, wire, backend, hooks)
			if result.Reservation == nil || result.EngineCalls != 1 || result.Reservation.ExecutionID == source.ExecutionID {
				t.Fatalf("collision control did not exercise a distinct actual execution: %+v %v", result, err)
			}
			expectedManifests := 1
			if control == "identical-original" {
				expectedManifests++
				if err != nil || result.Status != "completed" || result.Completion == nil {
					t.Fatalf("identical original reservation duplicate rejected: %+v %v", result, err)
				}
				accepted, err := ledger.ReadCompleted(ctx, executionattempt.Source{
					ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest,
				}, "execution-attempt.json")
				if err != nil || len(accepted.Publication.Manifest.Artifacts) != 2 ||
					!bytes.Equal(accepted.Reservation.Bytes, accepted.Publication.ArtifactBytes["execution-attempt.json"]) {
					t.Fatalf("accepted duplicate changed the actual reservation inventory: %+v %v", accepted, err)
				}
			} else {
				if err == nil || result.Status != "submission_error" || result.ExitCode != 2 || result.Completion != nil ||
					len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "artifact_observer_invalid" {
					t.Fatalf("attempt replacement became immutable evidence: %+v %v", result, err)
				}
				if record, err := ledger.ReadCompletion(ctx, result.Reservation.ExecutionID); record != nil ||
					!errors.Is(err, artifactmanifest.ErrMissing) {
					t.Fatalf("invalid attempt artifact left a completion: %+v %v", record, err)
				}
			}
			after, err := ledger.ReadCompleted(ctx, source, "terminal.json", "execution-attempt.json")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before.Reservation.Bytes, after.Reservation.Bytes) ||
				!bytes.Equal(before.CompletionBytes, after.CompletionBytes) ||
				!bytes.Equal(before.Publication.ManifestBytes, after.Publication.ManifestBytes) {
				t.Fatal("reserved-name collision overwrote original immutable sidecars or manifest")
			}
			for name, data := range before.Publication.ArtifactBytes {
				if !bytes.Equal(data, after.Publication.ArtifactBytes[name]) {
					t.Fatalf("reserved-name collision changed original %s", name)
				}
			}
			catalog, err := artifactmanifest.NewStore(backend).Catalog(ctx)
			if err != nil || len(catalog.ManifestSHA256) != expectedManifests {
				t.Fatalf("reserved-name admission published an unexpected manifest: %+v %v", catalog, err)
			}
		})
	}
}
