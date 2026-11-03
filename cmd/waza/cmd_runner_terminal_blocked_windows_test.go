package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestRunnerTerminalWindowsFullUndrainedStdoutPipe(t *testing.T) {
	binary := buildRecoveryCLI(t)
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	var read, write windows.Handle
	require.NoError(t, windows.CreatePipe(&read, &write, nil, 4096))
	reader := os.NewFile(uintptr(read), "runner-terminal-blocked-reader")
	childOutput := os.NewFile(uintptr(write), "runner-terminal-blocked-stdout")
	require.NotNil(t, reader)
	require.NotNil(t, childOutput)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	t.Cleanup(func() { _ = childOutput.Close() })
	filled := fillRunnerWindowsStdout(t, read, write)
	mode := uint32(windows.PIPE_WAIT)
	require.NoError(t, windows.SetNamedPipeHandleState(write, &mode, nil, nil))
	require.Equal(t, mode, runnerWindowsPipeState(t, write)&windows.PIPE_NOWAIT)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "runner", "run")
	command.Dir = t.TempDir()
	command.Stdin = bytes.NewReader(request)
	command.Stdout = childOutput
	var stderr bytes.Buffer
	command.Stderr = &stderr
	started := time.Now()
	waitErr := command.Run()
	elapsed := time.Since(started)
	require.NoError(t, ctx.Err(), "watchdog cleanup is not a natural exit: %v; %s", waitErr, stderr.String())
	exit, ok := errors.AsType[*exec.ExitError](waitErr)
	require.True(t, ok, "%v: %s", waitErr, stderr.String())
	require.Equal(t, 2, exit.ExitCode(), "stderr: %s", stderr.String())
	require.GreaterOrEqual(t, elapsed, 5*time.Second)
	require.Less(t, elapsed, 12*time.Second)
	require.Contains(t, stderr.String(), "i/o timeout")
	require.Equal(t, uint32(len(filled)), runnerWindowsPipeOccupancy(t, read),
		"the full pipe must remain undrained through natural process exit")
	require.Equal(t, mode, runnerWindowsPipeState(t, write)&windows.PIPE_NOWAIT)
	require.NoError(t, childOutput.Close())
	output, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, filled, output, "failed delivery must not append a fallback")
}
