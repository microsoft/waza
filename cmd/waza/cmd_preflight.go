package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/workspace"
	"github.com/spf13/cobra"
)

func newPreflightCommand() *cobra.Command {
	var format, contextDir, skillsDir, evalsDir, evalFile string
	var strict, workspaceMode bool
	cmd := &cobra.Command{
		Use:   "preflight <eval.yaml> | --workspace",
		Short: "Inspect eval prerequisites offline without starting an agent",
		Long: `Inspect task files, paths, graders, locks, mocks, descriptive requirement
references, and static runtime capabilities using local evidence only.
Uses the same read-only project workspace skill defaults as run where applicable;
scenario-only disabled discovery never injects ambient skills.

Use --workspace to inspect each evaluation resolved for discovered skills, plus
target-free evaluations directly under the evals directory or its immediate
subdirectories, using the project's configured paths and filename precedence.
Workspace mode also accepts --skills-dir, --evals-dir, and --eval-file to
override those settings. When skill discovery is enabled and no eval-level
directories are specified, --skills-dir also selects the required-skill discovery
root. It does not change the configuration used by a later run.
Unavailable discovery symlinks are errors. Native workspace discovery does not
follow symlinked skill-scan roots or skill directories; workspace preflight
reports those unsupported paths instead of returning an empty inventory.
Resolvable resource links are allowed when they contain no discoverable skills.
If discovery fails, reports for independently discovered evaluations are still
printed; errors are written to stderr and the command exits 2.

Never starts an agent, model, subprocess, mock server, live service, or update
check. Verified means a static check, not a satisfied requirement, enforced
boundary, valid credential, or successful external state change.

Exit 1 for invalid configuration. Unresolved or unsupported prerequisites warn
and exit 0 by default; --strict opts into exit 1 for those diagnostics too.
An empty workspace with no discovered skills or evaluations exits 2.
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
				inventory, discoveryErr := inspectWorkspacePreflight(".", workspacePreflightOverrides{
					skillsDir: skillsDir,
					evalsDir:  evalsDir,
					evalFile:  evalFile,
				})
				reports := inventory.reports
				if len(reports) == 0 && discoveryErr == nil {
					if format == "json" {
						if err := json.NewEncoder(cmd.OutOrStdout()).Encode(reports); err != nil {
							return fmt.Errorf("writing preflight JSON: %w", err)
						}
					}
					if inventory.skills == 0 {
						diagnostic := preflight.Diagnostic{
							Code: "workspace.empty", State: preflight.Unresolved, Severity: "error", Source: inventory.root,
							Message:     "No skills or evaluation files were discovered.",
							Remediation: "Run from the project root or pass an explicit eval path to waza preflight.",
						}
						if format == "json" {
							if err := json.NewEncoder(cmd.ErrOrStderr()).Encode(diagnostic); err != nil {
								return fmt.Errorf("writing empty workspace diagnostic: %w", err)
							}
						} else if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s %s [%s] %s: %s %s\n", diagnostic.State, diagnostic.Code, diagnostic.Severity, diagnostic.Source, diagnostic.Message, diagnostic.Remediation); err != nil {
							return fmt.Errorf("writing empty workspace diagnostic: %w", err)
						}
						return &ExitCodeError{Code: ExitError}
					}
					if format != "json" {
						if _, err := fmt.Fprintln(cmd.OutOrStdout(), "No evaluation files found; agent evaluation not run."); err != nil {
							return fmt.Errorf("writing empty workspace preflight notice: %w", err)
						}
					}
					return nil
				}
				if format == "json" {
					if reports == nil {
						reports = []*preflight.Report{}
					}
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
				if discoveryErr != nil {
					return discoveryErr
				}
				for _, report := range reports {
					if report.Failed(strict) {
						return &ExitCodeError{Code: 1, Err: fmt.Errorf("preflight prerequisites failed; inspect diagnostics")}
					}
				}
				return nil
			}
			if skillsDir != "" || evalsDir != "" || evalFile != "" {
				return fmt.Errorf("--skills-dir, --evals-dir, and --eval-file require --workspace")
			}
			report := preflight.Inspect(args[0], preflight.Options{ContextDir: contextDir, DefaultSkillPaths: standaloneSkillDefaults(args[0])})
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
	cmd.Flags().BoolVar(&workspaceMode, "workspace", false, "Preflight evaluations for discovered skills and target-free evaluations in the evals directory")
	cmd.Flags().StringVar(&skillsDir, "skills-dir", "", "Override the configured skills directory in workspace mode")
	cmd.Flags().StringVar(&evalsDir, "evals-dir", "", "Override the configured evaluations directory in workspace mode")
	cmd.Flags().StringVar(&evalFile, "eval-file", "", "Override the configured evaluation filename in workspace mode")
	cmd.Flags().BoolVar(&strict, "strict", false, "Exit 1 for unresolved or unsupported prerequisites as well as invalid configuration")
	return cmd
}

type workspacePreflightOverrides struct {
	skillsDir string
	evalsDir  string
	evalFile  string
}

type workspacePreflightInventory struct {
	reports []*preflight.Report
	root    string
	skills  int
}

func inspectWorkspacePreflight(root string, overrides workspacePreflightOverrides) (workspacePreflightInventory, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return workspacePreflightInventory{}, fmt.Errorf("resolving workspace path: %w", err)
	}
	paths, err := loadProjectWorkspaceDetectPaths(absRoot)
	if err != nil {
		return workspacePreflightInventory{}, err
	}
	resolveOverride := func(dir string) string {
		if filepath.IsAbs(dir) {
			return filepath.Clean(dir)
		}
		return filepath.Join(absRoot, dir)
	}

	if overrides.skillsDir != "" {
		paths.skillsDir = resolveOverride(overrides.skillsDir)
	}
	if overrides.evalsDir != "" {
		paths.evalsDir = resolveOverride(overrides.evalsDir)
	}
	if overrides.evalFile != "" {
		paths.evalFile = overrides.evalFile
	}

	detectOptions := append(paths.options(), workspace.WithDiscoverySymlinkValidation())
	wsCtx, err := workspace.DetectContext(absRoot, detectOptions...)
	partialContext := err != nil && wsCtx != nil
	var discoveryErrors []error
	if err != nil {
		discoveryErrors = append(discoveryErrors, fmt.Errorf("discovering workspace skills: %w", err))
		if wsCtx == nil {
			wsCtx = &workspace.WorkspaceContext{Root: absRoot, EvalsDir: paths.evalsDir, EvalFile: paths.evalFile}
		}
	}
	// Explicit-path run treats a workspace discovery failure as no owning
	// skill; use that same context for each inspected evaluation.
	runCtx, _ := resolveWorkspace(nil)
	optionsForEval := func(path string) preflight.Options {
		opts := preflight.Options{}
		if dir := standaloneSkillSpecPathInWorkspace(path, runCtx).skillDir; dir != "" {
			opts.DefaultSkillPaths = []string{dir}
		}
		if overrides.skillsDir != "" {
			opts.WorkspaceSkillsDir = paths.skillsDir
		}
		return opts
	}
	reports := make([]*preflight.Report, 0, len(wsCtx.Skills))
	seen := make(map[string]struct{}, len(wsCtx.Skills))
	markSeen := func(path string) bool {
		if _, ok := seen[path]; ok {
			return true
		}
		canonical, err := canonicalPath(path)
		if err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("resolving evaluation path %q: %w", path, err))
		} else {
			if _, ok := seen[canonical]; ok {
				return true
			}
			seen[canonical] = struct{}{}
		}
		seen[path] = struct{}{}
		return false
	}
	skillContexts := []*workspace.WorkspaceContext{wsCtx}
	if partialContext && filepath.Clean(wsCtx.Root) != absRoot {
		localOptions := append(detectOptions, workspace.WithoutAncestorDiscovery())
		localCtx, err := workspace.DetectContext(absRoot, localOptions...)
		if err != nil {
			discoveryErrors = append(discoveryErrors, fmt.Errorf("discovering requested workspace skills: %w", err))
		}
		if localCtx != nil {
			skillContexts = append(skillContexts, localCtx)
		}
	}
	skillCount := 0
	for _, skillCtx := range skillContexts {
		skillCount += len(skillCtx.Skills)
		for _, skill := range skillCtx.Skills {
			evalPath, err := workspace.FindEval(skillCtx, skill.Name)
			if err != nil {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("finding evaluation for skill %q: %w", skill.Name, err))
				continue
			}
			if evalPath == "" {
				continue
			}
			path, err := filepath.Abs(evalPath)
			if err != nil {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("resolving evaluation for skill %q: %w", skill.Name, err))
				continue
			}
			path = filepath.Clean(path)
			if markSeen(path) {
				continue
			}
			reports = append(reports, preflight.Inspect(path, optionsForEval(path)))
		}
	}
	// Target-free scenario evals use the separated layout without a matching skill.
	evalContexts := []*workspace.WorkspaceContext{wsCtx}
	// A partial ancestor context must not hide independent evals in the requested workspace.
	if partialContext && !filepath.IsAbs(wsCtx.EvalsDir) && filepath.Clean(wsCtx.Root) != absRoot {
		evalsDir := wsCtx.EvalsDir
		if evalsDir == "" {
			evalsDir = projectconfig.DefaultEvalsDir
		}
		ancestorInfo, ancestorErr := os.Stat(filepath.Join(wsCtx.Root, evalsDir))
		requestedInfo, requestedErr := os.Stat(filepath.Join(absRoot, evalsDir))
		if ancestorErr != nil || requestedErr != nil || !os.SameFile(ancestorInfo, requestedInfo) {
			requested := *wsCtx
			requested.Root = absRoot
			evalContexts = append(evalContexts, &requested)
		}
	}
	for _, evalCtx := range evalContexts {
		extra, err := separatedWorkspaceEvals(evalCtx)
		if err != nil {
			discoveryErrors = append(discoveryErrors, err)
		}
		for _, path := range extra {
			if markSeen(path) {
				continue
			}
			reports = append(reports, preflight.Inspect(path, optionsForEval(path)))
		}
	}
	return workspacePreflightInventory{reports: reports, root: absRoot, skills: skillCount}, errors.Join(discoveryErrors...)
}

// separatedWorkspaceEvals lists evaluation files directly under the evals root
// and its immediate subdirectories, using the configured filename precedence.
func separatedWorkspaceEvals(wsCtx *workspace.WorkspaceContext) ([]string, error) {
	evalsDir := wsCtx.EvalsDir
	if evalsDir == "" {
		evalsDir = projectconfig.DefaultEvalsDir
	}
	if !filepath.IsAbs(evalsDir) {
		evalsDir = filepath.Join(wsCtx.Root, evalsDir)
	}
	names := []string{projectconfig.DefaultEvalFile}
	if wsCtx.EvalFile != "" && wsCtx.EvalFile != projectconfig.DefaultEvalFile {
		names = []string{wsCtx.EvalFile, projectconfig.DefaultEvalFile}
	}
	firstEval := func(dir string) (string, error) {
		for _, name := range names {
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if errors.Is(err, fs.ErrNotExist) {
				if err := workspace.ValidateDiscoveryPath(path); err != nil {
					return "", err
				}
				continue
			}
			if err != nil {
				return "", fmt.Errorf("checking evaluation %q: %w", path, err)
			}
			if info.Mode().IsRegular() {
				return filepath.Clean(path), nil
			}
		}
		return "", nil
	}
	entries, err := os.ReadDir(evalsDir)
	if errors.Is(err, fs.ErrNotExist) {
		if err := workspace.ValidateDiscoveryPath(evalsDir); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing evaluations in %q: %w", evalsDir, err)
	}
	var paths []string
	var discoveryErrors []error
	rootEval, err := firstEval(evalsDir)
	if err != nil {
		discoveryErrors = append(discoveryErrors, err)
	} else if rootEval != "" {
		paths = append(paths, rootEval)
	}
	for _, entry := range entries {
		dir := filepath.Join(evalsDir, entry.Name())
		info, err := os.Stat(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && len(names) > 1 && rootEval == filepath.Clean(filepath.Join(evalsDir, names[0])) &&
				filepath.Clean(dir) == filepath.Clean(filepath.Join(evalsDir, names[1])) {
				continue
			}
			if pathErr := workspace.ValidateDiscoveryPath(dir); pathErr != nil {
				discoveryErrors = append(discoveryErrors, pathErr)
			} else {
				discoveryErrors = append(discoveryErrors, fmt.Errorf("checking evaluation entry %q: %w", dir, err))
			}
			continue
		}
		if !info.IsDir() {
			continue
		}
		path, err := firstEval(dir)
		if err != nil {
			discoveryErrors = append(discoveryErrors, err)
			continue
		}
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, errors.Join(discoveryErrors...)
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

// standaloneSkillDefaults mirrors run's owning-skill default for an explicit
// evaluation path inside a single-skill workspace.
func standaloneSkillDefaults(evalPath string) []string {
	if dir := standaloneSkillSpecPath(evalPath).skillDir; dir != "" {
		return []string{dir}
	}
	return nil
}
