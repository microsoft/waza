//go:build !windows

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const runInterruptHelperEnv = "WAZA_RUN_INTERRUPT_HELPER_SPEC"

// TestRunCommandSIGINTDuringBeforeRunHelperProcess is the child side of
// TestRunCommandSIGINTDuringBeforeRun.
func TestRunCommandSIGINTDuringBeforeRunHelperProcess(t *testing.T) {
	spec := os.Getenv(runInterruptHelperEnv)
	if spec == "" {
		return
	}
	resetRunGlobals()
	cmd := newRunCommand()
	cmd.SetArgs([]string{spec, "--context-dir", filepath.Dir(spec), "--output", filepath.Join(filepath.Dir(spec), "results.json")})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		os.Exit(4)
	}
	os.Exit(0)
}

// A real SIGINT while before_run is still running must stop the setup hook,
// skip admission and engine startup, and still run after_run exactly once.
func TestRunCommandSIGINTDuringBeforeRun(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "sleep-started")
	spec, log := writeInterruptSuite(t, dir, "    - command: '"+cliHookCommand(t, "sleep", marker)+"'\n")

	child := exec.Command(os.Args[0], "-test.run=^TestRunCommandSIGINTDuringBeforeRunHelperProcess$")
	child.Env = append(os.Environ(), runInterruptHelperEnv+"="+spec)
	require.NoError(t, child.Start())
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()

	require.Eventually(t, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}, 30*time.Second, 20*time.Millisecond, "before_run never started")
	require.NoError(t, child.Process.Signal(os.Interrupt))

	select {
	case err := <-done:
		require.NoError(t, err, "the run must exit cleanly with a canceled error after SIGINT")
	case <-time.After(30 * time.Second):
		_ = child.Process.Kill()
		require.FailNow(t, "run did not stop promptly after SIGINT")
	}
	requireCLIHookLog(t, log)
	require.NoFileExists(t, filepath.Join(dir, "results.json"))
}
