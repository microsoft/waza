package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/spboyer/waza/internal/scaffold"
)

func newInitCommand() *cobra.Command {
	var noSkill bool

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

If no directory is specified, the current directory is used.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return initCommandE(cmd, args, noSkill)
		},
	}

	cmd.Flags().BoolVar(&noSkill, "no-skill", false, "Skip the first-skill creation prompt")

	return cmd
}

func initCommandE(cmd *cobra.Command, args []string, noSkill bool) error {
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

	// Styled indicators
	greenCheck := lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("✓")
	yellowPlus := lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render("+")

	fmt.Fprintf(out, "🔧 Initializing waza project: %s\n", projectName) //nolint:errcheck

	// --- Phase 1: Determine what needs to be created ---
	wazaConfigPath := filepath.Join(dir, ".waza.yaml")
	_, wazaStatErr := os.Stat(wazaConfigPath)
	needConfigPrompt := wazaStatErr != nil
	needSkillPrompt := !noSkill

	// --- Phase 2: Prompts (before showing checklist) ---
	var engine, model, skillName string
	var createSkill bool
	engine = "copilot-sdk"
	model = "claude-sonnet-4.6"

	if isTTY {
		var groups []*huh.Group

		if needConfigPrompt {
			fmt.Fprintf(out, "\nConfigure project defaults:\n\n") //nolint:errcheck

			groups = append(groups, huh.NewGroup(
				huh.NewSelect[string]().
					Title("Default evaluation engine").
					Description("Choose how evals are executed").
					Options(
						huh.NewOption("Copilot SDK — real model execution", "copilot-sdk"),
						huh.NewOption("Mock — fast iteration, no API calls", "mock"),
					).
					Value(&engine),
			))

			// Model selection — only shown when engine is copilot-sdk
			// Note: Copilot SDK (v0.1.22) has no model enumeration API.
			groups = append(groups, huh.NewGroup(
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
			).WithHideFunc(func() bool {
				return engine != "copilot-sdk"
			}))
		}

		if needSkillPrompt {
			createSkill = true // default to Yes
			groups = append(groups, huh.NewGroup(
				huh.NewConfirm().
					Title("Create a new skill?").
					Affirmative("Yes").
					Negative("No").
					Value(&createSkill),
			))

			groups = append(groups, huh.NewGroup(
				huh.NewInput().
					Title("Skill name").
					Description("A kebab-case name for your skill (e.g. azure-deploy)").
					Placeholder("my-skill").
					Value(&skillName).
					Validate(func(s string) error {
						s = strings.TrimSpace(s)
						if s == "" {
							return fmt.Errorf("skill name is required")
						}
						if strings.ContainsAny(s, `/\ `) {
							return fmt.Errorf("skill name cannot contain spaces or path separators")
						}
						return nil
					}),
			).WithHideFunc(func() bool {
				return !createSkill
			}))
		}

		if len(groups) > 0 {
			form := huh.NewForm(groups...).
				WithInput(cmd.InOrStdin()).
				WithOutput(out)

			if err := form.Run(); err != nil {
				engine = "copilot-sdk"
				model = "claude-sonnet-4.6"
				createSkill = false
			}
		}
	} else {
		// Non-TTY: use defaults, no prompts
		fmt.Fprintf(out, "\nUsing defaults: engine=%s, model=%s\n", engine, model) //nolint:errcheck
	}

	// --- Phase 3: Create/verify project structure ---
	type initItem struct {
		path    string
		label   string
		isDir   bool
		content string
	}

	wazaConfigContent := ""
	if needConfigPrompt {
		wazaConfigContent = fmt.Sprintf(`# yaml-language-server: $schema=https://raw.githubusercontent.com/spboyer/waza/main/schemas/waza-config.schema.json
# Waza project configuration
# These defaults are used by 'waza new' when generating eval.yaml files
# and by 'waza run' as fallback values when not specified in eval.yaml.
defaults:
  engine: %s
  model: %s
`, engine, model)
	}

	configLabel := "Project defaults"
	if engine != "" {
		configLabel = fmt.Sprintf("Project defaults (%s, %s)", engine, model)
	}

	items := []initItem{
		{filepath.Join(dir, "skills"), "Skill definitions", true, ""},
		{filepath.Join(dir, "evals"), "Evaluation suites", true, ""},
		{filepath.Join(dir, ".waza.yaml"), configLabel, false, wazaConfigContent},
		{filepath.Join(dir, ".github", "workflows", "eval.yml"), "CI pipeline", false, initCIWorkflow()},
		{filepath.Join(dir, ".gitignore"), "Build artifacts excluded", false, initGitignore()},
		{filepath.Join(dir, "README.md"), "Getting started guide", false, initReadme(projectName)},
	}

	fmt.Fprintf(out, "\nProject structure:\n\n") //nolint:errcheck

	created := 0
	for _, item := range items {
		var status string
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
		if rel, err := filepath.Rel(dir, item.path); err == nil {
			relPath = rel
		}
		if item.isDir {
			relPath += string(filepath.Separator)
		}

		if existed {
			status = greenCheck
		} else {
			status = yellowPlus
			created++
		}

		fmt.Fprintf(out, "  %s %-35s %s\n", status, relPath, item.label) //nolint:errcheck
	}

	// --- Phase 4: Summary ---
	fmt.Fprintln(out) //nolint:errcheck
	if created == 0 {
		fmt.Fprintf(out, "%s Project up to date.\n", greenCheck) //nolint:errcheck
	} else if created == len(items) {
		fmt.Fprintf(out, "✅ Project created — %d items set up.\n", created) //nolint:errcheck
	} else {
		fmt.Fprintf(out, "✅ Repaired — %d item(s) added.\n", created) //nolint:errcheck
	}

	// --- Phase 5: Create skill if requested ---
	skillName = strings.TrimSpace(skillName)
	if createSkill && skillName != "" {
		if err := scaffold.ValidateName(skillName); err != nil {
			return err
		}
		origDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get working directory: %w", err)
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("failed to resolve directory: %w", err)
		}
		if err := os.Chdir(absDir); err != nil {
			return fmt.Errorf("failed to change to project directory: %w", err)
		}
		defer os.Chdir(origDir) //nolint:errcheck

		fmt.Fprintln(out) //nolint:errcheck
		skillContent := defaultSkillMD(skillName)
		if err := scaffoldInProject(cmd, absDir, skillName, skillContent); err != nil {
			return err
		}
	}

	// --- Phase 6: Next steps (first run only, TTY only) ---
	if created > 0 && isTTY {
		printNextSteps(out, skillName)
	}

	return nil
}

func printNextSteps(out io.Writer, skillName string) {
	fmt.Fprintln(out)                //nolint:errcheck
	fmt.Fprintln(out, "Next steps:") //nolint:errcheck
	fmt.Fprintln(out)                //nolint:errcheck
	skill := "my-skill"
	if skillName != "" {
		skill = skillName
	}
	fmt.Fprintf(out, "  waza dev %s       Improve skill compliance\n", skill) //nolint:errcheck
	fmt.Fprintf(out, "  waza run %s       Run evaluations\n", skill)          //nolint:errcheck
	fmt.Fprintf(out, "  waza check %s     Check skill readiness\n", skill)    //nolint:errcheck
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
        run: azd waza run --output results.json
      - name: Upload results
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: eval-results
          path: results.json
          retention-days: 30
`
}

func initGitignore() string {
	return `results.json
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
