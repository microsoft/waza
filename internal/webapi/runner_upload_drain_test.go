//go:build linux || darwin

package webapi

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
)

func TestRunnerRejectedTransportNeverDrainsAnUnfinishedBody(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprint(oversized), func(t *testing.T) {
			dir := t.TempDir()
			mux := http.NewServeMux()
			RegisterRunnerRoutes(mux, dir)
			server := httptest.NewServer(mux)
			defer server.Close()
			connection, err := net.Dial("tcp", server.Listener.Addr().String())
			require.NoError(t, err)
			defer connection.Close() //nolint:errcheck
			require.NoError(t, connection.SetDeadline(time.Now().Add(5*time.Second)))
			contentType, body := "text/plain", "{"
			if oversized {
				contentType = "application/json"
				body = strings.Repeat("x", runnercontract.MaxRequestBytes+1)
			}
			// Leave transport deliberately unfinished after the rejection point.
			_, err = fmt.Fprintf(connection, "POST /api/v1/runner/run HTTP/1.1\r\nHost: localhost\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n%s",
				contentType, len(body)+100, body)
			require.NoError(t, err)
			response, err := http.ReadResponse(bufio.NewReader(connection), nil)
			require.NoError(t, err, "rejected request must not wait for the remaining transport")
			defer response.Body.Close() //nolint:errcheck
			wire, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			result, err := runnercontract.ParseResult(wire)
			require.NoError(t, err)
			require.Equal(t, runnercontract.InvalidRequest, result.Outcome)
			if oversized {
				saved, err := readRunnerResults(dir)
				require.NoError(t, err)
				require.Len(t, saved, 1)
			} else {
				files, err := os.ReadDir(dir)
				require.NoError(t, err)
				require.Empty(t, files, "header rejection precedes runner admission")
			}
		})
	}
}
