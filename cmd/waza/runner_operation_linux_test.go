//go:build linux

package main

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRunnerOperationLinuxOutputDeadlineInterruptsWrite(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	writerClosed := false
	t.Cleanup(func() {
		if !writerClosed {
			require.NoError(t, writer.Close())
		}
		require.NoError(t, reader.Close())
	})

	filled := fillRunnerPipe(t, writer)
	require.NoError(t, unix.SetNonblock(int(writer.Fd()), true), "restore os.File poller mode after filling")
	err = writeInheritedRunnerJSONWithDeadline(writer, []byte("{\"status\":\"blocked\"}\n"), 100*time.Millisecond)
	require.ErrorIs(t, err, os.ErrDeadlineExceeded)

	require.NoError(t, reader.SetReadDeadline(time.Now().Add(time.Second)))
	require.NoError(t, writer.Close())
	writerClosed = true
	got, err := io.ReadAll(reader)
	require.NoError(t, err, "the timed-out write must be interrupted before the helper returns")
	require.Equal(t, filled, got, "a timed-out write must not append a partial record")
}
