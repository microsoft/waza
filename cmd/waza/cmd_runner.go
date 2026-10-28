package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/spf13/cobra"
)

func newRunnerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "runner", Short: "Inspect the versioned runner contract (offline; no execution)",
		SilenceUsage: true,
	}
	cmd.AddCommand(&cobra.Command{
		Use: "capabilities", Short: "Print supported runner versions and capabilities as JSON",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(runnercontract.Capabilities())
		},
	})
	negotiate := &cobra.Command{
		Use: "negotiate", Short: "Read one JSON request from stdin and check compatibility without execution",
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				return emitNegotiation(cmd, runnercontract.Rejected("runner negotiate accepts no arguments; send one JSON request on stdin"))
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			type readResult struct {
				data []byte
				err  error
			}
			input := cmd.InOrStdin()
			read := make(chan readResult, 1)
			go func() {
				data, err := io.ReadAll(io.LimitReader(input, runnercontract.MaxRequestBytes+1))
				read <- readResult{data, err}
			}()
			select {
			case <-ctx.Done():
				return emitNegotiation(cmd, runnercontract.Rejected(fmt.Sprintf("negotiation input interrupted: %v", context.Cause(ctx))))
			case result := <-read:
				if result.err != nil {
					return emitNegotiation(cmd, runnercontract.Rejected(fmt.Sprintf("reading negotiation request: %v", result.err)))
				}
				return emitNegotiation(cmd, runnercontract.Negotiate(result.data))
			}
		},
	}
	negotiate.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return emitNegotiation(cmd, runnercontract.Rejected(fmt.Sprintf("runner negotiate flags: %v; send requirements in JSON", err)))
	})
	cmd.AddCommand(negotiate)
	return cmd
}

func emitNegotiation(cmd *cobra.Command, response runnercontract.Response) error {
	if err := json.NewEncoder(cmd.OutOrStdout()).Encode(response); err != nil {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("writing runner negotiation: %w", err)}
	}
	if response.ExitCode() != 0 {
		return &ExitCodeError{Code: response.ExitCode(), Err: fmt.Errorf("runner negotiation: %s", response.Status)}
	}
	return nil
}
