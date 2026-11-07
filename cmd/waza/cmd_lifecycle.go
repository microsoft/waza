package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/spf13/cobra"
)

func newLifecycleCommand() *cobra.Command {
	var directory, azureURL string
	cmd := &cobra.Command{
		Use: "lifecycle", Short: "Inspect declared or original-source lifecycle metadata without execution",
		Long: "Read-only lifecycle projections compare explicit retained original context and ordered immutable observations. Content and declarations only: verified worker state/progress is unavailable; no retry, cancellation, reclaim or source ownership is authorized.",
	}
	cmd.PersistentFlags().StringVar(&directory, "store", ".waza-artifacts", "Existing private immutable store directory")
	cmd.PersistentFlags().StringVar(&azureURL, "azure-container-url", "", "Explicit HTTPS Azure container URL; no automatic sign-in")
	cmd.MarkFlagsMutuallyExclusive("store", "azure-container-url")
	cmd.AddCommand(&cobra.Command{
		Use: "source FILE.json", Short: "Inspect fixed lifecycle capture from one original finalized source", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readArtifactFile(args[0], executionlifecycle.MaxBytes)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			request, err := ownedsubmission.ParseLifecycleSourceRequest(data)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			backend, closeBackend, err := artifactmanifest.OpenBackend(directory, azureURL)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			reader, err := manifestaggregate.NewEvidenceReader(backend)
			var output ownedsubmission.LifecycleInspection
			if err == nil {
				output, err = reader.InspectLifecycle(ctx, request.Source, request.AsOf, request.StaleAfterMS)
			}
			if err := errors.Join(err, closeBackend(), ctx.Err()); err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			encoded, err := ownedsubmission.EncodeLifecycleInspection(output)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			if _, err := cmd.OutOrStdout().Write(append(encoded, '\n')); err != nil {
				return &ExitCodeError{Code: 2, Err: fmt.Errorf("writing original lifecycle source: %w", err)}
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "inspect FILE.json", Short: "Inspect ordered known observation digests against explicit caller-retained context", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readArtifactFile(args[0], executionlifecycle.MaxBytes)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			_, err = executionlifecycle.ParseInspection(data)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			backend, closeBackend, err := artifactmanifest.OpenBackend(directory, azureURL)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			output, err := executionlifecycle.Inspect(ctx, backend, data)
			if err := errors.Join(err, closeBackend(), ctx.Err()); err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(output); err != nil {
				return &ExitCodeError{Code: 2, Err: fmt.Errorf("writing lifecycle inspection: %w", err)}
			}
			return nil
		},
	})
	return cmd
}
