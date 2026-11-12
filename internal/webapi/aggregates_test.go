package webapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/stretchr/testify/require"
)

func TestAggregateAPIRejectsInvalidInputBeforeOpeningProvider(t *testing.T) {
	mux := http.NewServeMux()
	var calls atomic.Int32
	RegisterAggregateRoutesWithSources(mux, func() (manifestaggregate.Sources, func() error, error) {
		calls.Add(1)
		return manifestaggregate.Sources{}, nil, errors.New("private provider error")
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	call := func(input string) (int, string) {
		t.Helper()
		response, err := server.Client().Post(server.URL+"/api/v1/aggregates", "application/json", strings.NewReader(input))
		require.NoError(t, err)
		defer func() { require.NoError(t, response.Body.Close()) }()
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response.StatusCode, string(body)
	}
	for _, input := range []string{
		`null`, `[]`, `[{"manifest_sha256":"bad"}]`,
		`[{"manifest_sha256":"` + strings.Repeat("a", 64) + `","evidence_sha256":null}]`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		status, body := call(input)
		require.Equal(t, http.StatusBadRequest, status)
		require.JSONEq(t, `{"error":"invalid_aggregate_request"}`, body)
	}
	require.Zero(t, calls.Load())
	valid := `[{"manifest_sha256":"` + strings.Repeat("a", 64) + `"}]`
	for _, input := range []string{
		valid,
		valid + strings.Repeat(" ", artifactmanifest.MaxManifestBytes-len(valid)),
	} {
		status, body := call(input)
		require.Equal(t, http.StatusServiceUnavailable, status)
		require.NotContains(t, body, "private")
		require.JSONEq(t, `{"error":"aggregate_store_unavailable"}`, body)
	}
	require.EqualValues(t, 2, calls.Load(), "the exact existing byte limit remains admitted")
}
