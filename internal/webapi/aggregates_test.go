package webapi

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/stretchr/testify/require"
)

func TestAggregateAPIRejectsInvalidInputBeforeOpeningProvider(t *testing.T) {
	mux := http.NewServeMux()
	calls := 0
	RegisterAggregateRoutesWithSources(mux, func() (manifestaggregate.Sources, func() error, error) {
		calls++
		return manifestaggregate.Sources{}, nil, errors.New("private provider error")
	})
	for _, input := range []string{
		`null`, `[]`, `[{"manifest_sha256":"bad"}]`,
		`[{"manifest_sha256":"` + strings.Repeat("a", 64) + `","evidence_sha256":null}]`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", strings.NewReader(input)))
		require.Equal(t, http.StatusBadRequest, response.Code)
		require.JSONEq(t, `{"error":"invalid_aggregate_request"}`, response.Body.String())
	}
	require.Zero(t, calls)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates",
		bytes.NewBufferString(`[{"manifest_sha256":"`+strings.Repeat("a", 64)+`"}]`)))
	require.Equal(t, 1, calls)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.NotContains(t, response.Body.String(), "private")
	require.JSONEq(t, `{"error":"aggregate_store_unavailable"}`, response.Body.String())
}
