package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/microsoft/waza/internal/generate"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/workspace"
	"github.com/spf13/cobra"
)

func newEvalCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Work with evaluation suites",
		Long:  "Create and manage evaluation scaffolding and related artifacts.",
	}

	cmd.AddCommand(newEvalNewCommand())
	return cmd
}

func newEvalNewCommand() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "new <skill-name>",
		Short: "Scaffold a new eval suite for a skill",
		Long: `Generate an eval scaffold using a skill's SKILL.md frontmatter.

Creates:
  - evals/<skill-name>/eval.yaml
  - evals/<skill-name>/tasks/positive-trigger-1.yaml
  - evals/<skill-name>/tasks/positive-trigger-2.yaml
  - evals/<skill-name>/tasks/negative-trigger-1.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return evalNewCommandE(cmd, args[0], output)
		},
	}

	cmd.Flags().StringVar(&output, "output", "", "Path for eval.yaml (default: evals/<skill-name>/eval.yaml)")
	return cmd
}

func evalNewCommandE(cmd *cobra.Command, skillName, outputPath string) error {
	if err := scaffold.ValidateName(skillName); err != nil {
		return err
	}

	skillMDPath, err := resolveSkillMDPath(skillName)
	if err != nil {
		return err
	}

	frontmatter, err := generate.ParseSkillMD(skillMDPath)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", skillMDPath, err)
	}

	useFor, doNotUseFor := scaffold.ParseTriggerPhrases(frontmatter.Description)
	keywords := extractKeywords(useFor, skillName)
	positivePrompts := positivePromptsFor(useFor, skillName)
	negativePrompt := negativePromptFor(doNotUseFor)

	if outputPath == "" {
		outputPath = filepath.Join("evals", skillName, "eval.yaml")
	}
	tasksDir := filepath.Join(filepath.Dir(outputPath), "tasks")

	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		return fmt.Errorf("creating tasks directory: %w", err)
	}

	engine, model := scaffold.ReadProjectDefaults()
	evalContent := evalScaffoldYAML(skillName, engine, model)
	files := []struct {
		path    string
		content string
	}{
		{path: outputPath, content: evalContent},
		{
			path:    filepath.Join(tasksDir, "positive-trigger-1.yaml"),
			content: triggerTaskYAML("positive-trigger-001", "Positive Trigger 1", positivePrompts[0], true, keywords),
		},
		{
			path:    filepath.Join(tasksDir, "positive-trigger-2.yaml"),
			content: triggerTaskYAML("positive-trigger-002", "Positive Trigger 2", positivePrompts[1], true, keywords),
		},
		{
			path:    filepath.Join(tasksDir, "negative-trigger-1.yaml"),
			content: triggerTaskYAML("negative-trigger-001", "Negative Trigger 1", negativePrompt, false, keywords),
		},
	}

	for _, file := range files {
		if _, statErr := os.Stat(file.path); statErr == nil {
			return fmt.Errorf("refusing to overwrite existing file: %s", file.path)
		}
	}

	createdFiles := make([]string, 0, len(files))
	for _, file := range files {
		if err := os.WriteFile(file.path, []byte(file.content), 0o644); err != nil {
			for _, createdPath := range createdFiles {
				_ = os.Remove(createdPath)
			}
			return fmt.Errorf("writing %s: %w", file.path, err)
		}
		createdFiles = append(createdFiles, file.path)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "✅ Eval scaffold created for %s\n", skillName)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", outputPath)
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", filepath.Join(tasksDir, "positive-trigger-1.yaml"))
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", filepath.Join(tasksDir, "positive-trigger-2.yaml"))
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", filepath.Join(tasksDir, "negative-trigger-1.yaml"))

	return nil
}

func resolveSkillMDPath(skillName string) (string, error) {
	wd, err := os.Getwd()
	if err == nil {
		ctx, detectErr := workspace.DetectContext(wd, configDetectOptions()...)
		if detectErr == nil && ctx.Type != workspace.ContextNone {
			si, findErr := workspace.FindSkill(ctx, skillName)
			if findErr != nil {
				return "", fmt.Errorf("finding skill %q in workspace: %w", skillName, findErr)
			}
			return si.SkillPath, nil
		}
	}

	candidates := []string{
		filepath.Join("skills", skillName, "SKILL.md"),
		filepath.Join(skillName, "SKILL.md"),
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("SKILL.md not found for %q (looked in skills/%s/SKILL.md and %s/SKILL.md)", skillName, skillName, skillName)
}

func evalScaffoldYAML(skillName, engine, model string) string {
	return fmt.Sprintf(`name: %s-eval
