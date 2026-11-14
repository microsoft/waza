package execution

import (
	"context"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/executionbudget"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// Drives SDK start events through CopilotEngine.Execute into a real budget
// meter, so removing the event-to-budget bridge fails this test.
func TestCopilotExecuteEnforcesCountedBudgetsFromSessionEvents(t *testing.T) {
	one := int64(1)
	for _, tt := range []struct {
		name     string
		resource executionbudget.Resource
		limits   executionbudget.Limits
		event    copilot.SessionEventData
		starts   int
	}{
		{"model_turns at limit", executionbudget.ModelTurns, executionbudget.Limits{ModelTurns: &one}, &copilot.AssistantTurnStartData{}, 1},
		{"model_turns over limit", executionbudget.ModelTurns, executionbudget.Limits{ModelTurns: &one}, &copilot.AssistantTurnStartData{}, 2},
		{"tool_calls at limit", executionbudget.ToolCalls, executionbudget.Limits{ToolCalls: &one}, &copilot.ToolExecutionStartData{}, 1},
		{"tool_calls over limit", executionbudget.ToolCalls, executionbudget.Limits{ToolCalls: &one}, &copilot.ToolExecutionStartData{}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			over := int64(tt.starts) > one
			plan, unsupported := executionbudget.Plan(executionbudget.Request{Limits: tt.limits}, executionbudget.HostCapabilities())
			require.Empty(t, unsupported)
			ctx, meter := executionbudget.Start(t.Context(), *plan, func(ctx context.Context, meter *executionbudget.Meter) context.Context {
				return WithActivityObserver(ctx, meter)
			})

			ctrl := gomock.NewController(t)
			client := newClientMock(ctrl)
			session := newSessionMock(ctrl)
			const sessionID = "budget-session"
			client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
			client.EXPECT().DeleteSession(gomock.Any(), sessionID)
			session.EXPECT().SessionID().Return(sessionID).AnyTimes()
			session.EXPECT().Disconnect()
			var handlers []func(copilot.SessionEvent)
			var sendErr error
			session.EXPECT().On(gomock.Any()).Times(3).DoAndReturn(func(handler func(copilot.SessionEvent)) func() {
				handlers = append(handlers, handler)
				return func() {}
			})
			session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
				func(sendCtx context.Context, _ copilot.MessageOptions) (*copilot.SessionEvent, error) {
					for range tt.starts {
						for _, handler := range handlers {
							handler(copilot.SessionEvent{Data: tt.event})
						}
					}
					sendErr = sendCtx.Err()
					return &copilot.SessionEvent{}, sendErr
				},
			)

			engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
				NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
			}).Build()
			require.NoError(t, engine.Initialize(t.Context()))
			t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })

			response, err := engine.Execute(ctx, &ExecutionRequest{Message: "budgeted", EphemeralSession: true})
			require.NoError(t, err)
			if over {
				require.ErrorIs(t, sendErr, context.Canceled, "over-limit start must cancel the running call")
			} else {
				require.NoError(t, sendErr)
			}
			require.Equal(t, !over, response.Success)

			report := meter.Finish()
			for _, row := range report.Resources {
				if row.Resource == tt.resource {
					require.NotNil(t, row.Used)
					require.Equal(t, int64(tt.starts), *row.Used)
				}
			}
			if over {
				require.Equal(t, executionbudget.StateTerminated, report.State)
				require.Equal(t, &executionbudget.Termination{Resource: tt.resource, Limit: 1, Observed: 2}, report.Termination)
			} else {
				require.Equal(t, executionbudget.StateWithinBudget, report.State)
				require.Nil(t, report.Termination)
			}
		})
	}
}
