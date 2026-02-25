package main

import (
	"bytes"
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

	"github.com/spboyer/waza/internal/projectconfig"
	"github.com/spboyer/waza/internal/scaffold"
	"github.com/spboyer/waza/internal/workspace"
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

After scaffolding, prompts to create a new skill (calls waza new internally).

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
	var createSkill, scaffoldMissing bool
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
			workspace.WithEvalsDir(evalsPath))

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
				scaffoldMissingEvals(absDir, evalsPath, inventory, engine, model)
				// Re-display inventory showing updated state
				inventory = displayInventory(out, absDir,
					workspace.WithSkillsDir(skillsPath),
					workspace.WithEvalsDir(evalsPath))
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
			workspace.WithEvalsDir(evalsPath))

		for _, inv := range inventory {
			if !inv.HasEval {
				skillsMissingEvals++
			}
		}
		scaffoldMissing = skillsMissingEvals > 0

		if scaffoldMissing {
			scaffoldMissingEvals(absDir, evalsPath, inventory, engine, model)
			// Re-display inventory after scaffolding
			inventory = displayInventory(out, absDir,
				workspace.WithSkillsDir(skillsPath),
				workspace.WithEvalsDir(evalsPath))
			skillsMissingEvals = 0
			for _, inv := range inventory {
				if !inv.HasEval {
					skillsMissingEvals++
				}
			}
		}
	}

	// --- Phase 5: Create/verify project structure ---
	type initItem struct {
		path    string
		label   string
		isDir   bool
		content string
	}

	wazaConfigContent := ""
	if needConfigPrompt {
		wazaConfigContent = generateWazaConfig(engine, model, skillsPath, evalsPath, resultsPath)
	}

	configLabel := fmt.Sprintf("Project defaults (%s, %s)", engine, model)

	items := []initItem{
		{filepath.Join(absDir, skillsPath), "Skill definitions", true, ""},
		{filepath.Join(absDir, evalsPath), "Evaluation suites", true, ""},
		{filepath.Join(absDir, ".waza.yaml"), configLabel, false, wazaConfigContent},
		{filepath.Join(absDir, ".github", "workflows", "eval.yml"), "CI pipeline", false, initCIWorkflow()},
		{filepath.Join(absDir, ".gitignore"), "Build artifacts excluded", false, initGitignore()},
		{filepath.Join(absDir, "README.md"), "Getting started guide", false, initReadme(projectName)},
	}

	fmt.Fprintf(out, "\nProject structure:\n\n") //nolint:errcheck

	var buf2 bytes.Buffer
	tw2 := tabwriter.NewWriter(&buf2, 0, 0, 2, ' ', 0)
	created := 0
	for _, item := range items {
		var indicator string
		var existed bool

		if item.isDir {
			if info, err := os.Stat(item.path); err == nil && info.IsDir() {
				existed = true
			} else {
				if err := os.MkdirAll(item.path, 0o755); err != nil {
					return fmt.Errorf("failed to create %s: %w", item.path, err)
				}
			}
		} else {
			if _, err := os.Stat(item.path); err == nil {
				existed = true
			} else if item.content != "" {
				if err := os.MkdirAll(filepath.Dir(item.path), 0o755); err != nil {
					return fmt.Errorf("failed to create directory for %s: %w", item.path, err)
				}
				if err := os.WriteFile(item.path, []byte(item.content), 0o644); err != nil {
					return fmt.Errorf("failed to write %s: %w", item.path, err)
				}
			}
		}

		// Relative path for display
		relPath := item.path
		if rel, err := filepath.Rel(absDir, item.path); err == nil {
			relPath = rel
		}
		if item.isDir {
			relPath += string(filepath.Separator)
		}

		if existed {
			indicator = "{exist}"
		} else {
			indicator = "{new}"
			created++
		}

		fmt.Fprintf(tw2, "  %s\t%s\t%s\n", indicator, relPath, item.label) //nolint:errcheck
	}
	tw2.Flush() //nolint:errcheck
	result2 := buf2.String()
	result2 = strings.ReplaceAll(result2, "{exist}", "✅")
	result2 = strings.ReplaceAll(result2, "{new}", "➕")
	fmt.Fprint(out, result2) //nolint:errcheck

	// --- Phase 5b: Summary ---
	fmt.Fprintln(out) //nolint:errcheck
	if created == 0 {
		fmt.Fprintf(out, "✅ Project up to date.\n") //nolint:errcheck
	} else if created == len(items) {
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
			workspace.WithEvalsDir(evalsPath))
	}

	// --- Phase 6: Next steps (first run only, TTY only) ---
	if created > 0 && isTTY {
		printNextSteps(out)
	}

	return nil
}

