package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/runnercompat"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/spf13/cobra"
)

// Registration belongs to the sequential execution layer:
// newRunnerCommand must add newRunnerAdaptCommand alongside validate/run.
func newRunnerAdaptCommand() *cobra.Command {
	var options runnercompat.Options
	var timeoutMS int
	cmd := &cobra.Command{
		Use: "adapt", Short: "Capture one compatible local mock/text suite as runner JSON (offline)",
		Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, flag := range []string{"eval", "task", "run-key", "shard", "task-id", "trial", "attempt"} {
				if !cmd.Flags().Changed(flag) {
					return fmt.Errorf("--%s is required explicitly; runner adapt never discovers or selects adjacent work", flag)
				}
			}
			if cmd.Flags().Changed("timeout-ms") {
				options.TimeoutMS = &timeoutMS
			}
			fixtures := filepath.Join(filepath.Dir(options.EvalPath), "fixtures")
			if _, err := os.Lstat(fixtures); err == nil {
				return fmt.Errorf("cannot preserve local default fixture behavior: %q exists; keep using waza run (runner adapt does not capture context directories)", fixtures)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("checking local default fixture directory %q: %w", fixtures, err)
			}
			eval, evalErr := readRunnerAdapterSource(options.EvalPath)
			task, taskErr := readRunnerAdapterSource(options.TaskPath)
			if err := errors.Join(evalErr, taskErr); err != nil {
				return err
			}
			request, err := runnercompat.AdaptLocal(eval, task, options)
			if err != nil {
				return err
			}
			data, err := json.Marshal(request)
			if err != nil {
				return fmt.Errorf("encoding adapted runner request: %w", err)
			}
			data = append(data, '\n')
			n, err := cmd.OutOrStdout().Write(data)
			if err != nil {
				return fmt.Errorf("writing adapted runner request: %w", err)
			}
			if n != len(data) {
				return fmt.Errorf("writing adapted runner request: %w", io.ErrShortWrite)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&options.EvalPath, "eval", "", "Exact local eval filename (no discovery)")
	cmd.Flags().StringVar(&options.TaskPath, "task", "", "Exact sole declared task filename (no glob expansion)")
	cmd.Flags().StringVar(&options.RunKey, "run-key", "", "Caller-owned logical run key")
	cmd.Flags().StringVar(&options.Selection.Shard, "shard", "", "Exact declared shard key")
	cmd.Flags().StringVar(&options.Selection.Task, "task-id", "", "Exact captured task id")
	cmd.Flags().IntVar(&options.Selection.Trial, "trial", 0, "Explicit local trial (must be 1)")
	cmd.Flags().IntVar(&options.Selection.Attempt, "attempt", 0, "Explicit local attempt (must be 1)")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", 0, "Optional assertion of the effective local deadline in milliseconds")
	return cmd
}

func readRunnerAdapterSource(path string) (data []byte, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening exact adapter source %q: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing adapter source %q: %w", path, closeErr))
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspecting adapter source %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("adapter source %q must be a regular file", path)
	}
	data, err = io.ReadAll(io.LimitReader(file, runnercontract.MaxRequestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading adapter source %q: %w", path, err)
	}
	if len(data) > runnercontract.MaxRequestBytes {
		return nil, fmt.Errorf("adapter source %q exceeds the runner request limit", path)
	}
	return data, nil
}
