package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executiontelemetry"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
	"github.com/stretchr/testify/require"
)

func telemetryCLIInputs(t *testing.T) (terminal, manifest string) {
	t.Helper()
	directory := t.TempDir()
	store := filepath.Join(directory, "store")
	require.NoError(t, os.Mkdir(store, 0o700))
	backend, err := artifactmanifest.OpenLocal(store)
	require.NoError(t, err)
	published := manifestfixture.PublishWith(t, backend, "../../examples/runner/unit.json", manifestfixture.Options{})
	completed, err := artifactmanifest.NewStore(backend).ReadCompleted(t.Context(), published.Manifest)
	require.NoError(t, err)
	require.NoError(t, backend.Close())
	terminal = filepath.Join(directory, "terminal.json")
	manifest = filepath.Join(directory, "manifest.json")
	require.NoError(t, os.WriteFile(terminal, completed.ArtifactBytes[completed.Manifest.TerminalArtifact], 0o600))
	require.NoError(t, os.WriteFile(manifest, completed.ManifestBytes, 0o600))
	return terminal, manifest
}

func runTelemetryCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"runner", "telemetry"}, args...))
	err := root.Execute()
	return stdout.String(), err
}

func TestRunnerTelemetryWritesCorrelatedFilesAndReport(t *testing.T) {
	terminal, manifest := telemetryCLIInputs(t)
	out := filepath.Join(t.TempDir(), "otel")
	stdout, err := runTelemetryCLI(t, "--terminal", terminal, "--manifest", manifest,
		"--scheduler-work-id", "queue/job-7", "--out-dir", out)
	require.NoError(t, err)
	report, err := executiontelemetry.ParseReport([]byte(stdout))
	require.NoError(t, err)
	require.Equal(t, executiontelemetry.PayloadExclude, report.PayloadPolicy)
	require.Equal(t, "queue/job-7", report.Correlation.SchedulerWorkID)
	require.Len(t, report.Correlation.TraceID, 32)
	require.Len(t, report.Exports, 3)
	for _, export := range report.Exports {
		require.Equal(t, executiontelemetry.ExportExported, export.State)
	}
	for _, name := range []string{executiontelemetry.TracesFile, executiontelemetry.MetricsFile, executiontelemetry.LogsFile, executiontelemetry.ReportFile} {
		require.FileExists(t, filepath.Join(out, name))
	}
	saved, err := os.ReadFile(filepath.Join(out, executiontelemetry.ReportFile))
	require.NoError(t, err)
	require.Equal(t, stdout, string(saved))
}

func TestRunnerTelemetryExitCodes(t *testing.T) {
	terminal, manifest := telemetryCLIInputs(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	for name, tc := range map[string]struct {
		args []string
		code int
	}{
		"bad policy":       {[]string{"--terminal", terminal, "--payload-policy", "raw"}, 6},
		"bad endpoint":     {[]string{"--terminal", terminal, "--otlp-endpoint", "ftp://collector"}, 6},
		"header only":      {[]string{"--terminal", terminal, "--otlp-header", "authorization=x"}, 6},
		"bad header":       {[]string{"--terminal", terminal, "--otlp-endpoint", server.URL, "--otlp-header", "novalue"}, 6},
		"invalid terminal": {[]string{"--terminal", manifest}, 6},
		"export failure":   {[]string{"--terminal", terminal, "--otlp-endpoint", server.URL, "--otlp-header", "authorization=Bearer s3cr3t"}, 2},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, err := runTelemetryCLI(t, tc.args...)
			var exit *ExitCodeError
			require.True(t, errors.As(err, &exit), "%v", err)
			require.Equal(t, tc.code, exit.Code)
			require.NotContains(t, stdout, "s3cr3t")
			if tc.code == 2 {
				report, err := executiontelemetry.ParseReport([]byte(stdout))
				require.NoError(t, err, "export failures still print the report")
				require.Equal(t, http.StatusServiceUnavailable, report.Exports[0].HTTPStatus)
			}
		})
	}
}
