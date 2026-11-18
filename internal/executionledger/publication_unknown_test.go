package executionledger

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
)

type unknownMarkerBackend struct {
	artifactmanifest.Backend
	failure error
}

func (b unknownMarkerBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	if strings.HasPrefix(key, "final-") {
		return nil, b.failure
	}
	return b.Backend.Get(ctx, key, limit)
}

func TestUnknownPublicationCannotAuthorizeCompletionOrReuse(t *testing.T) {
	for _, control := range []struct {
		name string
		err  error
	}{
		{"unreadable-marker", errors.New("marker read denied")},
		{"mixed-missing-and-read-error", errors.Join(artifactmanifest.ErrMissing, errors.New("read denied"))},
		{"corrupt-marker", artifactmanifest.ErrCorrupt},
	} {
		t.Run(control.name, func(t *testing.T) {
			ctx := context.Background()
			ledger, backend, _, binding := ownerFixture(t)
			creator, reserved := reserveFixture(t, ledger, binding)
			digest := ownerPublication(t, ledger, binding, true)
			ledger.store = artifactmanifest.NewStore(unknownMarkerBackend{Backend: backend, failure: control.err})
			receipt, err := ledger.store.Inspect(ctx, digest)
			if err == nil || receipt.Publication != "unknown" {
				t.Fatalf("unreadable marker was treated as absent or finalized: %+v %v", receipt, err)
			}
			if completed, err := ledger.Complete(ctx, creator, digest); completed != nil || err == nil {
				t.Fatalf("unknown publication minted completion: %+v %v", completed, err)
			}
			if record, err := ledger.ReadCompletion(ctx, reserved.Attempt.ExecutionID); record != nil ||
				!errors.Is(err, artifactmanifest.ErrMissing) {
				t.Fatalf("failed verification appended completion: %+v %v", record, err)
			}
			// A later explicit read reconciles the already-existing marker.
			ledger.store = artifactmanifest.NewStore(backend)
			completed, err := ledger.Complete(ctx, creator, digest)
			if err != nil {
				t.Fatal(err)
			}
			original := string(completed.CompletionBytes)
			ledger.store = artifactmanifest.NewStore(unknownMarkerBackend{Backend: backend, failure: control.err})
			source := executionattempt.Source{ExecutionID: reserved.Attempt.ExecutionID, ManifestDigest: digest}
			if decision, reused, err := ledger.Reuse(ctx, binding, source); decision != nil || reused != nil || err == nil {
				t.Fatalf("unknown source publication authorized reuse: %+v %+v %v", decision, reused, err)
			}
			record, err := ledger.ReadCompletion(ctx, source.ExecutionID)
			if err != nil || string(record.Bytes) != original {
				t.Fatal("unknown-publication rejection changed existing completion")
			}
			keys, truncated, err := ledger.records.ListUnverified(ctx)
			if err != nil || truncated || len(keys) != 2 {
				t.Fatalf("failed reuse appended a decision: %v truncated=%v %v", keys, truncated, err)
			}
		})
	}
}
