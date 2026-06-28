package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/specverify"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var newSpecVerifyEngine = func(modelID string) execution.AgentEngine {
	return execution.NewCopilotEngineBuilder(modelID, nil).Build()
}

type specVerifyFlags struct {
	skillPath       string
	evalPath        string
	format          string
	warn            bool
	fail            bool
	failThreshold   int
	semantic        bool
	judgeModel      string
	semanticTimeout time.Duration
}

func newSpecCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spec",
		Short: "Verify eval coverage against SKILL.md requirements",
	}
	cmd.AddCommand(newSpecVerifyCommand())
	return cmd
}

func newSpecVerifyCommand() *cobra.Command {
	flags := &specVerifyFlags{
		format:          "human",
		warn:            true,
		failThreshold:   1,
		semanticTimeout: 30 * time.Second,
	}

	cmd := &cobra.Command{
		Use:   "verify [skill-path] [eval.yaml]",
		Short: "Verify eval coverage for SKILL.md requirements",
		Long: `Parse SKILL.md into machine-readable requirements and verify that eval tasks
exercise the description, USE FOR triggers, DO NOT USE FOR triggers, and parameter blocks.

Deterministic matching always runs first. Use --semantic to allow an LLM judge to
fill deterministic coverage gaps with the configured judge model.`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpecVerifyCommand(cmd, args, flags)
		},
		SilenceErrors: true,
	}

	cmd.Flags().StringVar(&flags.skillPath, "skill", "", "Path to SKILL.md or skill directory")
	cmd.Flags().StringVar(&flags.evalPath, "eval", "", "Path to eval.yaml")
	cmd.Flags().StringVar(&flags.format, "format", "human", "Output format: human, json, github-actions")
	cmd.Flags().BoolVar(&flags.warn, "warn", true, "Warn on uncovered requirements and exit 0")
	cmd.Flags().BoolVar(&flags.fail, "fail", false, "Exit 1 when uncovered requirements are greater than or equal to --threshold")
	cmd.Flags().IntVar(&flags.failThreshold, "threshold", 1, "Uncovered requirement count threshold for --fail")
	cmd.Flags().BoolVar(&flags.semantic, "semantic", false, "Use the configured judge model to fill deterministic coverage gaps")
	cmd.Flags().StringVar(&flags.judgeModel, "judge-model", "", "Judge model for --semantic (defaults to eval config judge_model, then model)")
	cmd.Flags().DurationVar(&flags.semanticTimeout, "semantic-timeout", 30*time.Second, "Timeout per semantic judge check")
	return cmd
}

func runSpecVerifyCommand(cmd *cobra.Command, args []string, flags *specVerifyFlags) error {
	if flags.format != "human" && flags.format != "json" && flags.format != "github-actions" {
		return fmt.Errorf("invalid format %q: expected human, json, or github-actions", flags.format)
	}
	if flags.failThreshold < 1 {
		return fmt.Errorf("--threshold must be at least 1")
	}
	if flags.fail {
		flags.warn = false
	}

	skillInput := flags.skillPath
	evalInput := flags.evalPath
	if len(args) > 0 {
		skillInput = args[0]
	}
	if len(args) > 1 {
		evalInput = args[1]
	}

	skillPath, err := resolveSpecSkillPath(skillInput)
	if err != nil {
		return err
	}
	evalPath, err := resolveSpecEvalPath(evalInput, skillPath)
	if err != nil {
		return err
	}

	opts := specverify.Options{
		SkillPath:     skillPath,
		EvalPath:      evalPath,
		Semantic:      flags.semantic,
		FailThreshold: flags.failThreshold,
	}

	var shutdown func(context.Context) error
	if flags.semantic {
		judgeModel, err := resolveSpecJudgeModel(evalPath, flags.judgeModel)
		if err != nil {
			return err
		}
		engine := newSpecVerifyEngine(judgeModel)
		if err := engine.Initialize(cmd.Context()); err != nil {
			return err
		}
		opts.JudgeModel = judgeModel
		opts.Matcher = &specverify.EngineSemanticMatcher{
			Engine:  engine,
			ModelID: judgeModel,
			Timeout: flags.semanticTimeout,
		}
		shutdown = engine.Shutdown
	}
	if shutdown != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		defer func() {
			_ = shutdown(shutdownCtx)
			_ = execution.ShutdownSharedClient(shutdownCtx)
		}()
	}

	report, err := specverify.Verify(cmd.Context(), opts)
	if err != nil {
		return err
	}

	switch flags.format {
	case "human":
		renderSpecVerifyHuman(cmd.OutOrStdout(), report)
	case "json":
		if err := renderSpecVerifyJSON(cmd.OutOrStdout(), report); err != nil {
			return err
		}
	case "github-actions":
		renderSpecVerifyGitHubActions(cmd.OutOrStdout(), report, flags.warn, flags.fail)
	}

	if flags.fail && report.Summary.UncoveredRequirements >= flags.failThreshold {
		return &TestFailureError{Message: fmt.Sprintf("spec verify failed: %d uncovered requirement(s) >= threshold %d", report.Summary.UncoveredRequirements, flags.failThreshold)}
	}
	return nil
}

