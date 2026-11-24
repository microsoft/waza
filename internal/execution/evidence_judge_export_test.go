package execution

import (
	"context"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
)

func EvidenceJudgeExecutorForTest(client interface {
	CopilotClient
	ForceStop()
}) *EvidenceJudgeExecutor {
	executor := NewEvidenceJudgeExecutor()
	executor.newClient = func(*copilot.ClientOptions) evidenceJudgeClient { return client }
	executor.resolveCLI = func(context.Context, *evidenceJudgeDirs) (string, error) { return "/unused/test-runtime", nil }
	return executor
}
