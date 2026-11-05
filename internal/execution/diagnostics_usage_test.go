package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestSessionEventModelObservationIndependentOfUsage(t *testing.T) {
	c := NewSessionUsageCollector()
	e := &CopilotEngine{usageCollectors: map[string]*SessionUsageCollector{"session": c}}
	require.Equal(t, SessionEventModelObservation{}, e.SessionEventModelObservation("unavailable"))
	require.Equal(t, SessionEventModelObservation{}, e.SessionEventModelObservation(""))
	require.Equal(t, SessionEventModelObservation{}, e.SessionEventModelObservation("session"))
	c.On(copilot.SessionEvent{})
	c.On(copilot.SessionEvent{Data: &copilot.SessionIdleData{}})
	require.Equal(t, SessionEventModelObservation{}, e.SessionEventModelObservation("session"))

	c.SetMetrics(completeDiagnosticMetrics())
	require.Equal(t, SessionEventModelObservation{}, e.SessionEventModelObservation("session"))
	c.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{Model: "event-conflict"}})
	require.Equal(t, []string{"observed-model"}, e.SessionUsageObservation("session").Models)
	require.Equal(t, "rpc", e.SessionUsageObservation("session").Source)
	require.True(t, e.SessionUsageObservation("session").Complete)
	require.Equal(t, SessionEventModelObservation{
		Models: []string{"event-conflict"}, EventsObserved: 1, Complete: true,
	}, e.SessionEventModelObservation("session"))
	require.Equal(t, 7, e.SessionUsage("session").InputTokens)
	require.Equal(t, 0.0, *e.SessionUsage("session").AICredits)

	c.On(copilot.SessionEvent{Data: &copilot.SessionShutdownData{
		ModelMetrics: map[string]copilot.ShutdownModelMetric{"shutdown-model": {}},
	}})
	c.SetMetrics(completeDiagnosticMetrics())
	before := e.SessionEventModelObservation("session")
	require.Equal(t, SessionEventModelObservation{
		Models: []string{"event-conflict", "shutdown-model"}, EventsObserved: 2, Complete: true,
	}, before)
	require.Equal(t, []string{"observed-model"}, e.SessionUsageObservation("session").Models)
	before.Models[0] = "mutated"
	require.NotContains(t, e.SessionEventModelObservation("session").Models, "mutated")
	c.beginTurn()
	require.Equal(t, SessionEventModelObservation{
		Models: []string{"event-conflict", "shutdown-model"}, EventsObserved: 2, Complete: true,
	}, e.SessionEventModelObservation("session"))
	require.Equal(t, SessionUsageObservation{}, e.SessionUsageObservation("session"))
	c.On(copilot.SessionEvent{Data: &copilot.SessionShutdownData{
		ModelMetrics: map[string]copilot.ShutdownModelMetric{"shutdown-model": {}},
	}})
	require.Equal(t, "shutdown", e.SessionUsageObservation("session").Source)
	require.Equal(t, []string{"shutdown-model"}, e.SessionUsageObservation("session").Models)
	require.Equal(t, SessionEventModelObservation{
		Models: []string{"event-conflict", "shutdown-model"}, EventsObserved: 3, Complete: true,
	}, e.SessionEventModelObservation("session"))
}

func TestSessionEventModelOnlyAndStickyIncomplete(t *testing.T) {
	c := NewSessionUsageCollector()
	c.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{Model: "z-model"}})
	require.Nil(t, c.UsageStats())
	require.Equal(t, SessionUsageObservation{}, c.Observation())
	c.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{Model: "a-model"}})
	c.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{Model: "z-model"}})
	require.Equal(t, SessionEventModelObservation{
		Models: []string{"a-model", "z-model"}, EventsObserved: 3, Complete: true,
	}, c.EventModelObservation())
	c.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{Model: ""}})
	c.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{Model: "z-model"}})
	c.SetMetrics(completeDiagnosticMetrics())
	c.beginTurn()
	require.Equal(t, SessionEventModelObservation{
		Models: []string{"a-model", "z-model"}, EventsObserved: 5, Complete: false,
	}, c.EventModelObservation())
}