description: Auto-generated eval for %s.
skill: %s
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 300
  parallel: false
  executor: %s
  model: %s
metrics:
  - name: task_completion
    weight: 0.7
    threshold: 0.8
    description: Did the skill complete trigger and anti-trigger checks?
  - name: efficiency
    weight: 0.3
    threshold: 0.7
    description: Did the skill stay within behavior limits?
graders:
  - type: behavior
    name: token-budget
    config:
      max_tokens: 1200
tasks:
  - "tasks/*.yaml"
`, skillName, skillName, skillName, engine, model)
}

func triggerTaskYAML(id, name, prompt string, shouldTrigger bool, keywords []string) string {
	modeKey := "contains"
	modeLabel := "contains-keywords"
	taskLabel := "positive-trigger"
	if !shouldTrigger {
		modeKey = "not_contains"
		modeLabel = "omits-skill-keywords"
		taskLabel = "negative-trigger"
	}

	return fmt.Sprintf(`id: %s
name: %s
description: Auto-generated %s task.
tags:
  - %s
inputs:
  prompt: %s
expected:
  should_trigger: %t
graders:
  - type: text
    name: %s
    config:
      %s:
%s
`, id, name, taskLabel, taskLabel, strconv.Quote(prompt), shouldTrigger, modeLabel, modeKey, quotedYAMLList(keywords, 8))
}

func quotedYAMLList(values []string, indent int) string {
	var b strings.Builder
	prefix := strings.Repeat(" ", indent)
	for _, v := range values {
		_, _ = fmt.Fprintf(&b, "%s- %s\n", prefix, strconv.Quote(v))
	}
	return b.String()
}

func positivePromptsFor(useFor []scaffold.TriggerPhrase, skillName string) []string {
	if len(useFor) >= 2 {
		return []string{useFor[0].Prompt, useFor[1].Prompt}
	}
	if len(useFor) == 1 {
		return []string{
			useFor[0].Prompt,
			fmt.Sprintf("Please help with a %s workflow", strings.ReplaceAll(skillName, "-", " ")),
		}
	}
	return []string{
		fmt.Sprintf("Use %s to help me complete this task", skillName),
		fmt.Sprintf("I need assistance with %s-related work", strings.ReplaceAll(skillName, "-", " ")),
	}
}

func negativePromptFor(doNotUseFor []scaffold.TriggerPhrase) string {
	if len(doNotUseFor) > 0 {
		return doNotUseFor[0].Prompt
	}
	return "Tell me a short joke about coffee."
}

func extractKeywords(useFor []scaffold.TriggerPhrase, skillName string) []string {
	stopWords := map[string]bool{
		"the": true, "and": true, "with": true, "from": true, "this": true,
		"that": true, "help": true, "need": true, "please": true, "about": true,
		"your": true, "into": true, "have": true, "will": true, "task": true,
	}

	wordRE := regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]{2,}`)
	seen := map[string]bool{}
	var keywords []string

	addWord := func(w string) {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" || stopWords[w] || seen[w] {
			return
		}
		seen[w] = true
		keywords = append(keywords, w)
	}

	for _, phrase := range useFor {
		for _, match := range wordRE.FindAllString(phrase.Prompt, -1) {
			addWord(match)
		}
	}
	for _, part := range strings.Split(skillName, "-") {
		addWord(part)
	}

	if len(keywords) == 0 {
		return []string{skillName}
	}

	if len(keywords) > 4 {
		keywords = keywords[:4]
	}
	slices.Sort(keywords)
	return keywords
}
