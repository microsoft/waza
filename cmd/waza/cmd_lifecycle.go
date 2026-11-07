package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/spf13/cobra"
)

func newLifecycleCommand() *cobra.Command {
	var directory, azureURL string
	cmd := &cobra.Command{
		Use: "lifecycle", Short: "Inspect caller-declared lifecycle evidence without execution",
		Long: "Read-only lifecycle projections compare explicit retained original context and ordered immutable observations. Content and declarations only: verified worker state/progress is unavailable; no retry, cancellation, reclaim or source ownership is authorized.",
	}
	cmd.PersistentFlags().StringVar(&directory, "store", ".waza-artifacts", "Existing private immutable store directory")
	cmd.PersistentFlags().StringVar(&azureURL, "azure-container-url", "", "Explicit HTTPS Azure container URL; no automatic sign-in")
	cmd.MarkFlagsMutuallyExclusive("store", "azure-container-url")
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