func TestSessionEventModelObservationSupportedPayloads(t *testing.T) {
	value := func(model *string) string {
		if model == nil {
			return ""
		}
		return *model
	}
	for _, tc := range []struct {
		name string
		make func(*string) copilot.SessionEvent
	}{
		{"usage", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.AssistantUsageData{Model: value(m)}}
		}},
		{"message", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.AssistantMessageData{Model: m}}
		}},
		{"turn start", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{Model: m}}
		}},
		{"turn end", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.AssistantTurnEndData{Model: m}}
		}},
		{"turn retry", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.AssistantTurnRetryData{Model: m}}
		}},
		{"call start", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.ModelCallStartData{Model: m}}
		}},
		{"call failure", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.ModelCallFailureData{Model: m}}
		}},
		{"session start", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SessionStartData{SelectedModel: m}}
		}},
		{"session resume", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SessionResumeData{SelectedModel: m}}
		}},
		{"model change", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SessionModelChangeData{NewModel: value(m)}}
		}},
		{"model deselected", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SessionModelDeselectedData{PreviousModel: value(m)}}
		}},
		{"shutdown current", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SessionShutdownData{CurrentModel: m}}
		}},
		{"shutdown metrics", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SessionShutdownData{ModelMetrics: map[string]copilot.ShutdownModelMetric{value(m): {}}}}
		}},
		{"compaction", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SessionCompactionStartData{Model: m}}
		}},
		{"compaction completed", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SessionCompactionCompleteData{
				CompactionTokensUsed: &copilot.CompactionCompleteCompactionTokensUsed{Model: m},
			}}
		}},
		{"fusion start", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.AssistantFusionPhaseStartedData{Model: value(m)}}
		}},
		{"fusion completed", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.AssistantFusionPhaseCompletedData{Model: value(m)}}
		}},
		{"fusion failed", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.AssistantFusionPhaseFailedData{Model: value(m)}}
		}},
		{"subagent configured", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SubagentConfiguredData{Model: value(m)}}
		}},
		{"subagent start", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SubagentStartedData{Model: m}}
		}},
		{"subagent completed", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SubagentCompletedData{Model: m}}
		}},
		{"subagent failed", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SubagentFailedData{Model: m}}
		}},
		{"interrupted", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.AgentInterruptedData{Model: m}}
		}},
		{"tools updated", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SessionToolsUpdatedData{Model: value(m)}}
		}},
		{"tool start", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{Model: m}}
		}},
		{"tool completed", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{Model: m}}
		}},
		{"skill", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.SkillInvokedData{Model: m}}
		}},
		{"exit plan", func(m *string) copilot.SessionEvent {
			return copilot.SessionEvent{Data: &copilot.ExitPlanModeRequestedData{Model: m}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewSessionUsageCollector()
			c.On(tc.make(utils.Ptr("sdk-model")))
			require.Equal(t, SessionEventModelObservation{Models: []string{"sdk-model"}, EventsObserved: 1, Complete: true}, c.EventModelObservation())
			for _, missing := range []*string{nil, utils.Ptr(""), utils.Ptr("  "), utils.Ptr("unknown"), utils.Ptr("AUTO")} {
				c := NewSessionUsageCollector()
				c.On(tc.make(missing))
				require.Equal(t, SessionEventModelObservation{EventsObserved: 1}, c.EventModelObservation())
			}
			event := tc.make(nil)
			data, ok := reflect.Zero(reflect.TypeOf(event.Data)).Interface().(copilot.SessionEventData)
			require.True(t, ok)
			event.Data = data
			c = NewSessionUsageCollector()
			c.On(event)
			require.Equal(t, SessionEventModelObservation{EventsObserved: 1}, c.EventModelObservation())
		})
	}
	c := NewSessionUsageCollector()
	c.On(copilot.SessionEvent{Data: &copilot.SessionModelChangeData{NewModel: "new", PreviousModel: utils.Ptr("old")}})
	require.Equal(t, []string{"new", "old"}, c.EventModelObservation().Models)
}

