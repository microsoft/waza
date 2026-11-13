package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/copilotconfig"
	"github.com/microsoft/waza/internal/executionisolation"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/telemetry"
	"github.com/spf13/cobra"
)

// isolationPreflightNow is the preflight clock; tests may replace it.
var isolationPreflightNow = time.Now

func newIsolationCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "isolation",
		Short: "Discover and preflight portable execution isolation capabilities",
		Long: `Discover which isolation boundaries a host or runtime provider can prove,
and reject requested boundaries that cannot be shown to hold before execution.

Waza enforces no boundary itself. Enforcement belongs to an outer host or
runtime declared with --host, or to the Copilot-native sandbox (#544) when
that integration is present (absent in this build).`,
	}
	cmd.AddCommand(newIsolationPreflightCommand())
	return cmd
}

func newIsolationPreflightCommand() *cobra.Command {
	var requestPath, hostPath, workspace, output string
	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Probe host isolation capabilities and match them against a request",
		Long: `Probe every isolation boundary a provider claims by attempting the violation,
then match the results against an optional request. Prints the
waza.execution-isolation-preflight 1.0 report as JSON.

Without --request every dimension is unrestricted, which reports host
discovery only. Exit code 1 means a requested boundary is unavailable; exit
code 2 means invalid input.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := isolationPreflight(cmd.Context(), requestPath, hostPath, workspace)
			if err != nil {
				return &ExitCodeError{Code: ExitError, Err: err}
			}
			data, err := report.Encode()
			if err != nil {
				return &ExitCodeError{Code: ExitError, Err: err}
			}
			if output != "" {
				if err := distinctReportPath(output, requestPath, hostPath); err != nil {
					return &ExitCodeError{Code: ExitError, Err: err}
				}
				if err := writeIsolationReport(output, data); err != nil {
					return &ExitCodeError{Code: ExitError, Err: err}
				}
			}
			if _, err := cmd.OutOrStdout().Write(data); err != nil {
				return err
			}
			if err := report.Admit(); err != nil {
				return &ExitCodeError{Code: ExitTestFailed, Err: err}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&requestPath, "request", "", "Isolation request JSON (waza.execution-isolation-request 1.0)")
	cmd.Flags().StringVar(&hostPath, "host", "", "Host isolation declaration JSON (waza.execution-isolation-host 1.0)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "Workspace the execution will use (default: a fresh temporary directory)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Also write the report to this file")
	return cmd
}

func readIsolationDocument(path, label string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading isolation %s: %w", label, err)
	}
	return data, nil
}

// isolationPreflight loads the request and host declaration and runs the
// preflight against the default providers. An empty workspace is replaced by
// a fresh temporary directory, matching how task workspaces are created.
func isolationPreflight(ctx context.Context, requestPath, hostPath, workspace string) (*executionisolation.Report, error) {
	request := executionisolation.Request{Kind: executionisolation.RequestKind, SchemaVersion: executionisolation.Version}
	if requestPath != "" {
		data, err := readIsolationDocument(requestPath, "request")
		if err != nil {
			return nil, err
		}
		if request, err = executionisolation.ParseRequest(data); err != nil {
			return nil, err
		}
	}
	var host *executionisolation.HostDeclaration
	if hostPath != "" {
		data, err := readIsolationDocument(hostPath, "host declaration")
		if err != nil {
			return nil, err
		}
		if host, err = executionisolation.ParseHostDeclaration(data); err != nil {
			return nil, err
		}
	}
	if workspace == "" {
		dir, err := os.MkdirTemp("", "waza-isolation-")
		if err != nil {
			return nil, fmt.Errorf("creating isolation probe workspace: %w", err)
		}
		defer os.RemoveAll(dir) //nolint:errcheck // best-effort removal of the empty probe workspace
		workspace = dir
	}
	if ctx == nil {
		ctx = context.Background()
	}
	providers := isolationProviders(host, workspace)
	return executionisolation.Preflight(ctx, request, providers, isolationPreflightNow()), nil
}

// distinctReportPath refuses a report path that names one of the run's
// inputs or outputs, so writing the report never truncates an input and a
// later writer never replaces the report.
func distinctReportPath(reportPath string, others ...string) error {
	report, err := filepath.Abs(reportPath)
	if err != nil {
		return fmt.Errorf("resolving isolation report path: %w", err)
	}
	reportInfo, reportErr := os.Stat(report)
	for _, other := range others {
		if other == "" {
			continue
		}
		abs, err := filepath.Abs(other)
		if err != nil {
			continue
		}
		same := strings.EqualFold(abs, report)
		if !same && reportErr == nil {
			if info, err := os.Stat(abs); err == nil {
				same = os.SameFile(reportInfo, info)
			}
		}
		if same {
			return fmt.Errorf("isolation report path %s must not be the same file as %s", reportPath, other)
		}
	}
	return nil
}

// isolationProviders discovers the capability providers; tests replace it.
var isolationProviders = executionisolation.DefaultProviders

// reportOverwritable refuses to replace an existing file that is not an
// isolation preflight report, so a mistyped report path can never truncate an
// eval, task, fixture or other input.
func reportOverwritable(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking isolation report path: %w", err)
	}
	var existing struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(data, &existing) != nil || existing.Kind != executionisolation.PreflightKind {
		return fmt.Errorf("refusing to overwrite %s: it is not a %s report", path, executionisolation.PreflightKind)
	}
	return nil
}

// telemetryFilePath is the span file the OpenTelemetry file exporter will
// truncate, or empty when no file exporter is configured.
func telemetryFilePath() string {
	if otelExporter == string(telemetry.ExporterFile) {
		return otelFile
	}
	return ""
}

// writeIsolationReport publishes a report without ever writing through a
// file it did not create. A missing destination is created exclusively, so a
// file or symlink created concurrently makes the write fail instead of being
// truncated. An existing destination must be a regular preflight report; the
// new bytes go to a sibling temporary file that is renamed over it, which
// replaces the directory entry rather than writing into whatever it names.
func writeIsolationReport(path string, data []byte) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("writing isolation report: %w", err)
		}
		_, writeErr := file.Write(data)
		if closeErr := file.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			return fmt.Errorf("writing isolation report: %w", writeErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking isolation report path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to overwrite %s: it is not a regular file", path)
	}
	if err := reportOverwritable(path); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".waza-isolation-report-*")
	if err != nil {
		return fmt.Errorf("writing isolation report: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName) //nolint:errcheck // no-op after a successful rename
	_, writeErr := temp.Write(data)
	if closeErr := temp.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return fmt.Errorf("writing isolation report: %w", writeErr)
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return fmt.Errorf("refusing to overwrite %s: it changed while the report was being written", path)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("writing isolation report: %w", err)
	}
	return nil
}

// reportClearOfRunOutputs refuses a report path that a run output writer
// would replace: --output and the files derived from it, JUnit reporters,
// and anything inside --output-dir.
func reportClearOfRunOutputs(reportPath string) error {
	destinations := []string{outputPath, telemetryFilePath()}
	for _, reporter := range reporters {
		if path, ok := strings.CutPrefix(reporter, "junit:"); ok {
			destinations = append(destinations, path)
		}
	}
	if err := distinctReportPath(reportPath, destinations...); err != nil {
		return err
	}
	report, err := filepath.Abs(reportPath)
	if err != nil {
		return fmt.Errorf("resolving isolation report path: %w", err)
	}
	if outputPath != "" {
		output, err := filepath.Abs(outputPath)
		if err == nil {
			ext := filepath.Ext(output)
			derivedPrefix := strings.TrimSuffix(output, ext) + "_"
			if strings.HasPrefix(strings.ToLower(report), strings.ToLower(derivedPrefix)) && strings.EqualFold(filepath.Ext(report), ext) {
				return fmt.Errorf("isolation report path %s may be replaced by a file derived from --output %s", reportPath, outputPath)
			}
		}
	}
	if outputDir != "" {
		dir, err := filepath.Abs(outputDir)
		if err == nil {
			if rel, err := filepath.Rel(dir, report); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("isolation report path %s must be outside --output-dir %s", reportPath, outputDir)
			}
		}
	}
	return nil
}

// publishedIsolationReport records the report bytes written for an admitted
// run so the run can confirm no later writer replaced them.
var publishedIsolationReport *struct {
	path string
	data []byte
}

// verifyPublishedIsolationReport fails the run when the admitted report was
// replaced after it was written.
func verifyPublishedIsolationReport() error {
	published := publishedIsolationReport
	if published == nil {
		return nil
	}
	publishedIsolationReport = nil
	data, err := os.ReadFile(published.path)
	if err != nil || !bytes.Equal(data, published.data) {
		return &ExitCodeError{Code: ExitError, Err: fmt.Errorf("isolation report %s was replaced or removed during the run; choose a report path no other output uses", published.path)}
	}
	return nil
}

// admittedIsolation is the report admitted by the run gate, consulted once
// each eval spec is loaded.
var admittedIsolation *executionisolation.Report

// pendingIsolationReport holds an admitted report until every selected eval
// spec has passed checkSpecIsolation, so the artifact never records an
// admission that the run later withdraws.
var pendingIsolationReport *struct {
	path string
	data []byte
}

// runIsolationGate runs the preflight for `waza run` before any execution.
// It writes the report when requested and refuses to continue on rejection.
// protected lists the run's inputs and outputs the report must not replace.
func runIsolationGate(ctx context.Context, requestPath, hostPath, reportPath string, stderr io.Writer, protected ...string) error {
	admittedIsolation = nil
	pendingIsolationReport = nil
	if requestPath == "" && hostPath == "" {
		if reportPath != "" {
			return &ExitCodeError{Code: ExitError, Err: errors.New("--isolation-report requires --isolation-request or --isolation-host")}
		}
		return nil
	}
	if reportPath != "" {
		if err := distinctReportPath(reportPath, append(protected, requestPath, hostPath)...); err != nil {
			return &ExitCodeError{Code: ExitError, Err: err}
		}
		if err := reportOverwritable(reportPath); err != nil {
			return &ExitCodeError{Code: ExitError, Err: err}
		}
	}
	report, err := isolationPreflight(ctx, requestPath, hostPath, "")
	if err != nil {
		return &ExitCodeError{Code: ExitError, Err: err}
	}
	var data []byte
	if reportPath != "" {
		if data, err = report.Encode(); err != nil {
			return err
		}
	}
	if err := report.Admit(); err != nil {
		if data != nil {
			if writeErr := writeIsolationReport(reportPath, data); writeErr != nil {
				return &ExitCodeError{Code: ExitError, Err: writeErr}
			}
		}
		return &ExitCodeError{Code: ExitError, Err: err}
	}
	admittedIsolation = report
	if data != nil {
		pendingIsolationReport = &struct {
			path string
			data []byte
		}{reportPath, data}
	}
	for _, capability := range report.Dimensions {
		if capability.Effective == executionisolation.EffectiveRestricted {
			_, _ = fmt.Fprintf(stderr, "isolation: %s restricted (enforced by %s)\n", capability.Dimension, capability.EnforcementSource) //nolint:errcheck // diagnostics only
		}
	}
	return nil
}

// admitIsolationSpecs checks every selected eval spec against the admitted
// isolation before the first one runs, then writes the pending report. It is
// a no-op without an isolation request.
func admitIsolationSpecs(specPaths []string) error {
	if admittedIsolation == nil {
		return nil
	}
	for _, path := range specPaths {
		spec, err := models.LoadEvalSpec(path)
		if err != nil {
			return fmt.Errorf("failed to load spec: %w", err)
		}
		if err := checkSpecIsolation(spec); err != nil {
			return err
		}
	}
	if pending := pendingIsolationReport; pending != nil {
		pendingIsolationReport = nil
		if err := distinctReportPath(pending.path, specPaths...); err != nil {
			return &ExitCodeError{Code: ExitError, Err: err}
		}
		if err := reportClearOfRunOutputs(pending.path); err != nil {
			return &ExitCodeError{Code: ExitError, Err: err}
		}
		if err := writeIsolationReport(pending.path, pending.data); err != nil {
			return &ExitCodeError{Code: ExitError, Err: err}
		}
		publishedIsolationReport = pending
	}
	return nil
}

// checkSpecIsolation rejects an eval whose configuration injects environment
// variables into MCP server processes while the environment boundary is
// restricted: those variables are set after the preflight and never probed.
func checkSpecIsolation(spec *models.EvalSpec) error {
	if admittedIsolation == nil || spec == nil {
		return nil
	}
	restricted := slices.ContainsFunc(admittedIsolation.Dimensions, func(c executionisolation.Capability) bool {
		return c.Dimension == executionisolation.Environment && c.Effective == executionisolation.EffectiveRestricted
	})
	if !restricted {
		return nil
	}
	var injecting []string
	for name, config := range spec.Config.ServerConfigs {
		server, ok := config.(map[string]any)
		if !ok {
			continue
		}
		// Mirror the runtime's transport selection and decoding so every key
		// spelling it accepts for env is also seen here.
		if serverType, _ := server["type"].(string); serverType != "" && !strings.EqualFold(serverType, "stdio") {
			continue
		}
		stdio, err := copilotconfig.DecodeMCPStdioServer(server)
		if err == nil && len(stdio.Env) > 0 {
			injecting = append(injecting, name)
		}
	}
	if len(injecting) == 0 {
		return nil
	}
	slices.Sort(injecting)
	return &ExitCodeError{Code: ExitError, Err: fmt.Errorf("environment isolation is restricted, but mcp_servers %s set environment variables the isolation preflight cannot verify; remove their env entries or do not request environment isolation", strings.Join(injecting, ", "))}
}
