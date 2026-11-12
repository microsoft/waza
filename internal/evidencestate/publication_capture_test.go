package evidencestate

import (
	"context"
	"testing"
)

type publicationCaptureBackend struct {
	*onceReadBackend
}

func (b *publicationCaptureBackend) Create(ctx context.Context, key string, data []byte) (bool, error) {
	return b.Backend.Create(ctx, key, data)
}

func TestPublishAdmitsOriginalSourcesOnlyOnce(t *testing.T) {
	backend, _ := localStore(t)
	d, _, artifacts, origin := sourceFixture(t)
	publishOrigin(t, backend, origin, artifacts)
	digest, err := Digest(d)
	if err != nil {
		t.Fatal(err)
	}
	capture := &publicationCaptureBackend{onceReadBackend: &onceReadBackend{
		Backend: backend, reads: make(map[string]int), newlyPublished: "blob-" + digest,
	}}
	receipt, err := Publish(context.Background(), capture, d)
	if err != nil || receipt.Integrity != "verified" || receipt.Document == nil {
		t.Fatalf("publication reread its captured source: %+v, %v", receipt, err)
	}
	if len(capture.reads) < 4 {
		t.Fatal("publication did not actually read source manifest, terminal, final marker and derived blob")
	}
	for key, count := range capture.reads {
		want := 1
		if key == capture.newlyPublished {
			want = 2 // Actual Store.Put verification and post-publication blob read.
		}
		if count != want {
			t.Fatalf("unexpected read count for %s: %d, want %d", key, count, want)
		}
	}
}