func TestSessionEventModelObservationNestedBillingEvidence(t *testing.T) {
	c := NewSessionUsageCollector()
	c.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{
		Model: "call-model",
		CopilotUsage: &copilot.AssistantUsageCopilotUsage{
			Model: utils.Ptr("billing-model"),
			TokenDetails: []copilot.AssistantUsageCopilotUsageTokenDetail{
				{Model: utils.Ptr("detail-model")}, {},
			},
		},
	}})
	require.Equal(t, []string{"billing-model", "call-model", "detail-model"}, c.EventModelObservation().Models)
	require.True(t, c.EventModelObservation().Complete)
	require.Nil(t, c.UsageStats())
	c.On(copilot.SessionEvent{Data: &copilot.SessionCompactionCompleteData{
		CompactionTokensUsed: &copilot.CompactionCompleteCompactionTokensUsed{
			Model: utils.Ptr("compaction-model"),
			CopilotUsage: &rpc.CompactionCompleteCompactionTokensUsedCopilotUsage{
				Model: utils.Ptr("compaction-billing-model"),
				TokenDetails: []rpc.CompactionCompleteCompactionTokensUsedCopilotUsageTokenDetail{
					{Model: utils.Ptr("compaction-detail-model")}, {},
				},
			},
		},
	}})
	c.On(copilot.SessionEvent{Data: &copilot.SessionShutdownData{
		CurrentModel: utils.Ptr("call-model"),
		ModelMetrics: map[string]copilot.ShutdownModelMetric{"call-model": {}},
		AgentMetrics: map[string]copilot.ShutdownAgentMetric{"child": {
			ModelMetrics: map[string]copilot.ShutdownModelMetric{"agent-model": {}},
		}},
	}})
	require.Equal(t, SessionEventModelObservation{
		Models: []string{"agent-model", "billing-model", "call-model", "compaction-billing-model",
			"compaction-detail-model", "compaction-model", "detail-model"},
		EventsObserved: 3, Complete: true,
	}, c.EventModelObservation())
	c.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{
		Model: "call-model", CopilotUsage: &copilot.AssistantUsageCopilotUsage{
			TokenDetails: []copilot.AssistantUsageCopilotUsageTokenDetail{{Model: utils.Ptr("")}},
		},
	}})
	require.False(t, c.EventModelObservation().Complete)
	for _, event := range []copilot.SessionEvent{
		{Data: &copilot.SessionCompactionCompleteData{}},
		{Data: &copilot.SessionModelChangeData{NewModel: "call-model", PreviousModel: utils.Ptr("")}},
	} {
		c := NewSessionUsageCollector()
		c.On(event)
		require.False(t, c.EventModelObservation().Complete)
		require.EqualValues(t, 1, c.EventModelObservation().EventsObserved)
	}
}

func TestSessionEventModelObservationConcurrentIngestAndGetter(t *testing.T) {
	c := NewSessionUsageCollector()
	e := &CopilotEngine{usageCollectors: map[string]*SessionUsageCollector{"session": c}}
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Go(func() {
			for range 50 {
				if worker%2 == 0 {
					c.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{Model: "received"}})
				} else {
					c.SetMetrics(completeDiagnosticMetrics())
					c.beginTurn()
				}
				observation := e.SessionEventModelObservation("session")
				if len(observation.Models) > 0 {
					observation.Models[0] = "mutated"
				}
			}
		})
	}
	workers.Wait()
	require.Equal(t, SessionEventModelObservation{Models: []string{"received"}, EventsObserved: 200, Complete: true}, e.SessionEventModelObservation("session"))
}

