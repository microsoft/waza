package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/spf13/cobra"
)

var newPublicRunnerService = func() *runnercontract.Service { return runnercontract.NewService() }

func newRunnerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "runner", Short: "Versioned, exact single-unit runner (no discovery or retries)",
		SilenceUsage: true,
	}
	cmd.AddCommand(&cobra.Command{
		Use: "capabilities", Short: "Print the versioned runner capability advertisement",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				SchemaVersion   string   `json:"schema_version"`
				Versions        []string `json:"versions"`
				ResultVersions  []string `json:"result_versions"`
				IdentityVersion string   `json:"identity_version"`
				Capabilities    []string `json:"capabilities"`
			}{runnercontract.RequestVersion, []string{runnercontract.RequestVersion},
				[]string{runnercontract.ResultVersion}, runnercontract.IdentityVersion, runnercontract.Capabilities()})
		},
	})
	run := &cobra.Command{
		Use: "run", Short: "Read one request from stdin and write one terminal JSON result to stdout",
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 {
				result := runnercontract.Rejected("runner run accepts no arguments; send one JSON request on stdin")
				return emitRunnerResult(cmd, &result)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), runnerSignals()...)
			defer stop()
			input := cmd.InOrStdin()
			stopRead := func() bool { return true }
			if closer, ok := input.(io.Closer); ok {
				stopRead = context.AfterFunc(ctx, func() { _ = closer.Close() })
			}
			data, err := io.ReadAll(io.LimitReader(input, runnercontract.MaxRequestBytes+1))
			stopRead()
			var result runnercontract.Result
			if ctx.Err() != nil {
				result = runnercontract.Interrupted(ctx.Err())
			} else if err != nil {
				result = runnercontract.Rejected(fmt.Sprintf("reading request: %v", err))
			} else {
				result = newPublicRunnerService().Run(ctx, data)
			}
			return emitRunnerResult(cmd, &result)
		},
	}
	run.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		result := runnercontract.Rejected(fmt.Sprintf("runner run flags: %v; send selection in the JSON request", err))
		return emitRunnerResult(cmd, &result)
	})
	validate := &cobra.Command{
		Use: "validate-result", Short: "Validate a saved terminal envelope from stdin (offline)",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), runnercontract.MaxResultBytes+1))
			if err != nil {
				return err
			}
			if len(data) > runnercontract.MaxResultBytes {
				return fmt.Errorf("terminal envelope exceeds 16 MiB")
			}
			_, err = runnercontract.ParseResult(data)
			return err
		},
	}
	cmd.AddCommand(run, validate, newRunnerAdaptCommand())
	return cmd
}

func emitRunnerResult(cmd *cobra.Command, result *runnercontract.Result) error {
	if err := runnercontract.Write(cmd.OutOrStdout(), result); err != nil {
		return &ExitCodeError{Code: 2, Err: fmt.Errorf("terminal result could not be written: %w", err)}
	}
	if result.ExitCode != 0 {
		return &ExitCodeError{Code: result.ExitCode}
	}
	return nil
}

func newRunnerAdaptCommand() *cobra.Command {
	var evalPath, taskPath, runKey string
	var selection runnercontract.Selection
	var timeoutMS int
	cmd := &cobra.Command{
		Use: "adapt", Short: "Capture explicit local eval/task sources as a runner 1.0 request",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if evalPath == "" || taskPath == "" {
				return fmt.Errorf("--eval and --task are required exact file paths; batch discovery remains waza run")
			}
			read := func(name string) (data []byte, err error) {
				f, err := os.Open(name)
				if err != nil {
					return nil, err
				}
				defer func() { err = errors.Join(err, f.Close()) }()
				data, err = io.ReadAll(io.LimitReader(f, runnercontract.MaxRequestBytes+1))
				if err == nil && len(data) > runnercontract.MaxRequestBytes {
					err = fmt.Errorf("source exceeds runner request limit")
				}
				return data, err
			}
			eval, err := read(evalPath)
			if err != nil {
				return fmt.Errorf("reading exact eval: %w", err)
			}
			task, err := read(taskPath)
			if err != nil {
				return fmt.Errorf("reading exact task: %w", err)
			}
			request, err := runnercontract.AdaptLocal(eval, task, runKey, selection, timeoutMS)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(request)
		},
	}
	cmd.Flags().StringVar(&evalPath, "eval", "", "Exact eval source path (no discovery)")
	cmd.Flags().StringVar(&taskPath, "task", "", "Exact task source path (no glob)")
	cmd.Flags().StringVar(&runKey, "run-key", "", "Stable caller-owned logical run key")
	cmd.Flags().StringVar(&selection.Shard, "shard", "", "Declared shard key")
	cmd.Flags().StringVar(&selection.Task, "task-id", "", "Exact authored task ID")
	cmd.Flags().IntVar(&selection.Trial, "trial", 0, "One-based declared trial")
	cmd.Flags().IntVar(&selection.Attempt, "attempt", 0, "One-based declared attempt; no automatic retry")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", 60000, "Whole unit deadline in milliseconds")
	return cmd
}
