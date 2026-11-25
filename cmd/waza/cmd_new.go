package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/microsoft/waza/internal/generate"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/wizard"
)

func newNewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Create new skills and tasks",
	}

	cmd.AddCommand(newNewSkillCommand())
	cmd.AddCommand(newNewTaskCommand())
	cmd.AddCommand(newEvalNewCommand())
	return cmd
}

func newNewSkillCommand() *cobra.Command {
	var template string
	var outputDir string

	cmd := &cobra.Command{
		Use:   "skill [skill-name]",
		Short: "Create a new skill with its eval suite",
		Long: `Create a new skill and its evaluation suite with a compliant directory structure.

Idempotent: detects what already exists and fills in only the missing pieces.
If SKILL.md already exists, it is parsed for the skill name instead of being
regenerated.

Two modes of operation:

  Inside a project (skills/ directory detected):
    Creates skills/{name}/SKILL.md and evals/{name}/ with an eval file,
    task files, and fixtures.

  Standalone (no skills/ directory):
    Creates {name}/ with SKILL.md, evals/, .github/workflows/eval.yml,
    .gitignore, and README.md.

When running in a terminal (TTY), launches an interactive wizard for skill
metadata collection unless a SKILL.md already exists for the given name.
In non-interactive environments (CI, pipes), the skill name must be provided
as an argument; remaining fields use defaults.

Use --output-dir to scaffold into a specific directory instead of the
current working directory.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return newCommandE(cmd, args, template, outputDir)
		},
	}

	cmd.Flags().StringVarP(&template, "template", "t", "", "Template pack to use (coming soon)")
	cmd.Flags().StringVarP(&outputDir, "output-dir", "d", "", "Directory to scaffold into (default: current directory)")

	return cmd
}

func newCommandE(cmd *cobra.Command, args []string, templatePack, outputDir string) error {
	initialName := ""
	if len(args) > 0 {
		initialName = args[0]
		if err := scaffold.ValidateName(initialName); err != nil {
			return err
		}
	}

	if templatePack != "" {
		fmt.Fprintln(cmd.OutOrStdout(), "Note: template packs coming soon. Using default template.") //nolint:errcheck
	}

	// If --output-dir is specified, chdir so scaffolding writes there
	if outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return fmt.Errorf("creating output directory: %w", err)
		}
		origDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		if err := os.Chdir(outputDir); err != nil {
			return fmt.Errorf("changing to output directory: %w", err)
		}
		defer os.Chdir(origDir) //nolint:errcheck
	}

	// Determine mode based on skills/ directory presence
	projectRoot, inProject := findSkillProjectRoot(outputDir != "" || cmd.Flags().Changed("output-dir"))

	var skillName string
	var skillMDContent string
	var existingSkill bool
	var overwriteSkillMD bool

	// Check TTY
	inReader := cmd.InOrStdin()
	isTTY := false
	if f, ok := inReader.(*os.File); ok {
		isTTY = term.IsTerminal(int(f.Fd()))
	}

	if isTTY {
		// If skill name was provided, check for existing SKILL.md
		if initialName != "" {
			existingContent, status := detectExistingSkillMD(projectRoot, inProject, initialName)
			switch status {
			case skillMDValid:
				// Valid SKILL.md — skip wizard, go straight to inventory
				skillName = initialName
				skillMDContent = existingContent
				existingSkill = true
			case skillMDMalformed:
				// File exists but is empty/malformed — warn, then run wizard to fill it
				fmt.Fprintf(cmd.OutOrStdout(), "⚠️  SKILL.md for '%s' exists but is empty or malformed — launching wizard to populate it.\n\n", initialName) //nolint:errcheck
				overwriteSkillMD = true
				// Fall through to wizard below
			case skillMDNotFound:
				// New skill — fall through to wizard below
			}
		}
		// Run wizard if we didn't find a valid existing SKILL.md
		if !existingSkill {
			spec, err := wizard.RunSkillWizard(cmd.InOrStdin(), cmd.OutOrStdout(), initialName)
			if err != nil {
				return fmt.Errorf("wizard failed: %w", err)
			}
			skillName = spec.Name
			if err := scaffold.ValidateName(skillName); err != nil {
				return fmt.Errorf("invalid skill name: %w", err)
			}
			// After wizard collects name, check for existing SKILL.md
			_, postStatus := detectExistingSkillMD(projectRoot, inProject, skillName)
			switch postStatus {
			case skillMDValid:
				fmt.Fprintf(cmd.OutOrStdout(), "Skill '%s' already exists — checking inventory...\n", skillName) //nolint:errcheck
				existingSkill = true
			case skillMDMalformed:
				overwriteSkillMD = true
			}
			// Generate SKILL.md content from wizard (used for new or malformed)
			content, err := wizard.GenerateSkillMD(spec)
			if err != nil {
				return fmt.Errorf("failed to generate SKILL.md: %w", err)
			}
			skillMDContent = content
		}
	} else {
		if initialName == "" {
			return fmt.Errorf("skill name is required in non-interactive mode (provide as argument)")
		}
		skillName = initialName
		// Check for existing SKILL.md
		existingContent, status := detectExistingSkillMD(projectRoot, inProject, skillName)
		switch status {
		case skillMDValid:
			skillMDContent = existingContent
			existingSkill = true
		case skillMDMalformed:
			fmt.Fprintf(cmd.OutOrStdout(), "⚠️  SKILL.md for '%s' exists but is empty or malformed — using defaults.\n", skillName) //nolint:errcheck
			skillMDContent = defaultSkillMD(skillName)
			overwriteSkillMD = true
		case skillMDNotFound:
			skillMDContent = defaultSkillMD(skillName)
		}
	}

	if inProject {
		return scaffoldInProject(cmd, projectRoot, skillName, skillMDContent, existingSkill, overwriteSkillMD)
	}
	return scaffoldStandalone(cmd, skillName, skillMDContent, existingSkill, overwriteSkillMD)
}

// skillMDStatus describes the state of an existing SKILL.md file.
type skillMDStatus int

const (
	skillMDNotFound  skillMDStatus = iota // File does not exist
	skillMDMalformed                      // File exists but is empty or has invalid frontmatter
	skillMDValid                          // File exists and has valid frontmatter
)

// detectExistingSkillMD checks whether a SKILL.md already exists for the given
// skill name and returns its content and status.
func detectExistingSkillMD(projectRoot string, inProject bool, skillName string) (string, skillMDStatus) {
	var skillMDPath string
	if inProject {
		skillMDPath = filepath.Join(projectRoot, "skills", skillName, "SKILL.md")
	} else {
		skillMDPath = filepath.Join(skillName, "SKILL.md")
	}

	if _, err := os.Stat(skillMDPath); err != nil {
		if os.IsNotExist(err) {
			return "", skillMDNotFound
		}
		return "", skillMDMalformed
	}

	data, readErr := os.ReadFile(skillMDPath)
	if readErr != nil {
		return "", skillMDMalformed
	}

	content := string(data)
	if _, parseErr := generate.ParseSkillMD(skillMDPath); parseErr != nil {
		return content, skillMDMalformed
	}

	return content, skillMDValid
}

// findProjectRoot walks up from CWD looking for a skills/ directory.
// Returns the directory containing skills/ and true, or ("", false) if not found.
func findProjectRoot() (string, bool) {
	return findSkillProjectRoot(false)
}

// Explicit skill output may use a project at the destination, but not its ancestors.
func findSkillProjectRoot(explicitOutput bool) (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}

	for {
		candidate := filepath.Join(dir, "skills")
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return dir, true
		}
		if explicitOutput {
			break
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", false
}

// scaffoldInProject creates files within an existing project structure.
func scaffoldInProject(cmd *cobra.Command, projectRoot, skillName, skillMD string, existing, overwriteSkill bool) error {
	engine, model := scaffold.ReadProjectDefaults()
	files := scaffold.ReadProjectFiles()
	skillDir := filepath.Join(projectRoot, "skills", skillName)
	evalDir := filepath.Join(projectRoot, "evals", skillName)
	tasksDir := filepath.Join(evalDir, "tasks")
	fixturesDir := filepath.Join(evalDir, "fixtures")

	// Create directories
	for _, d := range []string{skillDir, tasksDir, fixturesDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d, err)
		}
	}

	// If malformed SKILL.md needs replacement, remove it so FileWriter creates fresh
	if overwriteSkill {
		if err := os.Remove(filepath.Join(skillDir, "SKILL.md")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove malformed SKILL.md: %w", err)
		}
	}

	entries := []scaffold.FileEntry{
		{Path: filepath.Join(skillDir, "SKILL.md"), Label: "Skill definition", Content: skillMD},
		{Path: filepath.Join(evalDir, files.EvalFile), Label: "Eval configuration", Content: scaffold.EvalYAMLWithTaskGlob(skillName, engine, model, files.TaskGlob)},
	}

	// Only add default tasks if the tasks directory is empty
	if !dirHasFiles(tasksDir) {
		tasks := scaffold.TaskFilesWithSuffix(files.TaskFileSuffix)
		for name, content := range tasks {
			entries = append(entries, scaffold.FileEntry{Path: filepath.Join(tasksDir, name), Label: taskLabel(name), Content: content})
		}
	}

	// Only add default fixture if the fixtures directory is empty
	if !dirHasFiles(fixturesDir) {
		entries = append(entries, scaffold.FileEntry{Path: filepath.Join(fixturesDir, "sample.py"), Label: "Fixture", Content: scaffold.Fixture()})
	}

	return writeScaffold(cmd, entries, skillName, existing, tasksDir, fixturesDir)
}

// scaffoldStandalone creates a self-contained skill directory.
func scaffoldStandalone(cmd *cobra.Command, skillName, skillMD string, existing, overwriteSkill bool) error {
	engine, model := scaffold.ReadProjectDefaults()
	files := scaffold.ReadProjectFiles()
	rootDir := skillName
	evalsDir := filepath.Join(rootDir, "evals")
	tasksDir := filepath.Join(evalsDir, "tasks")
	fixturesDir := filepath.Join(evalsDir, "fixtures")
	workflowDir := filepath.Join(rootDir, ".github", "workflows")

	workflowPath := filepath.Join(workflowDir, "eval.yml")
	_, workflowStatErr := os.Lstat(workflowPath)
	if workflowStatErr != nil && !errors.Is(workflowStatErr, fs.ErrNotExist) {
		return fmt.Errorf("checking generated workflow %s: %w", workflowPath, workflowStatErr)
	}
	if workflowStatErr == nil {
		// FileWriter follows links and rejects a directory, so fail before repair.
		// A dangling link would otherwise be skipped as "existing" here and as
		// "missing with no content" by FileWriter, leaving no usable workflow.
		info, err := os.Stat(workflowPath)
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("generated workflow %s is a dangling symlink; remove it or restore its target", workflowPath)
		}
		if err != nil {
			return fmt.Errorf("checking generated workflow %s: %w", workflowPath, err)
		}
		if info.IsDir() {
			return fmt.Errorf("path %s exists but is a directory", workflowPath)
		}
	}
	// FileWriter preserves an existing workflow, so only validate one that
	// will be written, and do so before any repair mutates the skill.
	var workflow string
	if errors.Is(workflowStatErr, fs.ErrNotExist) {
		var err error
		workflow, err = ciWorkflow("", "evals", files.EvalFile, filepath.Join("evals", files.EvalFile))
		if err != nil {
			return err
		}
	}

	// Create directories
	for _, d := range []string{tasksDir, fixturesDir, workflowDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d, err)
		}
	}

	// If malformed SKILL.md needs replacement, remove it so FileWriter creates fresh
	if overwriteSkill {
		if err := os.Remove(filepath.Join(rootDir, "SKILL.md")); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove malformed SKILL.md: %w", err)
		}
	}

	entries := []scaffold.FileEntry{
		{Path: filepath.Join(rootDir, "SKILL.md"), Label: "Skill definition", Content: skillMD},
		{Path: filepath.Join(evalsDir, files.EvalFile), Label: "Eval configuration", Content: scaffold.EvalYAMLWithTaskGlob(skillName, engine, model, files.TaskGlob)},
	}

	// Only add default tasks if the tasks directory is empty
	if !dirHasFiles(tasksDir) {
		tasks := scaffold.TaskFilesWithSuffix(files.TaskFileSuffix)
		for name, content := range tasks {
			entries = append(entries, scaffold.FileEntry{Path: filepath.Join(tasksDir, name), Label: taskLabel(name), Content: content})
		}
	}

	// Only add default fixture if the fixtures directory is empty
	if !dirHasFiles(fixturesDir) {
		entries = append(entries, scaffold.FileEntry{Path: filepath.Join(fixturesDir, "sample.py"), Label: "Fixture", Content: scaffold.Fixture()})
	}

	entries = append(entries,
		scaffold.FileEntry{Path: workflowPath, Label: "CI pipeline", Content: workflow},
		scaffold.FileEntry{Path: filepath.Join(rootDir, ".gitignore"), Label: "Build artifacts excluded", Content: defaultGitignore()},
		scaffold.FileEntry{Path: filepath.Join(rootDir, "README.md"), Label: "Getting started guide", Content: defaultReadme(skillName)},
	)

	return writeScaffold(cmd, entries, skillName, existing, tasksDir, fixturesDir)
}

// writeScaffold uses the shared FileWriter to create missing files, then prints
// the inventory with header and summary footer.
func writeScaffold(cmd *cobra.Command, entries []scaffold.FileEntry, skillName string, existing bool, tasksDir, fixturesDir string) error {
	out := cmd.OutOrStdout()
	baseDir, _ := os.Getwd() //nolint:errcheck // best-effort for display paths

	fw := scaffold.NewFileWriter(baseDir)
	inv, err := fw.Write(entries)
	if err != nil {
		return err
	}

	if existing {
		fmt.Fprintf(out, "🔧 Checking skill: %s\n", skillName) //nolint:errcheck
	} else {
		fmt.Fprintf(out, "🔧 Scaffolding skill: %s\n", skillName) //nolint:errcheck
	}
	fmt.Fprintf(out, "\nSkill structure:\n\n") //nolint:errcheck

	inv.Fprint(out)

	// Show summary lines for user-owned tasks/fixtures directories
	printDirSummary(out, inv, tasksDir, baseDir, "Tasks")
	printDirSummary(out, inv, fixturesDir, baseDir, "Fixtures")

	created := inv.CreatedCount()

	fmt.Fprintln(out) //nolint:errcheck
	if created == 0 {
		fmt.Fprintf(out, "✅ Project up to date.\n") //nolint:errcheck
	} else if created == len(entries) {
		fmt.Fprintf(out, "✅ Skill created — %d file(s) scaffolded.\n", created) //nolint:errcheck
	} else {
		fmt.Fprintf(out, "✅ Repaired — %d item(s) added.\n", created) //nolint:errcheck
	}

	return nil
}

// printDirSummary prints a summary line for a directory that has existing files
// but wasn't part of the scaffolded entries.
func printDirSummary(out io.Writer, inv *scaffold.Inventory, dir, baseDir, label string) {
	count := dirFileCount(dir)
	if count == 0 {
		return
	}

	for _, item := range inv.Items {
		if filepath.Dir(item.Entry.Path) == dir {
			return
		}
	}

	relDir := dir
	if abs, err := filepath.Abs(dir); err == nil {
		if rel, err := filepath.Rel(baseDir, abs); err == nil {
			relDir = rel
		}
	}
	fmt.Fprintf(out, "  ✅  %s  %s (%d files)\n", relDir+string(filepath.Separator), label, count) //nolint:errcheck
}

// taskLabel returns a descriptive label for a task file.
func taskLabel(filename string) string {
	switch {
	case strings.HasPrefix(filename, "basic-usage"):
		return "Task: basic usage"
	case strings.HasPrefix(filename, "edge-case"):
		return "Task: edge case"
	case strings.HasPrefix(filename, "should-not-trigger"):
		return "Task: negative test"
	default:
		return "Task file"
	}
}

// dirHasFiles returns true if the directory exists and contains at least one file.
func dirHasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			return true
		}
	}
	return false
}

// dirFileCount returns the number of files in a directory (0 if it doesn't exist).
func dirFileCount(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			count++
		}
	}
	return count
}

// --- Template content functions (standalone-only templates remain here) ---

func defaultSkillMD(name string) string {
	return fmt.Sprintf(`---
name: %s
description: |
  USE FOR: %s tasks, ...
  DO NOT USE FOR: unrelated tasks, ...
---

# %s

## Overview

Describe what this skill does and when an agent should use it.

## Usage

Provide examples of prompts that should trigger this skill.

## References

List any reference documents or APIs this skill depends on.
`, name, name, scaffold.TitleCase(name))
}

func defaultGitignore() string {
	return `results.json
.waza-cache/
coverage.txt
`
}

func defaultReadme(name string) string {
	return fmt.Sprintf(`# %s

A skill for agent evaluation with [waza](https://github.com/microsoft/waza).

## Quick Start

1. Edit `+"`SKILL.md`"+` with your skill's description and triggers.
2. Customize the task files in `+"`evals/tasks/`"+`.
3. Add real fixtures to `+"`evals/fixtures/`"+`.
4. Run the evaluation:

`+"```bash"+`
waza run evals/eval.yaml --context-dir evals/fixtures -v
`+"```"+`

## CI

Pull requests run offline configuration inventory, not agent evaluation.
CI installs checksum-verified standalone Waza 0.42.1 and prints its version.
Invalid configuration fails; unresolved runtime prerequisites warn. An empty
inventory is not a successful evaluation.

For intentional agent execution, review the eval, model, tools, graders, and
dependencies. Before adding WAZA_COPILOT_TOKEN with model access to the waza-agent
environment, restrict its deployment branches to the default branch and require
reviewers; the workflow's branch check does not stop a writer from dispatching a
modified workflow from another branch. Manually dispatch on the repository's default
branch with run-agent enabled and the reviewed eval-path. Pull requests cannot
run the agent job. Execution can incur usage; fixture isolation is not sandboxing.
The agent-evidence gate requires a fresh, passing copilot-sdk result and rejects
mock or unknown executors. Existing workflows are never overwritten.

## Structure

`+"```"+`
%s/
├── SKILL.md                  # Skill definition
├── evals/
│   ├── eval.yaml             # Eval configuration
│   ├── tasks/
│   │   ├── basic-usage.yaml
│   │   ├── edge-case.yaml
│   │   └── should-not-trigger.yaml
│   └── fixtures/
│       └── sample.py
├── .github/workflows/
│   └── eval.yml              # CI workflow
├── .gitignore
└── README.md
`+"```"+`

## Learn More

- [Waza Documentation](https://github.com/microsoft/waza)
`, scaffold.TitleCase(name), name)
}
