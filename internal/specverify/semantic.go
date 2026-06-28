package specverify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/execution"
)

// EngineSemanticMatcher uses the configured judge model to fill deterministic coverage gaps.
type EngineSemanticMatcher struct {
	Engine  execution.AgentEngine
	ModelID string
	Timeout time.Duration
}

// Matches asks the judge whether a task exercises a requirement.
func (m *EngineSemanticMatcher) Matches(ctx context.Context, req Requirement, task TaskRef) (bool, string, error) {
	if m.Engine == nil {
		return false, "", fmt.Errorf("semantic matching requires an execution engine")
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	judgeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := semanticPrompt(req, task)
	resp, err := m.Engine.Execute(judgeCtx, &execution.ExecutionRequest{
		ModelID:              m.ModelID,
		Message:              prompt,
		EphemeralSession:     true,
		SkipWorkspaceCapture: true,
	})
	if err != nil {
		return false, "", fmt.Errorf("semantic match for %s against task %s: %w", req.ID, task.ID, err)
	}
	if resp == nil {
		return false, "", fmt.Errorf("semantic match for %s against task %s: empty judge response", req.ID, task.ID)
	}
	if !resp.Success {
		msg := strings.TrimSpace(resp.ErrorMsg)
		if msg == "" {
			msg = "judge execution failed"
		}
		return false, "", fmt.Errorf("semantic match for %s against task %s: %s", req.ID, task.ID, msg)
	}
	covered, reason := ParseSemanticResponse(resp.FinalOutput)
	return covered, reason, nil
}

func semanticPrompt(req Requirement, task TaskRef) string {
	return fmt.Sprintf(`You are verifying whether a waza eval task exercises one SKILL.md requirement.

Return only compact JSON:
{"covered":true|false,"reason":"short reason"}

Requirement:
- id: %s
- kind: %s
- text: %q

Task:
- id: %s
- name: %s
- expected_trigger: %s
- content:
%s

Rules:
- "covered" is true only if the task prompt, metadata, expectations, or graders would exercise this requirement.
- For kind "dont", covered is true only if the task is a negative trigger test.
- Do not infer coverage from unrelated implementation details.
`, req.ID, req.Kind, req.Text, task.ID, task.Name, expectedTriggerText(task.ExpectedTrigger), task.Text)
}

func expectedTriggerText(v *bool) string {
	if v == nil {
		return "unspecified"
	}
	if *v {
		return "true"
	}
	return "false"
}
