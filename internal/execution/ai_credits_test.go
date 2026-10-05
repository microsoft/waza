package execution

import (
	"context"
	"errors"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestUsageMetricsAuthoritativeOverShutdown(t *testing.T) {
	collector := NewSessionUsageCollector()
	collector.On(copilot.SessionEvent{Data: &copilot.AssistantTurnStartData{}})
	collector.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{InputTokens: utils.Ptr(int64(999))}})
	collector.SetMetrics(&rpc.UsageGetMetricsResult{
		TotalNanoAiu:            utils.Ptr(1_123_456_789.0),
		TotalPremiumRequestCost: 2,
		ModelMetrics: map[string]rpc.UsageMetricsModelMetric{
			"reported": {
				TotalNanoAiu: utils.Ptr(1_123_456_789.0),
				Usage:        rpc.UsageMetricsModelMetricUsage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 3, CacheWriteTokens: 4},
				Requests:     rpc.UsageMetricsModelMetricRequests{Count: 5, Cost: 2},
			},
			"unavailable": {Usage: rpc.UsageMetricsModelMetricUsage{InputTokens: 1}},
		},
	})
	collector.On(copilot.SessionEvent{Data: &copilot.SessionShutdownData{TotalNanoAiu: utils.Ptr(99_000_000_000.0)}})
	usage := collector.UsageStats()
	require.Equal(t, 1.123456789, *usage.AICredits)
	require.Equal(t, 11, usage.InputTokens)
	require.Equal(t, 20, usage.OutputTokens)
	require.Equal(t, 3, usage.CacheReadTokens)
	require.Equal(t, 4, usage.CacheWriteTokens)
	require.Equal(t, 1, usage.Turns)
	require.Equal(t, 2.0, usage.PremiumRequests)
	require.Equal(t, 5.0, usage.ModelMetrics["reported"].RequestCount)
	require.Equal(t, 2.0, usage.ModelMetrics["reported"].RequestCost)
	require.Equal(t, 1.123456789, *usage.ModelMetrics["reported"].AICredits)
	require.Nil(t, usage.ModelMetrics["unavailable"].AICredits)
	require.True(t, collector.hasMetrics())

	collector.beginTurn()
	require.False(t, collector.hasMetrics())
	require.Nil(t, collector.UsageStats().AICredits, "a resumed turn must not reuse a stale final total")
	collector.SetMetrics(&rpc.UsageGetMetricsResult{TotalNanoAiu: utils.Ptr(0.0)})
	require.Equal(t, 0.0, *collector.UsageStats().AICredits)
	require.Zero(t, collector.UsageStats().InputTokens, "authoritative zero must not fall back to per-turn tokens")
	collector.SetMetrics(&rpc.UsageGetMetricsResult{})
	require.Nil(t, collector.UsageStats().AICredits, "a successful RPC without credits must remain unavailable")
}

func TestCaptureUsageFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name        string
		metrics     *rpc.UsageGetMetricsResult
		metricsErr  error
		shutdown    *copilot.SessionShutdownData
		shutdownErr error
		final       bool
		want        *float64
	}{
		{name: "RPC wins", metrics: &rpc.UsageGetMetricsResult{TotalNanoAiu: utils.Ptr(2e9)}, final: true, want: utils.Ptr(2.0)},
		{name: "RPC absent credits authoritative", metrics: &rpc.UsageGetMetricsResult{}, final: true},
		{name: "RPC error shutdown fallback", metricsErr: errors.New("method unavailable"), shutdown: &copilot.SessionShutdownData{TotalNanoAiu: utils.Ptr(3e9)}, final: true, want: utils.Ptr(3.0)},
		{name: "empty RPC shutdown fallback", shutdown: &copilot.SessionShutdownData{TotalNanoAiu: utils.Ptr(0.0)}, final: true, want: utils.Ptr(0.0)},
		{name: "shutdown error retains tokens", metricsErr: errors.New("RPC failed"), shutdownErr: errors.New("shutdown failed"), final: true},
		{name: "no shutdown event", metricsErr: errors.New("RPC failed"), final: true},
		{name: "nonfinal does not shutdown", metricsErr: errors.New("RPC failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := NewMockCopilotSession(gomock.NewController(t))
			session.EXPECT().UsageMetrics(gomock.Any()).DoAndReturn(func(ctx context.Context) (*rpc.UsageGetMetricsResult, error) {
				_, bounded := ctx.Deadline()
				require.True(t, bounded)
				require.NoError(t, ctx.Err())
				return tc.metrics, tc.metricsErr
			})
			if tc.final && tc.metrics == nil {
				session.EXPECT().ShutdownUsage(gomock.Any()).Return(tc.shutdown, tc.shutdownErr)
			}
			collector := NewSessionUsageCollector()
			collector.On(copilot.SessionEvent{Data: &copilot.AssistantUsageData{InputTokens: utils.Ptr(int64(8))}})
			(&CopilotEngine{}).captureUsage("s", session, collector, tc.final)
			require.Equal(t, tc.want, collector.UsageStats().AICredits)
			if tc.metrics == nil {
				require.Equal(t, 8, collector.UsageStats().InputTokens)
			}
		})
	}
}

