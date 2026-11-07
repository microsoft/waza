package webapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/stretchr/testify/require"
)

func checkpointBlobFixture(t *testing.T, boundary string) (string, string, *artifactmanifest.Local, executioncheckpoint.Checkpoint) {
	t.Helper()
	directory := t.TempDir()
	store := filepath.Join(directory, ".waza-artifacts")
	require.NoError(t, os.Mkdir(store, 0o700))
	backend, err := artifactmanifest.OpenLocal(store)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	checkpoint, err := executioncheckpoint.Parse(checkpointCanonicalFixture(t))
	require.NoError(t, err)
	checkpoint.Boundary = boundary
	if boundary == executioncheckpoint.FinalizedUnit {
		checkpoint.CompletionDigest = strings.Repeat("e", 64)
		checkpoint.ManifestDigest = strings.Repeat("f", 64)
	}
	// Use the actual canonical writer. These references are authored declarations,
	// not authenticated reservations/completions or execution/recovery evidence.
	digest, err := executioncheckpoint.Put(context.Background(), backend, *checkpoint)
	require.NoError(t, err)
	return directory, digest, backend, *checkpoint
}

func inspectCheckpointHTTP(t *testing.T, mux *http.ServeMux, digest string, status int) CheckpointInspection {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/checkpoints/"+digest, nil))
	require.Equal(t, status, recorder.Code, recorder.Body.String())
	require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
	var result CheckpointInspection
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	require.Equal(t, digest, result.CheckpointSHA256)
	require.Equal(t, "canonical-declaration-only", result.Verification)
	require.Equal(t, "unavailable", result.Compatibility)
	require.Nil(t, result.RestoredUnits)
	require.Nil(t, result.RemainingWork)
	require.NotEmpty(t, result.Diagnostics)
	return result
}

func TestCheckpointAPIDeclaredBoundariesDoNotInventRecovery(t *testing.T) {
	for _, boundary := range []string{executioncheckpoint.BeforeUnit, executioncheckpoint.FinalizedUnit} {
		t.Run(boundary, func(t *testing.T) {
			directory, digest, _, checkpoint := checkpointBlobFixture(t, boundary)
			mux := http.NewServeMux()
			RegisterCheckpointRoutes(mux, directory, nil)
			result := inspectCheckpointHTTP(t, mux, digest, http.StatusOK)
			require.Equal(t, "blob-verified", result.Availability)
			require.Equal(t, &checkpoint, result.Checkpoint)
			require.Equal(t, "source_not_authenticated", result.Diagnostics[0].Code)
			wire, err := json.Marshal(result)
			require.NoError(t, err)
			require.NotContains(t, string(wire), directory)
			require.Contains(t, string(wire), `"restored_units":null`)
			require.Contains(t, string(wire), `"remaining_work":null`)

			entries, err := os.ReadDir(filepath.Join(directory, ".waza-artifacts"))
			require.NoError(t, err)
			for _, entry := range entries {
				require.False(t, strings.HasPrefix(entry.Name(), "final-"), "inspection must not finalize a declaration")
			}
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/checkpoints/"+digest, nil))
			require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
		})
	}
}

func TestCheckpointAPIRejectsInvalidInputBeforeStorage(t *testing.T) {
	opens := 0
	mux := http.NewServeMux()
	registerCheckpointRoutes(mux, checkpointBlobInspector{open: func() (artifactmanifest.Backend, func() error, error) {
		opens++
		return nil, nil, errors.New("must not open storage")
	}})
	for _, digest := range []string{"short", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("a", 65)} {
		result := inspectCheckpointHTTP(t, mux, digest, http.StatusBadRequest)
		require.Equal(t, "unavailable", result.Availability)
		require.Nil(t, result.Checkpoint)
		require.Equal(t, "invalid_digest", result.Diagnostics[0].Code)
	}
	require.Zero(t, opens)
}

