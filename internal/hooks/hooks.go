package hooks

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	wazatemplate "github.com/microsoft/waza/internal/template"
)

// hookWaitDelay bounds how long a hook may keep its output pipes open after it
// exits or is canceled, for example through a background descendant. After it,
// the pipes are closed and the hook returns.
var hookWaitDelay = 5 * time.Second

// HookConfig defines a single hook command.
type HookConfig struct {
	Command          string `yaml:"command" json:"command"`
	WorkingDirectory string `yaml:"working_directory,omitempty" json:"working_directory,omitempty"`
	ExitCodes        []int  `yaml:"exit_codes,omitempty" json:"exit_codes,omitempty"`
	ErrorOnFail      bool   `yaml:"error_on_fail,omitempty" json:"error_on_fail,omitempty"`
}

// HooksConfig holds all lifecycle hooks.
type HooksConfig struct {
	BeforeRun  []HookConfig `yaml:"before_run,omitempty" json:"before_run,omitempty"`
	AfterRun   []HookConfig `yaml:"after_run,omitempty" json:"after_run,omitempty"`
	BeforeTask []HookConfig `yaml:"before_task,omitempty" json:"before_task,omitempty"`
	AfterTask  []HookConfig `yaml:"after_task,omitempty" json:"after_task,omitempty"`
}

// Runner executes hook commands at lifecycle points.
type Runner struct {
	Verbose bool
	Vars    map[string]string
}

// Execute runs all hooks for a given lifecycle point.
// name identifies the lifecycle point (e.g. "before_run") for logging and error context.
func (r *Runner) Execute(ctx context.Context, name string, hooks []HookConfig) error {
	for i, h := range hooks {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("hook %s: context canceled: %w", name, err)
		}

		if err := r.runHook(ctx, name, i, h); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) runHook(ctx context.Context, name string, index int, h HookConfig) error {
	if strings.TrimSpace(h.Command) == "" {
		return fmt.Errorf("hook %s[%d]: empty command", name, index)
	}

	command, err := wazatemplate.Render(h.Command, &wazatemplate.Context{Vars: r.Vars})
	if err != nil {
		return fmt.Errorf("hook %s[%d]: render command: %w", name, index, err)
	}

	parts := strings.Fields(command)
	//nolint:gosec // hook commands are user-configured in eval YAML, not untrusted input
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)

	if h.WorkingDirectory != "" {
		cmd.Dir = h.WorkingDirectory
	}
	cmd.WaitDelay = hookWaitDelay
	configureHookProcess(cmd)

	output, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrWaitDelay) {
		// The hook exited successfully but a descendant still held its output.
		fmt.Printf("[WARN] hook %s[%d] left a background process holding its output; continuing\n", name, index)
		err = nil
	}

	if r.Verbose && len(output) > 0 {
		fmt.Printf("[hook:%s] %s\n", name, string(output))
	}

	if err != nil {
		var exitErr *exec.ExitError
		if ok := errors.As(err, &exitErr); ok {
			exitCode := exitErr.ExitCode()

			if !isAcceptableExit(exitCode, h.ExitCodes) {
				if h.ErrorOnFail {
					return fmt.Errorf("hook %s[%d]: command exited with code %d", name, index, exitCode)
				}
				fmt.Printf("[WARN] hook %s[%d] exited with code %d (continuing)\n", name, index, exitCode)
			}
		} else {
			// Non-exit error (e.g. command not found)
			if h.ErrorOnFail {
				return fmt.Errorf("hook %s[%d]: %w", name, index, err)
			}
			fmt.Printf("[WARN] hook %s[%d] failed: %v\n", name, index, err)
		}
		return nil
	}

	// err == nil means exit code 0; verify 0 is acceptable
	if !isAcceptableExit(0, h.ExitCodes) {
		if h.ErrorOnFail {
			return fmt.Errorf("hook %s[%d]: command exited with code 0 but expected %v", name, index, h.ExitCodes)
		}
		fmt.Printf("[WARN] hook %s[%d] exited with code 0 but expected %v (continuing)\n", name, index, h.ExitCodes)
	}

	return nil
}

// isAcceptableExit checks whether exitCode is in the allowed list.
// An empty allowedCodes list defaults to allowing only exit code 0.
func isAcceptableExit(exitCode int, allowedCodes []int) bool {
	if len(allowedCodes) == 0 {
		return exitCode == 0
	}
	for _, code := range allowedCodes {
		if exitCode == code {
			return true
		}
	}
	return false
}
