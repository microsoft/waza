package dev

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/spinner"
	"github.com/microsoft/waza/internal/trigger"
	"github.com/microsoft/waza/internal/workspace"
)

var (
	// newDevEngine is a test hook for replacing the agent engine in tests
	newDevEngine = func(modelID string) execution.AgentEngine {
		return execution.NewCopilotEngineBuilder(modelID, nil).Build()
	}
	// startDevSpinner is a test hook for replacing the spinner in tests
	startDevSpinner = spinner.Start

	//go:embed suggest_prompt.md
	suggestPrompt string
)

type copilotReport struct {
	Skill        *skill.Skill
	ModelID      string
	TriggerSpec  *trigger.TestSpec
	Suggestions  string
	TriggerTotal int
}

func runDevCopilot(cfg *devConfig) error {
	skillPath := filepath.Join(cfg.SkillDir, "SKILL.md")
	sk, err := readSkillFile(skillPath)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("no SKILL.md found in " + cfg.SkillDir)
	}
	if err != nil {
		return err
	}

	ctx := cfg.Context
	if ctx == nil {
		ctx = context.Background()
	}
	errOut := cfg.Err
	if errOut == nil {
		errOut = cfg.Out
	}

	engine := newDevEngine(cfg.ModelID)

	if err := engine.Initialize(ctx); err != nil {
		return err
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if shutdownErr := engine.Shutdown(shutdownCtx); shutdownErr != nil && errOut != nil {
			fprintf(errOut, "\n- ⚠️ Copilot engine shutdown error: %v\n", shutdownErr)
		}
	}()

	stopSpinner := startDevSpinner(errOut, "Generating report with Copilot...")
	defer stopSpinner()

	triggerSpec, triggerErr := discoverTriggerSpec(sk.Frontmatter.Name)
	if triggerErr != nil {
		if errOut != nil {
			fprintf(errOut, "⚠️  %s\n", "Failed to load trigger prompts: "+triggerErr.Error())
		}
		triggerSpec = nil
	}

	suggestions, err := getCopilotSuggestions(ctx, engine, skillPath, triggerSpec)
	if err != nil {
		return err
	}

	triggers := 0
	if triggerSpec != nil {
		triggers = len(triggerSpec.ShouldTriggerPrompts) + len(triggerSpec.ShouldNotTriggerPrompts)
	}
	report := &copilotReport{
		Skill:        sk,
		ModelID:      cfg.ModelID,
		TriggerSpec:  triggerSpec,
		Suggestions:  suggestions,
		TriggerTotal: triggers,
	}
	stopSpinner()
	displayCopilotReport(cfg.Out, report)
	return nil
}

func discoverTriggerSpec(skillName string) (*trigger.TestSpec, error) {
	evalDir := resolveWorkspaceEvalDir(skillName)
	if evalDir == "" {
		return nil, nil
	}
	spec, err := trigger.Discover(evalDir)
	if err != nil {
		return nil, err
	}
	return spec, nil
}

func resolveWorkspaceEvalDir(skillName string) string {
	if skillName == "" {
		return ""
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	ctx, err := workspace.DetectContext(wd)
	if err != nil || ctx.Type == workspace.ContextNone {
		return ""
	}
	evalPath, err := workspace.FindEval(ctx, skillName)
	if err != nil || evalPath == "" {
		return ""
	}
	return filepath.Dir(evalPath)
}

func getCopilotSuggestions(ctx context.Context, engine execution.AgentEngine, skillPath string, triggerSpec *trigger.TestSpec) (string, error) {
	skillContent, err := os.ReadFile(skillPath)
	if err != nil {
		return "", fmt.Errorf("reading SKILL.md: %w", err)
	}

	var prompt strings.Builder
	prompt.WriteString(strings.TrimRight(suggestPrompt, " \t\r\n"))
	prompt.WriteString("\n\n")
	prompt.Write(skillContent)
	if triggerSpec != nil {
		prompt.WriteString("\n\n")
		prompt.WriteString(formatTriggerExamples(triggerSpec))
	}

	res, err := engine.Execute(ctx, &execution.ExecutionRequest{
		Message: prompt.String(),
		Timeout: 120 * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("getting Copilot suggestions: %w", err)
	}

	suggestions := strings.TrimSpace(res.FinalOutput)
	if suggestions == "" {
		return "", errors.New("no suggestions returned by Copilot")
	}
	return suggestions, nil
}

func formatTriggerExamples(spec *trigger.TestSpec) string {
	var b strings.Builder
	b.WriteString("## Trigger prompt examples\n\n")

	if len(spec.ShouldTriggerPrompts) > 0 {
		b.WriteString("Prompts that SHOULD trigger this skill:\n")
		for _, p := range spec.ShouldTriggerPrompts {
			fmt.Fprintf(&b, "- %q\n", p.Prompt)
		}
		b.WriteString("\n")
	}
	if len(spec.ShouldNotTriggerPrompts) > 0 {
		b.WriteString("Prompts that SHOULD NOT trigger this skill:\n")
		for _, p := range spec.ShouldNotTriggerPrompts {
			fmt.Fprintf(&b, "- %q\n", p.Prompt)
		}
	}
	return strings.TrimSpace(b.String())
}

func displayCopilotReport(w io.Writer, r *copilotReport) {
	fprintln(w, "# Skill Selection Suggestions")
	fprintln(w)

	fprintf(w, "These suggestions were written by `%s` ", r.ModelID)
	if r.TriggerSpec != nil {
		fprintf(w, "using %d prompt example(s) from `trigger_tests.yaml`.\n", r.TriggerTotal)
	} else {
		fprintln(w, "without any example prompts. Create a `trigger_tests.yaml` file to have the model consider these when writing suggestions.")
	}
	fprintln(w)

	fprintln(w, "## Skill")
	fprintf(w, "- Name: `%s`\n", r.Skill.Frontmatter.Name)
	fprintf(w, "- Path: `%s`\n", r.Skill.Path)
	fprintf(w, "- Tokens: %d\n", r.Skill.Tokens)
	fprintln(w)

	fprintln(w, "## Suggestions")
	fprintln(w, r.Suggestions)
	fprintln(w)
}
