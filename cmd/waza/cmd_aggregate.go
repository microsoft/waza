package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/spf13/cobra"
)

func newAggregateCommand() *cobra.Command {
	var directory, azureURL string
	cmd := &cobra.Command{
		Use:          "aggregate DIGEST [DIGEST...]",
		SilenceUsage: true,
		Short:        "Aggregate completed immutable manifests without workers or agents",
		Long:         "Derive a deterministic JSON report from an explicit expected manifest set. Uses only the selected immutable store; never loads project configuration, executes agents, or rewrites historical results. Exit 0 means complete compatible membership, 1 means a recorded evaluation failure, and 2 means incomplete, conflicting, incompatible or unreadable inputs. None is an evidence-assurance decision.",
		Args:         cobra.RangeArgs(1, manifestaggregate.MaxInputs),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := manifestaggregate.Request{Manifests: args}
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
			data, encodeErr := manifestaggregate.Encode(report)
			if encodeErr != nil {
				return errors.Join(aggregateErr, closeErr, encodeErr)
			}
			n, writeErr := fmt.Fprintln(cmd.OutOrStdout(), string(data))
			if writeErr == nil && n != len(data)+1 {
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
	cmd.MarkFlagsMutuallyExclusive("store", "azure-container-url")
	return cmd
}
