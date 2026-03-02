package dev

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestNewCommand_CopilotFlags(t *testing.T) {
	cmd := NewCommand()

	copilot, err := cmd.Flags().GetBool("copilot")
	require.NoError(t, err)
	require.False(t, copilot)

	model, err := cmd.Flags().GetString("model")
	require.NoError(t, err)
	require.Equal(t, defaultCopilotModel, model)
}

func TestRunDev_ModelWithoutCopilot(t *testing.T) {
	dir := writeSkillFixture(t, "model-test", "---\nname: model-test\ndescription: \"desc\"\n---\n# Body\n")

	cmd := NewCommand()
	cmd.SetArgs([]string{dir, "--model", "custom-model"})

	err := cmd.Execute()
	require.ErrorContains(t, err, "--model is valid only with --copilot")
}

func TestRunDev_CopilotRejectsIterativeFlags(t *testing.T) {
	dir := writeSkillFixture(t, "reject-flags", "---\nname: reject-flags\ndescription: \"desc\"\n---\n# Body\n")

	tests := []struct {
		name    string
		args    []string
		errLike string
	}{
		{name: "target", args: []string{dir, "--copilot", "--target", "high"}, errLike: "--target is not valid with --copilot"},
		{name: "max-iterations", args: []string{dir, "--copilot", "--max-iterations", "2"}, errLike: "--max-iterations is not valid with --copilot"},
		{name: "auto", args: []string{dir, "--copilot", "--auto"}, errLike: "--auto is not valid with --copilot"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCommand()
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			require.ErrorContains(t, err, tt.errLike)
		})
	}
}

func TestRunDev_CopilotReport_NoMutation(t *testing.T) {
	skillDir := writeSkillFixture(t, "report-skill", `---
name: report-skill
description: "Short"
---

# Report Skill
`)
	skillPath := filepath.Join(skillDir, "SKILL.md")
	before, err := os.ReadFile(skillPath)
	require.NoError(t, err)

	engine := &copilotTestEngine{
		skillName:        "report-skill",
		suggestionOutput: "1. Add clearer trigger phrases.",
	}
	withDevTestEngine(t, engine)

	var out bytes.Buffer
	cmd := NewCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{skillDir, "--copilot"})

	require.NoError(t, cmd.Execute())

	after, err := os.ReadFile(skillPath)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "copilot mode must not modify SKILL.md")

	output := out.String()
	require.Contains(t, output, "# Skill Selection Suggestions")
	require.Contains(t, output, fmt.Sprintf("These suggestions were written by `%s` without any example prompts.", defaultCopilotModel))
	require.Contains(t, output, "Create a `trigger_tests.yaml` file to have the model consider these when writing suggestions.")
	require.Contains(t, output, "## Suggestions")
	require.Contains(t, output, "Add clearer trigger phrases")
}

func TestRunDev_CopilotReport_CustomModelShown(t *testing.T) {
	skillDir := writeSkillFixture(t, "custom-model-skill", `---
name: custom-model-skill
description: "Short"
---

# Custom Model Skill
`)

	engine := &copilotTestEngine{
		skillName:        "custom-model-skill",
		suggestionOutput: "1. Improve trigger wording.",
	}
	withDevTestEngine(t, engine)

	var out bytes.Buffer
	cmd := NewCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{skillDir, "--copilot", "--model", "custom-model"})

	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "These suggestions were written by `custom-model` without any example prompts.")
}

func TestRunDev_CopilotStartsSpinner(t *testing.T) {
	skillDir := writeSkillFixture(t, "spinner-skill", `---
name: spinner-skill
description: "Short"
---

# Spinner Skill
`)

	engine := &copilotTestEngine{
		skillName:        "spinner-skill",
		suggestionOutput: "1. Improve triggers.",
	}
	withDevTestEngine(t, engine)

	spinnerCalled := false
	withDevTestSpinner(t, func(_ io.Writer, message string) func() {
		spinnerCalled = true
		require.Equal(t, "Generating report with Copilot...", message)
		return func() {}
	})

	var out bytes.Buffer
	cmd := NewCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{skillDir, "--copilot"})

	require.NoError(t, cmd.Execute())
	require.True(t, spinnerCalled)
}

func TestRunDev_CopilotReport_EngineError(t *testing.T) {
	skillDir := writeSkillFixture(t, "engine-error-skill", `---
name: engine-error-skill
description: "Short"
---

# Skill
`)

	engine := &copilotTestEngine{executeErr: errors.New("boom")}
	withDevTestEngine(t, engine)

	cmd := NewCommand()
	cmd.SetArgs([]string{skillDir, "--copilot"})
	err := cmd.Execute()
	require.ErrorContains(t, err, "getting Copilot suggestions: boom")
}

func TestRunDev_CopilotReport_EmptySuggestions(t *testing.T) {
	skillDir := writeSkillFixture(t, "empty-output-skill", `---
name: empty-output-skill
description: "Short"
---

# Skill
`)

	engine := &copilotTestEngine{suggestionOutput: "  \n\t  "}
	withDevTestEngine(t, engine)

	cmd := NewCommand()
	cmd.SetArgs([]string{skillDir, "--copilot"})
	err := cmd.Execute()
	require.ErrorContains(t, err, "no suggestions returned by Copilot")
}