func resolveSpecSkillPath(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		input = "."
	}
	path, err := filepath.Abs(input)
	if err != nil {
		return "", fmt.Errorf("resolving skill path: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("invalid skill path %q: %w", input, err)
	}
	if info.IsDir() {
		candidate := filepath.Join(path, "SKILL.md")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		return resolveSingleDiscoveredSkill(path)
	}
	if filepath.Base(path) != "SKILL.md" {
		return "", fmt.Errorf("expected SKILL.md or skill directory, got %s", input)
	}
	return path, nil
}

func resolveSingleDiscoveredSkill(root string) (string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "SKILL.md" {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("searching for SKILL.md under %s: %w", root, err)
	}
	if len(found) == 0 {
		return "", fmt.Errorf("no SKILL.md found under %s", root)
	}
	if len(found) > 1 {
		return "", fmt.Errorf("multiple SKILL.md files found under %s; pass --skill or run from a skill directory", root)
	}
	return found[0], nil
}

func resolveSpecEvalPath(input, skillPath string) (string, error) {
	if strings.TrimSpace(input) != "" {
		path, err := filepath.Abs(input)
		if err != nil {
			return "", fmt.Errorf("resolving eval path: %w", err)
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("invalid eval path %q: %w", input, err)
		}
		return path, nil
	}

	skillDir := filepath.Dir(skillPath)
	candidates := []string{
		filepath.Join(skillDir, "eval.yaml"),
		filepath.Join(skillDir, "eval.yml"),
		filepath.Join(skillDir, "evals", "eval.yaml"),
		filepath.Join(skillDir, "evals", "eval.yml"),
		filepath.Join(skillDir, "tests", "eval.yaml"),
		filepath.Join(skillDir, "tests", "eval.yml"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no eval.yaml found for %s; pass --eval or provide eval.yaml as the second argument", skillPath)
}

func resolveSpecJudgeModel(evalPath, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		return override, nil
	}
	spec, err := specverifyLoadEvalConfig(evalPath)
	if err != nil {
		return "", err
	}
	if spec.JudgeModel != "" {
		return spec.JudgeModel, nil
	}
	if spec.ModelID != "" {
		return spec.ModelID, nil
	}
	return "", errors.New("--semantic requires --judge-model or config.judge_model/config.model in eval.yaml")
}

type specVerifyEvalConfig struct {
	ModelID    string `yaml:"model"`
	JudgeModel string `yaml:"judge_model"`
}

func specverifyLoadEvalConfig(evalPath string) (*specVerifyEvalConfig, error) {
	data, err := os.ReadFile(evalPath)
	if err != nil {
		return nil, fmt.Errorf("reading eval.yaml: %w", err)
	}
	var raw struct {
		Config specVerifyEvalConfig `yaml:"config"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing eval.yaml: %w", err)
	}
	return &raw.Config, nil
}

func renderSpecVerifyHuman(w io.Writer, report *specverify.Report) {
	fmt.Fprintln(w, "Spec Verification")                                                                                                                                                  //nolint:errcheck
	fmt.Fprintf(w, "Coverage: %d/%d requirements covered (%d uncovered)\n\n", report.Summary.CoveredRequirements, report.Summary.TotalRequirements, report.Summary.UncoveredRequirements) //nolint:errcheck
	for _, row := range report.Coverage {
		status := "MISS"
		target := "no task exercises this"
		if len(row.CoveredBy) > 0 {
			status = "OK"
			target = "covered by tasks: [" + coveredTaskList(row.CoveredBy) + "]"
		}
		fmt.Fprintf(w, "%s %s  %q  -> %s (%s:%d)\n", status, row.Requirement.ID, row.Requirement.Text, target, row.Requirement.Source.File, row.Requirement.Source.StartLine) //nolint:errcheck
	}
}

func renderSpecVerifyJSON(w io.Writer, report *specverify.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func renderSpecVerifyGitHubActions(w io.Writer, report *specverify.Report, warn, fail bool) {
	if !warn && !fail {
		return
	}
	level := "warning"
	if fail {
		level = "error"
	}
	for _, row := range report.Coverage {
		if len(row.CoveredBy) > 0 {
			continue
		}
		_, _ = fmt.Fprintf(
			w,
			"::%s file=%s,line=%d,title=Uncovered spec requirement::%s %s\n",
			level,
			escapeGitHubActionsProperty(githubActionsAnnotationPath(row.Requirement.Source.File)),
			row.Requirement.Source.StartLine,
			row.Requirement.ID,
			escapeGitHubActionsMessage(row.Requirement.Text),
		)
	}
}

func coveredTaskList(items []specverify.CoveredBy) string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.TaskID)
	}
	return strings.Join(ids, ", ")
}

func githubActionsAnnotationPath(path string) string {
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(path)
	}
	wd, err := os.Getwd()
	if err != nil {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func escapeGitHubActionsMessage(s string) string {
	s = strings.ReplaceAll(s, "%", "%25")
	s = strings.ReplaceAll(s, "\r", "%0D")
	s = strings.ReplaceAll(s, "\n", "%0A")
	return s
}

func escapeGitHubActionsProperty(s string) string {
	s = escapeGitHubActionsMessage(s)
	s = strings.ReplaceAll(s, ":", "%3A")
	s = strings.ReplaceAll(s, ",", "%2C")
	return s
}
