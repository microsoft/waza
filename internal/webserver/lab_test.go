package webserver

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDashboardURL(t *testing.T) {
	for _, test := range []struct {
		name string
		port int
		lab  bool
		url  string
	}{
		{"classic", 3000, false, "http://localhost:3000"},
		{"lab", 3000, true, "http://localhost:3000/#/lab"},
		{"custom port", 4188, true, "http://localhost:4188/#/lab"},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv, err := New(Config{Port: test.port, Lab: test.lab, ResultsDir: t.TempDir(), NoBrowser: true})
			require.NoError(t, err)
			require.Equal(t, test.url, srv.dashboardURL())
			require.Equal(t, "127.0.0.1:"+strconv.Itoa(test.port), srv.srv.Addr)
			for _, path := range []string{"/", "/lab", "/api/v1/lab/runs"} {
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				require.Equal(t, http.StatusOK, rec.Code)
				require.Empty(t, rec.Header().Get("Location"), "root must not redirect hash routes")
			}
		})
	}
}