func TestCheckpointAPIReadsCurrentContentAndRejectsMissingAndMalformedBlobs(t *testing.T) {
	directory, digest, backend, _ := checkpointBlobFixture(t, executioncheckpoint.BeforeUnit)
	mux := http.NewServeMux()
	RegisterCheckpointRoutes(mux, directory, nil)
	require.NotNil(t, inspectCheckpointHTTP(t, mux, digest, http.StatusOK).Checkpoint)
	path := filepath.Join(directory, ".waza-artifacts", "blob-"+digest)
	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.WriteFile(path, []byte("deliberate-corrupt-control"), 0o600))
	result := inspectCheckpointHTTP(t, mux, digest, http.StatusConflict)
	require.Nil(t, result.Checkpoint, "prior successful inspection must not survive a corrupt current read")
	require.Equal(t, "corrupt_object", result.Diagnostics[0].Code)
	missing := inspectCheckpointHTTP(t, mux, strings.Repeat("0", 64), http.StatusNotFound)
	require.Nil(t, missing.Checkpoint)
	require.Equal(t, "missing_object", missing.Diagnostics[0].Code)

	for _, data := range [][]byte{[]byte(`{"kind":"not-a-checkpoint"}`), append([]byte(" "), checkpointCanonicalFixture(t)...)} {
		other := artifactmanifest.Hash(data)
		created, err := backend.Create(context.Background(), "blob-"+other, data)
		require.NoError(t, err)
		require.True(t, created, "malformed control must have its own valid content-addressed object")
		result := inspectCheckpointHTTP(t, mux, other, http.StatusConflict)
		require.Nil(t, result.Checkpoint)
		require.Equal(t, "corrupt_object", result.Diagnostics[0].Code)
	}
}

func checkpointCanonicalFixture(t *testing.T) []byte {
	t.Helper()
	wire, err := os.ReadFile("../executioncheckpoint/testdata/before-unit.json")
	require.NoError(t, err)
	checkpoint, err := executioncheckpoint.Parse(wire)
	require.NoError(t, err)
	canonical, err := executioncheckpoint.Encode(*checkpoint)
	require.NoError(t, err)
	return canonical
}

func TestCheckpointAPIStorageAndCloseFailuresWithholdSource(t *testing.T) {
	directory, digest, backend, _ := checkpointBlobFixture(t, executioncheckpoint.BeforeUnit)
	privateError := errors.New("https://private-provider.example/secret " + directory)
	for _, tc := range []struct {
		name string
		open func() (artifactmanifest.Backend, func() error, error)
	}{
		{"open", func() (artifactmanifest.Backend, func() error, error) { return nil, nil, privateError }},
		{"close", func() (artifactmanifest.Backend, func() error, error) {
			return backend, func() error { return privateError }, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			registerCheckpointRoutes(mux, checkpointBlobInspector{open: tc.open})
			result := inspectCheckpointHTTP(t, mux, digest, http.StatusServiceUnavailable)
			require.Equal(t, "unavailable", result.Availability)
			require.Nil(t, result.Checkpoint)
			wire, err := json.Marshal(result)
			require.NoError(t, err)
			require.NotContains(t, string(wire), directory)
			require.NotContains(t, string(wire), "private-provider")
			require.Equal(t, "storage_error", result.Diagnostics[len(result.Diagnostics)-1].Code)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	inspector := checkpointBlobInspector{open: func() (artifactmanifest.Backend, func() error, error) {
		t.Fatal("canceled inspection opened storage")
		return nil, nil, nil
	}}
	result, err := inspector.inspect(ctx, digest)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result.Checkpoint)
	require.Equal(t, "inspection_canceled", result.Diagnostics[0].Code)
}

func TestCheckpointAPIHonorsDisabledAndUnsupportedProviders(t *testing.T) {
	directory, digest, _, _ := checkpointBlobFixture(t, executioncheckpoint.BeforeUnit)
	for _, tc := range []struct {
		name    string
		enabled bool
		status  int
	}{
		{"unsupported-active", true, http.StatusServiceUnavailable},
		{"explicitly-disabled", false, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			RegisterCheckpointRoutes(mux, directory, &projectconfig.StorageConfig{Enabled: tc.enabled, Provider: "unsupported"})
			result := inspectCheckpointHTTP(t, mux, digest, tc.status)
			if tc.enabled {
				require.Nil(t, result.Checkpoint, "active unsupported providers must not fall back to a valid local blob")
			} else {
				require.NotNil(t, result.Checkpoint)
			}
		})
	}
}
