package executioncheckpoint

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

func TestBeforePublicationSerializesConcurrentCreatorMutations(t *testing.T) {
	for _, operation := range []string{"dispatch", "complete"} {
		t.Run(operation, func(t *testing.T) {
			checkpoint, backend, ledger, creator, manifest, terminal := beforeCheckpointSource(t)
			publishCheckpointSource(t, backend, manifest, terminal)
			digest := artifactmanifest.Hash(manifest)
			if _, err := artifactmanifest.NewStore(backend).Finalize(t.Context(), digest); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			fault := &checkpointFaultBackend{Backend: backend, afterRead: func() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}}
			publication := make(chan error, 1)
			request := readFixture(t, "../../examples/runner/unit.json")
			go func() {
				_, err := PublishBefore(ctx, fault, ledger, creator,
					request, checkpoint.Compatibility.RunnerRevision)
				publication <- err
			}()
			select {
			case <-entered:
			case err := <-publication:
				t.Fatalf("publication did not enter immutable Put: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			attempting, mutated := make(chan struct{}), make(chan error, 1)
			copy := creator
			go func() {
				close(attempting)
				var err error
				if operation == "dispatch" {
					_, err = ledger.Distinct(ctx, copy)
				} else {
					_, err = ledger.Complete(ctx, copy, digest)
				}
				mutated <- err
			}()
			<-attempting
			if operation == "dispatch" {
				if started, err := ledger.HasDistinctDecision(ctx, checkpoint.ExecutionID); err != nil || started {
					t.Fatalf("dispatch became visible while Put was blocked: started=%v %v", started, err)
				}
			} else if _, err := ledger.ReadCompletion(ctx, checkpoint.ExecutionID); !errors.Is(err, artifactmanifest.ErrMissing) {
				t.Fatalf("completion became visible while Put was blocked: %v", err)
			}
			select {
			case err := <-mutated:
				t.Fatalf("creator mutation escaped blocked publication gate: %v", err)
			default:
			}
			once.Do(func() { close(release) })
			for _, done := range []<-chan error{publication, mutated} {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			if stored, err := PublishBefore(ctx, backend, ledger, creator,
				readFixture(t, "../../examples/runner/unit.json"), checkpoint.Compatibility.RunnerRevision); stored != "" || err == nil {
				t.Fatalf("mutation permitted subsequent before publication: %s %v", stored, err)
			}
		})
	}
}
