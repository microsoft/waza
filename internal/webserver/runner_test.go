package webserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunnerRoutesRegisteredOnPublicServer(t *testing.T) {
	server, err := New(Config{ResultsDir: t.TempDir(), Lab: true, NoBrowser: true})
	require.NoError(t, err)
	for _, path := range []string{"/api/v1/runner/capabilities", "/api/v1/lab/runner-results"} {
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, w.Code, path)
	}
}
