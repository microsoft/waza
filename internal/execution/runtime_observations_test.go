package execution

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func runtimeOperations(snapshot RuntimeObservationSnapshot) []string {
	result := make([]string, len(snapshot.Observations))
	for i, observation := range snapshot.Observations {
		result[i] = observation.Operation
	}
	return result
}

func TestOwnedRuntimeObservationsNilLegacyPath(t *testing.T) {
	var capture *OwnedRuntimeObservations
	require.NotPanics(t, func() {
		capture.observeEvent(copilot.SessionEvent{Data: &copilot.AbortData{}})
		capture.observeContext(context.Background())
	})
}

func TestOwnedRuntimeObservationsLegacyExecuteAcceptsRootAbort(t *testing.T) {
	ctrl := gomock.NewController(t)
	client := newClientMock(ctrl)
	session := newSessionMock(ctrl)
	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	client.EXPECT().DeleteSession(gomock.Any(), "legacy-abort-session").Return(nil)
	session.EXPECT().SessionID().Return("legacy-abort-session")
	session.EXPECT().Disconnect().Return(nil)
	var handlers []func(copilot.SessionEvent)
	session.EXPECT().On(gomock.Any()).Times(3).DoAndReturn(func(handler func(copilot.SessionEvent)) func() {
		handlers = append(handlers, handler)
		return func() {}
	})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			for _, handler := range handlers {
				handler(copilot.SessionEvent{ID: "legacy-root-abort", Data: &copilot.AbortData{}})
			}
			return nil, nil
		})
	engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
	}).Build()
	require.Nil(t, engine.runtimeObservations)
	require.NoError(t, engine.Initialize(t.Context()))
	response, err := engine.Execute(t.Context(), &ExecutionRequest{
		Message: "legacy root abort control", EphemeralSession: true, NoSkills: true,
		SourceDir: t.TempDir(), SkipWorkspaceCapture: true,
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.True(t, response.Success)
	require.Len(t, response.Events, 1, "the real legacy collector must retain the root abort event")
	require.NoError(t, engine.Shutdown(t.Context()))
}

func TestOwnedRuntimeObservationsActualClientCalls(t *testing.T) {
	for _, failing := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failing], func(t *testing.T) {
			capture := NewOwnedRuntimeObservations()
			client := NewMockCopilotClient(gomock.NewController(t))
			var controlled error
			if failing {
				controlled = errors.New("private runtime failure must not enter metadata")
			}
			client.EXPECT().Start(gomock.Any()).Return(nil)
			client.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{IsAuthenticated: true}, nil)
			client.EXPECT().Stop().Return(controlled)
			engine := NewCopilotEngineBuilder("test-model", &CopilotEngineBuilderOptions{
				NewCopilotClient:    func(*copilot.ClientOptions) CopilotClient { return client },
				RuntimeObservations: capture,
			}).Build()
			require.Nil(t, engine.diagnostics)
			require.NoError(t, engine.Initialize(t.Context()))
			err := engine.Shutdown(t.Context())
			if failing {
				require.ErrorIs(t, err, controlled)
			} else {
				require.NoError(t, err)
			}
			snapshot, err := capture.Seal()
			require.NoError(t, err)
			require.True(t, snapshot.OwnedEngineBound)
			require.Equal(t, []string{"client-start-invoked", "client-start-returned", "client-stop-invoked", "client-stop-returned"}, runtimeOperations(snapshot))
			require.Equal(t, runtimeCallResult(controlled), snapshot.Observations[3].Result)
			require.Equal(t, "unknown", snapshot.ProcessExit, "Stop return is not exposed Cmd.Wait evidence")
			for i, observation := range snapshot.Observations {
				require.Equal(t, i+1, observation.Sequence)
				require.Equal(t, "sdk-client", observation.Scope)
				at, err := time.Parse(time.RFC3339Nano, observation.ObservedAt)
				require.NoError(t, err)
				require.Equal(t, at.UTC().Format(time.RFC3339Nano), observation.ObservedAt)
			}
		})
	}
}

