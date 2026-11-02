package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestRunnerTerminalWindowsFullUndrainedStdoutPipe(t *testing.T) {
	binary := buildRecoveryCLI(t)
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	reader, filler, err := os.Pipe()
	require.NoError(t, err)
	var command *exec.Cmd
	var waitDone chan error
	fillerDone := make(chan error, 1)
	t.Cleanup(func() {
		if command != nil && command.ProcessState == nil {
			_ = command.Process.Kill()
			<-waitDone
		}
		_ = reader.Close()
		_ = filler.Close()
		select {
		case <-fillerDone:
		case <-time.After(5 * time.Second):
			t.Error("pipe-filling writer did not stop after the reader closed")
		}
	})

	go func() {
		remaining := bytes.Repeat([]byte("f"), 2<<20)
		for len(remaining) != 0 {
			n, writeErr := filler.Write(remaining)
			remaining = remaining[n:]
			if writeErr != nil {
				fillerDone <- writeErr
				return
			}
			if n == 0 {
				fillerDone <- io.ErrShortWrite
				return
			}
		}
		fillerDone <- nil
	}()

	select {
	case err := <-fillerDone:
		require.NoError(t, err)
		t.Fatal("2 MiB unexpectedly fit in the undrained anonymous pipe")
	case <-time.After(100 * time.Millisecond):
	}

	var childHandle windows.Handle
	require.NoError(t, windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(filler.Fd()),
		windows.CurrentProcess(), &childHandle, 0, false, windows.DUPLICATE_SAME_ACCESS))
	childOutput := os.NewFile(uintptr(childHandle), "runner-terminal-blocked-stdout")
	require.NotNil(t, childOutput)
	t.Cleanup(func() { _ = childOutput.Close() })
	command = exec.Command(binary, "runner", "run")
	command.Dir = t.TempDir()
	command.Stdin = bytes.NewReader(request)
	command.Stdout = childOutput
	var stderr bytes.Buffer
	command.Stderr = &stderr
	started := time.Now()
	require.NoError(t, command.Start())
	waitDone = make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	require.NoError(t, childOutput.Close())
	select {
	case waitErr := <-waitDone:
		exit, ok := errors.AsType[*exec.ExitError](waitErr)
		require.True(t, ok, "%v: %s", waitErr, stderr.String())
		require.Equal(t, 2, exit.ExitCode(), "stderr: %s", stderr.String())
		require.Less(t, time.Since(started), 7*time.Second, "blocked stdout must not prevent the selected process exit")
		require.Contains(t, stderr.String(), "i/o timeout")
	case <-time.After(8 * time.Second):
		t.Fatal("runner process did not exit after its bounded terminal write")
	}
	select {
	case err := <-fillerDone:
		require.NoError(t, err)
		t.Fatal("the full output pipe drained or the filler write completed")
	default:
	}
}

func peekRunnerWindowsPipe(reader *os.File) (uint32, error) {
	var available uint32
	result, _, callErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe").Call(
		reader.Fd(), 0, 0, 0, uintptr(unsafe.Pointer(&available)), 0)
	if result == 0 {
		return 0, runnerWindowsCallError("peeking native anonymous pipe", callErr)
	}
	return available, nil
}