func TestEphemeralUsageCapturedBeforeDeletionAndRetained(t *testing.T) {
	for _, rpcAvailable := range []bool{true, false} {
		t.Run(map[bool]string{true: "RPC", false: "shutdown"}[rpcAvailable], func(t *testing.T) {
			ctrl := gomock.NewController(t)
			client := newClientMock(ctrl)
			session := NewMockCopilotSession(ctrl)
			client.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(session, nil)
			session.EXPECT().SessionID().Return("grader")
			session.EXPECT().On(gomock.Any()).AnyTimes().Return(func() {})
			session.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(nil, errors.New("post-grade model error"))
			if rpcAvailable {
				session.EXPECT().UsageMetrics(gomock.Any()).Return(&rpc.UsageGetMetricsResult{TotalNanoAiu: utils.Ptr(2e9)}, nil)
			} else {
				session.EXPECT().UsageMetrics(gomock.Any()).Return(nil, errors.New("old runtime"))
				session.EXPECT().ShutdownUsage(gomock.Any()).Return(&copilot.SessionShutdownData{TotalNanoAiu: utils.Ptr(2e9)}, nil)
			}
			session.EXPECT().Disconnect()
			engine := NewCopilotEngineBuilder("judge", &CopilotEngineBuilderOptions{
				NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
			}).Build()
			client.EXPECT().DeleteSession(gomock.Any(), "grader").DoAndReturn(func(context.Context, string) error {
				require.Equal(t, 2.0, *engine.SessionUsage("grader").AICredits)
				return errors.New("delete failed")
			})
			require.NoError(t, engine.Initialize(t.Context()))
			resp, err := engine.Execute(t.Context(), &ExecutionRequest{Message: "grade", NoSkills: true, EphemeralSession: true})
			require.NoError(t, err)
			require.False(t, resp.Success)
			require.Contains(t, resp.ErrorMsg, "post-grade")
			require.Equal(t, 2.0, *resp.Usage.AICredits)
			require.Equal(t, 2.0, *engine.SessionUsage("grader").AICredits)
			require.NoError(t, engine.Shutdown(t.Context()))
		})
	}
}

func TestUpdateOutcomeUsageIncludesGraderSessionsOnce(t *testing.T) {
	credits := &models.UsageStats{InputTokens: 10, AICredits: utils.Ptr(1.0)}
	judge := &models.UsageStats{InputTokens: 20, AICredits: utils.Ptr(2.0)}
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{true: "missing grader", false: "complete"}[missing], func(t *testing.T) {
			outcome := &models.EvaluationOutcome{TestOutcomes: []models.TestOutcome{{Runs: []models.RunResult{{
				SessionDigest: models.SessionDigest{SessionID: "task"},
				GraderSessions: []models.SessionDigest{
					{SessionID: "task"}, {SessionID: "judge"}, {SessionID: "judge"},
				},
			}}}}}
			engine := &stubUsageEngine{usage: map[string]*models.UsageStats{"task": credits}}
			if !missing {
				engine.usage["judge"] = judge
			}

			UpdateOutcomeUsage(outcome, engine)
			require.NotNil(t, outcome.Digest.Usage)
			if missing {
				require.Nil(t, outcome.Digest.Usage.AICredits)
			} else {
				require.Equal(t, 3.0, *outcome.Digest.Usage.AICredits)
				require.Equal(t, 30, outcome.Digest.Usage.InputTokens)
			}
		})
	}
}

func TestShutdownUsesFallbackOnlyWithoutFinalRPC(t *testing.T) {
	for _, reported := range []bool{false, true} {
		t.Run(map[bool]string{true: "RPC", false: "shutdown fallback"}[reported], func(t *testing.T) {
			ctrl := gomock.NewController(t)
			session := NewMockCopilotSession(ctrl)
			client := NewMockCopilotClient(ctrl)
			collector := NewSessionUsageCollector()
			if reported {
				collector.SetMetrics(&rpc.UsageGetMetricsResult{TotalNanoAiu: utils.Ptr(2e9)})
			} else {
				session.EXPECT().ShutdownUsage(gomock.Any()).DoAndReturn(func(ctx context.Context) (*copilot.SessionShutdownData, error) {
					_, bounded := ctx.Deadline()
					require.True(t, bounded)
					return &copilot.SessionShutdownData{TotalNanoAiu: utils.Ptr(2e9)}, nil
				})
			}
			engine := &CopilotEngine{
				client:          client,
				sessions:        map[string]CopilotSession{"s": session},
				usageCollectors: map[string]*SessionUsageCollector{"s": collector},
			}
			client.EXPECT().DeleteSession(gomock.Any(), "s").DoAndReturn(func(context.Context, string) error {
				require.Equal(t, 2.0, *engine.SessionUsage("s").AICredits)
				return nil
			})
			require.NoError(t, engine.Shutdown(t.Context()))
			require.Equal(t, 2.0, *engine.SessionUsage("s").AICredits)
			require.NoError(t, engine.Shutdown(t.Context()))
		})
	}
}