func TestOwnedRuntimeObservationsForceRequestRequiresOwnedSupportedClient(t *testing.T) {
	for _, owned := range []bool{false, true} {
		for _, supported := range []bool{false, true} {
			capture := NewOwnedRuntimeObservations()
			clientMock := NewMockCopilotClient(gomock.NewController(t))
			var client CopilotClient = clientMock
			calls := 0
			if supported {
				client = &forceStopTestClient{
					MockCopilotClient: clientMock,
					forceStop:         func() { calls++ },
				}
			}
			engine := &CopilotEngine{ownsClient: owned, runtimeObservations: capture}
			engine.forceStopClient(client)
			snapshot, err := capture.Seal()
			require.NoError(t, err)
			if owned && supported {
				require.Equal(t, 1, calls)
				require.Len(t, snapshot.Observations, 1)
				require.Equal(t, "force-stop-requested", snapshot.Observations[0].Operation)
				require.Equal(t, "sdk-client-control", snapshot.Observations[0].Scope)
				require.Equal(t, "requested", snapshot.Observations[0].Result)
			} else {
				require.Zero(t, calls)
				require.Empty(t, snapshot.Observations)
			}
			require.Equal(t, "unknown", snapshot.ProcessExit)
		}
	}
}

func TestOwnedRuntimeObservationsRejectSharedReusedAndSealedSinks(t *testing.T) {
	capture := NewOwnedRuntimeObservations()
	rejected := NewCopilotEngineBuilder("", &CopilotEngineBuilderOptions{RuntimeObservations: capture}).Build()
	require.ErrorIs(t, rejected.Initialize(t.Context()), ErrRuntimeObservation)
	require.False(t, capture.bound.Load(), "shared-client rejection must precede binding or shared factory access")
	factoryCalls := 0
	factory := func(*copilot.ClientOptions) CopilotClient {
		factoryCalls++
		return NewMockCopilotClient(gomock.NewController(t))
	}
	NewCopilotEngineBuilder("", &CopilotEngineBuilderOptions{RuntimeObservations: capture, NewCopilotClient: factory})
	rejected = NewCopilotEngineBuilder("", &CopilotEngineBuilderOptions{RuntimeObservations: capture, NewCopilotClient: factory}).Build()
	require.ErrorIs(t, rejected.Initialize(t.Context()), ErrRuntimeObservation)
	require.Equal(t, 1, factoryCalls, "a second engine must not invoke its factory")
	sealed := NewOwnedRuntimeObservations()
	_, err := sealed.Seal()
	require.NoError(t, err)
	rejected = NewCopilotEngineBuilder("", &CopilotEngineBuilderOptions{RuntimeObservations: sealed, NewCopilotClient: factory}).Build()
	require.ErrorIs(t, rejected.Initialize(t.Context()), ErrRuntimeObservation)
	require.Equal(t, 1, factoryCalls)
}

func TestOwnedRuntimeObservationsBoundContentionAndLateIsolation(t *testing.T) {
	for _, contention := range []bool{false, true} {
		capture := NewOwnedRuntimeObservations()
		if contention {
			capture.mu.Lock()
			capture.record("force-stop-invoked", "sdk-client", "invoked")
			capture.mu.Unlock()
		} else {
			for range maxOwnedRuntimeObservations {
				capture.record("client-start-invoked", "sdk-client", "invoked")
			}
			capture.record("force-stop-invoked", "sdk-client", "invoked")
		}
		snapshot, err := capture.Seal()
		require.ErrorIs(t, err, ErrRuntimeObservation)
		require.EqualValues(t, 1, snapshot.Dropped)
		require.LessOrEqual(t, len(snapshot.Observations), maxOwnedRuntimeObservations)
		capture.record("force-stop-returned", "sdk-client", "returned")
		require.EqualValues(t, 1, capture.LateObservations())
		second, err := capture.Seal()
		require.ErrorIs(t, err, ErrRuntimeObservation)
		require.Equal(t, snapshot, second)
		fresh := NewOwnedRuntimeObservations()
		freshSnapshot, err := fresh.Seal()
		require.NoError(t, err)
		require.Empty(t, freshSnapshot.Observations)
		require.Zero(t, fresh.LateObservations())
	}
}

