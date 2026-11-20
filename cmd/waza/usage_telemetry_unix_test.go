//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/usagetelemetry"
	"github.com/stretchr/testify/require"
)

func TestUsageTelemetry_NonRegularPolicyFilesDoNotBlockStartup(t *testing.T) {
	settings := isolateUsageTelemetry(t)
	collector := newUsageCollector(t)
	t.Cleanup(usagetelemetry.OverrideActivationForTesting(collector.server.URL, collector.server.Client(), true, false))
	require.NoError(t, os.MkdirAll(filepath.Dir(settings), 0o700))
	require.NoError(t, syscall.Mkfifo(settings, 0o600))
	wd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, syscall.Mkfifo(filepath.Join(wd, ".waza.yaml"), 0o600))

	done := make(chan invocation, 1)
	go func() { done <- invokeCLI(t, "--no-telemetry", "--version") }()
	select {
	case got := <-done:
		require.Equal(t, ExitSuccess, got.code)
	case <-time.After(10 * time.Second):
		t.Fatal("a FIFO policy file blocked command startup")
	}
	require.Empty(t, collector.take())
}
