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