func TestOwnedRuntimeObservationsConcurrentSealAndDetachedCopies(t *testing.T) {
	capture := NewOwnedRuntimeObservations()
	capture.record("client-start-invoked", "sdk-client", "invoked")
	var joined sync.WaitGroup
	results := make(chan RuntimeObservationSnapshot, 16)
	for range 16 {
		joined.Go(func() {
			snapshot, err := capture.Seal()
			require.NoError(t, err)
			results <- snapshot
		})
	}
	joined.Wait()
	close(results)
	first := <-results
	for result := range results {
		require.Equal(t, first, result)
	}
	first.Observations[0].Operation = "caller-mutated"
	original, err := capture.Seal()
	require.NoError(t, err)
	require.Equal(t, []string{"client-start-invoked"}, runtimeOperations(original))
}

func TestOwnedRuntimeObservationsCompletedAppendIsNotPending(t *testing.T) {
	inspected := 0
	for i := range 1000 {
		capture := NewOwnedRuntimeObservations()
		start := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			<-start
			capture.record("force-stop-invoked", "sdk-client", "invoked")
			close(finished)
		}()
		close(start)
		if i%10 == 0 {
			<-finished
		}
		snapshot, err := capture.Seal()
		<-finished
		if len(snapshot.Observations) == 1 {
			inspected++
			require.NoError(t, err)
			require.Zero(t, snapshot.PendingAtSeal, "an appended observation is joined before exposing the capture lock")
			require.Zero(t, snapshot.Dropped)
		}
	}
	require.GreaterOrEqual(t, inspected, 100, "known completed controls must inspect actual captured observations")
}

func TestOwnedRuntimeObservationsTypedAbortIsNotStopOrDiagnosticAck(t *testing.T) {
	capture := NewOwnedRuntimeObservations()
	capture.observeEvent(copilot.SessionEvent{})
	capture.observeEvent(copilot.SessionEvent{Data: &copilot.SessionErrorData{Message: "expected_skill_cancellation"}})
	subagent := "other-agent"
	capture.observeEvent(copilot.SessionEvent{AgentID: &subagent, Data: &copilot.AbortData{}})
	capture.observeEvent(copilot.SessionEvent{Data: &copilot.AbortData{}})
	snapshot, err := capture.Seal()
	require.NoError(t, err)
	require.Equal(t, []string{"turn-abort-event-observed"}, runtimeOperations(snapshot))
	require.Equal(t, "sdk-turn", snapshot.Observations[0].Scope)
	require.Equal(t, "observed", snapshot.Observations[0].Result)
	require.Equal(t, "unknown", snapshot.ProcessExit)
	require.False(t, slices.Contains(runtimeOperations(snapshot), "process-wait-returned"))
}

func TestOwnedRuntimeObservationsUnsupportedForceIsNotInvented(t *testing.T) {
	client := NewMockCopilotClient(gomock.NewController(t))
	capture := NewOwnedRuntimeObservations()
	wrapped := observeRuntimeClient(client, capture)
	_, supported := wrapped.(interface{ ForceStop() })
	require.False(t, supported)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	capture.observeContext(ctx)
	snapshot, err := capture.Seal()
	require.NoError(t, err)
	require.Equal(t, []string{"cancellation-context-observed"}, runtimeOperations(snapshot))
	require.Equal(t, "host-context", snapshot.Observations[0].Scope)
	require.Equal(t, "canceled", snapshot.Observations[0].Result)
}