func TestDiagnosticUsagePresenceBeforeFlattening(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		metrics              *rpc.UsageGetMetricsResult
		complete, attributed bool
	}{
		{"explicit zero", completeDiagnosticMetrics(), true, true},
		{"missing total", &rpc.UsageGetMetricsResult{ModelMetrics: completeDiagnosticMetrics().ModelMetrics}, false, true},
		{"missing model credits", &rpc.UsageGetMetricsResult{TotalNanoAiu: utils.Ptr(0.0), ModelMetrics: map[string]rpc.UsageMetricsModelMetric{"actual": {Usage: rpc.UsageMetricsModelMetricUsage{InputTokens: 7}}}}, false, true},
		{"unknown model", &rpc.UsageGetMetricsResult{TotalNanoAiu: utils.Ptr(0.0), ModelMetrics: map[string]rpc.UsageMetricsModelMetric{"unknown": {TotalNanoAiu: utils.Ptr(0.0)}}}, false, false},
		{"partial attribution", &rpc.UsageGetMetricsResult{TotalNanoAiu: utils.Ptr(0.0), ModelMetrics: map[string]rpc.UsageMetricsModelMetric{"actual": {TotalNanoAiu: utils.Ptr(0.0)}, "": {TotalNanoAiu: utils.Ptr(0.0)}}}, false, false},
		{"no models", &rpc.UsageGetMetricsResult{TotalNanoAiu: utils.Ptr(0.0)}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewSessionUsageCollector()
			c.SetMetrics(tc.metrics)
			observation := c.Observation()
			require.Equal(t, tc.complete, observation.Complete)
			require.Equal(t, tc.attributed, observation.ModelAttributionComplete)
			require.NotNil(t, c.UsageStats())
			if len(observation.Models) > 0 {
				observation.Models[0] = "mutated"
				require.NotContains(t, c.Observation().Models, "mutated")
			}
			c.beginTurn()
			require.Equal(t, SessionUsageObservation{}, c.Observation())
		})
	}
	for _, countPresent := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing request count", true: "complete shutdown"}[countPresent], func(t *testing.T) {
			c := NewSessionUsageCollector()
			var count *int64
			if countPresent {
				count = utils.Ptr(int64(0))
			}
			c.On(copilot.SessionEvent{Data: &copilot.SessionShutdownData{
				TotalNanoAiu: utils.Ptr(0.0), TotalPremiumRequests: utils.Ptr(0.0),
				ModelMetrics: map[string]copilot.ShutdownModelMetric{"actual": {
					TotalNanoAiu: utils.Ptr(0.0),
					Requests:     copilot.ShutdownModelMetricRequests{Count: count, Cost: utils.Ptr(0.0)},
					Usage:        copilot.ShutdownModelMetricUsage{InputTokens: 3},
				}},
			}})
			require.Equal(t, countPresent, c.Observation().Complete)
			require.Equal(t, []string{"actual"}, c.Observation().Models)
			require.Equal(t, 3, c.UsageStats().InputTokens)
			require.Equal(t, 0.0, *c.UsageStats().AICredits)
		})
	}
	c := NewSessionUsageCollector()
	c.SetMetrics(nil)
	require.Nil(t, c.UsageStats())
	c.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{Model: "actual-event-model", InputTokens: utils.Ptr(int64(9))}})
	require.False(t, c.Observation().Complete)
	require.Equal(t, []string{"actual-event-model"}, c.Observation().Models)
	require.Nil(t, c.UsageStats().AICredits)
}

func TestDiagnosticNativeSDKFallbackReportsAllFailuresAndRetainsPartialUsage(t *testing.T) {
	for _, tc := range []struct {
		method string
		code   DiagnosticCode
		data   bool
	}{
		{"session.resume", CodeResume, false},
		{"session.shutdown", CodeShutdownRPC, false},
		{"session.getMessages", CodeHistory, false},
		{"session.detach", CodeFallbackDisconnect, true},
		{"", CodeUsageMissingOrPartial, false},
	} {
		t.Run(tc.method, func(t *testing.T) {
			var log bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })
			var events []copilot.SessionEvent
			if tc.method != "" {
				events = []copilot.SessionEvent{{Data: &copilot.SessionShutdownData{TotalNanoAiu: utils.Ptr(0.0)}}}
			}
			session, _ := usageRPCSession(t, tc.method, events)
			err := session.Disconnect()
			if tc.method == "session.detach" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			var seen []ExecutionDiagnostic
			e, _ := diagnosticTestEngine(t, func(d ExecutionDiagnostic) error {
				seen = append(seen, d)
				return errors.New(privateDiagnosticSentinel)
			})
			op := &diagnosticOperation{state: e.diagnostics}
			collector := NewSessionUsageCollector()
			ctx := context.WithValue(t.Context(), diagnosticOperationKey{}, op)
			e.captureShutdownUsage(ctx, "private-id", session, collector)
			require.ErrorContains(t, op.err(), string(tc.code))
			require.ErrorContains(t, op.err(), "observer_failed")
			require.NotContains(t, op.err().Error(), "test RPC failure")
			require.NotContains(t, log.String(), "test RPC failure")
			require.NotContains(t, op.err().Error(), privateDiagnosticSentinel)
			require.NotContains(t, log.String(), privateDiagnosticSentinel)
			require.NotEmpty(t, seen)
			if tc.data {
				require.NotNil(t, collector.UsageStats())
				require.Equal(t, 0.0, *collector.UsageStats().AICredits)
			} else {
				require.Nil(t, collector.UsageStats())
			}
		})
	}
}

