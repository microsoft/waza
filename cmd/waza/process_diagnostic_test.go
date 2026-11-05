package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type processDiagnosticWriteFunc func([]byte) (int, error)

func (f processDiagnosticWriteFunc) Write(data []byte) (int, error) { return f(data) }

func TestProcessDiagnosticWriterResults(t *testing.T) {
	writeErr := errors.New("diagnostic output failure")
	for _, control := range []struct {
		name string
		n    int
		err  error
		want error
	}{
		{"complete", 8, nil, nil},
		{"short", 3, nil, io.ErrShortWrite},
		{"empty-short", 0, nil, io.ErrShortWrite},
		{"failed", 0, writeErr, writeErr},
		{"partial-error", 3, writeErr, writeErr},
	} {
		t.Run(control.name, func(t *testing.T) {
			var attempted bytes.Buffer
			calls := 0
			writer := &processDiagnosticWriter{output: processDiagnosticWriteFunc(func(data []byte) (int, error) {
				calls++
				attempted.Write(data)
				return control.n, control.err
			})}
			n, err := writer.Write([]byte("message\n"))
			require.Equal(t, control.n, n)
			require.ErrorIs(t, err, control.want)
			require.Equal(t, "message\n", attempted.String(), "ordinary diagnostic bytes are unchanged")
			if control.want != nil {
				n, err = writer.Write([]byte("no fallback\n"))
				require.Zero(t, n)
				require.ErrorIs(t, err, control.want)
				require.Equal(t, 1, calls, "failed diagnostics never retry, even after a partial write")
				require.Equal(t, "message\n", attempted.String())
			}
		})
	}
}

func TestProcessDiagnosticWriterSharedBudgetAndOwnedBuffer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release, returned := make(chan struct{}), make(chan []byte, 1)
		calls := 0
		writer := &processDiagnosticWriter{output: processDiagnosticWriteFunc(func(data []byte) (int, error) {
			calls++
			if calls == 1 {
				time.Sleep(processDiagnosticBudget / 2)
				return len(data), nil
			}
			<-release
			returned <- bytes.Clone(data)
			return len(data), nil
		})}
		start := time.Now()
		n, err := writer.Write([]byte("Cobra\n"))
		require.NoError(t, err)
		require.Equal(t, 6, n)
		data := []byte("final\n")
		n, err = writer.Write(data)
		require.Zero(t, n, "a timed-out write cannot claim an observed byte count")
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)
		require.Equal(t, processDiagnosticBudget, time.Since(start), "Cobra and main must not get independent budgets")
		copy(data, "reuse\n")
		n, err = writer.Write([]byte("no fallback\n"))
		require.Zero(t, n)
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)
		close(release)
		require.Equal(t, []byte("final\n"), <-returned, "a timed-out write owns its input until completion")
		synctest.Wait()
		require.Equal(t, 2, calls)
	})
}

func TestProcessDiagnosticWriterIdleTimePreservesWritableOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		writer := &processDiagnosticWriter{output: &output}
		_, err := writer.Write([]byte("first\n"))
		require.NoError(t, err)
		time.Sleep(time.Hour)
		n, err := writer.Write([]byte("final\n"))
		require.Equal(t, 6, n)
		require.NoError(t, err)
		require.Equal(t, "first\nfinal\n", output.String(), "time between diagnostics must not suppress a later writable error")
	})
}

func TestProcessDiagnosticWriterExhaustedBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		writer := &processDiagnosticWriter{output: &output, spent: processDiagnosticBudget}
		n, err := writer.Write([]byte("too late\n"))
		require.Zero(t, n)
		require.ErrorIs(t, err, os.ErrDeadlineExceeded)
		require.Empty(t, output.Bytes(), "an exhausted budget cannot launch a further write")
	})
}

func TestProcessDiagnosticWriterConcurrentBlockedCalls(t *testing.T) {
	release, returned := make(chan struct{}), make(chan struct{})
	calls := 0
	writer := &processDiagnosticWriter{output: processDiagnosticWriteFunc(func(data []byte) (int, error) {
		calls++
		<-release
		close(returned)
		return len(data), nil
	})}
	var group sync.WaitGroup
	for range 3 {
		group.Go(func() {
			n, err := writer.Write([]byte("diagnostic\n"))
			if n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Errorf("blocked diagnostic: got (%d, %v)", n, err)
			}
		})
	}
	group.Wait()
	close(release)
	<-returned
	require.Equal(t, 1, calls, "timeout must not leave several blocked writers behind")
}
