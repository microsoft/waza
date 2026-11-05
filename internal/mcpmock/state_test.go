package mcpmock

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func attemptStateConfig(t *testing.T) *Config {
	t.Helper()
	cfg := &Config{Name: "counter", Tools: map[string]Tool{
		"read": {Responses: []Response{
			{Match: map[string]any{"id": "item"}, Sequence: []ResponseStep{
				{Return: map[string]any{"ordinal": 1}},
				{Return: map[string]any{"ordinal": 2}},
			}},
			{Match: map[string]any{"id": "item"}, Return: map[string]any{"fallback": true}},
		}},
		"write": {Responses: []Response{{Sequence: []ResponseStep{{Return: "written"}}}}},
	}}
	prepareAttemptState(t, cfg)
	return cfg
}

func prepareAttemptState(t *testing.T, cfg *Config) {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, PrepareSequenceState(cfg, root, "state"))
	require.NoError(t, root.Close())
}

func TestAttemptSequenceStateSurvivesServerRestarts(t *testing.T) {
	cfg := attemptStateConfig(t)
	args := map[string]any{"id": "item"}

	requireToolJSON(t, NewServer(cfg, nil), "read", args, `"ordinal":1`)
	requireToolJSON(t, NewServer(cfg, nil), "read", args, `"ordinal":2`)
	requireToolError(t, NewServer(cfg, nil), "read", args, "response sequence exhausted")
	requireToolJSON(t, NewServer(cfg, nil), "write", map[string]any{}, `"written"`)

	fresh := attemptStateConfig(t)
	requireToolJSON(t, NewServer(fresh, nil), "read", args, `"ordinal":1`)
}

func TestAttemptSequenceStateKeepsInMemoryControl(t *testing.T) {
	cfg := attemptStateConfig(t)
	legacy := *cfg
	legacy.StateDir = ""
	args := map[string]any{"id": "item"}

	requireToolJSON(t, NewServer(&legacy, nil), "read", args, `"ordinal":1`)
	requireToolJSON(t, NewServer(&legacy, nil), "read", args, `"ordinal":1`)
}

func TestAttemptSequenceStateReservesDistinctStepsAcrossServers(t *testing.T) {
	cfg := attemptStateConfig(t)
	results := make(chan string, 2)
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := NewServer(cfg, nil).call(context.Background(), "read", map[string]any{"id": "item"})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var got []string
	for result := range results {
		got = append(got, result)
	}
	require.ElementsMatch(t, []string{`{"ordinal":1}`, `{"ordinal":2}`}, got)
}

func TestAttemptSequenceStateCancellationConsumesStartedStep(t *testing.T) {
	cfg := &Config{Name: "inventory", Tools: map[string]Tool{
		"read": {Responses: []Response{{Sequence: []ResponseStep{
			{Error: "late failure", DelayMS: 1000},
			{Return: "recovered"},
		}}}},
	}}
	prepareAttemptState(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewServer(cfg, nil).call(ctx, "read", map[string]any{})
	require.ErrorContains(t, err, "canceled")
	requireToolJSON(t, NewServer(cfg, nil), "read", map[string]any{}, `"recovered"`)
}

func TestAttemptSequenceStateReportsUnavailableState(t *testing.T) {
	cfg := attemptStateConfig(t)
	require.NoError(t, os.RemoveAll(cfg.StateDir))
	requireToolError(t, NewServer(cfg, nil), "read", map[string]any{"id": "item"}, "reserving fault sequence step")
}

func TestPrepareSequenceStateRejectsUnsafeRoots(t *testing.T) {
	cfg := &Config{Name: "counter", Tools: map[string]Tool{}}
	require.ErrorContains(t, PrepareSequenceState(nil, nil, "state"), "requires a configuration")
	require.ErrorContains(t, PrepareSequenceState(cfg, nil, "state"), "absolute private path")
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	for _, name := range []string{"", ".", "..", "../state", filepath.Join("nested", "state"), root.Name()} {
		require.ErrorContains(t, PrepareSequenceState(cfg, root, name), "absolute private path")
		require.Empty(t, cfg.StateDir)
	}
	require.NoError(t, root.Mkdir("existing", 0700))
	require.ErrorContains(t, PrepareSequenceState(cfg, root, "existing"), "create mcp mock sequence state")
	require.Empty(t, cfg.StateDir)
}

func TestPrepareSequenceStateUsesRetainedParent(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "owned")
	require.NoError(t, os.Mkdir(path, 0700))
	root, err := os.OpenRoot(path)
	require.NoError(t, err)
	rootClosed := false
	t.Cleanup(func() {
		if !rootClosed {
			require.NoError(t, root.Close())
		}
	})
	moved := path + "-moved"
	renameErr := os.Rename(path, moved)
	if runtime.GOOS == "windows" && renameErr != nil {
		require.ErrorIs(t, renameErr, syscall.Errno(32), "only a Windows sharing violation is an expected retained-root pin")
		require.NoError(t, PrepareSequenceState(&Config{Name: "counter", Tools: map[string]Tool{
			"read": {Responses: []Response{{Sequence: []ResponseStep{{Return: "ok"}}}}},
		}}, root, "state"))
		_, err = root.Stat("state")
		require.NoError(t, err, "the pinned retained root must remain usable")
		require.DirExists(t, sequenceStateDir(filepath.Join(path, "state"), 0, 0), "the pinned retained root must remain usable")
		require.NoError(t, root.Close())
		rootClosed = true
		require.NoError(t, os.Rename(path, moved), "closing the owning root must release the Windows pin")
		require.DirExists(t, sequenceStateDir(filepath.Join(moved, "state"), 0, 0))
		require.NoError(t, os.Mkdir(path, 0700))
		sentinel := filepath.Join(path, "sentinel")
		require.NoError(t, os.WriteFile(sentinel, []byte("unrelated"), 0600))
		body, err := os.ReadFile(sentinel)
		require.NoError(t, err)
		require.Equal(t, "unrelated", string(body))
		return
	}
	require.NoError(t, renameErr)
	require.NoError(t, os.Mkdir(path, 0700))
	sentinel := filepath.Join(path, "sentinel")
	require.NoError(t, os.WriteFile(sentinel, []byte("unrelated"), 0600))

	cfg := &Config{Name: "counter", Tools: map[string]Tool{
		"read": {Responses: []Response{{Sequence: []ResponseStep{{Return: "ok"}}}}},
	}}
	require.NoError(t, PrepareSequenceState(cfg, root, "state"))
	require.DirExists(t, sequenceStateDir(filepath.Join(moved, "state"), 0, 0))
	require.NoDirExists(t, filepath.Join(path, "state"), "creation must not reopen the saved pathname")
	body, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "unrelated", string(body))
}
