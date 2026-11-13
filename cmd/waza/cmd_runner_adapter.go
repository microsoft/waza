package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/microsoft/waza/internal/runneradapter"
	"github.com/spf13/cobra"
)

type adapterContractOptions struct {
	directory, azureURL, endpoint, executable string
	storeChanged, azureChanged                bool
}

type adapterContractFactory func(options adapterContractOptions) (runneradapter.Contract, error)

func newRunnerAdapterCommand() *cobra.Command {
	return newRunnerAdapterCommandWithContract(defaultAdapterContract)
}

// defaultAdapterContract selects the public transport. It forwards store flags
// verbatim and never chooses a store, worker or endpoint on its own.
func defaultAdapterContract(options adapterContractOptions) (runneradapter.Contract, error) {
	if options.endpoint != "" {
		return runneradapter.HTTPContract{BaseURL: options.endpoint}, nil
	}
	executable := options.executable
	if executable == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locating waza executable: %w", err)
		}
		executable = self
	}
	return runneradapter.ProcessContract{Executable: executable, StoreArgs: adapterStoreArgs(options), Stderr: os.Stderr}, nil
}

func adapterStoreArgs(options adapterContractOptions) []string {
	switch {
	case options.azureChanged:
		return []string{"--azure-container-url", options.azureURL}
	case options.storeChanged:
		return []string{"--store", options.directory}
	}
	return nil
}

func translateAdapterInput(ctx context.Context, cmd *cobra.Command) runneradapter.Translation {
	data, err := readRunnerInputLimit(ctx, cmd.InOrStdin(), runneradapter.MaxDescriptorBytes)
	if err != nil {
		return runneradapter.Unreadable(data, err)
	}
	return runneradapter.Translate(data)
}

func newRunnerAdapterCommandWithContract(contractFor adapterContractFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adapter",
		Short: "Translate external work descriptors into exact public runner contract calls",
		Long: `Portable external execution adapter. It translates one
waza.adapter-work-descriptor into exactly one public runner contract call
(runner submit, runner checkpoint create|resume or runner lineage) and maps the
public reply to a waza.adapter-result with the returned manifest. It owns no
queue, placement, fleet or retry policy: the external scheduler decides what to
dispatch, when, where and whether to retry.`,
		SilenceUsage: true,
	}
	translate := &cobra.Command{
		Use:   "translate",
		Short: "Read one work descriptor from stdin and print its exact translation without dispatching",
		Args:  cobra.NoArgs, SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			translation := translateAdapterInput(ctx, cmd)
			encoded, err := runneradapter.EncodeTranslation(translation)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: fmt.Errorf("validating adapter translation: %w", err)}
			}
			exitCode := 0
			if translation.Status != "translated" {
				exitCode = 6
			}
			return emitRunnerOperationJSON(cmd, encoded, exitCode, translation.Status)
		},
	}
	var options adapterContractOptions
	dispatch := &cobra.Command{
		Use:   "dispatch",
		Short: "Read one work descriptor from stdin, make its one public call and print the adapter result",
		Long: `Read one waza.adapter-work-descriptor from stdin, translate it, make exactly
one public runner contract call and emit one waza.adapter-result. The process
exit code equals the public exit code (6 for an untranslatable descriptor, 2
when the public contract was unavailable or contradictory). The adapter never
retries; redeliver or retry from the external scheduler.`,
		Args: cobra.NoArgs, SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			options.storeChanged = cmd.Flags().Changed("store")
			options.azureChanged = cmd.Flags().Changed("azure-container-url")
			if options.storeChanged && options.azureChanged {
				return &ExitCodeError{Code: 6, Err: fmt.Errorf("--store and --azure-container-url are mutually exclusive")}
			}
			if options.endpoint != "" && (options.storeChanged || options.azureChanged || options.executable != "") {
				return &ExitCodeError{Code: 6, Err: fmt.Errorf("--endpoint uses the server's store; omit --store, --azure-container-url and --waza")}
			}
			// Signals stay captured through dispatch so the public command is
			// interrupted gracefully and its reply is still mapped and emitted.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			translation := translateAdapterInput(ctx, cmd)
			contract, err := contractFor(options)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			result := runneradapter.Dispatch(ctx, contract, translation, nil)
			encoded, err := runneradapter.EncodeResult(result)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: fmt.Errorf("validating adapter result: %w", err)}
			}
			return emitRunnerOperationJSON(cmd, encoded, result.ExitCode, result.Status)
		},
	}
	addStoreFlags(dispatch, &options.directory, &options.azureURL)
	dispatch.Flags().StringVar(&options.endpoint, "endpoint", "", "Public waza HTTP API base URL (for example http://127.0.0.1:3000) instead of the CLI")
	dispatch.Flags().StringVar(&options.executable, "waza", "", "waza executable that serves the public CLI contract (default: this executable)")
	cmd.AddCommand(translate, dispatch)
	return cmd
}