// scaffoldMissingEvals creates eval suites for skills that lack them.
func scaffoldMissingEvals(absDir, evalsDir string, inventory []skillEntry, engine, model string) {
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
		os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte(scaffold.EvalYAML(inv.Name, engine, model)), 0o644) //nolint:errcheck
		for name, content := range scaffold.TaskFiles(inv.Name) {
			os.WriteFile(filepath.Join(tasksDir, name), []byte(content), 0o644) //nolint:errcheck
		}
		os.WriteFile(filepath.Join(fixturesDir, "sample.py"), []byte(scaffold.Fixture()), 0o644) //nolint:errcheck
	}
}

func printNextSteps(out io.Writer) {
	fmt.Fprintln(out)                                                         //nolint:errcheck
	fmt.Fprintln(out, "Next steps:")                                          //nolint:errcheck
	fmt.Fprintln(out)                                                         //nolint:errcheck
	fmt.Fprintln(out, "  waza new <name>          Create another skill")      //nolint:errcheck
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

func initCIWorkflow() string {
	return `name: Run Skill Evaluations

on:
  pull_request:
    branches: [main]
    paths:
      - 'evals/**'
      - 'skills/**'

permissions:
  contents: read

jobs:
  eval:
    name: Run Evaluations
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install Azure Developer CLI
        uses: Azure/setup-azd@v2
      - name: Install waza extension
        run: |
          azd config set alpha.extensions on
          azd ext source add -n waza -t url -l https://raw.githubusercontent.com/spboyer/waza/main/registry.json
          azd ext install microsoft.azd.waza
      - name: Run evaluations
        run: azd waza run --output-dir ./results
      - name: Upload results
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: eval-results
          path: ./results
          retention-days: 30
`
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
   waza new my-skill
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

5. Push to trigger CI:
   `+"`"+``+"`"+``+"`"+`bash
   git push
   `+"`"+``+"`"+``+"`"+`
`, projectName)
}

// generateWazaConfig produces the expanded .waza.yaml content with all config sections.
// Active values (paths, defaults) are YAML-marshaled for safety; commented-out
// sections showing available options are appended as a string template.
func generateWazaConfig(engine, model, skillsPath, evalsPath, resultsPath string) string {
	cfg := projectconfig.ProjectConfig{
		Paths: projectconfig.PathsConfig{
			Skills:  skillsPath,
			Evals:   evalsPath,
			Results: resultsPath,
		},
		Defaults: projectconfig.DefaultsConfig{
			Engine: engine,
			Model:  model,
		},
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&cfg); err != nil {
		return fmt.Sprintf("defaults:\n  engine: %s\n  model: %s\n", engine, model)
	}
	_ = enc.Close()

	var sb strings.Builder
	sb.WriteString("# yaml-language-server: $schema=https://raw.githubusercontent.com/spboyer/waza/main/schemas/config.schema.json\n\n")
	sb.Write(buf.Bytes())
	sb.WriteString("  # judge_model: \"\"\n")
	sb.WriteString("  # timeout: 300\n")
	sb.WriteString("  # parallel: false\n")
	sb.WriteString("  # workers: 4\n")
	sb.WriteString("  # verbose: false\n")
	sb.WriteString("  # session_log: false\n")
	sb.WriteString("\n# cache:\n#   enabled: false\n#   dir: .waza-cache\n")
	sb.WriteString("\n# server:\n#   port: 3000\n#   results_dir: .\n")
	sb.WriteString("\n# dev:\n#   model: claude-sonnet-4-20250514\n#   target: medium-high\n#   max_iterations: 5\n")
	sb.WriteString("\n# tokens:\n#   warning_threshold: 2500\n#   fallback_limit: 2000\n#   limits:\n")
	sb.WriteString("#     defaults:\n#       \"SKILL.md\": 500\n#       \"references/**/*.md\": 1000\n")
	sb.WriteString("#       \"docs/**/*.md\": 1500\n#       \"*.md\": 2000\n")
	sb.WriteString("#     overrides:\n#       \"README.md\": 3000\n#       \"CONTRIBUTING.md\": 2500\n")
	sb.WriteString("\n# graders:\n#   program_timeout: 30\n")

	return sb.String()
}
