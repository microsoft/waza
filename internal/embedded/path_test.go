//go:build (linux && (amd64 || arm64)) || (darwin && (amd64 || arm64)) || (windows && (amd64 || arm64))

package embedded

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
)

func TestPath(t *testing.T) {
	// The SDK caches installation process-wide, so isolate each scenario.
	if scenario := os.Getenv("WAZA_EMBEDDED_TEST"); scenario != "" {
		path, err := Path()
		if scenario == "unwritable-cache" {
			require.ErrorContains(t, err, "installing embedded Copilot CLI failed")
			require.Empty(t, path)
			return
		}
		require.NoError(t, err)
		require.FileExists(t, path)
		require.FileExists(t, filepath.Join(filepath.Dir(path), "sdk", "index.js"))
		again, err := Path()
		require.NoError(t, err)
		require.Equal(t, path, again)

		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancel()
		client := copilot.NewClient(&copilot.ClientOptions{
			Connection:      copilot.StdioConnection{Path: path},
			UseLoggedInUser: copilot.Bool(false),
		})
		t.Cleanup(func() { require.NoError(t, client.Stop()) })
		require.NoError(t, client.Start(ctx))
		pong, err := client.Ping(ctx, "embedded CLI smoke test")
		require.NoError(t, err)
		require.NotNil(t, pong)
		status, err := client.GetStatus(ctx)
		require.NoError(t, err)
		require.Equal(t, "1.0.85", status.Version)
		schemaBytes, err := os.ReadFile(filepath.Join(filepath.Dir(path), "schemas", "session-events.schema.json"))
		require.NoError(t, err)
		var schema struct {
			Definitions map[string]struct {
				Properties map[string]struct {
					Const json.RawMessage `json:"const"`
				} `json:"properties"`
			} `json:"definitions"`
		}
		require.NoError(t, json.Unmarshal(schemaBytes, &schema))
		require.JSONEq(t, `"factory"`, string(schema.Definitions["PermissionRequestFactory"].Properties["kind"].Const))

		var providerRequests atomic.Int32
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			providerRequests.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer provider.Close()
		session, err := client.CreateSession(ctx, &copilot.SessionConfig{
			Model:                 "offline-smoke-test",
			Provider:              &copilot.ProviderConfig{Type: "openai", BaseURL: provider.URL},
			WorkingDirectory:      t.TempDir(),
			EnableConfigDiscovery: copilot.Bool(false),
			AvailableTools:        []string{},
			OnPermissionRequest:   copilot.PermissionHandler.ApproveAll,
		})
		require.NoError(t, err)
		require.NotEmpty(t, session.SessionID)
		_, err = session.GetEvents(ctx)
		require.NoError(t, err)
		require.NoError(t, session.Disconnect())
		require.NoError(t, client.DeleteSession(ctx, session.SessionID))
		require.Zero(t, providerRequests.Load(), "no provider requests are permitted in this smoke test")
		t.Logf("bundled CLI %s: native ping/status/session.create/session.getMessages/session.detach/session.delete passed; schema kind=factory; provider requests=0", status.Version)
		return
	}

	for _, scenario := range []string{"install", "unwritable-cache"} {
		t.Run(scenario, func(t *testing.T) {
			home := t.TempDir()
			if scenario == "unwritable-cache" {
				// A file blocks cache creation on Windows as well as Unix.
				require.NoError(t, os.WriteFile(filepath.Join(home, "cache"), nil, 0600))
			}
			t.Setenv("COPILOT_HOME", home)
			t.Setenv("WAZA_EMBEDDED_TEST", scenario)
			t.Setenv("COPILOT_GITHUB_TOKEN", "")
			t.Setenv("GH_TOKEN", "")
			t.Setenv("GITHUB_TOKEN", "")
			t.Setenv("NO_COLOR", "1")
			executable, err := os.Executable()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, executable, "-test.run=^TestPath$", "-test.v").CombinedOutput()
			require.NoError(t, err, "%s", output)
			t.Logf("%s", output)
		})
	}
}
