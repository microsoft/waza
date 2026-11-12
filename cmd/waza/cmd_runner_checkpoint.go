package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/spf13/cobra"
)

func newRunnerCheckpointCommand(service *runnerunit.Service) *cobra.Command {
	return newRunnerCheckpointCommandWithRevision(service, executioncheckpoint.CurrentRunnerRevision)
}

func newRunnerCheckpointCommandWithRevision(service *runnerunit.Service, revision func() (string, error)) *cobra.Command {
	var directory, azureURL string
	cmd := &cobra.Command{
		Use: "checkpoint", Short: "Create or restore immutable exact-unit checkpoints from captured submission JSON",
		SilenceUsage: true,
	}
	cmd.PersistentFlags().StringVar(&directory, "store", ".waza-artifacts", "Existing private local immutable store directory")
	cmd.PersistentFlags().StringVar(&azureURL, "azure-container-url", "", "Explicit HTTPS Azure container URL using existing credentials")
	emit := func(cmd *cobra.Command, result runnercheckpoint.Result) error {
		data, err := runnercheckpoint.EncodeResult(result)
		if err != nil {
			return &ExitCodeError{Code: 2, Err: fmt.Errorf("validating checkpoint operation output: %w", err)}
		}
		return emitRunnerOperationJSON(cmd, data, result.ExitCode, result.Status)
	}
	rejectWithDiagnostic := func(cmd *cobra.Command, code, stage string, cause error) error {
		_, diagnosticErr := fmt.Fprintf(cmd.ErrOrStderr(), "checkpoint %s: %v\n", stage, cause)
		return errors.Join(emit(cmd, runnercheckpoint.RejectPublic(code)), diagnosticErr)
	}
	for _, action := range []string{"create", "resume"} {
		unit := &cobra.Command{
			Use: action, Short: "Read one captured submission from stdin; emit one protected checkpoint result",
			SilenceErrors: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				if cmd.Flags().Changed("store") && cmd.Flags().Changed("azure-container-url") {
					return emit(cmd, runnercheckpoint.RejectPublic("checkpoint_flags_conflict"))
				}
				if (action == "create" && len(args) != 0) || (action == "resume" && len(args) != 1) {
					return emit(cmd, runnercheckpoint.RejectPublic("checkpoint_arguments_invalid"))
				}
				if action == "resume" {
					if err := executioncheckpoint.ValidateDigest(args[0]); err != nil {
						return emit(cmd, runnercheckpoint.RejectPublic("checkpoint_digest_invalid"))
					}
				}
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				data, err := readRunnerInputLimit(ctx, cmd.InOrStdin(), runnerunit.MaxSubmissionBytes)
				if err != nil {
					return rejectWithDiagnostic(cmd, "checkpoint_input_unavailable", "input read", err)
				}
				if _, err := runnerunit.ParseSubmission(data); err != nil {
					return rejectWithDiagnostic(cmd, "checkpoint_input_invalid", "input admission", err)
				}
				buildRevision, err := revision()
				if err != nil {
					return rejectWithDiagnostic(cmd, "checkpoint_build_incompatible", "build admission", err)
				}
				checkpoints, err := runnercheckpoint.NewService(service, buildRevision)
				if err != nil {
					return &ExitCodeError{Code: 2, Err: err}
				}
				backend, closeBackend, err := artifactmanifest.OpenBackend(directory, azureURL)
				if err != nil {
					return &ExitCodeError{Code: 2, Err: fmt.Errorf("opening checkpoint store: %w", err)}
				}
				observers := runnercheckpoint.Hooks{Published: func(_ context.Context, reference runnercheckpoint.Reference) error {
					_, err := fmt.Fprintf(cmd.ErrOrStderr(), "checkpoint %s %s\n", reference.Boundary, reference.Digest)
					return err
				}}
				var result runnercheckpoint.Result
				if action == "resume" {
					result, err = checkpoints.ResumeCaptured(ctx, data, backend, args[0], observers)
				} else {
					result, err = checkpoints.SubmitCaptured(ctx, data, backend, observers)
				}
				if closeErr := closeBackend(); closeErr != nil {
					return &ExitCodeError{Code: 2, Err: errors.Join(err, fmt.Errorf("closing checkpoint store: %w", closeErr))}
				}
				var diagnosticErr error
				if err != nil {
					_, diagnosticErr = fmt.Fprintf(cmd.ErrOrStderr(), "checkpoint operation: %v\n", err)
				}
				return errors.Join(emit(cmd, result), diagnosticErr)
			},
		}
		if action == "resume" {
			unit.Use = "resume CHECKPOINT_SHA256"
		}
		unit.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
			_, diagnosticErr := fmt.Fprintf(cmd.ErrOrStderr(), "checkpoint flags: %v\n", err)
			return errors.Join(emit(cmd, runnercheckpoint.RejectPublic("checkpoint_flags_invalid")), diagnosticErr)
		})
		cmd.AddCommand(unit)
	}
	return cmd
}
