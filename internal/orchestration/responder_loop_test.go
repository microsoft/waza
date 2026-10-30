package orchestration

import (
	"context"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/responder"
	"github.com/stretchr/testify/require"
)

// scriptedClassifier returns a queued sequence of decisions, repeating the last.
type scriptedClassifier struct {
	decisions []responder.Decision
	idx       int
	calls     int
}

func (s *scriptedClassifier) Classify(_ context.Context, _ string) (responder.Decision, error) {
	s.calls++
	d := s.decisions[s.idx]
	if s.idx < len(s.decisions)-1 {
		s.idx++
	}
	return d, nil
}

func (s *scriptedClassifier) Close(_ context.Context) error { return nil }

func newResponderTestRunner(t *testing.T) *EvalRunner {
	t.Helper()
	spec := &models.EvalSpec{
		SpecIdentity: models.SpecIdentity{Name: "test-benchmark"},
		SkillName:    "my-skill",
		Config: models.Config{
			EngineType: "mock",
			ModelID:    "gpt-4",
			TimeoutSec: 120,
		},
	}
	cfg := config.NewEvalConfig(spec)
	engine := execution.NewMockEngine("gpt-4")
	require.NoError(t, engine.Initialize(context.Background()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
	return NewEvalRunner(cfg, engine, WithSkipGraders())
}

func TestResponderLoopReplyThenStop(t *testing.T) {
	r := newResponderTestRunner(t)
	sc := &scriptedClassifier{decisions: []responder.Decision{
		{Kind: responder.DecisionReply, Answer: "research-agent"},
		{Kind: responder.DecisionStop},
	}}
	r.newClassifier = func(models.ResponderConfig, string) responderClassifier { return sc }

	tc := &models.TestCase{
		TestID:   "t1",
		Stimulus: models.TaskStimulus{Message: "add agent", Responder: &models.ResponderConfig{Instructions: "be research-agent", MaxFollowups: 5}},
	}
	rr := r.executeRun(context.Background(), tc, 1)

	require.NotNil(t, rr.Responder)
	require.Equal(t, models.ResponderOutcomeStopped, rr.Responder.Outcome)
	require.Equal(t, 1, rr.Responder.FollowupsSent)
}

func TestResponderLoopAbstainMarksError(t *testing.T) {
	r := newResponderTestRunner(t)
	sc := &scriptedClassifier{decisions: []responder.Decision{
		{Kind: responder.DecisionAbstain, Reason: "too vague"},
	}}
	r.newClassifier = func(models.ResponderConfig, string) responderClassifier { return sc }

	tc := &models.TestCase{
		TestID:   "t1",
		Stimulus: models.TaskStimulus{Message: "add agent", Responder: &models.ResponderConfig{Instructions: "x", MaxFollowups: 5}},
	}
	rr := r.executeRun(context.Background(), tc, 1)

	require.Equal(t, models.StatusError, rr.Status)
	require.NotNil(t, rr.Responder)
	require.Equal(t, models.ResponderOutcomeAbstained, rr.Responder.Outcome)
	require.Contains(t, rr.ErrorMsg, "abstained")
	require.Contains(t, rr.ErrorMsg, "too vague")
}

func TestResponderLoopCapExhausted(t *testing.T) {
	r := newResponderTestRunner(t)
	sc := &scriptedClassifier{decisions: []responder.Decision{
		{Kind: responder.DecisionReply, Answer: "a"},
	}}
	r.newClassifier = func(models.ResponderConfig, string) responderClassifier { return sc }

	tc := &models.TestCase{
		TestID:   "t1",
		Stimulus: models.TaskStimulus{Message: "add agent", Responder: &models.ResponderConfig{Instructions: "x", MaxFollowups: 2}},
	}
	rr := r.executeRun(context.Background(), tc, 1)

	require.NotNil(t, rr.Responder)
	require.Equal(t, models.ResponderOutcomeCapExhausted, rr.Responder.Outcome)
	require.Equal(t, 2, rr.Responder.FollowupsSent)
	require.NotEqual(t, models.StatusError, rr.Status)
}

type responderExecutorFunc func(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error)

func (f responderExecutorFunc) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return f(ctx, req)
}

func TestResponderLoopPreservesExecutionFailure(t *testing.T) {
	for _, phase := range []string{"initial", "resumed"} {
		t.Run(phase, func(t *testing.T) {
			r := newResponderTestRunner(t)
			calls := 0
			exec := responderExecutorFunc(func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				calls++
				if calls == 1 {
					require.Empty(t, req.SessionID)
				} else {
					require.Equal(t, "responder-session", req.SessionID)
				}
				if phase == "resumed" && calls == 1 {
					for _, tool := range req.Tools {
						if tool.Name == "responder_reply" {
							_, err := tool.Handler(copilot.ToolInvocation{Arguments: map[string]any{"answer": "synthetic answer"}})
							require.NoError(t, err)
							return &execution.ExecutionResponse{SessionID: "responder-session", Success: true}, nil
						}
					}
					t.Fatal("responder_reply tool missing")
				}
				return &execution.ExecutionResponse{
					SessionID: "responder-session", Success: false, ErrorMsg: "synthetic provider rejection",
				}, nil
			})
			r.newClassifier = func(cfg models.ResponderConfig, model string) responderClassifier {
				return responder.New(exec, cfg, model)
			}
			tc := &models.TestCase{
				TestID: "synthetic-responder-failure",
				Stimulus: models.TaskStimulus{
					Message: "Synthetic question?",
					Responder: &models.ResponderConfig{
						Instructions: "Use the synthetic answer.", MaxFollowups: 3,
					},
				},
			}
			rr := r.executeRun(t.Context(), tc, 1)
			require.Equal(t, models.StatusError, rr.Status)
			require.Equal(t, "responder error: responder execution failed: synthetic provider rejection", rr.ErrorMsg)
			require.NotNil(t, rr.Responder)
			require.Equal(t, models.ResponderOutcomeError, rr.Responder.Outcome)
			require.Equal(t, "responder execution failed: synthetic provider rejection", rr.Responder.Reason)
			wantCalls := 1
			if phase == "resumed" {
				wantCalls = 2
			}
			require.Equal(t, wantCalls, calls)
			require.Equal(t, wantCalls-1, rr.Responder.FollowupsSent)
		})
	}
}
