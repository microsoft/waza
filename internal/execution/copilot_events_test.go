package execution

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestCopilotExecuteSnapshotsActiveTerminalEvent(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			name := "initial"
			if resumed {
				name = "resumed"
			}
			if failed {
				name += "/provider_failure"
			} else {
				name += "/idle"
			}
			t.Run(name, func(t *testing.T) {
				ctrl := gomock.NewController(t)
				client := newClientMock(ctrl)
				session := newSessionMock(ctrl)
				const sessionID = "terminal-event-session"
				if resumed {
					client.EXPECT().ResumeSessionWithOptions(gomock.Any(), sessionID, gomock.Any()).Return(session, nil)
				} else {
					client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
				}
				client.EXPECT().DeleteSession(gomock.Any(), sessionID)
				session.EXPECT().SessionID().Return(sessionID).AnyTimes()
				session.EXPECT().Disconnect()
				var handlers []func(copilot.SessionEvent)
				session.EXPECT().On(gomock.Any()).Times(3).DoAndReturn(func(handler func(copilot.SessionEvent)) func() {
					handlers = append(handlers, handler)
					return func() {}
				})

				engine := NewCopilotEngineBuilder("actor-model", &CopilotEngineBuilderOptions{
					NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
				}).Build()
				require.NoError(t, engine.Initialize(context.Background()))
				t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })

				entered := make(chan struct{})
				release := make(chan struct{})
				callbackDone := make(chan struct{})
				executeDone := make(chan struct{})
				var releaseOnce sync.Once
				t.Cleanup(func() {
					releaseOnce.Do(func() { close(release) })
					for _, done := range []<-chan struct{}{callbackDone, executeDone} {
						select {
						case <-done:
						case <-time.After(5 * time.Second):
							t.Error("terminal-event test goroutine did not finish")
						}
					}
				})
				var data copilot.SessionEventData = &copilot.SessionIdleData{}
				var sendErr error
				if failed {
					sendErr = errors.New("synthetic provider failure")
					data = &copilot.SessionErrorData{Message: sendErr.Error()}
				}
				event := copilot.SessionEvent{
					ID: "terminal-event",
					Data: &blockedSessionEventData{
						SessionEventData: data,
						entered:          entered,
						release:          release,
					},
				}
				session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
					func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
						go func() {
							defer close(callbackDone)
							handlers[0](event)
						}()
						<-entered
						return nil, sendErr
					},
				)

				req := &ExecutionRequest{
					Message: "classify", NoSkills: true, SkipWorkspaceCapture: true,
					SourceDir: t.TempDir(),
				}
				if resumed {
					req.SessionID = sessionID
				}
				var resp *ExecutionResponse
				var executeErr error
				go func() {
					defer close(executeDone)
					resp, executeErr = engine.Execute(context.Background(), req)
				}()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("collector did not enter the terminal callback")
				}
				select {
				case <-executeDone:
					t.Error("response finalized while the terminal callback was still collecting its event")
				case <-time.After(100 * time.Millisecond):
				}
				releaseOnce.Do(func() { close(release) })
				select {
				case <-executeDone:
				case <-time.After(5 * time.Second):
					t.Fatal("execution did not finish after releasing the terminal callback")
				}
				require.NoError(t, executeErr)
				require.NotNil(t, resp)
				require.Equal(t, !failed, resp.Success)
				if failed {
					require.Equal(t, sendErr.Error(), resp.ErrorMsg)
				} else {
					require.Empty(t, resp.ErrorMsg)
				}
				require.Equal(t, sessionID, resp.SessionID)
				require.Len(t, resp.Events, 1)
				collected, ok := copilotevents.AsSDKEvent(resp.Events[0])
				require.True(t, ok)
				require.Equal(t, event.ID, collected.ID)
			})
		}
	}
}

// Pause inside the collector, not before callback admission or after collection.
type blockedSessionEventData struct {
	copilot.SessionEventData
	once    sync.Once
	entered chan struct{}
	release <-chan struct{}
}

func (data *blockedSessionEventData) Type() copilot.SessionEventType {
	data.once.Do(func() {
		close(data.entered)
		<-data.release
	})
	return data.SessionEventData.Type()
}
