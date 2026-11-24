package graders

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/judgeevidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestEvidencePromptFixtureLiteralsAdmissionAndIsolation(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const contact = "fixture@example.invalid"
	for _, tc := range []struct {
		name    string
		task    string
		args    map[string]any
		result  string
		declare bool
		missing bool
		pass    bool
	}{
		{name: "declared task SHA", task: "Inspect " + sha, declare: true, pass: true},
		{name: "declared GitHub result and fixture contact", task: "Inspect fixtures", result: sha + " " + contact, declare: true, pass: true},
		{name: "declared tracker identifiers", task: "Inspect fixtures", args: map[string]any{"projectKey": "PROJ-1", "issueIdOrKey": "PROJ-1"}, declare: true, pass: true},
		{name: "absent declaration retains SHA rejection", task: "Inspect " + sha},
		{name: "undeclared same-shaped credential", task: "Inspect " + strings.Repeat("b", 40), declare: true},
		{name: "undeclared contact", task: "Inspect private@example.invalid", declare: true},
		{name: "undeclared key credential", task: "Inspect fixtures", args: map[string]any{"projectKey": "UNDECLARED_CREDENTIAL_CANARY"}, declare: true},
		{name: "declared substring in longer hex token", task: "Inspect " + sha + "aa", declare: true},
		{name: "declared substring in longer identifier", task: "Inspect prefix-" + sha, declare: true},
		{name: "declared contact substring", task: "Inspect prefix." + contact, declare: true},
		{name: "earlier rule cannot manufacture boundary", task: "Inspect AKIAIOSFODNN7EXAMPLE" + contact, declare: true},
		{name: "key value substring", task: "Inspect fixtures", args: map[string]any{"issueIdOrKey": "PROJ-1-extra"}, declare: true},
		{name: "key container not exempt", task: "Inspect fixtures", args: map[string]any{"projectKey": []string{"PROJ-1"}}, declare: true},
		{name: "actor cannot declare exemptions", task: "Inspect fixtures", args: map[string]any{"non_secret_literals": []string{strings.Repeat("b", 40)}, "projectKey": strings.Repeat("b", 40)}, declare: true},
		{name: "missing required evidence", task: "Inspect " + sha, declare: true, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := `{"prompt":"Judge.","evidence_context":{}}`
			if tc.declare {
				config = `{"prompt":"Judge.","evidence_context":{"non_secret_literals":["` + sha + `","` + contact + `","PROJ-1","UNUSED_DECLARATION_CANARY"]}}`
			}
			var params models.PromptGraderParameters
			require.NoError(t, json.Unmarshal([]byte(config), &params))
			require.NoError(t, ValidateConfig("judge", params))
			selection, err := judgeevidence.Selection([]models.GraderConfig{{Parameters: params}}, nil)
			require.NoError(t, err)
			grading := evidenceGradingContext(t)
			events := []copilot.SessionEvent{
				{Data: &copilot.UserMessageData{Content: tc.task}},
				{Data: &copilot.ToolExecutionStartData{ToolCallID: "call", ToolName: "github_query", Arguments: tc.args}},
				{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "call", Success: true, Result: &copilot.ToolExecutionCompleteResult{Content: tc.result}}},
				{Data: &copilot.AssistantMessageData{Content: "Fixtures inspected"}},
			}
			if tc.missing {
				events = nil
			}
			grading.JudgeEvidence = judgeevidence.Capture(judgeevidence.CaptureInput{
				Origin: grading.JudgeOrigin, Task: tc.task, Output: "Fixtures inspected",
				OutputPresent: true, Events: events, Selection: selection, ExecutionMode: "mock",
			})
			encoded, err := json.Marshal(grading.JudgeEvidence)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "UNUSED_DECLARATION_CANARY")
			require.NotContains(t, string(encoded), "non_secret_literals")
			var restored models.JudgeEvidence
			require.NoError(t, json.Unmarshal(encoded, &restored))
			grading.JudgeEvidence = &restored
			actor := &fakePromptExecutor{execute: func(*execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				t.Fatal("fixture declarations must never grant actor access")
				return nil, nil
			}}
			grading.Executor = actor
			judge := &fakePromptExecutor{execute: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				require.Empty(t, req.SessionID)
				require.Empty(t, req.WorkspaceDir)
				require.Empty(t, req.MCPServers)
				require.Empty(t, req.Resources)
				require.True(t, req.NoSkills)
				require.True(t, req.EphemeralSession)
				require.Len(t, req.Tools, 2)
				for _, private := range []string{"ACTOR_SESSION_CANARY", "ACTOR_WORKSPACE_CANARY", "MUTATED_OUTPUT_CANARY", "UNUSED_DECLARATION_CANARY", "non_secret_literals"} {
					require.NotContains(t, req.Message, private)
				}
				require.Contains(t, req.Message, tc.task)
				require.Contains(t, req.Message, tc.result)
				if len(tc.args) > 0 {
					require.Contains(t, req.Message, `"projectKey":"PROJ-1"`)
					require.Contains(t, req.Message, `"issueIdOrKey":"PROJ-1"`)
				}
				_, err := req.Tools[0].Handler(copilot.ToolInvocation{ToolName: req.Tools[0].Name})
				return &execution.ExecutionResponse{Success: true}, err
			}}
			grading.EvidenceExecutor = judge
			g, err := NewPromptGrader("judge", params)
			require.NoError(t, err)
			result, err := g.Grade(t.Context(), grading)
			if tc.pass {
				require.NoError(t, err)
				require.True(t, result.Passed)
				require.Equal(t, 1, judge.calls)
				require.NotNil(t, restored.FixtureLiterals)
				require.Positive(t, restored.FixtureLiterals.PreservedMatches)
				require.Zero(t, restored.Redaction.MatchCount)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
				require.Zero(t, judge.calls)
				require.NotEmpty(t, restored.Diagnostics)
				if !tc.missing {
					require.Contains(t, string(encoded), "redacted_required")
					if tc.args == nil {
						require.NotContains(t, string(encoded), tc.task, "rejected required content must be withheld")
					} else {
						require.Equal(t, "unavailable", restored.ToolEvents.Availability)
						require.Nil(t, restored.ToolEvents.Items)
					}
				}
			}
			require.Zero(t, actor.calls)
		})
	}
}