func TestDiagnosticConcurrentNewSessionsDoNotShareFailures(t *testing.T) {
	var mu sync.Mutex
	var seen []ExecutionDiagnostic
	e, client := diagnosticTestEngine(t, func(d ExecutionDiagnostic) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, d)
		return nil
	})
	requests := []*ExecutionRequest{diagnosticRequest(t), diagnosticRequest(t)}
	sessions := []*MockCopilotSession{NewMockCopilotSession(gomock.NewController(t)), NewMockCopilotSession(gomock.NewController(t))}
	bothSending := make(chan struct{}, 2)
	release := make(chan struct{})
	var originHandler copilot.SessionEventHandler
	for i, session := range sessions {
		id := []string{"failed-id", "successful-id"}[i]
		client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, config *copilot.SessionConfig) (CopilotSession, error) {
			if config.WorkingDirectory == requests[0].WorkspaceDir {
				return sessions[0], nil
			}
			return sessions[1], nil
		})
		session.EXPECT().SessionID().Return(id)
		if i == 0 {
			session.EXPECT().On(gomock.Any()).Times(2).DoAndReturn(func(handler copilot.SessionEventHandler) func() {
				if originHandler == nil {
					originHandler = handler
				}
				return func() {}
			})
		} else {
			session.EXPECT().On(gomock.Any()).Return(func() {}).AnyTimes()
		}
		session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
			bothSending <- struct{}{}
			<-release
			return nil, nil
		})
		session.EXPECT().UsageMetrics(gomock.Any()).Return(completeDiagnosticMetrics(), nil)
		session.EXPECT().Disconnect().Return(nil)
		client.EXPECT().DeleteSession(gomock.Any(), id).Return(nil)
	}
	result := []chan error{make(chan error, 1), make(chan error, 1)}
	for i, req := range requests {
		go func() { _, err := e.Execute(context.Background(), req); result[i] <- err }()
	}
	<-bothSending
	<-bothSending
	// Both invocations are active when the first session asynchronously fails.
	eventDone := make(chan struct{})
	go func() {
		originHandler(copilot.SessionEvent{Data: &copilot.SessionErrorData{Message: privateDiagnosticSentinel}})
		close(eventDone)
	}()
	<-eventDone
	close(release)
	require.ErrorContains(t, <-result[0], "send_failed")
	require.NoError(t, <-result[1])
	require.Equal(t, []string{"observed-model"}, e.SessionUsageObservation("successful-id").Models)
	mu.Lock()
	require.Equal(t, []ExecutionDiagnostic{{Stage: StageExecute, Code: CodeSend, SessionID: "failed-id"}}, seen)
	mu.Unlock()
	client.EXPECT().Stop().Return(nil)
	require.NoError(t, e.Shutdown(t.Context()))
}

func TestDiagnosticRetainsSendFailureAfterToolCallback(t *testing.T) {
	e, client := diagnosticTestEngine(t, func(ExecutionDiagnostic) error { return nil })
	req := diagnosticRequest(t)
	called := false
	req.Tools = []copilot.Tool{{Name: "grade", Handler: func(copilot.ToolInvocation) (copilot.ToolResult, error) {
		called = true
		return copilot.ToolResult{}, nil
	}}}
	session := NewMockCopilotSession(gomock.NewController(t))
	var tools []copilot.Tool
	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, config *copilot.SessionConfig) (CopilotSession, error) {
		tools = config.Tools
		return session, nil
	})
	session.EXPECT().SessionID().Return("judge-id")
	session.EXPECT().On(gomock.Any()).Return(func() {}).AnyTimes()
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
		_, err := tools[0].Handler(copilot.ToolInvocation{})
		require.NoError(t, err)
		return nil, errors.New(privateDiagnosticSentinel)
	})
	session.EXPECT().UsageMetrics(gomock.Any()).Return(completeDiagnosticMetrics(), nil)
	session.EXPECT().Disconnect().Return(nil)
	client.EXPECT().DeleteSession(gomock.Any(), "judge-id").Return(nil)
	resp, err := e.Execute(t.Context(), req)
	require.True(t, called)
	require.ErrorContains(t, err, "send_failed")
	require.NotContains(t, resp.ErrorMsg, privateDiagnosticSentinel)
	require.NotNil(t, resp.Usage)
	client.EXPECT().Stop().Return(nil)
	require.NoError(t, e.Shutdown(t.Context()))
}

