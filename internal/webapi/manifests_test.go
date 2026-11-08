package webapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/stretchr/testify/require"
)

type manifestReadFailure struct{ failure error }

func (manifestReadFailure) Create(context.Context, string, []byte) (bool, error) {
	return false, errors.New("unexpected create in read-only inspection")
}

func (b manifestReadFailure) Get(_ context.Context, key string, _ int64) ([]byte, error) {
	if strings.HasPrefix(key, "manifest-") {
		return nil, b.failure
	}
	return nil, artifactmanifest.ErrMissing
}

func TestManifestInspectionLogsMixedFailuresWithoutExposingOperatorDetails(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		cause  error
		status int
	}{
		{"missing", artifactmanifest.ErrMissing, http.StatusNotFound},
		{"corrupt", artifactmanifest.ErrCorrupt, http.StatusConflict},
		{"storage", errors.New("storage unavailable"), http.StatusServiceUnavailable},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "operator.log")
			sink, err := os.Create(path)
			require.NoError(t, err)
			defer func() { require.NoError(t, sink.Close()) }()
			logger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(sink, nil)))
			defer slog.SetDefault(logger)
			operatorDetail := "injected authentication failure /synthetic/private-store"
			store := artifactmanifest.NewStore(manifestReadFailure{
				failure: errors.Join(scenario.cause, errors.New(operatorDetail)),
			})
			mux := http.NewServeMux()
			RegisterManifestRoutesWithInspector(mux, store)
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/manifests/"+strings.Repeat("a", 64), nil))
			require.Equal(t, scenario.status, recorder.Code)
			var receipt artifactmanifest.Receipt
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &receipt))
			require.NotEmpty(t, receipt.Diagnostics)
			require.NotContains(t, recorder.Body.String(), operatorDetail)
			logged, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(logged), "immutable manifest inspection failed")
			require.Contains(t, string(logged), operatorDetail)
		})
	}
}

type manifestMarkerFailure struct {
	artifactmanifest.Backend
	key     string
	failure error
}

func (b manifestMarkerFailure) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	if key == b.key {
		return nil, b.failure
	}
	return b.Backend.Get(ctx, key, limit)
}

func TestManifestInspectionUnknownMarkerDoesNotClaimAbsence(t *testing.T) {
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, backend.Close()) }()
	store := artifactmanifest.NewStore(backend)
	wire, err := os.ReadFile("../artifactmanifest/testdata/publication-manifest.json")
	require.NoError(t, err)
	m, err := artifactmanifest.Parse(wire)
	require.NoError(t, err)
	terminal, err := os.ReadFile("../artifactmanifest/testdata/publication-terminal.json")
	require.NoError(t, err)
	digest, err := store.Create(context.Background(), wire)
	require.NoError(t, err)
	require.NoError(t, store.Put(context.Background(), m.Artifacts[0], terminal))
	before, err := store.Finalize(context.Background(), digest)
	require.NoError(t, err)
	require.Equal(t, "finalized", before.Publication)
	require.Equal(t, "verified", before.Integrity)
	for _, scenario := range []struct {
		name   string
		cause  error
		status int
	}{
		{"permission", os.ErrPermission, http.StatusServiceUnavailable},
		{"mixed-missing", errors.Join(artifactmanifest.ErrMissing, os.ErrPermission), http.StatusNotFound},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			faulty := artifactmanifest.NewStore(manifestMarkerFailure{
				Backend: backend, key: "final-" + digest, failure: scenario.cause,
			})
			mux := http.NewServeMux()
			RegisterManifestRoutesWithInspector(mux, faulty)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/manifests/"+digest, nil))
			require.Equal(t, scenario.status, response.Code)
			var receipt artifactmanifest.Receipt
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &receipt))
			require.Equal(t, "unknown", receipt.Publication)
			require.Equal(t, "failed", receipt.Integrity)
			require.Equal(t, before.Attempt, receipt.Attempt)
			require.Equal(t, before.Artifacts, receipt.Artifacts, "independently verified content remains observed")
			require.NotEmpty(t, receipt.Diagnostics)
			completed, err := faulty.ReadCompleted(context.Background(), digest)
			require.Error(t, err)
			require.Nil(t, completed)
		})
	}
}
