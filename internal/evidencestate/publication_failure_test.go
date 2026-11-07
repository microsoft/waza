package evidencestate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

type publicationFailureBackend struct {
	artifactmanifest.Backend
	target      string
	failCreate  bool
	targetReads int
}

func (b *publicationFailureBackend) Create(ctx context.Context, key string, data []byte) (bool, error) {
	if key == b.target && b.failCreate {
		return false, errors.New("injected write failure /private/operator/store")
	}
	return b.Backend.Create(ctx, key, data)
}

func (b *publicationFailureBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	if key == b.target {
		b.targetReads++
		if b.targetReads > 1 {
			return nil, errors.New("injected publication read failure /private/operator/store")
		}
	}
	return b.Backend.Get(ctx, key, limit)
}

func TestPublicationWriteAndPostPublicationReadFailuresWithholdEvidence(t *testing.T) {
	for _, failCreate := range []bool{false, true} {
		backend, _ := localStore(t)
		d, _, artifacts, origin := sourceFixture(t)
		publishOrigin(t, backend, origin, artifacts)
		digest, err := Digest(d)
		if err != nil {
			t.Fatal(err)
		}
		fault := &publicationFailureBackend{Backend: backend, target: "blob-" + digest, failCreate: failCreate}
		receipt, err := Publish(context.Background(), fault, d)
		if err == nil || receipt.Integrity != "failed" || receipt.Document != nil || receipt.EvidenceSHA256 != digest {
			t.Fatalf("publication failure became successful evidence: %+v, %v", receipt, err)
		}
		if strings.Contains(string(wire(t, receipt)), "/private/") {
			t.Fatal("operator-only error text leaked into receipt")
		}
		persisted, readErr := backend.Get(context.Background(), fault.target, MaxDocumentBytes)
		if failCreate {
			if !errors.Is(readErr, artifactmanifest.ErrMissing) {
				t.Fatal("failed exclusive creation produced a sidecar")
			}
		} else {
			if fault.targetReads != 2 || readErr != nil || artifactmanifest.Hash(persisted) != digest {
				t.Fatal("post-publication failure erased or misrepresented the persisted blob")
			}
		}
		current, err := artifactmanifest.NewStore(backend).Inspect(context.Background(), d.OriginManifest)
		if err != nil || current.Publication != "finalized" || current.Integrity != "verified" {
			t.Fatal("failed derived publication changed the original source")
		}
	}
}