func TestDiagnosticNativeEventsAndExpectedSkillCancellation(t *testing.T) {
	for _, kind := range []string{"session error", "malformed skill", "expected cancellation", "cancellation observer failure"} {
		t.Run(kind, func(t *testing.T) {
			var seen []ExecutionDiagnostic
			e, client := diagnosticTestEngine(t, func(d ExecutionDiagnostic) error {
				seen = append(seen, d)
				if kind == "cancellation observer failure" {
					return errors.New(privateDiagnosticSentinel)
				}
				return nil
			})
			req := diagnosticRequest(t)
			req.CancelOnSkillInvocation = true
			session := NewMockCopilotSession(gomock.NewController(t))
			client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
			session.EXPECT().SessionID().Return("event-id")
			var handlers []copilot.SessionEventHandler
			session.EXPECT().On(gomock.Any()).Times(2).DoAndReturn(func(handler copilot.SessionEventHandler) func() {
				handlers = append(handlers, handler)
				return func() {}
			})
			session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ copilot.MessageOptions) (*copilot.SessionEvent, error) {
				var event copilot.SessionEvent
				switch kind {
				case "session error":
					event.Data = &copilot.SessionErrorData{Message: privateDiagnosticSentinel}
				case "malformed skill":
					event.Data = &copilot.SkillInvokedData{}
				default:
					event.Data = &copilot.SkillInvokedData{Name: "skill", Path: "SKILL.md"}
				}
				for _, handler := range handlers {
					handler(event)
				}
				return nil, ctx.Err()
			})
			session.EXPECT().UsageMetrics(gomock.Any()).Return(completeDiagnosticMetrics(), nil)
			session.EXPECT().Disconnect().Return(nil)
			client.EXPECT().DeleteSession(gomock.Any(), "event-id").Return(nil)
			resp, err := e.Execute(t.Context(), req)
			if kind == "expected cancellation" {
				require.NoError(t, err)
				require.True(t, resp.Success)
				require.Len(t, resp.SkillInvocations, 1)
			} else {
				require.Error(t, err)
				require.False(t, resp.Success)
			}
			if kind == "cancellation observer failure" {
				require.ErrorContains(t, err, "observer_failed")
				require.NotContains(t, err.Error(), "send_failed")
			}
			if kind == "session error" {
				data, err := json.Marshal(resp.Events)
				require.NoError(t, err)
				require.NotContains(t, string(data), privateDiagnosticSentinel)
			}
			require.Len(t, seen, 1)
			client.EXPECT().Stop().Return(nil)
			require.NoError(t, e.Shutdown(t.Context()))
		})
	}
}

func TestDiagnosticDrainsInFlightEventObserverBeforeReturning(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	e, client := diagnosticTestEngine(t, func(ExecutionDiagnostic) error {
		close(entered)
		<-release
		return errors.New(privateDiagnosticSentinel)
	})
	session := NewMockCopilotSession(gomock.NewController(t))
	client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
	session.EXPECT().SessionID().Return("observer-id")
	var handlers []copilot.SessionEventHandler
	session.EXPECT().On(gomock.Any()).Times(2).DoAndReturn(func(handler copilot.SessionEventHandler) func() {
		handlers = append(handlers, handler)
		return func() {}
	})
	callbackDone := make(chan struct{})
	session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, copilot.MessageOptions) (*copilot.SessionEvent, error) {
		go func() {
			defer close(callbackDone)
			handlers[0](copilot.SessionEvent{Data: &copilot.SessionErrorData{Message: privateDiagnosticSentinel}})
		}()
		<-entered
		return nil, nil
	})
	session.EXPECT().UsageMetrics(gomock.Any()).Return(completeDiagnosticMetrics(), nil)
	session.EXPECT().Disconnect().Return(nil)
	client.EXPECT().DeleteSession(gomock.Any(), "observer-id").Return(nil)
	done := make(chan error, 1)
	req := diagnosticRequest(t)
	go func() { _, err := e.Execute(context.Background(), req); done <- err }()
	<-entered
	select {
	case <-done:
		t.Fatal("execution returned before observer completed")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	err := <-done
	require.ErrorContains(t, err, "send_failed")
	require.ErrorContains(t, err, "observer_failed")
	require.NotContains(t, err.Error(), privateDiagnosticSentinel)
	<-callbackDone
	client.EXPECT().Stop().Return(nil)
	require.NoError(t, e.Shutdown(t.Context()))
}