func TestRunDev_CopilotTriggerLoadFailureWarnsAndContinues(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "bad-trigger-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: bad-trigger-skill
description: "Short"
---

# Skill
`), 0o644))

	evalDir := filepath.Join(root, "evals", "bad-trigger-skill")
	require.NoError(t, os.MkdirAll(evalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte("name: test\nskill: bad-trigger-skill\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "trigger_tests.yaml"), []byte(`should_trigger_prompts:
  - prompt: "Explain this"
`), 0o644))

	t.Chdir(root)

	engine := &copilotTestEngine{suggestionOutput: "1. Tighten trigger language."}
	withDevTestEngine(t, engine)

	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd := NewCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{skillDir, "--copilot"})

	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "without any example prompts")
	require.Contains(t, errOut.String(), "Failed to load trigger prompts")
}

func TestRunDev_CopilotIncludesTriggerExamplesFromEvalDir(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "code-explainer")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: code-explainer
description: "Short"
---
# Body
`), 0o644))

	evalDir := filepath.Join(root, "evals", "code-explainer")
	require.NoError(t, os.MkdirAll(evalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte("name: test\nskill: code-explainer\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "trigger_tests.yaml"), []byte(`skill: code-explainer
should_trigger_prompts:
  - prompt: "Explain this code to me"
should_not_trigger_prompts:
  - prompt: "Write me a new function"
`), 0o644))

	t.Chdir(root)

	engine := &copilotTestEngine{
		skillName:        "code-explainer",
		suggestionOutput: "1. Add stronger anti-triggers.",
	}
	withDevTestEngine(t, engine)

	var out bytes.Buffer
	cmd := NewCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"code-explainer", "--copilot"})

	require.NoError(t, cmd.Execute())

	report := out.String()
	require.Contains(t, report, fmt.Sprintf("These suggestions were written by `%s` using 2 prompt example(s) from `trigger_tests.yaml`.", defaultCopilotModel))

	suggestionPrompt := engine.LastSuggestionMessage()
	require.Contains(t, suggestionPrompt, "## Trigger prompt examples")
	require.Contains(t, suggestionPrompt, `"Explain this code to me"`)
	require.Contains(t, suggestionPrompt, `"Write me a new function"`)
}

func TestRunDev_CopilotUsesEvalDirTriggerExamples(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "code-explainer")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: code-explainer
description: "Short"
---
# Body
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "trigger_tests.yaml"), []byte(`skill: code-explainer
should_trigger_prompts:
  - prompt: "Local trigger prompt"
`), 0o644))

	evalDir := filepath.Join(root, "evals", "code-explainer")
	require.NoError(t, os.MkdirAll(evalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte("name: test\nskill: code-explainer\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "trigger_tests.yaml"), []byte(`skill: code-explainer
should_trigger_prompts:
  - prompt: "Eval trigger prompt"
`), 0o644))

	t.Chdir(root)

	engine := &copilotTestEngine{suggestionOutput: "1. Add anti-triggers."}
	withDevTestEngine(t, engine)

	cmd := NewCommand()
	cmd.SetArgs([]string{skillDir, "--copilot"})
	require.NoError(t, cmd.Execute())

	suggestionPrompt := engine.LastSuggestionMessage()
	require.NotContains(t, suggestionPrompt, `"Local trigger prompt"`)
	require.Contains(t, suggestionPrompt, `"Eval trigger prompt"`)
}

func TestRunDev_CopilotUsesCommandContext(t *testing.T) {
	skillDir := writeSkillFixture(t, "ctx-skill", `---
name: ctx-skill
description: "Short"
---

# Skill
`)

	engine := &copilotTestEngine{respectContext: true, suggestionOutput: "1. Improve scope."}
	withDevTestEngine(t, engine)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cmd := NewCommand()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{skillDir, "--copilot"})
	err := cmd.Execute()
	require.ErrorContains(t, err, "context canceled")
}

func withDevTestEngine(t *testing.T, engine execution.AgentEngine) {
	t.Helper()
	orig := newDevEngine
	newDevEngine = func(_ string) execution.AgentEngine { return engine }
	t.Cleanup(func() {
		newDevEngine = orig
	})
}

func withDevTestSpinner(t *testing.T, fn func(io.Writer, string) func()) {
	t.Helper()
	orig := startDevSpinner
	startDevSpinner = fn
	t.Cleanup(func() {
		startDevSpinner = orig
	})
}

func writeSkillFixture(t *testing.T, name, content string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
	return dir
}

type copilotTestEngine struct {
	mu sync.Mutex

	skillName        string
	suggestionOutput string
	executeErr       error
	respectContext   bool
	messages         []string
	triggerCalls     int
}

func (e *copilotTestEngine) Initialize(context.Context) error { return nil }

func (e *copilotTestEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if e.respectContext {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	if e.executeErr != nil {
		return nil, e.executeErr
	}

	e.mu.Lock()
	e.messages = append(e.messages, req.Message)
	e.mu.Unlock()

	if req.SkillName != "" {
		e.mu.Lock()
		e.triggerCalls++
		e.mu.Unlock()
		invocations := []execution.SkillInvocation{}
		if strings.Contains(strings.ToLower(req.Message), "explain") {
			invocations = append(invocations, execution.SkillInvocation{Name: req.SkillName})
		}
		return &execution.ExecutionResponse{
			FinalOutput:      "trigger result",
			SkillInvocations: invocations,
			Success:          true,
		}, nil
	}

	return &execution.ExecutionResponse{
		FinalOutput: e.suggestionOutput,
		Success:     true,
	}, nil
}

func (e *copilotTestEngine) Shutdown(context.Context) error { return nil }

func (e *copilotTestEngine) SessionUsage(string) *models.UsageStats { return nil }

func (e *copilotTestEngine) LastSuggestionMessage() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(e.messages) - 1; i >= 0; i-- {
		if strings.Contains(e.messages[i], "## Skill to analyze") {
			return e.messages[i]
		}
	}
	return ""
}
