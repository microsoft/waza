package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/spf13/cobra"
)

func newRunnerSubmitCommand(service *runnerunit.Service) *cobra.Command {
	var directory, azureURL string
	cmd := &cobra.Command{
		Use: "submit", Short: "Reserve one distinct execution or explicitly reuse its verified immutable original",
		SilenceErrors: true, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return emitSubmission(cmd, ownedsubmission.RejectPublic("submission_arguments_invalid"))
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			data, err := readRunnerInputLimit(ctx, cmd.InOrStdin(), runnerunit.MaxSubmissionBytes)
			if err != nil {
				return emitSubmission(cmd, ownedsubmission.RejectPublic("submission_input_unavailable"))
			}
			if err := ownedsubmission.ValidateInput(data); err != nil {
				return emitSubmission(cmd, ownedsubmission.RejectPublic("submission_input_invalid"))
			}
			backend, closeBackend, err := artifactmanifest.OpenBackend(directory, azureURL)
			if err != nil {
				emitted := emitSubmission(cmd, ownedsubmission.FailPublic("submission_storage_unavailable"))
				return &ExitCodeError{Code: 2, Err: errors.Join(emitted, fmt.Errorf("opening submission store: %w", err))}
			}
			result, operationErr := ownedsubmission.SubmitCaptured(ctx, service, data, backend)
			if err := closeBackend(); err != nil {
				result = ownedsubmission.CloseFailed(result)
				operationErr = errors.Join(operationErr, fmt.Errorf("closing submission store: %w", err))
			}
			emitted := emitSubmission(cmd, result)
			if operationErr != nil {
				if exit, selected := errors.AsType[*ExitCodeError](emitted); selected {
					return &ExitCodeError{Code: exit.Code, Err: errors.Join(exit.Err, operationErr)}
				}
				return &ExitCodeError{Code: 2, Err: errors.Join(emitted, operationErr)}
			}
			return emitted
		},
	}
	cmd.Flags().StringVar(&directory, "store", ".waza-artifacts", "Existing private local immutable store directory")
	cmd.Flags().StringVar(&azureURL, "azure-container-url", "", "Explicit HTTPS Azure container URL using existing credentials")
	cmd.MarkFlagsMutuallyExclusive("store", "azure-container-url")
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, _ error) error {
		return emitSubmission(cmd, ownedsubmission.RejectPublic("submission_flags_invalid"))
	})
	return cmd
}
