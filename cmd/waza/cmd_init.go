package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/workspace"
)

// skillEntry holds inventory information about a discovered skill.
type skillEntry struct {
	Name     string
	Dir      string
	HasEval  bool
	EvalPath string
}

func newInitCommand() *cobra.Command {
	var noSkill bool
	var flagSkillsDir, flagEvalsDir, flagResultsDir string

	cmd := &cobra.Command{
		Use:   "init [directory]",
		Short: "Initialize a waza project",
		Long: `Initialize a waza project with the required directory structure.

Idempotently ensures the project has:
  - skills/         Skill definitions directory
  - evals/          Evaluation suites directory
  - .github/workflows/eval.yml  CI pipeline
  - .gitignore      With waza-specific entries
  - README.md       Getting started guide

Only creates what's missing — never overwrites existing files.

After scaffolding, prompts to create a new skill (calls waza new skill internally).

Use --no-skill to skip the skill creation prompt.

Path detection: waza init scans the project directory for existing skills,
evals, and results. Detected paths are offered as defaults in interactive
mode. Use --skills-dir, --evals-dir, --results-dir to override in
non-interactive sessions.

If no directory is specified, the current directory is used.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return initCommandE(cmd, args, noSkill, flagSkillsDir, flagEvalsDir, flagResultsDir)
		},
	}

	cmd.Flags().BoolVar(&noSkill, "no-skill", false, "Skip the first-skill creation prompt")
	cmd.Flags().StringVar(&flagSkillsDir, "skills-dir", "", "Skills directory (overrides detection and defaults)")
	cmd.Flags().StringVar(&flagEvalsDir, "evals-dir", "", "Evals directory (overrides detection and defaults)")
	cmd.Flags().StringVar(&flagResultsDir, "results-dir", "", "Results directory (overrides detection and defaults)")

	return cmd
}

// displayInventory scans the workspace and prints a structured inventory table.
// Returns the discovered skill entries for downstream logic.
func displayInventory(out io.Writer, absDir string, opts ...workspace.DetectOption) []skillEntry {
	var inventory []skillEntry

	wsCtx, wsErr := workspace.DetectContext(absDir, opts...)
	if wsErr == nil && wsCtx != nil {
		for _, si := range wsCtx.Skills {
			evalPath, _ := workspace.FindEval(wsCtx, si.Name) //nolint:errcheck // missing eval is expected
			inventory = append(inventory, skillEntry{
				Name:     si.Name,
				Dir:      si.Dir,
				HasEval:  evalPath != "",
				EvalPath: evalPath,
			})
		}
	}

	if len(inventory) == 0 {
		fmt.Fprintf(out, "\nNo skills found yet.\n") //nolint:errcheck
		return inventory
	}

	missing := 0
	for _, inv := range inventory {
		if !inv.HasEval {
			missing++
		}
	}

	// Use tabwriter for alignment, then post-process to add emoji indicators.
	// Tabwriter computes column widths with plain-text placeholders; emoji are
	// swapped in after flush so ANSI/width issues don't affect alignment.
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintf(out, "\nSkills in this project:\n") //nolint:errcheck
	for _, inv := range inventory {
		if inv.HasEval {
			fmt.Fprintf(tw, "  {ok}\tSkill: %s\tEval: %s\n", inv.Name, inv.Name) //nolint:errcheck
		} else {
			fmt.Fprintf(tw, "  {miss}\tSkill: %s\tmissing eval\n", inv.Name) //nolint:errcheck
		}
	}
	tw.Flush() //nolint:errcheck
	result := buf.String()
	result = strings.ReplaceAll(result, "{ok}", "✅")
	result = strings.ReplaceAll(result, "{miss}", "❌")
	fmt.Fprint(out, result)                                                           //nolint:errcheck
	fmt.Fprintf(out, "\n%d skills found, %d missing eval\n", len(inventory), missing) //nolint:errcheck

	return inventory
}

// detectedPaths holds paths discovered by scanning the project directory.
type detectedPaths struct {
	SkillsDir  string // relative path to the directory containing skill subdirectories
	EvalsDir   string // relative path to the directory containing eval subdirectories
	ResultsDir string // relative path to the directory containing result files
}

// detectPaths scans root for existing skills, evals, and results directories.
// It walks the full tree (skipping hidden dirs, node_modules, vendor) and
// returns the first match found for each path type as a relative path.
func detectPaths(root string) detectedPaths {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return detectedPaths{}
	}

	var dp detectedPaths

	_ = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		// Skip hidden directories, node_modules, vendor
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return fs.SkipDir
			}
		}

		if d.IsDir() {
			return nil
		}

		// All three detected — stop walking
		if dp.SkillsDir != "" && dp.EvalsDir != "" && dp.ResultsDir != "" {
			return fs.SkipAll
		}

		fileName := d.Name()

		// Detect skills: SKILL.md → grandparent is the skills root.
		// Only accept if the grandparent directory name suggests a skills folder.
		if dp.SkillsDir == "" && fileName == "SKILL.md" {
			skillDir := filepath.Dir(path)
			skillsRoot := filepath.Dir(skillDir)
			rootName := strings.ToLower(filepath.Base(skillsRoot))
			if strings.Contains(rootName, "skill") {
				if rel, relErr := filepath.Rel(absRoot, skillsRoot); relErr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
					dp.SkillsDir = filepath.ToSlash(rel) + "/"
				}
			}
		}

		// Detect evals: eval.yaml → grandparent is the evals root.
		// Only accept if the grandparent directory name suggests an evals folder.
		if dp.EvalsDir == "" && (fileName == "eval.yaml" || fileName == "eval.yml") {
			evalDir := filepath.Dir(path)
			evalsRoot := filepath.Dir(evalDir)
			rootName := strings.ToLower(filepath.Base(evalsRoot))
			if strings.Contains(rootName, "eval") {
				if rel, relErr := filepath.Rel(absRoot, evalsRoot); relErr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
					dp.EvalsDir = filepath.ToSlash(rel) + "/"
				}
			}
		}

		// Detect results: *results*.json → containing directory is the results root
		if dp.ResultsDir == "" && strings.HasSuffix(fileName, ".json") && strings.Contains(strings.ToLower(fileName), "results") {
			resultsRoot := filepath.Dir(path)
			if rel, relErr := filepath.Rel(absRoot, resultsRoot); relErr == nil && rel != "." {
				dp.ResultsDir = filepath.ToSlash(rel) + "/"
			}
		}

		return nil
	})

	return dp
}

func initCommandE(cmd *cobra.Command, args []string, noSkill bool, flagSkillsDir, flagEvalsDir, flagResultsDir string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	out := cmd.OutOrStdout()
	projectName := filepath.Base(absOrDefault(dir))
	isTTY := term.IsTerminal(int(os.Stdin.Fd()))
	absDir := absOrDefault(dir)

	fmt.Fprintf(out, "🔧 Initializing waza project: %s\n", projectName) //nolint:errcheck

	wazaConfigPath := filepath.Join(absDir, ".waza.yaml")
	_, wazaStatErr := os.Stat(wazaConfigPath)
	needConfigPrompt := wazaStatErr != nil
	needSkillPrompt := !noSkill

	// --- Phase 1: Configuration (if .waza.yaml missing) ---
	var engine, model string
	var skillsPath, evalsPath, resultsPath string
	files := projectconfig.New().Files
	var createSkill, scaffoldMissing bool
	var configuredSkills, configuredEvals string
	hasConfiguredPaths := false
	engine = projectconfig.DefaultEngine
	model = projectconfig.DefaultModel
	skillsPath = projectconfig.DefaultSkillsDir
	evalsPath = projectconfig.DefaultEvalsDir
	resultsPath = projectconfig.DefaultResultsDir

	// If .waza.yaml exists, read engine/model from it so scaffolded evals
	// match the project's actual config instead of hardcoded defaults.
	if !needConfigPrompt {
		if cfg, err := projectconfig.Load(absDir); err == nil {
			engine = cfg.Defaults.Engine
			model = cfg.Defaults.Model
			skillsPath = cfg.Paths.Skills
			evalsPath = cfg.Paths.Evals
			resultsPath = cfg.Paths.Results
			files = cfg.Files
			configuredSkills, configuredEvals = cfg.Paths.Skills, cfg.Paths.Evals
			hasConfiguredPaths = true
		}
	}

	// --- Path resolution: CLI flags > detected paths > config defaults ---
	// Only run detection when at least one path is still using its default
	// value and no corresponding CLI flag has been provided.
	needDetection := needConfigPrompt ||
		(skillsPath == projectconfig.DefaultSkillsDir && flagSkillsDir == "") ||
		(evalsPath == projectconfig.DefaultEvalsDir && flagEvalsDir == "") ||
		(resultsPath == projectconfig.DefaultResultsDir && flagResultsDir == "")

	var detected detectedPaths
	if needDetection {
		detected = detectPaths(absDir)
		if detected.SkillsDir != "" && skillsPath == projectconfig.DefaultSkillsDir {
			skillsPath = detected.SkillsDir
		}
		if detected.EvalsDir != "" && evalsPath == projectconfig.DefaultEvalsDir {
			evalsPath = detected.EvalsDir
		}
		if detected.ResultsDir != "" && resultsPath == projectconfig.DefaultResultsDir {
			resultsPath = detected.ResultsDir
		}
	}

	// CLI flags take highest priority
	if flagSkillsDir != "" {
		skillsPath = flagSkillsDir
	}
	if flagEvalsDir != "" {
		evalsPath = flagEvalsDir
	}
	if flagResultsDir != "" {
		resultsPath = flagResultsDir
	}
	if hasConfiguredPaths {
		workflowPath := filepath.Join(absDir, ".github", "workflows", "eval.yml")
		if _, err := os.Lstat(workflowPath); errors.Is(err, fs.ErrNotExist) {
			if err := rejectConfiguredPathOverride("--skills-dir", "paths.skills", flagSkillsDir, configuredSkills); err != nil {
				return err
			}
			if err := rejectConfiguredPathOverride("--evals-dir", "paths.evals", flagEvalsDir, configuredEvals); err != nil {
				return err
			}
		}
	}

	// Show detection results when paths were discovered
	if needConfigPrompt && (detected.SkillsDir != "" || detected.EvalsDir != "" || detected.ResultsDir != "") {
		fmt.Fprintf(out, "\n🔍 Detected existing paths:\n") //nolint:errcheck
		if detected.SkillsDir != "" {
			fmt.Fprintf(out, "   Skills:  %s\n", detected.SkillsDir) //nolint:errcheck
		}
		if detected.EvalsDir != "" {
			fmt.Fprintf(out, "   Evals:   %s\n", detected.EvalsDir) //nolint:errcheck
		}
		if detected.ResultsDir != "" {
			fmt.Fprintf(out, "   Results: %s\n", detected.ResultsDir) //nolint:errcheck
		}
	}

	// Validate resolved paths are safe (applies to all code paths: flags, detection, defaults)
	for _, p := range []string{skillsPath, evalsPath, resultsPath} {
		cleaned := filepath.Clean(p)
		if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
			return fmt.Errorf("path %q must be relative and within the project directory", p)
		}
		if strings.ContainsAny(p, ":#") {
			return fmt.Errorf("path %q contains invalid characters", p)
		}
	}

	var inventory []skillEntry
	var skillsMissingEvals int

	if isTTY {
		// Engine selector (separate form)
		if needConfigPrompt {
			fmt.Fprintf(out, "\nConfigure project:\n\n") //nolint:errcheck

			engineForm := huh.NewForm(
				huh.NewGroup(
					huh.NewSelect[string]().
						Title("Default evaluation engine").
						Description("Choose how evals are executed").
						Options(
							huh.NewOption("Copilot SDK — real model execution", "copilot-sdk"),
							huh.NewOption("Mock — fast iteration, no API calls", "mock"),
						).
						Value(&engine),
				),
			).WithInput(cmd.InOrStdin()).WithOutput(out)

			if err := engineForm.Run(); err != nil {
				engine = projectconfig.DefaultEngine
			}

			// Model selector (hidden when engine ≠ copilot-sdk)
			if engine == "copilot-sdk" {
				modelForm := huh.NewForm(
					huh.NewGroup(
						huh.NewSelect[string]().
							Title("Default model").
							Description("Model used for evaluations").
							Options(
								huh.NewOption("claude-sonnet-4.6", "claude-sonnet-4.6"),
								huh.NewOption("claude-sonnet-4.5", "claude-sonnet-4.5"),
								huh.NewOption("claude-haiku-4.5", "claude-haiku-4.5"),
								huh.NewOption("claude-opus-4.6", "claude-opus-4.6"),
								huh.NewOption("claude-opus-4.6-fast", "claude-opus-4.6-fast"),
								huh.NewOption("claude-opus-4.5", "claude-opus-4.5"),
								huh.NewOption("claude-sonnet-4", "claude-sonnet-4"),
								huh.NewOption("gemini-3-pro-preview", "gemini-3-pro-preview"),
								huh.NewOption("gpt-5.3-codex", "gpt-5.3-codex"),
								huh.NewOption("gpt-5.2-codex", "gpt-5.2-codex"),
								huh.NewOption("gpt-5.2", "gpt-5.2"),
								huh.NewOption("gpt-5.1-codex-max", "gpt-5.1-codex-max"),
								huh.NewOption("gpt-5.1-codex", "gpt-5.1-codex"),
								huh.NewOption("gpt-5.1", "gpt-5.1"),
								huh.NewOption("gpt-5", "gpt-5"),
								huh.NewOption("gpt-5.1-codex-mini", "gpt-5.1-codex-mini"),
								huh.NewOption("gpt-5-mini", "gpt-5-mini"),
								huh.NewOption("gpt-4.1", "gpt-4.1"),
							).
							Value(&model),
					),
				).WithInput(cmd.InOrStdin()).WithOutput(out)

				if err := modelForm.Run(); err != nil {
					model = projectconfig.DefaultModel
				}
			}

			pathsForm := huh.NewForm(
				huh.NewGroup(
					huh.NewInput().
						Title("Skills directory").
						Description("Where skill definitions live").
						Value(&skillsPath),
					huh.NewInput().
						Title("Evals directory").
						Description("Where evaluation suites live").
						Value(&evalsPath),
					huh.NewInput().
						Title("Results directory").
						Description("Where evaluation results are saved").
						Value(&resultsPath),
				),
			).WithInput(cmd.InOrStdin()).WithOutput(out)

			if err := pathsForm.Run(); err != nil {
				// keep current values on error
				_ = err
			}
		}

		// --- Phase 2: Discover & Display Inventory (using configured paths) ---
		inventory = displayInventory(out, absDir,
			workspace.WithSkillsDir(skillsPath),
			workspace.WithEvalsDir(evalsPath),
			workspace.WithEvalFile(files.EvalFile))

		for _, inv := range inventory {
			if !inv.HasEval {
				skillsMissingEvals++
			}
		}

		// --- Phase 3: Fix Missing Evals (separate prompt) ---
		if len(inventory) > 0 && skillsMissingEvals > 0 {
			scaffoldMissing = true
			confirmEvals := huh.NewForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title(fmt.Sprintf("Set up default evals for %d skill(s) missing them?", skillsMissingEvals)).
						Affirmative("Yes").
						Negative("No").
						Value(&scaffoldMissing),
				),
			).WithInput(cmd.InOrStdin()).WithOutput(out)

			if err := confirmEvals.Run(); err != nil {
				scaffoldMissing = false
			}

			if scaffoldMissing {
				scaffoldMissingEvals(absDir, evalsPath, inventory, engine, model, files)
				// Re-display inventory showing updated state
				inventory = displayInventory(out, absDir,
					workspace.WithSkillsDir(skillsPath),
					workspace.WithEvalsDir(evalsPath),
					workspace.WithEvalFile(files.EvalFile))
				skillsMissingEvals = 0
				for _, inv := range inventory {
					if !inv.HasEval {
						skillsMissingEvals++
					}
				}
			}
		}

		// --- Phase 4: Create New Skill (separate prompt) ---
		if needSkillPrompt {
			createSkill = true // default to Yes
			title := "Create a new skill?"
			if len(inventory) > 0 {
				title = "Create another skill?"
			}
			confirmSkill := huh.NewForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title(title).
						Affirmative("Yes").
						Negative("No").
						Value(&createSkill),
				),
			).WithInput(cmd.InOrStdin()).WithOutput(out)

			if err := confirmSkill.Run(); err != nil {
				createSkill = false
			}
		}
	} else {
		// Non-TTY: use defaults, skip forms
		fmt.Fprintf(out, "\nUsing defaults: engine=%s, model=%s, scaffold missing evals=yes, create skill=no\n", engine, model) //nolint:errcheck

		// Discover inventory using configured paths
		inventory = displayInventory(out, absDir,
			workspace.WithSkillsDir(skillsPath),
			workspace.WithEvalsDir(evalsPath),
			workspace.WithEvalFile(files.EvalFile))

		for _, inv := range inventory {
			if !inv.HasEval {
				skillsMissingEvals++
			}
		}
		scaffoldMissing = skillsMissingEvals > 0

		if scaffoldMissing {
			scaffoldMissingEvals(absDir, evalsPath, inventory, engine, model, files)
			// Re-display inventory after scaffolding
			inventory = displayInventory(out, absDir,
				workspace.WithSkillsDir(skillsPath),
				workspace.WithEvalsDir(evalsPath),
				workspace.WithEvalFile(files.EvalFile))
			skillsMissingEvals = 0
			for _, inv := range inventory {
				if !inv.HasEval {
					skillsMissingEvals++
				}
			}
		}
	}

	// --- Phase 5: Create/verify project structure ---
	wazaConfigContent := ""
	if needConfigPrompt {
		wazaConfigContent = generateWazaConfig(engine, model, skillsPath, evalsPath, resultsPath)
	}

	configLabel := fmt.Sprintf("Project defaults (%s, %s)", engine, model)

	workflow, err := ciWorkflow(skillsPath, evalsPath, files.EvalFile, filepath.Join(evalsPath, "my-skill", files.EvalFile))
	if err != nil {
		return err
	}
	entries := []scaffold.FileEntry{
		{Path: filepath.Join(absDir, skillsPath), Label: "Skill definitions", IsDir: true},
		{Path: filepath.Join(absDir, evalsPath), Label: "Evaluation suites", IsDir: true},
		{Path: filepath.Join(absDir, ".waza.yaml"), Label: configLabel, Content: wazaConfigContent},
		{Path: filepath.Join(absDir, ".github", "workflows", "eval.yml"), Label: "CI pipeline", Content: workflow},
		{Path: filepath.Join(absDir, ".gitignore"), Label: "Build artifacts excluded", Content: initGitignore()},
		{Path: filepath.Join(absDir, "README.md"), Label: "Getting started guide", Content: initReadme(projectName)},
	}
	evalsRoot := filepath.Join(absDir, evalsPath)
	evalsHaveFiles := false
	// WalkDir does not follow a symlinked root, so inspect its target. A
	// target outside the project cannot be kept by this repository's Git.
	walkRoot := evalsRoot
	if resolved, err := filepath.EvalSymlinks(evalsRoot); err == nil && resolved != evalsRoot {
		walkRoot = resolved
		if !pathWithinResolvedDir(absDir, resolved) {
			evalsHaveFiles = true
		}
	}
	walkErr := filepath.WalkDir(walkRoot, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			evalsHaveFiles = true
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		return fmt.Errorf("checking evaluation directory %s: %w", evalsRoot, walkErr)
	}
	if !evalsHaveFiles {
		entries = append(entries, scaffold.FileEntry{
			Path:    filepath.Join(evalsRoot, ".gitkeep"),
			Label:   "Keep empty evaluation directory in Git",
			Content: "# Preserve this directory for generated CI.\n",
		})
	}

	fw := scaffold.NewFileWriter(absDir)
	inv, err := fw.Write(entries)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\nProject structure:\n\n") //nolint:errcheck
	inv.Fprint(out)

	created := inv.CreatedCount()

	// --- Phase 5b: Summary ---
	fmt.Fprintln(out) //nolint:errcheck
	if created == 0 {
		fmt.Fprintf(out, "✅ Project up to date.\n") //nolint:errcheck
	} else if created == len(entries) {
		fmt.Fprintf(out, "✅ Project created — %d items set up.\n", created) //nolint:errcheck
	} else {
		fmt.Fprintf(out, "✅ Repaired — %d item(s) added.\n", created) //nolint:errcheck
	}

	// --- Phase 5c: Create skill if requested ---
	if createSkill {
		origDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get working directory: %w", err)
		}
		if err := os.Chdir(absDir); err != nil {
			return fmt.Errorf("failed to change to project directory: %w", err)
		}
		defer os.Chdir(origDir) //nolint:errcheck

		fmt.Fprintln(out) //nolint:errcheck
		if err := newCommandE(cmd, nil, "", ""); err != nil {
			return err
		}

		// Re-display final inventory after skill creation
		displayInventory(out, absDir,
			workspace.WithSkillsDir(skillsPath),
			workspace.WithEvalsDir(evalsPath),
			workspace.WithEvalFile(files.EvalFile))
	}

	// --- Phase 6: Next steps (first run only, TTY only) ---
	if created > 0 && isTTY {
		printNextSteps(out)
	}

	return nil
}

// scaffoldMissingEvals creates eval suites for skills that lack them.
func scaffoldMissingEvals(absDir, evalsDir string, inventory []skillEntry, engine, model string, files projectconfig.FilesConfig) {
	for _, inv := range inventory {
		if inv.HasEval {
			continue
		}
		evalDir := filepath.Join(absDir, evalsDir, inv.Name)
		tasksDir := filepath.Join(evalDir, "tasks")
		fixturesDir := filepath.Join(evalDir, "fixtures")

		for _, d := range []string{tasksDir, fixturesDir} {
			os.MkdirAll(d, 0o755) //nolint:errcheck
		}
		os.WriteFile(filepath.Join(evalDir, files.EvalFile), []byte(scaffold.EvalYAMLWithTaskGlob(inv.Name, engine, model, files.TaskGlob)), 0o644) //nolint:errcheck
		for name, content := range scaffold.TaskFilesWithSuffix(files.TaskFileSuffix) {
			os.WriteFile(filepath.Join(tasksDir, name), []byte(content), 0o644) //nolint:errcheck
		}
		os.WriteFile(filepath.Join(fixturesDir, "sample.py"), []byte(scaffold.Fixture()), 0o644) //nolint:errcheck
	}
}

func printNextSteps(out io.Writer) {
	fmt.Fprintln(out)                                                         //nolint:errcheck
	fmt.Fprintln(out, "Next steps:")                                          //nolint:errcheck
	fmt.Fprintln(out)                                                         //nolint:errcheck
	fmt.Fprintln(out, "  waza new skill <name>    Create another skill")      //nolint:errcheck
	fmt.Fprintln(out, "  waza dev <name>          Improve skill compliance")  //nolint:errcheck
	fmt.Fprintln(out, "  waza run                 Run all evaluations")       //nolint:errcheck
	fmt.Fprintln(out, "  waza check               Check all skill readiness") //nolint:errcheck
	fmt.Fprintln(out)                                                         //nolint:errcheck
}

// absOrDefault returns the absolute path, falling back to the input on error.
func absOrDefault(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// --- Init template content ---

// rejectConfiguredPathOverride prevents generated CI from validating a root
// that ordinary runs, which read .waza.yaml, would not select.
func rejectConfiguredPathOverride(flag, key, override, configured string) error {
	if override == "" || filepath.Clean(override) == filepath.Clean(configured) {
		return nil
	}
	return fmt.Errorf("%s %q conflicts with .waza.yaml %s %q; generated CI must use the configured path. Update .waza.yaml or omit %s", flag, override, key, configured, flag)
}

// pathWithinResolvedDir reports whether resolvedPath is dir or inside dir
// after resolving dir's own symlinks.
func pathWithinResolvedDir(dir, resolvedPath string) bool {
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(resolvedDir, resolvedPath)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// ciWorkflow renders the generated workflow. GitHub Actions evaluates ${{ in
// otherwise quoted scalars, so literal paths containing it are rejected.
func ciWorkflow(skillsPath, evalsPath, evalFile, defaultEvalPath string) (string, error) {
	for _, value := range []string{skillsPath, evalsPath, evalFile, defaultEvalPath} {
		if strings.Contains(value, "${{") {
			return "", fmt.Errorf("generated CI cannot use path %q: GitHub Actions would evaluate its ${{ expression; choose a path without ${{", value)
		}
	}
	content := `name: Validate and Evaluate Skills

on:
  pull_request:
  workflow_dispatch:
    inputs:
      run-agent:
        description: 'Intentionally run a trusted evaluation (requires configured credentials/model access)'
        type: boolean
        default: false
      eval-path:
        description: 'Reviewed evaluation file to execute'
        type: string
        default: __DEFAULT_EVAL_PATH__

permissions:
  contents: read

env:
  # Standalone release; azd extension availability is a separate contract.
  WAZA_VERSION: '0.42.1'

jobs:
  validate:
    name: Offline configuration inventory (not an evaluation)
    runs-on: ubuntu-24.04
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - name: Install checksum-verified standalone Waza
        run: |
          curl -fsSL \
            "https://raw.githubusercontent.com/microsoft/waza/v${WAZA_VERSION}/.github/scripts/install-waza-release.sh" \
            -o "$RUNNER_TEMP/install-waza-release.sh"
          bash "$RUNNER_TEMP/install-waza-release.sh" "$WAZA_VERSION"
      - name: Observe installed version
        run: waza --version
      - name: Inventory evaluation configuration offline
        env:
          SKILLS_PATH: __SKILLS_PATH__
          EVALS_PATH: __EVALS_PATH__
          EVAL_FILE: __EVAL_FILE__
        run: |
          if [ ! -d "$EVALS_PATH" ]; then
            echo "::error::Evaluation directory is missing: $EVALS_PATH"
            exit 1
          fi
          waza preflight --workspace --skills-dir "$SKILLS_PATH" --evals-dir "$EVALS_PATH" --eval-file "$EVAL_FILE"
          echo "Offline inventory only. Unresolved prerequisites are warnings, not runtime readiness or evaluation success."

  agent:
    name: Explicit trusted evaluation
    if: github.event_name == 'workflow_dispatch' && inputs.run-agent && github.ref == format('refs/heads/{0}', github.event.repository.default_branch)
    needs: validate
    runs-on: ubuntu-24.04
    timeout-minutes: 45
    # Restrict this environment to the default branch and require reviewers
    # before adding WAZA_COPILOT_TOKEN; the branch check below is not a boundary.
    environment: waza-agent
    steps:
      # Expression string comparison ignores case; Git refs do not.
      - name: Require exact default-branch ref
        id: exact-default-ref
        env:
          DEFAULT_REF: refs/heads/${{ github.event.repository.default_branch }}
        run: |
          if [ "$GITHUB_REF" != "$DEFAULT_REF" ]; then
            echo "::error::Trusted evaluations run only from $DEFAULT_REF, not $GITHUB_REF."
            exit 1
          fi
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - name: Install checksum-verified standalone Waza
        run: |
          curl -fsSL \
            "https://raw.githubusercontent.com/microsoft/waza/v${WAZA_VERSION}/.github/scripts/install-waza-release.sh" \
            -o "$RUNNER_TEMP/install-waza-release.sh"
          bash "$RUNNER_TEMP/install-waza-release.sh" "$WAZA_VERSION"
      - name: Observe installed version
        run: waza --version
      - name: Run reviewed evaluation
        id: run-eval
        env:
          EVAL_PATH: ${{ inputs.eval-path }}
          COPILOT_GITHUB_TOKEN: ${{ secrets.WAZA_COPILOT_TOKEN }}
        run: |
          rm -f -- results.json
          if [ -z "$COPILOT_GITHUB_TOKEN" ]; then
            echo "::error::Configure WAZA_COPILOT_TOKEN with Copilot/model access in the protected waza-agent environment before opting in."
            exit 1
          fi
          if [ ! -f "$EVAL_PATH" ]; then
            echo "::error::Review and select an existing evaluation file."
            exit 1
          fi
          set +e
          waza run "$EVAL_PATH" --no-cache --output results.json
          exit_code=$?
          set -e
          echo "exit_code=$exit_code" >> "$GITHUB_OUTPUT"
      - name: Validate fresh result artifact and retain verdict
        id: validate-result
        if: always()
        env:
          RUN_EXIT_CODE: ${{ steps.run-eval.outputs.exit_code }}
        run: |
          curl -fsSL \
            "https://raw.githubusercontent.com/microsoft/waza/v${WAZA_VERSION}/.github/scripts/validate-waza-result.sh" \
            -o "$RUNNER_TEMP/validate-waza-result.sh"
          bash "$RUNNER_TEMP/validate-waza-result.sh" results.json 0 "$RUN_EXIT_CODE"
          if ! jq -e -s 'length == 1 and (.[0].config.engine_type == "copilot-sdk")' < results.json >/dev/null; then
            echo "::error::Result executor is not copilot-sdk; mock or unknown execution cannot satisfy the agent-evidence gate."
            exit 1
          fi
      - name: Upload results
        if: always()
        uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
        with:
          name: eval-results
          path: results.json
          retention-days: 30
          if-no-files-found: error
      - name: Enforce evaluation status
        if: always()
        env:
          VALIDATION_OUTCOME: ${{ steps.validate-result.outcome }}
          EVALUATION_STATUS: ${{ steps.validate-result.outputs.status }}
        run: |
          if [ "$VALIDATION_OUTCOME" != "success" ] || [ "$EVALUATION_STATUS" != "success" ]; then
            exit 1
          fi
`
	// JSON string quoting is also valid YAML quoting for user-selected paths.
	quotedSkillsPath, _ := json.Marshal(filepath.ToSlash(skillsPath))
	quotedPath, _ := json.Marshal(filepath.ToSlash(evalsPath))
	quotedEvalFile, _ := json.Marshal(evalFile)
	quotedDefault, _ := json.Marshal(filepath.ToSlash(defaultEvalPath))
	return strings.NewReplacer(
		"__SKILLS_PATH__", string(quotedSkillsPath),
		"__EVALS_PATH__", string(quotedPath),
		"__EVAL_FILE__", string(quotedEvalFile),
		"__DEFAULT_EVAL_PATH__", string(quotedDefault),
	).Replace(content), nil
}

func initGitignore() string {
	return `results/
.waza-cache/
coverage.txt
*.exe
`
}

func initReadme(projectName string) string {
	return fmt.Sprintf(`# %s

## Getting Started

1. Create a new skill:
   `+"`"+``+"`"+``+"`"+`bash
    waza new skill my-skill
   `+"`"+``+"`"+``+"`"+`

2. Edit your skill:
   - Update `+"`"+`skills/my-skill/SKILL.md`+"`"+` with your skill definition
   - Customize eval tasks in `+"`"+`evals/my-skill/tasks/`+"`"+`
   - Add test fixtures to `+"`"+`evals/my-skill/fixtures/`+"`"+`

3. Run evaluations:
   `+"`"+``+"`"+``+"`"+`bash
   waza run                    # run all evals
   waza run my-skill           # run one skill's evals
   `+"`"+``+"`"+``+"`"+`

4. Check compliance:
   `+"`"+``+"`"+``+"`"+`bash
   waza check                  # check all skills
   waza dev my-skill           # improve with real-time scoring
   `+"`"+``+"`"+``+"`"+`

5. Open a pull request for automatic **offline configuration inventory**.
   CI installs checksum-verified standalone Waza 0.42.1 (not the azd extension)
   and prints the installed version. It runs preflight without starting an agent,
   model, or grader subprocess. Invalid configuration fails; unresolved runtime
   prerequisites warn and are not verified readiness. No eval files means an
   explicit not-run notice, not a successful evaluation. No result artifact is
   produced by the offline job.

## Intentional agent-backed CI

Review the evaluation, executor, model, tools, graders, and dependencies first.
Configure the **waza-agent** GitHub environment and its **WAZA_COPILOT_TOKEN**
secret: a supported Copilot token with model access. Before adding the secret,
restrict the environment's deployment branches to the default branch and require
reviewers. The workflow's branch check is not a security boundary: a writer can
dispatch a modified workflow from another branch.
CI passes it as **COPILOT_GITHUB_TOKEN**, not the workflow's default GitHub token.
Local login state does not authenticate a hosted runner.

On the repository's default branch, manually dispatch the generated workflow,
set **run-agent** to true, and choose **eval-path**. It defaults to offline-only;
the generated workflow skips the agent job for pull requests and non-default branches. Intentional
execution can incur usage and runs reviewed code with that credential. Fixture
isolation is not sandboxing. The executor in your eval is preserved, never
silently replaced with mock; the agent-evidence gate rejects mock or unknown
result executors and requires copilot-sdk.

The live job requires a fresh results.json, disables cached results, validates
the shared result contract, and uploads diagnostics even for completed failing
grading. Missing credentials/model access, task/runtime errors, missing or
malformed artifacts, and failed verdicts cannot pass as successful evaluations.
JUnit publication is not configured here. Existing workflows are never
overwritten by re-running init. Update the release pin deliberately only after
verifying published standalone assets and the shared installer/result scripts;
offline inventory is neither release approval nor external-consumer evidence.
`, projectName)
}

// generateWazaConfig produces the .waza.yaml content by marshaling a ProjectConfig struct.
// projectconfig.New() populates all defaults, so most fields will appear in the output;
// omitempty only omits truly zero/nil values (e.g. empty judgeModel). No hand-coded comments.
func generateWazaConfig(engine, model, skillsPath, evalsPath, resultsPath string) string {
	cfg := projectconfig.New()
	cfg.Paths.Skills = skillsPath
	cfg.Paths.Evals = evalsPath
	cfg.Paths.Results = resultsPath
	cfg.Defaults.Engine = engine
	cfg.Defaults.Model = model

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Sprintf("defaults:\n  engine: %s\n  model: %s\n", engine, model)
	}
	_ = enc.Close()

	var sb strings.Builder
	sb.WriteString("# yaml-language-server: $schema=https://raw.githubusercontent.com/microsoft/waza/main/schemas/config.schema.json\n\n")
	sb.Write(buf.Bytes())

	return sb.String()
}
