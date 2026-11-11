package main

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func duplicateRunnerOperationTestOutput(t *testing.T, handle windows.Handle) *os.File {
	t.Helper()
	var duplicate windows.Handle
	require.NoError(t, windows.DuplicateHandle(windows.CurrentProcess(), handle,
		windows.CurrentProcess(), &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS))
	return os.NewFile(uintptr(duplicate), "runner-operation-test-output")
}

func TestRunnerOperationWindowsOutputSmallWrite(t *testing.T) {
	var read, write windows.Handle
	require.NoError(t, windows.CreatePipe(&read, &write, nil, 4096))
	reader := os.NewFile(uintptr(read), "runner-operation-test-reader")
	writer := os.NewFile(uintptr(write), "runner-operation-test-writer")
	t.Cleanup(func() {
		require.NoError(t, reader.Close())
		require.NoError(t, writer.Close())
	})

	payload := []byte("{\"status\":\"ok\"}\n")
	require.NoError(t, writeInheritedRunnerJSON(
		duplicateRunnerOperationTestOutput(t, write), payload, time.Second))
	extra := []byte("still-open")
	_, err := writer.Write(extra)
	require.NoError(t, err, "closing the owned duplicate must leave the inherited stdout handle open")
	got := make([]byte, len(payload)+len(extra))
	_, err = io.ReadFull(reader, got)
	require.NoError(t, err)
	require.Equal(t, append(append([]byte(nil), payload...), extra...), got)
}

func TestRunnerOperationWindowsOutputTimeoutInterruptsAndJoinsWriter(t *testing.T) {
	var read, write windows.Handle
	require.NoError(t, windows.CreatePipe(&read, &write, nil, 4096))
	reader := os.NewFile(uintptr(read), "runner-operation-test-reader")
	writer := os.NewFile(uintptr(write), "runner-operation-test-writer")
	readerClosed, writerClosed := false, false
	t.Cleanup(func() {
		if !writerClosed {
			require.NoError(t, writer.Close())
		}
		if !readerClosed {
			require.NoError(t, reader.Close())
		}
	})

	filled := fillRunnerWindowsStdout(t, read, write)
	mode := uint32(windows.PIPE_WAIT)
	require.NoError(t, windows.SetNamedPipeHandleState(write, &mode, nil, nil))
	start := time.Now()
	err := writeInheritedRunnerJSON(
		duplicateRunnerOperationTestOutput(t, write), []byte("{\"status\":\"blocked\"}\n"), 2*time.Second)
	require.Less(t, time.Since(start), 4*time.Second, "the blocked writer process must be terminated and reaped")
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	require.ErrorContains(t, err, "writing runner operation output", "the child must reach the blocked write before the deadline")

	require.NoError(t, writer.Close())
	writerClosed = true
	type readResult struct {
		data []byte
		err  error
	}
	drained := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(reader)
		drained <- readResult{data: data, err: err}
	}()
	select {
	case result := <-drained:
		require.NoError(t, result.err)
		require.Equal(t, filled, result.data,
			"the timed-out write must be finished and its duplicate closed before return")
	case <-time.After(2 * time.Second):
		require.NoError(t, reader.Close())
		readerClosed = true
		select {
		case <-drained:
		case <-time.After(2 * time.Second):
		}
		require.FailNow(t, "timed-out inherited-pipe writer or its duplicate remained open")
	}
}
