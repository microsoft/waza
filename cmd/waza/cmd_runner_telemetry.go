package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executiontelemetry"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/spf13/cobra"
)

// maxTelemetryRecordBytes bounds attempt and lineage inputs.
const maxTelemetryRecordBytes = 16 << 20

func newRunnerTelemetryCommand() *cobra.Command {
	var terminal, manifest, attempt, lineage, report, aggregate string
	var schedulerWorkID, policy, outDir, endpoint string
	var headers []string
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "Derive correlated, payload-free OTLP traces, metrics and logs from runner contract documents",
		Long: `Derive vendor-neutral, OpenTelemetry-shaped traces, metrics and logs from
published runner contract documents and print a waza.execution-telemetry report.

Only --terminal is required. Optional documents add attempts, retries, artifacts,
model and tool activity and aggregation; each must describe the same run and work
or the command exits 6. Trace and span IDs are derived deterministically from the
run, work, execution and attempt identifiers.

Payloads are excluded by default. --payload-policy digest adds SHA-256 and length
surrogates only; there is no raw-payload mode. Signals are written as OTLP/JSON
files with --out-dir and/or sent to an OTLP/HTTP JSON receiver with
--otlp-endpoint. Export failures exit 2 after printing the report.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			payloadPolicy := executiontelemetry.PayloadPolicy(policy)
			if payloadPolicy != executiontelemetry.PayloadExclude && payloadPolicy != executiontelemetry.PayloadDigest {
				return &ExitCodeError{Code: 6, Err: fmt.Errorf("--payload-policy must be exclude or digest")}
			}
			headerMap, err := parseOTLPHeaders(headers)
			if err != nil {
				return &ExitCodeError{Code: 6, Err: err}
			}
			if endpoint != "" {
				if _, err := executiontelemetry.ValidateEndpoint(endpoint); err != nil {
					return &ExitCodeError{Code: 6, Err: err}
				}
			} else if len(headerMap) > 0 {
				return &ExitCodeError{Code: 6, Err: fmt.Errorf("--otlp-header requires --otlp-endpoint")}
			}
			in := executiontelemetry.Inputs{SchedulerWorkID: schedulerWorkID}
			for _, source := range []struct {
				flag, path string
				limit      int64
				target     *[]byte
			}{
				{"--terminal", terminal, runnerterminal.MaxEnvelopeBytes, &in.Terminal},
				{"--manifest", manifest, artifactmanifest.MaxManifestBytes, &in.Manifest},
				{"--attempt", attempt, maxTelemetryRecordBytes, &in.Attempt},
				{"--lineage", lineage, maxTelemetryRecordBytes, &in.Lineage},
				{"--report", report, executiontelemetry.MaxReportBytes, &in.Report},
				{"--aggregate", aggregate, manifestaggregate.MaxReportBytes, &in.Aggregate},
			} {
				if source.path == "" {
					continue
				}
				data, err := readArtifactFile(source.path, source.limit)
				if err != nil {
					return &ExitCodeError{Code: 6, Err: fmt.Errorf("reading %s: %w", source.flag, err)}
				}
				if len(data) == 0 {
					return &ExitCodeError{Code: 6, Err: fmt.Errorf("reading %s: file is empty", source.flag)}
				}
				*source.target = data
			}
			src, err := executiontelemetry.Load(in)
			if err != nil {
				return &ExitCodeError{Code: 6, Err: err}
			}
			bundle := executiontelemetry.Build(src, executiontelemetry.Options{Policy: payloadPolicy, ServiceVersion: version})
			exportErr := executiontelemetry.Deliver(cmd.Context(), bundle, executiontelemetry.ExportOptions{
				Dir: outDir, OTLPEndpoint: endpoint, Headers: headerMap,
			})
			if exportErr != nil && !errors.Is(exportErr, executiontelemetry.ErrExport) {
				return &ExitCodeError{Code: 2, Err: exportErr}
			}
			data, err := executiontelemetry.EncodeReport(bundle.Report)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			if exportErr != nil {
				return emitRunnerOperationJSON(cmd, data, 2, exportErr.Error())
			}
			return emitRunnerOperationJSON(cmd, data, 0, "")
		},
	}
	cmd.Flags().StringVar(&terminal, "terminal", "", "Runner terminal envelope (waza.runner-terminal 1.0)")
	_ = cmd.MarkFlagRequired("terminal")
	cmd.Flags().StringVar(&manifest, "manifest", "", "Artifact manifest for the same work (waza.artifact-manifest 1.0)")
	cmd.Flags().StringVar(&attempt, "attempt", "", "Execution attempt record (waza.execution-attempt 1.0)")
	cmd.Flags().StringVar(&lineage, "lineage", "", "Execution lineage for the same logical work (waza.execution-lineage 1.0)")
	cmd.Flags().StringVar(&report, "report", "", "Results JSON from waza run -o (model and tool activity)")
	cmd.Flags().StringVar(&aggregate, "aggregate", "", "Manifest aggregate report (waza.manifest-aggregate 1.0)")
	cmd.Flags().StringVar(&schedulerWorkID, "scheduler-work-id", "", "External scheduler work identifier recorded as a resource attribute")
	cmd.Flags().StringVar(&policy, "payload-policy", string(executiontelemetry.PayloadExclude), "Payload policy: exclude or digest")
	cmd.Flags().StringVar(&outDir, "out-dir", "", "Write traces/metrics/logs OTLP JSON files and the report to this new-file directory")
	cmd.Flags().StringVar(&endpoint, "otlp-endpoint", "", "OTLP/HTTP base URL; signals are posted as JSON to /v1/traces, /v1/metrics and /v1/logs")
	cmd.Flags().StringArrayVar(&headers, "otlp-header", nil, "OTLP request header as key=value (repeatable; never recorded)")
	return cmd
}

func parseOTLPHeaders(values []string) (map[string]string, error) {
	headers := map[string]string{}
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || strings.ContainsAny(key, " \t\r\n:") || strings.ContainsAny(val, "\r\n") {
			return nil, fmt.Errorf("--otlp-header must be key=value")
		}
		headers[key] = val
	}
	return headers, nil
}
