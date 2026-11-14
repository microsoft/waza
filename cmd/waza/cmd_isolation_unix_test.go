//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunGateRefusesFIFOReportWithoutBlocking(t *testing.T) {
	t.Cleanup(resetRunGlobals)
	dir := t.TempDir()
	request := writeIsolationFile(t, dir, "admitted.json",
		`{"kind":"waza.execution-isolation-request","schema_version":"1.0","dimensions":{}}`)
	fifo := filepath.Join(dir, "report.fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o644))

	done := make(chan error, 1)
	go func() {
		resetRunGlobals()
		cmd := newRootCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"run", createTestSpec(t, "mock"), "--isolation-request", request, "--isolation-report", fifo})
		done <- cmd.Execute()
	}()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "already exists")
	case <-time.After(10 * time.Second):
		t.Fatal("the run gate blocked on a FIFO report destination")
	}
	info, err := os.Lstat(fifo)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeNamedPipe)
}
