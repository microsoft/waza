package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/workspace"
	"github.com/spf13/cobra"
)

func newPreflightCommand() *cobra.Command {
	var format, contextDir string
	var strict, workspaceMode bool
	cmd := &cobra.Command{
		Use:   "preflight <eval.yaml> | --workspace",
		Short: "Inspect eval prerequisites offline without starting an agent",
		Long: `Inspect task files, paths, graders, locks, mocks, descriptive requirement
references, and static runtime capabilities using local evidence only.
Uses the same read-only project workspace skill defaults as run where applicable;
scenario-only disabled discovery never injects ambient skills.

Use --workspace to inspect each evaluation resolved for discovered skills using
the project's configured paths and filename precedence.

Never starts an agent, model, subprocess, mock server, live service, or update
check. Verified means a static check, not a satisfied requirement, enforced
boundary, valid credential, or successful external state change.

Exit 1 for invalid configuration. Unresolved or unsupported prerequisites warn
and exit 0 by default; --strict opts into exit 1 for those diagnostics too.
Existing run/check exit policies are unchanged.`,
		Args: func(_ *cobra.Command, args []string) error {
			if workspaceMode {
				if len(args) == 0 {
					return nil
				}
				return fmt.Errorf("--workspace does not accept an eval path")
			}
			if len(args) != 1 {
				return fmt.Errorf("requires exactly one eval path unless --workspace is set")
			}
			return nil
		},
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "human" && format != "json" {
				return fmt.Errorf("invalid --format: use human or json")
			}
			if workspaceMode {
				if contextDir != "" {
					return fmt.Errorf("--context-dir cannot be used with --workspace")
				}
				reports, err := inspectWorkspacePreflight(".")
				if err != nil {
					return err
				}
				if len(reports) == 0 {
					if format == "json" {
						if err := json.NewEncoder(cmd.OutOrStdout()).Encode(reports); err != nil {
							return fmt.Errorf("writing preflight JSON: %w", err)
						}
					} else if _, err := fmt.Fprintln(cmd.OutOrStdout(), "No evaluation files found; agent evaluation not run."); err != nil {
						return fmt.Errorf("writing empty workspace preflight notice: %w", err)
					}
					return nil
				}
				if format == "json" {
					encoder := json.NewEncoder(cmd.OutOrStdout())
					encoder.SetIndent("", "  ")
					if err := encoder.Encode(reports); err != nil {
						return fmt.Errorf("writing preflight JSON: %w", err)
					}
				} else {
					for i, report := range reports {
						if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Evaluation %d of %d: %s\n", i+1, len(reports), report.Source); err != nil {
							return fmt.Errorf("writing workspace preflight heading: %w", err)
						}
						if err := renderPreflightHuman(cmd.OutOrStdout(), report); err != nil {
							return err
						}
					}
				}
				for _, report := range reports {
					if report.Failed(strict) {
						return &ExitCodeError{Code: 1, Err: fmt.Errorf("preflight prerequisites failed; inspect diagnostics")}
					}
				}
				return nil
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
	cmd.Flags().BoolVar(&workspaceMode, "workspace", false, "Preflight evaluations resolved for discovered skills")
	cmd.Flags().BoolVar(&strict, "strict", false, "Exit 1 for unresolved or unsupported prerequisites as well as invalid configuration")
	return cmd
}

func inspectWorkspacePreflight(root string) ([]*preflight.Report, error) {
	cfg, err := projectconfig.Load(root)
	if err != nil {
		return nil, fmt.Errorf("loading workspace configuration: %w", err)
	}
	wsCtx, err := workspace.DetectContext(root,
		workspace.WithSkillsDir(cfg.Paths.Skills),
		workspace.WithEvalsDir(cfg.Paths.Evals),
		workspace.WithEvalFile(cfg.Files.EvalFile),
	)
	if err != nil {
		return nil, fmt.Errorf("discovering workspace skills: %w", err)
	}

	reports := make([]*preflight.Report, 0, len(wsCtx.Skills))
	seen := make(map[string]struct{}, len(wsCtx.Skills))
	for _, skill := range wsCtx.Skills {
		evalPath, err := workspace.FindEval(wsCtx, skill.Name)
		if err != nil {
			return nil, fmt.Errorf("finding evaluation for skill %q: %w", skill.Name, err)
		}
		if evalPath == "" {
			continue
		}
		path, err := filepath.Abs(evalPath)
		if err != nil {
			return nil, fmt.Errorf("resolving evaluation for skill %q: %w", skill.Name, err)
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		reports = append(reports, preflight.Inspect(path, preflight.Options{}))
	}
	return reports, nil
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
