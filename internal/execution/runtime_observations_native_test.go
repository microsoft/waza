//go:build (linux && (amd64 || arm64)) || (darwin && (amd64 || arm64)) || (windows && (amd64 || arm64))

package execution

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/embedded"
	"github.com/stretchr/testify/require"
)

func TestOwnedRuntimeNativeNoSendControl(t *testing.T) {
	if os.Getenv("WAZA_NATIVE_LIFECYCLE_SMOKE") != "1" {
		t.Skip("requires an explicit native no-Send allocation; ordinary rejecting-runtime gates do not launch this fixture")
	}
	if os.Getenv("WAZA_NATIVE_LIFECYCLE_CHILD") == "1" {
		runOwnedRuntimeNativeNoSendControl(t)
		return
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	working, err := os.Getwd()
	require.NoError(t, err)
	repository, err := filepath.EvalSymlinks(filepath.Join(working, "../.."))
	require.NoError(t, err)
	relative, err := filepath.Rel(repository, root)
	require.NoError(t, err)
	require.False(t, filepath.IsLocal(relative), "native control must not inherit repository ancestry")
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second-startupCleanupTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestOwnedRuntimeNativeNoSendControl$", "-test.v")
	command.WaitDelay = startupCleanupTimeout
	command.Dir = root
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	for _, assignment := range os.Environ() {
		name, _, present := strings.Cut(assignment, "=")
		require.True(t, present)
		switch strings.ToUpper(name) {
		case "PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT":
			command.Env = append(command.Env, assignment)
		}
	}
	command.Env = append(command.Env,
		"WAZA_NATIVE_LIFECYCLE_CHILD=1", "WAZA_NATIVE_LIFECYCLE_SMOKE=1", "NO_COLOR=1",
		"HOME="+root, "USERPROFILE="+root, "COPILOT_HOME="+filepath.Join(root, "copilot"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"TMPDIR="+root, "TMP="+root, "TEMP="+root,
	)
	setup := time.Now()
	require.NoError(t, command.Start())
	t.Logf("native control scope=test-worker setup_elapsed=%s post_start_ctx_err=%v", time.Since(setup), ctx.Err())
	waitErr := command.Wait()
	t.Logf("native control scope=test-worker wait_elapsed=%s post_wait_ctx_err=%v", time.Since(setup), ctx.Err())
	t.Logf("native child stdout after joined Wait:\n%s", stdout.String())
	require.NoError(t, waitErr, "joined native child stderr: %s", stderr.String())
	require.NoError(t, ctx.Err())
}

func runOwnedRuntimeNativeNoSendControl(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	working, err := os.Getwd()
	require.NoError(t, err)
	working, err = filepath.EvalSymlinks(working)
	require.NoError(t, err)
	require.Equal(t, working, os.Getenv("HOME"))
	require.Empty(t, os.Getenv("COPILOT_CLI_PATH"), "only this explicit embedded native fixture bypasses the rejecting path")
	for _, name := range []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "COPILOT_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		require.Empty(t, os.Getenv(name))
	}
	var providerRequests, permissionRequests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer provider.Close()
	require.NoError(t, os.Setenv("COPILOT_BASE_URL", provider.URL))
	require.NoError(t, os.Setenv("COPILOT_PROVIDER", "openai"))
	setup := time.Now()
	path, err := embedded.Path()
	require.NoError(t, err)
	require.FileExists(t, path)
	capture := NewOwnedRuntimeObservations()
	var client *copilot.Client
	engine := NewCopilotEngineBuilder("offline-smoke-test", &CopilotEngineBuilderOptions{
		DisableAmbientDiscovery: true, DisableCommandMocks: true, RuntimeObservations: capture,
		NewCopilotClient: func(options *copilot.ClientOptions) CopilotClient {
			options.Connection = copilot.StdioConnection{Path: path}
			options.UseLoggedInUser = copilot.Bool(false)
			options.WorkingDirectory = working
			options.BaseDirectory = filepath.Join(working, "copilot")
			options.Mode = copilot.ModeEmpty
			client = copilot.NewClient(options)
			return &copilotClientWrapper{inner: client}
		},
	}).Build()
	defer func() {
		stop := time.Now()
		err := engine.Shutdown(context.WithoutCancel(ctx))
		t.Logf("native control scope=sdk-client shutdown_elapsed=%s ctx_err=%v", time.Since(stop), ctx.Err())
		require.NoError(t, err)
		snapshot, err := capture.Seal()
		require.NoError(t, err)
		require.True(t, snapshot.OwnedEngineBound)
		require.Equal(t, []string{"client-start-invoked", "client-start-returned", "client-stop-invoked", "client-stop-returned"}, runtimeOperations(snapshot))
		require.Equal(t, "success", snapshot.Observations[1].Result)
		require.Equal(t, "success", snapshot.Observations[3].Result)
		require.Equal(t, "unknown", snapshot.ProcessExit, "SDK Stop return is not exposed SDK Cmd.Wait evidence")
		require.Zero(t, providerRequests.Load(), "no model request is permitted")
		require.Zero(t, permissionRequests.Load(), "no tool or ambient policy request is permitted")
		t.Log("native owned startup/stop observed; sdk process_exit=unknown; provider_requests=0; no Send")
	}()
	require.NoError(t, engine.Initialize(ctx))
	t.Logf("native control scope=sdk-client setup_elapsed=%s post_start_ctx_err=%v", time.Since(setup), ctx.Err())
	require.NotNil(t, client)
	pong, err := client.Ping(ctx, "owned lifecycle native no-Send control")
	require.NoError(t, err)
	require.NotNil(t, pong)
	status, err := client.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, "1.0.105", status.Version)
	session, err := client.CreateSession(ctx, &copilot.SessionConfig{
		Model: "offline-smoke-test", Provider: &copilot.ProviderConfig{Type: "openai", BaseURL: provider.URL},
		WorkingDirectory: working, ConfigDirectory: filepath.Join(working, "copilot"),
		EnableConfigDiscovery: copilot.Bool(false), EnableOnDemandInstructionDiscovery: copilot.Bool(false),
		EnableFileHooks: copilot.Bool(false), EnableHostGitOperations: copilot.Bool(false),
		EnableSessionStore: copilot.Bool(false), EnableSkills: copilot.Bool(false),
		SkipEmbeddingRetrieval: copilot.Bool(true), AvailableTools: []string{},
		OnPermissionRequest: func(copilot.PermissionRequest, copilot.PermissionInvocation) (rpc.PermissionDecision, error) {
			permissionRequests.Add(1)
			return &rpc.PermissionDecisionDeniedByRules{}, nil
		},
	})
	require.NoError(t, err)
	defer func() {
		abandoned, retired, detachErr := disconnectBounded(ctx,
			&copilotSessionWrapper{inner: session, client: client}, engine.forceStopTransport, capture)
		deleteErr := client.DeleteSession(ctx, session.SessionID)
		require.False(t, abandoned)
		require.False(t, retired)
		require.NoError(t, errors.Join(detachErr, deleteErr))
	}()
	require.NotEmpty(t, session.SessionID)
	_, err = session.GetEvents(ctx)
	require.NoError(t, err)
	require.NoError(t, session.Abort(ctx))
	t.Log("native session.abort RPC returned; no model acknowledgement or SDK process-exit inference")
	require.NoError(t, ctx.Err())
}
