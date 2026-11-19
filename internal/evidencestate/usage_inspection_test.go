package evidencestate

import (
	"context"
	"errors"
	"testing"
)

func TestInspectWithUsageKeepsByteIntegritySeparateFromSemanticFailure(t *testing.T) {
	backend, digest := usagePublication(t, []byte(`{"turns":null}`), Present, false)
	once := &onceReadBackend{Backend: backend, reads: make(map[string]int)}
	inspection, usage, err := InspectWithUsage(context.Background(), once, digest)
	if !errors.Is(err, ErrInvalid) || inspection.Integrity != "verified" || inspection.Document == nil ||
		usage.State != Unavailable || usage.DeclaredState != Present || usage.Usage != nil {
		t.Fatalf("usage error rewrote source integrity or admitted missing fields: %+v, %+v, %v", inspection, usage, err)
	}
	found := false
	for _, record := range inspection.Document.Categories {
		if record.Category == Usage {
			found = true
			if record.State != Present || record.ObservedCount == nil || *record.ObservedCount != 1 {
				t.Fatal("recorded declaration was rewritten into a semantic verdict")
			}
		}
	}
	if !found || len(once.reads) < 5 {
		t.Fatal("combined inspection did not exercise real source admission")
	}
}
