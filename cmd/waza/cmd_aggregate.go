package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/spf13/cobra"
)

func newAggregateCommand() *cobra.Command {
	var directory, azureURL, format string
	var evidence []string
	cmd := &cobra.Command{
		Use:          "aggregate DIGEST [DIGEST...]",
		SilenceUsage: true,
		Short:        "Aggregate completed immutable manifests without workers or agents",
		Long:         "Derive a deterministic JSON or CSV report from an explicit expected manifest set, optionally selecting evidence sidecars. Uses only the selected immutable store; never loads project configuration, executes agents, or rewrites historical results. Declared categories and admitted native shapes remain separate; usage totals are withheld. Exit 0 means complete compatible membership, 1 means a recorded evaluation failure, and 2 means incomplete, conflicting, incompatible or unreadable inputs. None is an evidence-assurance decision.",
		Args:         cobra.RangeArgs(1, manifestaggregate.MaxInputs),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := manifestaggregate.Request{Manifests: args}
			if format != "json" && format != "csv" {
				return fmt.Errorf("%w: format must be json or csv", manifestaggregate.ErrInvalid)
			}
			if cmd.Flags().Changed("evidence") {
				references := []manifestaggregate.EvidenceInput{}
				for _, value := range evidence {
					origin, digest, ok := strings.Cut(value, "=")
					if !ok {
						return fmt.Errorf("%w: evidence requires MANIFEST_SHA256=EVIDENCE_SHA256", manifestaggregate.ErrInvalid)
					}
					references = append(references, manifestaggregate.EvidenceInput{ManifestSHA256: origin, EvidenceSHA256: digest})
				}
				request.Evidence = &references
			}
			if err := request.Validate(); err != nil {
				return err
			}
			backend, closeBackend, err := artifactmanifest.OpenBackend(directory, azureURL)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			report, aggregateErr := manifestaggregate.Aggregate(ctx, backend, request)
			closeErr := closeBackend()
			if closeErr != nil {
				report.Completion = "incomplete"
				report.Diagnostics = append(report.Diagnostics, artifactmanifest.Diagnostic{
					Code: "storage_close_failed", Message: "Store close failed; aggregation did not complete successfully.",
				})
			}
			encode := manifestaggregate.Encode
			if format == "csv" {
				encode = manifestaggregate.EncodeCSV
			}
			data, encodeErr := encode(report)
			if encodeErr != nil {
				return errors.Join(aggregateErr, closeErr, encodeErr)
			}
			if format == "json" {
				data = append(data, '\n')
			}
			n, writeErr := cmd.OutOrStdout().Write(data)
			if writeErr == nil && n != len(data) {
				writeErr = errors.New("aggregate report output was incomplete")
			}
			if err := errors.Join(aggregateErr, closeErr, writeErr); err != nil {
				return err
			}
			for _, member := range report.Members {
				if member.Outcome == runnerterminal.EvaluationFailure {
					return &ExitCodeError{Code: ExitTestFailed, Err: errors.New("completed aggregate contains a recorded evaluation failure")}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&directory, "store", ".waza-artifacts", "Existing local immutable store; no ambient configuration or provider fallback")
	cmd.Flags().StringVar(&azureURL, "azure-container-url", "", "Explicit HTTPS Azure container URL; existing credentials, no automatic login or local fallback")
	cmd.Flags().StringArrayVar(&evidence, "evidence", nil, "Explicit MANIFEST_SHA256=EVIDENCE_SHA256 selection; repeatable, at most 128 before deduplication")
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or csv; recorded usage is not new-operation billing")
	cmd.MarkFlagsMutuallyExclusive("store", "azure-container-url")
	return cmd
}