func evidenceGradingContext(t *testing.T) *Context {
	t.Helper()
	origin := models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"}
	bundle := judgeevidence.Capture(judgeevidence.CaptureInput{
		Origin: origin, Task: "FROZEN_TASK_CANARY", Output: "FROZEN_OUTPUT_CANARY", OutputPresent: true, ExecutionMode: "mock",
		Events: []copilot.SessionEvent{
			{Data: &copilot.UserMessageData{Content: "FROZEN_TASK_CANARY"}},
			{Data: &copilot.AssistantMessageData{Content: "FROZEN_OUTPUT_CANARY"}},
		},
	})
	require.NoError(t, judgeevidence.Verify(bundle, origin))
	return &Context{JudgeEvidence: bundle, JudgeOrigin: origin, SessionID: "ACTOR_SESSION_CANARY",
		WorkspaceDir: "/private/ACTOR_WORKSPACE_CANARY", Output: "MUTATED_OUTPUT_CANARY",
		TestCase: &models.TestCase{Stimulus: models.TaskStimulus{Message: "MUTATED_TASK_CANARY"}}}
}

func TestEvidencePromptExcludesMCPResultMetaFromJudgeRequest(t *testing.T) {
	const metadataCanary = "UI_ONLY_MCP_RESULT_META_CANARY"
	grading := evidenceGradingContext(t)
	grading.JudgeEvidence = judgeevidence.Capture(judgeevidence.CaptureInput{
		Origin: grading.JudgeOrigin, Task: "Inspect the tool result.", Output: "Done.", OutputPresent: true, ExecutionMode: "mock",
		Events: []copilot.SessionEvent{
			{Data: &copilot.UserMessageData{Content: "Inspect the tool result."}},
			{Data: &copilot.ToolExecutionStartData{ToolCallID: "call", ToolName: "lookup"}},
			{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "call", Success: true,
				Result: &copilot.ToolExecutionCompleteResult{
					Content: "Public tool result.", MCPResultMeta: map[string]any{"ui": metadataCanary},
				}}},
			{Data: &copilot.AssistantMessageData{Content: "Done."}},
		},
	})
	require.NoError(t, judgeevidence.Verify(grading.JudgeEvidence, grading.JudgeOrigin))
	toolResult, ok := (*grading.JudgeEvidence.ToolEvents.Items)[0].Result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Public tool result.", toolResult["content"])
	require.NotContains(t, toolResult, "mcpResultMeta")

	grading.EvidenceExecutor = &fakePromptExecutor{execute: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		require.Contains(t, req.Message, "Public tool result.")
		require.NotContains(t, req.Message, metadataCanary)
		_, err := req.Tools[0].Handler(copilot.ToolInvocation{ToolName: wazaPassToolName})
		require.NoError(t, err)
		return &execution.ExecutionResponse{Success: true}, nil
	}}
	g, err := NewPromptGrader("judge", models.PromptGraderParameters{
		Prompt: "Judge the recorded execution.", EvidenceContext: &models.PromptEvidenceContext{},
	})
	require.NoError(t, err)
	result, err := g.Grade(t.Context(), grading)
	require.NoError(t, err)
	require.True(t, result.Passed)
}

