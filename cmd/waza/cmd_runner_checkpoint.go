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
	return newRunnerCheckpointCommandWithBackend(service, revision, artifactmanifest.OpenBackend)
}

func newRunnerCheckpointCommandWithBackend(
	service *runnerunit.Service,
	revision func() (string, error),
	openBackend func(string, string) (artifactmanifest.Backend, func() error, error),
) *cobra.Command {
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
	operationFailure := func(cmd *cobra.Command, code, stage string, cause error) error {
		_, diagnosticErr := fmt.Fprintf(cmd.ErrOrStderr(), "checkpoint %s: %v\n", stage, cause)
		return errors.Join(emit(cmd, runnercheckpoint.OperationErrorResult(code)), diagnosticErr)
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
					return operationFailure(cmd, runnercheckpoint.ErrorCodeBuildUnavailable, "build admission", err)
				}
				checkpoints, err := runnercheckpoint.NewService(service, buildRevision)
				if err != nil {
					return operationFailure(cmd, runnercheckpoint.ErrorCodeRunnerUnavailable, "runner admission", err)
				}
				backend, closeBackend, err := openBackend(directory, azureURL)
				if err != nil {
					return operationFailure(cmd, runnercheckpoint.ErrorCodeStorageUnavailable, "store open", err)
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
					var diagnosticErr error
					if err != nil {
						_, diagnosticErr = fmt.Fprintf(cmd.ErrOrStderr(), "checkpoint operation: %v\n", err)
					}
					_, closeDiagnosticErr := fmt.Fprintf(cmd.ErrOrStderr(), "checkpoint store close: %v\n", closeErr)
					result = runnercheckpoint.WithOperationError(result, runnercheckpoint.ErrorCodeStorageCloseFailed)
					return errors.Join(emit(cmd, result), diagnosticErr, closeDiagnosticErr)
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
			return rejectWithDiagnostic(cmd, "checkpoint_flags_invalid", "flags", err)
		})
		cmd.AddCommand(unit)
	}
	cmd.AddCommand(newRunnerCheckpointAvailabilityCommand(service, revision, openBackend, &directory, &azureURL))
	return cmd
}

func newRunnerCheckpointAvailabilityCommand(
	service *runnerunit.Service,
	revision func() (string, error),
	openBackend func(string, string) (artifactmanifest.Backend, func() error, error),
	directory, azureURL *string,
) *cobra.Command {
	emit := func(cmd *cobra.Command, result runnercheckpoint.Availability) error {
		data, err := runnercheckpoint.EncodeAvailability(result)
		if err != nil {
			return &ExitCodeError{Code: 2, Err: fmt.Errorf("validating checkpoint availability output: %w", err)}
		}
		return emitRunnerOperationJSON(cmd, data, result.ExitCode, result.Status)
	}
	report := func(cmd *cobra.Command, result runnercheckpoint.Availability, stage string, cause error) error {
		var diagnosticErr error
		if cause != nil {
			_, diagnosticErr = fmt.Fprintf(cmd.ErrOrStderr(), "checkpoint availability %s: %v\n", stage, cause)
		}
		return errors.Join(emit(cmd, result), diagnosticErr)
	}
	unit := &cobra.Command{
		Use:           "availability CHECKPOINT_SHA256",
		Short:         "Read one captured submission from stdin; report whether the checkpoint can resume, without executing",
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			digest := ""
			if len(args) == 1 {
				digest = args[0]
			}
			if cmd.Flags().Changed("store") && cmd.Flags().Changed("azure-container-url") {
				return emit(cmd, runnercheckpoint.AvailabilityRejected(digest, "checkpoint_flags_conflict"))
			}
			if len(args) != 1 {
				return emit(cmd, runnercheckpoint.AvailabilityRejected("", "checkpoint_arguments_invalid"))
			}
			if err := executioncheckpoint.ValidateDigest(digest); err != nil {
				return emit(cmd, runnercheckpoint.AvailabilityRejected("", "checkpoint_digest_invalid"))
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			data, err := readRunnerInputLimit(ctx, cmd.InOrStdin(), runnerunit.MaxSubmissionBytes)
			if err != nil {
				return report(cmd, runnercheckpoint.AvailabilityRejected(digest, "checkpoint_input_unavailable"), "input read", err)
			}
			buildRevision, err := revision()
			if err != nil {
				return report(cmd, runnercheckpoint.AvailabilityOperationError(digest, runnercheckpoint.ErrorCodeBuildUnavailable), "build admission", err)
			}
			checkpoints, err := runnercheckpoint.NewService(service, buildRevision)
			if err != nil {
				return report(cmd, runnercheckpoint.AvailabilityOperationError(digest, runnercheckpoint.ErrorCodeRunnerUnavailable), "runner admission", err)
			}
			backend, closeBackend, err := openBackend(*directory, *azureURL)
			if err != nil {
				return report(cmd, runnercheckpoint.AvailabilityOperationError(digest, runnercheckpoint.ErrorCodeStorageUnavailable), "store open", err)
			}
			result, err := checkpoints.Availability(ctx, data, backend, digest)
			if closeErr := closeBackend(); closeErr != nil {
				return report(cmd, runnercheckpoint.AvailabilityOperationError(digest, runnercheckpoint.ErrorCodeStorageCloseFailed),
					"store close", errors.Join(err, closeErr))
			}
			return report(cmd, result, "check", err)
		},
	}
	unit.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return report(cmd, runnercheckpoint.AvailabilityRejected("", "checkpoint_flags_invalid"), "flags", err)
	})
	return unit
}
