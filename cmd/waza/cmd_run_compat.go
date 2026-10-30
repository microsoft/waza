package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/runnercompat"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func runLocalCompatibility(cmd *cobra.Command, args []string, service *runnerunit.Service) error {
	var unsupported []string
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		switch flag.Name {
		case "runner-compat", "output", "no-summary", "no-update-check", "debug":
		default:
			unsupported = append(unsupported, "--"+flag.Name)
		}
	})
	if len(unsupported) != 0 {
		return fmt.Errorf("runner compatibility cannot preserve %s; remove --runner-compat to retain the original local workflow", strings.Join(unsupported, ", "))
	}
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("--runner-compat requires one exact eval filename; remove the flag to use workspace discovery")
	}
	if _, err := projectconfig.FindPath("."); err == nil {
		return fmt.Errorf("runner compatibility cannot preserve ambient .waza.yaml defaults, discovery or storage; run from an unconfigured directory or remove --runner-compat")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking local project configuration: %w; retain waza run without --runner-compat", err)
	}
	evalPath, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolving exact local eval: %w", err)
	}
	// Native loading opens the original spelling, but resolves tasks from its
	// cleaned directory. A symlink followed by ".." makes these paths distinct.
	eval, err := readRunnerAdapterSource(args[0])
	if err != nil {
		return err
	}
	spec, err := models.ParseCapturedEvalSpecOffline(eval, evalPath)
	if err != nil {
		return fmt.Errorf("captured local eval: %w; remove --runner-compat for historical local loading", err)
	}
	if len(spec.Tasks) != 1 || strings.TrimSpace(spec.Tasks[0]) == "" || strings.ContainsAny(spec.Tasks[0], "*?[") {
		return fmt.Errorf("--runner-compat requires one literal eval.tasks filename; remove the flag for batch/glob/discovery workflows")
	}
	if filepath.IsAbs(spec.Tasks[0]) {
		return fmt.Errorf("--runner-compat requires an eval-relative task filename; remove --runner-compat for absolute task entries")
	}
	taskPath := filepath.Join(filepath.Dir(evalPath), spec.Tasks[0])
	if err := runnercompat.ValidateLiteralTaskPath(taskPath); err != nil {
		return fmt.Errorf("%w; remove --runner-compat to retain native task selection", err)
	}
	task, err := readRunnerAdapterSource(taskPath)
	if err != nil {
		return err
	}
	tc, err := models.ParseCapturedTestCaseOffline(task, taskPath, func(string) ([]byte, error) {
		return nil, fmt.Errorf("runner compatibility requires an inline prompt; retain waza run without --runner-compat for prompt_file")
	})
	if err != nil {
		return fmt.Errorf("captured local task: %w; remove --runner-compat for historical local loading", err)
	}
	if err := rejectAdapterFixtures(evalPath); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(evalPath), "trigger_tests.yaml")); err == nil {
		return fmt.Errorf("runner compatibility cannot preserve adjacent trigger tests; remove --runner-compat to run the original suite")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking local trigger tests: %w", err)
	}
	request, err := runnercompat.AdaptLocal(eval, task, runnercompat.Options{
		EvalPath: evalPath, TaskPath: taskPath, RunKey: "local", Selection: runnercontract.Selection{
			Shard: "local", Task: tc.TestID, Trial: 1, Attempt: 1,
		},
	})
	if err != nil {
		return err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encoding local runner request: %w", err)
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	outcome, err := service.RunLocal(ctx, data)
	if err != nil {
		return err
	}
	for _, task := range outcome.TestOutcomes {
		for _, run := range task.Runs {
			if run.ErrorMsg != "" {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "[ERROR] %s\n\n", run.ErrorMsg); err != nil {
					return fmt.Errorf("reporting local runner execution error: %w", err)
				}
			}
		}
	}
	printSummary(outcome)
	path, err := cmd.Flags().GetString("output")
	if err != nil {
		return err
	}
	if path != "" {
		if err := saveOutcome(outcome, path); err != nil {
			return fmt.Errorf("saving local runner output: %w", err)
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "\nResults saved to: %s\n", path); err != nil {
			return fmt.Errorf("reporting saved local runner output: %w", err)
		}
	}
	if outcome.Digest.Failed > 0 || outcome.Digest.Errors > 0 {
		return &TestFailureError{Message: fmt.Sprintf("benchmark completed: %d failed and %d error(s)", outcome.Digest.Failed, outcome.Digest.Errors)}
	}
	return nil
}