func TestEvidencePromptUsesFrozenDataFreshExecutorAndNoGenericDetails(t *testing.T) {
	grading := evidenceGradingContext(t)
	actor := &fakePromptExecutor{execute: func(_ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		t.Fatal("actor executor must never run")
		return nil, nil
	}}
	grading.Executor = actor
	var observations []models.PromptJudgeObservation
	var requests []*execution.ExecutionRequest
	usage := &models.UsageStats{InputTokens: 7}
	judges := &fakePromptExecutor{execute: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		requests = append(requests, req)
		require.Empty(t, req.SessionID)
		require.Empty(t, req.WorkspaceDir)
		require.Empty(t, req.Resources)
		require.Empty(t, req.Instructions)
		require.Empty(t, req.MCPServers)
		require.Empty(t, req.MessageMode)
		require.False(t, req.Streaming)
		require.Nil(t, req.CaptureJudgeFiles)
		require.True(t, req.NoSkills)
		require.True(t, req.EphemeralSession)
		require.NotNil(t, req.ToolPolicy)
		require.Contains(t, req.Message, "FROZEN_TASK_CANARY")
		require.Contains(t, req.Message, "FROZEN_OUTPUT_CANARY")
		for _, canary := range []string{"ACTOR_SESSION_CANARY", "ACTOR_WORKSPACE_CANARY", "MUTATED_TASK_CANARY", "MUTATED_OUTPUT_CANARY"} {
			require.NotContains(t, req.Message, canary)
		}
		_, err := req.Tools[0].Handler(copilot.ToolInvocation{ToolName: wazaPassToolName})
		require.NoError(t, err)
		return &execution.ExecutionResponse{Success: true, SessionID: "new-judge", ModelID: "judge-model", Usage: usage}, nil
	}}
	grading.EvidenceExecutor = judges
	grading.RecordPromptJudging = func(observation models.PromptJudgeObservation) { observations = append(observations, observation) }
	g, err := NewPromptGrader("judge", models.PromptGraderParameters{Prompt: "Judge the recorded execution.", EvidenceContext: &models.PromptEvidenceContext{}})
	require.NoError(t, err)
	for range 2 {
		result, err := g.Grade(t.Context(), grading)
		require.NoError(t, err)
		require.True(t, result.Passed)
		require.Nil(t, result.Details)
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "FROZEN_OUTPUT_CANARY")
	}
	require.Zero(t, actor.calls)
	require.Equal(t, 2, judges.calls)
	require.NotSame(t, requests[0], requests[1])
	require.Len(t, observations, 2)
	require.Equal(t, "submitted", observations[0].State)
	require.Equal(t, "not_observable", observations[0].Inspection)
	require.Same(t, usage, observations[0].Usage)
	require.NotEmpty(t, observations[0].BundleSHA256)
	require.NotEmpty(t, observations[0].DeliverySHA256)
	_, err = requests[0].Tools[0].Handler(copilot.ToolInvocation{ToolName: wazaPassToolName})
	require.ErrorContains(t, err, "no longer active")
}

