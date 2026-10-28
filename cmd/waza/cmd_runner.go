package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
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
					if action == "run" {
						return emitTerminal(cmd, runnerterminal.NewFailure(runnerterminal.InvalidInput, "input", "not_checked",
							"invalid_arguments", "runner run accepts no arguments; send one complete JSON request on stdin"), nil)
					}
					return fmt.Errorf("runner %s accepts no arguments; send one complete JSON request on stdin", action)
				}
				ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
				defer stop()
				data, err := readRunnerInput(ctx, cmd.InOrStdin())
				if err != nil {
					if action == "run" {
						return emitTerminal(cmd, runnerterminal.Failure("input", err), nil)
					}
					return &ExitCodeError{Code: 2, Err: err}
				}
				if action == "run" {
					envelope, binding := service.RunTerminal(ctx, data)
					return emitTerminal(cmd, envelope, binding)
				}
				report := runnerunit.Validate(data)
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
					return &ExitCodeError{Code: 2, Err: fmt.Errorf("writing runner unit receipt: %w", err)}
				}
				if code := report.ExitCode(); code != 0 {
					return &ExitCodeError{Code: code, Err: fmt.Errorf("runner %s: %s", action, report.Status)}
				}
				return nil
			},
		}
		if action == "run" {
			unit.Short = "Run one exact mock unit from JSON stdin; emit one terminal JSON result with exit 0-6"
			unit.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
				return emitTerminal(cmd, runnerterminal.NewFailure(runnerterminal.InvalidInput, "input", "not_checked",
					"invalid_flags", fmt.Sprintf("runner run flags: %v; send requirements in JSON", err)), nil)
			})
		}
		cmd.AddCommand(unit)
	}
	return cmd
}

// Supplied readers remain caller-owned. Inherited stdin needs a separate path
// because it can be a blocking pipe without deadline support.
func readRunnerInput(ctx context.Context, input io.Reader) (data []byte, resultErr error) {
	if input == os.Stdin {
		return readInheritedRunnerInput(ctx)
	}
	if file, ok := input.(*os.File); ok {
		if err := file.SetReadDeadline(time.Time{}); err == nil {
			finished := make(chan struct{})
			stop := context.AfterFunc(ctx, func() {
				defer close(finished)
				// The initial successful deadline call established poller support.
				if err := file.SetReadDeadline(time.Now()); err != nil {
					fmt.Fprintf(os.Stderr, "interrupting runner input: %v\n", err)
				}
			})
			defer func() {
				if !stop() {
					<-finished
				}
				if err := file.SetReadDeadline(time.Time{}); err != nil {
					resultErr = errors.Join(resultErr, fmt.Errorf("resetting runner input deadline: %w", err))
				}
			}()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("runner input interrupted: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(input, runnercontract.MaxRequestBytes+1))
	if cause := ctx.Err(); cause != nil {
		return nil, fmt.Errorf("runner input interrupted: %w", cause)
	}
	if err != nil {
		return nil, fmt.Errorf("reading runner request: %w", err)
	}
	return data, nil
}

func emitTerminal(cmd *cobra.Command, envelope runnerterminal.Envelope, binding *runnerterminal.Binding) (resultErr error) {
	writer := cmd.OutOrStdout()
	if writer == os.Stdout {
		code, err := emitInheritedRunnerTerminal(envelope, binding)
		if err != nil {
			return &ExitCodeError{Code: 2, Err: err}
		}
		if code != 0 {
			return &ExitCodeError{Code: code, Err: fmt.Errorf("runner run: %s", envelope.Outcome)}
		}
		return nil
	}
	code, err := runnerterminal.NewEmitter(writer).Emit(envelope, binding)
	if err != nil {
		return &ExitCodeError{Code: 2, Err: err}
	}
	if code != 0 {
		return &ExitCodeError{Code: code, Err: fmt.Errorf("runner run: %s", envelope.Outcome)}
	}
	return nil
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
