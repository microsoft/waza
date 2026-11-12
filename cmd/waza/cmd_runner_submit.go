package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnersubmission"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/spf13/cobra"
)

type openBackendFunc func(string, string) (artifactmanifest.Backend, func() error, error)

func addStoreFlags(cmd *cobra.Command, directory, azureURL *string) {
	cmd.Flags().StringVar(directory, "store", ".waza-artifacts", "Existing private local immutable store directory")
	cmd.Flags().StringVar(azureURL, "azure-container-url", "", "Explicit HTTPS Azure container URL using existing credentials")
}

func newRunnerSubmitCommand(service *runnerunit.Service, openBackend openBackendFunc) *cobra.Command {
	var directory, azureURL string
	emit := func(cmd *cobra.Command, result runnerunit.Submission) error {
		data, err := runnerunit.EncodeSubmissionResult(result)
		if err != nil {
			return &ExitCodeError{Code: 2, Err: fmt.Errorf("validating submission output: %w", err)}
		}
		return emitRunnerOperationJSON(cmd, data, result.ExitCode, result.Status)
	}
	fail := func(cmd *cobra.Command, result runnerunit.Submission, stage string, cause error) error {
		_, diagnosticErr := fmt.Fprintf(cmd.ErrOrStderr(), "submission %s: %v\n", stage, cause)
		return errors.Join(emit(cmd, result), diagnosticErr)
	}
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Read one execution-submission JSON from stdin; reserve a distinct attempt or reuse a verified one",
		Long: `Read one waza.execution-submission JSON document from stdin and emit one
waza.execution-submission-result. Policy "distinct" (default) always reserves a
new execution ID and may name a retry or branch parent; policy "reuse" admits a
verified finalized source and makes zero engine calls. Existing records are
never overwritten.`,
		SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 || (cmd.Flags().Changed("store") && cmd.Flags().Changed("azure-container-url")) {
				return emit(cmd, runnersubmission.Rejected(runnersubmission.CodeFlagsInvalid))
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			data, err := readRunnerInputLimit(ctx, cmd.InOrStdin(), runnerunit.MaxSubmissionBytes)
			if err != nil {
				return fail(cmd, runnersubmission.Rejected(runnersubmission.CodeInputUnavailable), "input read", err)
			}
			if _, err := runnerunit.ParseSubmission(data); err != nil {
				return fail(cmd, runnersubmission.Rejected(runnersubmission.CodeInputInvalid), "input admission", err)
			}
			backend, closeBackend, err := openBackend(directory, azureURL)
			if err != nil {
				return fail(cmd, runnersubmission.OperationError(runnersubmission.CodeStorageUnavailable), "store open", err)
			}
			result, err := runnersubmission.SubmitCaptured(ctx, service, data, backend)
			var diagnosticErr error
			if err != nil {
				_, diagnosticErr = fmt.Fprintf(cmd.ErrOrStderr(), "submission operation: %v\n", err)
			}
			if closeErr := closeBackend(); closeErr != nil {
				_, closeDiagnosticErr := fmt.Fprintf(cmd.ErrOrStderr(), "submission store close: %v\n", closeErr)
				diagnosticErr = errors.Join(diagnosticErr, closeDiagnosticErr)
				if result.Status != "completed" && result.Status != "reused" {
					failed := runnersubmission.OperationError(runnersubmission.CodeStorageClose)
					failed.EngineCalls = result.EngineCalls
					return errors.Join(emit(cmd, failed), diagnosticErr)
				}
				// The finalized record stays authoritative; signal the close
				// failure through the process status only.
				if emitErr := emit(cmd, result); emitErr != nil {
					return errors.Join(emitErr, diagnosticErr)
				}
				return errors.Join(&ExitCodeError{Code: 2, Err: fmt.Errorf("submission store close failed")}, diagnosticErr)
			}
			return errors.Join(emit(cmd, result), diagnosticErr)
		},
	}
	addStoreFlags(cmd, &directory, &azureURL)
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fail(cmd, runnersubmission.Rejected(runnersubmission.CodeFlagsInvalid), "flags", err)
	})
	return cmd
}

func newRunnerLineageCommand(openBackend openBackendFunc) *cobra.Command {
	var directory, azureURL string
	var logical executionattempt.Logical
	cmd := &cobra.Command{
		Use:   "lineage",
		Short: "Print the ordered attempts, retry parents, reuse decisions and writer diagnostics for one logical work item",
		Long: `Read every ledger record for one logical work item and emit one
waza.execution-lineage JSON document. Order follows parent relationships, not
wall-clock time. Concurrent writers, missing parents, dispatched attempts without
completion and unverifiable completions are reported as diagnostics. The command
is read-only and fails closed when the store listing is truncated or a record is
unreadable.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("store") && cmd.Flags().Changed("azure-container-url") {
				return &ExitCodeError{Code: 6, Err: fmt.Errorf("--store and --azure-container-url are mutually exclusive")}
			}
			if err := executionledger.ValidateLogical(logical); err != nil {
				return &ExitCodeError{Code: 6, Err: err}
			}
			backend, closeBackend, err := openBackend(directory, azureURL)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: fmt.Errorf("opening store: %w", err)}
			}
			lineage, err := readLineage(cmd, backend, logical)
			if closeErr := closeBackend(); closeErr != nil && err == nil {
				err = fmt.Errorf("closing store: %w", closeErr)
			}
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			return emitRunnerOperationJSON(cmd, lineage, 0, "")
		},
	}
	cmd.Flags().StringVar(&logical.Run, "run", "", "Logical run ID (run-<sha256>)")
	cmd.Flags().StringVar(&logical.Work, "work", "", "Logical work ID (trial-<sha256>)")
	_ = cmd.MarkFlagRequired("run")
	_ = cmd.MarkFlagRequired("work")
	addStoreFlags(cmd, &directory, &azureURL)
	return cmd
}

func readLineage(cmd *cobra.Command, backend artifactmanifest.Backend, logical executionattempt.Logical) ([]byte, error) {
	ledger, err := executionledger.New(backend)
	if err != nil {
		return nil, fmt.Errorf("opening ledger: %w", err)
	}
	lineage, err := ledger.Lineage(cmd.Context(), logical)
	if err != nil {
		return nil, fmt.Errorf("reading lineage: %w", err)
	}
	return executionledger.EncodeLineage(*lineage)
}
