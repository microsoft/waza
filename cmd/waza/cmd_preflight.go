package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/microsoft/waza/internal/preflight"
	"github.com/spf13/cobra"
)

func newPreflightCommand() *cobra.Command {
	var format, contextDir string
	var strict bool
	cmd := &cobra.Command{
		Use:   "preflight <eval.yaml>",
		Short: "Inspect eval prerequisites offline without starting an agent",
		Long: `Inspect task files, paths, graders, locks, mocks, descriptive requirement
references, and static runtime capabilities using local evidence only.
Uses the same read-only project workspace skill defaults as run where applicable;
scenario-only disabled discovery never injects ambient skills.

Never starts an agent, model, subprocess, mock server, live service, or update
check. Verified means a static check, not a satisfied requirement, enforced
boundary, valid credential, or successful external state change.

Exit 1 for invalid configuration. Unresolved or unsupported prerequisites warn
and exit 0 by default; --strict opts into exit 1 for those diagnostics too.
Existing run/check exit policies are unchanged.`,
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "human" && format != "json" {
				return fmt.Errorf("invalid --format: use human or json")
			}
			report := preflight.Inspect(args[0], preflight.Options{ContextDir: contextDir})
			if format == "json" {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return fmt.Errorf("writing preflight JSON: %w", err)
				}
			} else if err := renderPreflightHuman(cmd.OutOrStdout(), report); err != nil {
				return err
			}
			if report.Failed(strict) {
				return &ExitCodeError{Code: 1, Err: fmt.Errorf("preflight prerequisites failed; inspect diagnostics")}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "human", "Output format: human or json")
	cmd.Flags().StringVar(&contextDir, "context-dir", "", "Context directory (relative to cwd; default: eval directory/fixtures)")
	cmd.Flags().BoolVar(&strict, "strict", false, "Exit 1 for unresolved or unsupported prerequisites as well as invalid configuration")
	return cmd
}

func renderPreflightHuman(w io.Writer, r *preflight.Report) error {
	if _, err := fmt.Fprintf(w, "Offline preflight: %s\nExecutor: %s\nInventory complete: %t (not assurance)\n", r.Source, r.Executor, r.Complete); err != nil {
		return fmt.Errorf("writing preflight summary: %w", err)
	}
	for _, task := range r.Tasks {
		if _, err := fmt.Fprintf(w, "Task %s: %s (enabled=%t, context=%s)\n", task.ID, task.Name, task.Enabled, task.ContextDir); err != nil {
			return fmt.Errorf("writing preflight task: %w", err)
		}
		for _, req := range task.Requirements {
			if _, err := fmt.Fprintf(w, "  Requirement %s [%s]: %s - %s\n", req.ID, req.Category, req.State, req.Meaning); err != nil {
				return fmt.Errorf("writing preflight requirement: %w", err)
			}
		}
	}
	for _, capability := range r.Capabilities {
		if _, err := fmt.Fprintf(w, "Capability %s: %s - %s\n", capability.Name, capability.State, capability.Meaning); err != nil {
			return fmt.Errorf("writing preflight capability: %w", err)
		}
	}
	for _, dep := range r.Dependencies {
		if _, err := fmt.Fprintf(w, "Dependency %s/%s: %s, %s (task=%s)\n", dep.Kind, dep.Name, dep.Mode, dep.State, dep.TaskID); err != nil {
			return fmt.Errorf("writing preflight dependency: %w", err)
		}
	}
	for _, d := range r.Diagnostics {
		if _, err := fmt.Fprintf(w, "%s %s [%s] %s task=%s requirement=%s: %s %s\n",
			d.State, d.Code, d.Severity, d.Source, d.TaskID, d.RequirementID, d.Message, d.Remediation); err != nil {
			return fmt.Errorf("writing preflight diagnostic: %w", err)
		}
	}
	return nil
}
