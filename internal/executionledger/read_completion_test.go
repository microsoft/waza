package executionledger

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
)

func TestReadCompletionDistinguishesMissingAndInvalidWithoutHydration(t *testing.T) {
	ctx := context.Background()
	ledger, _, directory, binding := ownerFixture(t)
	creator, reserved := reserveFixture(t, ledger, binding)
	id := reserved.Attempt.ExecutionID
	if got, err := ledger.ReadCompletion(ctx, id); got != nil || !errors.Is(err, artifactmanifest.ErrMissing) {
		t.Fatalf("unfinished reservation became completion: %+v %v", got, err)
	}
	digest := ownerPublication(t, ledger, binding, true)
	completed, err := ledger.Complete(ctx, creator, digest)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ledger.ReadCompletion(ctx, id)
	if err != nil || record.Completion.ManifestDigest != digest ||
		record.Digest != artifactmanifest.Hash(completed.CompletionBytes) || !bytes.Equal(record.Bytes, completed.CompletionBytes) {
		t.Fatalf("typed completion lookup invented/lost source reference: %+v %v", record, err)
	}
	record.Bytes[0] = 'X'
	again, err := ledger.ReadCompletion(ctx, id)
	if err != nil || !bytes.Equal(again.Bytes, completed.CompletionBytes) {
		t.Fatalf("returned completion bytes mutated storage: %+v %v", again, err)
	}
	if _, err := ledger.ReadCompletion(ctx, "../escape"); !errors.Is(err, artifactmanifest.ErrInvalid) {
		t.Fatalf("completion lookup accepted arbitrary key: %v", err)
	}
	original := ledger.records
	ioError := errors.New("completion lookup I/O failed")
	differentID := "exec-" + strings.Repeat("d", 64)
	if differentID == id {
		differentID = "exec-" + strings.Repeat("e", 64)
	}
	for _, scenario := range []struct {
		name string
		data []byte
		err  error
		want error
	}{
		{"io", nil, ioError, ioError},
		{"invalid", []byte("{}"), nil, ErrInvalid},
		{"noncanonical", append(bytes.Clone(completed.CompletionBytes), '\n'), nil, ErrContext},
		{"wrong-key", bytes.Replace(completed.CompletionBytes, []byte(id), []byte(differentID), 1), nil, ErrContext},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ledger.records = recordFault{records: original, read: func(context.Context, string) ([]byte, error) {
				return scenario.data, scenario.err
			}}
			if got, err := ledger.ReadCompletion(ctx, id); got != nil || !errors.Is(err, scenario.want) || errors.Is(err, artifactmanifest.ErrMissing) {
				t.Fatalf("corrupt/I/O completion was repaired or treated missing: %+v %v", got, err)
			}
		})
	}
	ledger.records = original
	if err := os.Chmod(filepath.Join(directory, "final-"+digest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, "final-"+digest)); err != nil {
		t.Fatal(err)
	}
	present, err := ledger.ReadCompletion(ctx, id)
	if err != nil || present == nil {
		t.Fatalf("missing referenced final marker erased completion presence: %+v %v", present, err)
	}
	if source, err := ledger.ReadCompleted(ctx, executionattempt.Source{ExecutionID: id, ManifestDigest: present.Completion.ManifestDigest}); source != nil || !errors.Is(err, artifactmanifest.ErrMissing) {
		t.Fatalf("missing final marker unexpectedly verified: %+v %v", source, err)
	}
	// Callers retain the independently discovered record. Missing content above
	// is not an absent completion and must not authorize before-unit recovery.
	if present.Completion.ManifestDigest != digest {
		t.Fatal("verification failure changed retained completion reference")
	}
}
