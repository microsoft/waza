//go:build unix

package main

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRunnerOperationUnixOutputSmallWrite(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, reader.Close())
		require.NoError(t, writer.Close())
	})

	payload := []byte("{\"status\":\"ok\"}\n")
	require.NoError(t, writeInheritedRunnerJSON(writer, payload, time.Second))

	extra := []byte("still-open")
	_, err = writer.Write(extra)
	require.NoError(t, err, "writing through the helper must leave inherited stdout open")
	got := make([]byte, len(payload)+len(extra))
	_, err = io.ReadFull(reader, got)
	require.NoError(t, err)
	require.Equal(t, append(bytes.Clone(payload), extra...), got)
}

func TestRunnerOperationUnixOutputTimeoutInterruptsWrite(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	writerClosed := false
	var outputClosed bool
	var output *os.File
	t.Cleanup(func() {
		if output != nil && !outputClosed {
			require.NoError(t, output.Close())
		}
		if !writerClosed {
			require.NoError(t, writer.Close())
		}
		require.NoError(t, reader.Close())
	})

	filled := fillRunnerPipe(t, writer)
	fd, err := unix.Dup(int(writer.Fd()))
	require.NoError(t, err)
	output = os.NewFile(uintptr(fd), "runner-operation-test-output")

	start := time.Now()
	err = writeInheritedRunnerJSON(output, []byte("{\"status\":\"blocked\"}\n"), 2*time.Second)
	require.Less(t, time.Since(start), 3*time.Second, "the blocked writer must be killed and joined after its timeout")
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)
	require.ErrorContains(t, err, "writing runner operation output", "the child must reach the blocked write before the deadline")

	require.NoError(t, reader.SetReadDeadline(time.Now().Add(time.Second)))
	require.NoError(t, output.Close())
	outputClosed = true
	require.NoError(t, writer.Close())
	writerClosed = true
	got, err := io.ReadAll(reader)
	require.NoError(t, err, "the timed-out write must be finished and its duplicate closed before return")
	require.Equal(t, filled, got, "a timed-out write must not append a partial record")
}
