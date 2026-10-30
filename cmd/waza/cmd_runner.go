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
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/spf13/cobra"
)

func newRunnerCommand() *cobra.Command {
	return newRunnerCommandWithService(runnerunit.NewService(nil))
}

func newRunnerCommandWithService(service *runnerunit.Service) *cobra.Command {
	cmd := &cobra.Command{
		Use: "runner", Short: "Negotiate, validate or execute one exact captured work unit",
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
	for _, action := range []string{"validate", "run"} {
		unit := &cobra.Command{
			Use: action, Short: "Read one complete JSON request from stdin (" + action + " exact inline.mock-text unit)",
			SilenceErrors: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				if len(args) != 0 {
					return fmt.Errorf("runner %s accepts no arguments; send one complete JSON request on stdin", action)
				}
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				data, err := readRunnerInput(ctx, cmd.InOrStdin())
				if err != nil {
					return &ExitCodeError{Code: 2, Err: err}
				}
				var report runnerunit.Report
				if action == "run" {
					report = service.Run(ctx, data)
				} else {
					report = runnerunit.Validate(data)
				}
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
					return &ExitCodeError{Code: 2, Err: fmt.Errorf("writing runner unit receipt: %w", err)}
				}
				if code := report.ExitCode(); code != 0 {
					return &ExitCodeError{Code: code, Err: fmt.Errorf("runner %s: %s", action, report.Status)}
				}
				return nil
			},
		}
		cmd.AddCommand(unit)
	}
	return cmd
}

// Cancellation must not depend on a producer closing an inherited stdin pipe.
func readRunnerInput(ctx context.Context, input io.Reader) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	read := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(input, runnercontract.MaxRequestBytes+1))
		read <- result{data, err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("runner input interrupted: %w", context.Cause(ctx))
	case value := <-read:
		if value.err != nil {
			return nil, fmt.Errorf("reading runner request: %w", value.err)
		}
		return value.data, nil
	}
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