func TestEvidencePromptRealExecutorAdmitsFreshRequest(t *testing.T) {
	grading := evidenceGradingContext(t)
	grading.EvidenceExecutor = execution.NewEvidenceJudgeExecutor()
	g, err := NewPromptGrader("judge", models.PromptGraderParameters{
		Model: "judge-model", Prompt: "Judge the recorded execution.", EvidenceContext: &models.PromptEvidenceContext{},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := g.Grade(ctx, grading)
	require.ErrorIs(t, err, context.Canceled, "the real executor must admit the request before honoring cancellation, without starting a runtime")
	require.Nil(t, result)
}

func TestEvidencePromptFailuresCannotBecomePassingGrades(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(*execution.ExecutionRequest) (*execution.ExecutionResponse, error)
	}{
		{"post-grade error", func(_ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			return &execution.ExecutionResponse{Success: true}, errors.New("cleanup failed")
		}},
		{"failed response", func(_ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			return &execution.ExecutionResponse{Success: false}, nil
		}},
		{"response error", func(_ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			return &execution.ExecutionResponse{Success: true, ErrorMsg: "submission failed"}, nil
		}},
		{"nil response", func(_ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) { return nil, nil }},
		{"policy denial", func(_ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			return &execution.ExecutionResponse{Success: true, ToolPolicyDenials: []execution.ToolPolicyDenial{{Tool: "bash"}}}, nil
		}},
		{"invalid callback", func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, err := req.Tools[0].Handler(copilot.ToolInvocation{ToolName: "spoofed"})
			require.Error(t, err)
			return &execution.ExecutionResponse{Success: true}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grading := evidenceGradingContext(t)
			var observed *models.PromptJudgeObservation
			grading.RecordPromptJudging = func(observation models.PromptJudgeObservation) { observed = &observation }
			grading.EvidenceExecutor = &fakePromptExecutor{execute: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				_, err := req.Tools[0].Handler(copilot.ToolInvocation{ToolName: wazaPassToolName})
				require.NoError(t, err)
				return tc.fail(req)
			}}
			g, err := NewPromptGrader("judge", models.PromptGraderParameters{Prompt: "Judge.", EvidenceContext: &models.PromptEvidenceContext{}})
			require.NoError(t, err)
			result, err := g.Grade(t.Context(), grading)
			require.Error(t, err)
			require.Nil(t, result)
			require.NotNil(t, observed)
			require.Equal(t, "failed", observed.State)
			require.NotEmpty(t, observed.Diagnostics)
		})
	}
}

func TestEvidencePromptUnusableEvidenceDoesNotInvokeJudge(t *testing.T) {
	g, err := NewPromptGrader("judge", models.PromptGraderParameters{Prompt: "Judge.", EvidenceContext: &models.PromptEvidenceContext{}})
	require.NoError(t, err)
	for _, mutate := range []func(*Context){
		func(grading *Context) { grading.JudgeEvidence = nil },
		func(grading *Context) { grading.JudgeOrigin.TaskID = "other" },
		func(grading *Context) { grading.JudgeEvidence.SHA256 = "mismatch" },
		func(grading *Context) { grading.JudgeEvidence.ToolEvents.Items = nil },
		func(grading *Context) { grading.EvidenceExecutor = nil },
	} {
		grading := evidenceGradingContext(t)
		executor := &fakePromptExecutor{execute: func(_ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			t.Fatal("unusable evidence must not start a judge")
			return nil, nil
		}}
		grading.EvidenceExecutor = executor
		mutate(grading)
		_, err := g.Grade(t.Context(), grading)
		require.Error(t, err)
		require.Zero(t, executor.calls)
	}
}

func TestEvidencePromptConcurrentCallbacksAndCancellation(t *testing.T) {
	grading := evidenceGradingContext(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	for _, canceled := range []bool{false, true} {
		grading.EvidenceExecutor = &fakePromptExecutor{execute: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			var group sync.WaitGroup
			results := make(chan error, 8)
			for i := range 8 {
				group.Go(func() {
					tool := req.Tools[i%2]
					_, err := tool.Handler(copilot.ToolInvocation{ToolName: tool.Name})
					results <- err
				})
			}
			group.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				}
			}
			require.Equal(t, 1, successes)
			if canceled {
				cancel()
			}
			return &execution.ExecutionResponse{Success: true}, nil
		}}
		g, err := NewPromptGrader("judge", models.PromptGraderParameters{Prompt: "Judge.", EvidenceContext: &models.PromptEvidenceContext{}})
		require.NoError(t, err)
		result, err := g.Grade(ctx, grading)
		if canceled {
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, result)
		} else {
			require.Error(t, err)
			require.Nil(t, result)
		}
	}
}
