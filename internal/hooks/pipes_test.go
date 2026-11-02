package hooks

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHookPipeHelperProcess is not a test on its own: hook commands re-invoke
// the test binary with this test selected and an action after "--".
func TestHookPipeHelperProcess(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "hold":
		// Keep the inherited stdout/stderr open.
		time.Sleep(2 * time.Minute)
	case "spawn":
		// Start a descendant that inherits this hook's output pipes, then
		// either exit at once or keep running until canceled.
		child := exec.Command(os.Args[0], "-test.run=^TestHookPipeHelperProcess$", "--", "hold")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		require.NoError(t, child.Start())
		require.NoError(t, os.WriteFile(args[2], []byte("started"), 0o600))
		if args[1] == "wait" {
			time.Sleep(2 * time.Minute)
		}
	default:
		t.Fatalf("unknown hook helper action %q", args[0])
	}
}

func pipeHookCommand(t *testing.T, mode, marker string) string {
	t.Helper()
	parts := []string{os.Args[0], "-test.run=^TestHookPipeHelperProcess$", "--", "spawn", mode, marker}
	for _, part := range parts {
		if strings.ContainsAny(part, " \t") {
			t.Skipf("hook command argument %q contains whitespace", part)
		}
	}
	return strings.Join(parts, " ")
}

func shortWaitDelay(t *testing.T) {
	t.Helper()
	previous := hookWaitDelay
	hookWaitDelay = 500 * time.Millisecond
	t.Cleanup(func() { hookWaitDelay = previous })
}

// A canceled hook whose descendant still holds the output pipes must return
// within the context deadline plus hookWaitDelay, not when the descendant exits.
func TestExecuteCanceledHookWithDescendantHoldingPipesReturnsPromptly(t *testing.T) {
	shortWaitDelay(t)
	marker := t.TempDir() + string(os.PathSeparator) + "started"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- (&Runner{}).Execute(ctx, "after_run", []HookConfig{{Command: pipeHookCommand(t, "wait", marker), ErrorOnFail: true}})
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 30*time.Second, 20*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.Error(t, err, "a canceled hook is a failure")
		require.Less(t, time.Since(start), 30*time.Second)
	case <-time.After(20 * time.Second):
		require.FailNow(t, "canceled hook did not return while a descendant held its pipes")
	}
}

// A hook that exits successfully but leaves a descendant holding its output
// is not blocked by it and is not reported as a failure.
func TestExecuteSuccessfulHookWithBackgroundDescendantReturns(t *testing.T) {
	shortWaitDelay(t)
	marker := t.TempDir() + string(os.PathSeparator) + "started"
	done := make(chan error, 1)
	go func() {
		done <- (&Runner{}).Execute(context.Background(), "after_run", []HookConfig{{Command: pipeHookCommand(t, "exit", marker), ErrorOnFail: true}})
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
		require.FileExists(t, marker)
	case <-time.After(20 * time.Second):
		require.FailNow(t, "hook did not return while a background descendant held its pipes")
	}
}
