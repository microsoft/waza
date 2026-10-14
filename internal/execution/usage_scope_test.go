package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

type accountingEngine struct {
	stubUsageEngine
	execute func(*ExecutionRequest) (*ExecutionResponse, error)
}

func (e *accountingEngine) Execute(_ context.Context, req *ExecutionRequest) (*ExecutionResponse, error) {
	return e.execute(req)
}

func accountedUsage(tokens int, credits float64) *models.UsageStats {
	return &models.UsageStats{
		InputTokens: tokens, AICredits: utils.Ptr(credits),
		ModelMetrics: map[string]models.ModelUsage{"model": {InputTokens: tokens, AICredits: utils.Ptr(credits)}},
	}
}

func TestUsageScopeAccountsEveryExecution(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses []*ExecutionResponse
		want      *float64
		tokens    int
		sessions  int
	}{
		{"retries", []*ExecutionResponse{
			{SessionID: "failed", Usage: accountedUsage(10, 1)},
			{SessionID: "passed", Usage: accountedUsage(20, 2)},
		}, utils.Ptr(3.0), 30, 2},
		{"same cumulative session", []*ExecutionResponse{
			{SessionID: "s", Usage: accountedUsage(100, 1), UsageIsCumulative: true},
			{SessionID: "s", Usage: accountedUsage(250, 2), UsageIsCumulative: true},
		}, utils.Ptr(2.0), 250, 1},
		{"generic per turn", []*ExecutionResponse{
			{SessionID: "s", Usage: accountedUsage(100, 1)},
			{SessionID: "s", Usage: accountedUsage(150, 2)},
		}, utils.Ptr(3.0), 250, 1},
		{"anonymous independent turns", []*ExecutionResponse{
			{Usage: accountedUsage(10, 1), UsageIsCumulative: true},
			{Usage: accountedUsage(20, 2), UsageIsCumulative: true},
		}, utils.Ptr(3.0), 30, 2},
		{"missing response", []*ExecutionResponse{{SessionID: "s", Usage: accountedUsage(10, 1)}, nil}, nil, 10, 2},
		{"missing session usage", []*ExecutionResponse{{SessionID: "s", Usage: accountedUsage(10, 1)}, {SessionID: "missing"}}, nil, 10, 2},
		{"missing later cumulative usage", []*ExecutionResponse{
			{SessionID: "s", Usage: accountedUsage(10, 1), UsageIsCumulative: true},
			{SessionID: "s", Usage: &models.UsageStats{InputTokens: 20}, UsageIsCumulative: true},
		}, nil, 20, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, scope := NewUsageScope(t.Context())
			call := 0
			engine := &accountingEngine{execute: func(*ExecutionRequest) (*ExecutionResponse, error) {
				resp := tc.responses[call]
				call++
				return resp, errors.New("execution error must not discard usage")
			}}
			for range tc.responses {
				_, err := ExecuteRecorded(ctx, engine, &ExecutionRequest{})
				require.Error(t, err)
			}
			outcome := &models.EvaluationOutcome{EvaluationUsage: scope.Snapshot()}
			UpdateOutcomeUsage(outcome, engine)
			require.Len(t, outcome.EvaluationUsage.Sessions, tc.sessions)
			require.NotNil(t, outcome.Digest.Usage)
			require.Equal(t, tc.want, outcome.Digest.Usage.AICredits)
			require.Equal(t, tc.tokens, outcome.Digest.Usage.InputTokens)
			if tc.want == nil {
				require.Nil(t, outcome.Digest.Usage.ModelMetrics["model"].AICredits)
			}
		})
	}
}

func TestUsageScopeExcludesCachedDiagnosticsAndOldEngineSessions(t *testing.T) {
	cached := accountedUsage(900, 99)
	engine := &accountingEngine{stubUsageEngine: stubUsageEngine{usage: map[string]*models.UsageStats{"old": cached}}}
	for _, live := range []bool{false, true} {
		t.Run(fmt.Sprint(live), func(t *testing.T) {
			ctx, scope := NewUsageScope(t.Context())
			outcome := &models.EvaluationOutcome{TestOutcomes: []models.TestOutcome{{
				Cached: true, Runs: []models.RunResult{{Usage: cached, SessionDigest: models.SessionDigest{SessionID: "old", Usage: cached}}},
			}}}
			if live {
				engine.execute = func(*ExecutionRequest) (*ExecutionResponse, error) {
					return &ExecutionResponse{SessionID: "trigger", Usage: accountedUsage(10, 2)}, nil
				}
				_, err := ExecuteRecorded(ctx, engine, &ExecutionRequest{})
				require.NoError(t, err)
			}
			outcome.EvaluationUsage = scope.Snapshot()
			UpdateOutcomeUsage(outcome, engine)
			require.Same(t, cached, outcome.TestOutcomes[0].Runs[0].Usage)
			if live {
				require.Equal(t, 2.0, *outcome.Digest.Usage.AICredits)
				require.Equal(t, 2.0, *outcome.Digest.Usage.ModelMetrics["model"].AICredits)
			} else {
				require.Nil(t, outcome.Digest.Usage)
			}
			data, err := json.Marshal(outcome)
			require.NoError(t, err)
			var restored models.EvaluationOutcome
			require.NoError(t, json.Unmarshal(data, &restored))
			require.NotNil(t, restored.EvaluationUsage, "empty accounting must survive serialization")
			UpdateOutcomeUsage(&restored, engine)
			require.Equal(t, outcome.Digest.Usage, restored.Digest.Usage)
		})
	}
}

func TestUsageScopeReusedSessionSubtractsPriorEvaluation(t *testing.T) {
	prior := accountedUsage(100, 1)
	prior.PremiumRequests = 1
	prior.ModelMetrics["model"] = models.ModelUsage{InputTokens: 100, RequestCount: 1, RequestCost: 1, AICredits: utils.Ptr(1.0)}
	engine := &accountingEngine{stubUsageEngine: stubUsageEngine{usage: map[string]*models.UsageStats{"s": prior}}}
	engine.execute = func(*ExecutionRequest) (*ExecutionResponse, error) {
		current := accountedUsage(250, 3)
		current.PremiumRequests = 3
		current.ModelMetrics["model"] = models.ModelUsage{InputTokens: 250, RequestCount: 3, RequestCost: 3, AICredits: utils.Ptr(3.0)}
		engine.usage["s"] = current
		return &ExecutionResponse{SessionID: "s", Usage: current, UsageIsCumulative: true}, nil
	}
	ctx, scope := NewUsageScope(t.Context())
	_, err := ExecuteRecorded(ctx, engine, &ExecutionRequest{SessionID: "s"})
	require.NoError(t, err)
	outcome := &models.EvaluationOutcome{EvaluationUsage: scope.Snapshot()}
	UpdateOutcomeUsage(outcome, engine)
	require.Equal(t, 150, outcome.Digest.Usage.InputTokens)
	require.Equal(t, 2.0, *outcome.Digest.Usage.AICredits)
	require.Equal(t, 2.0, outcome.Digest.Usage.PremiumRequests)
	require.Equal(t, 2.0, *outcome.Digest.Usage.ModelMetrics["model"].AICredits)
	require.Equal(t, 150, outcome.Digest.Usage.ModelMetrics["model"].InputTokens)
	require.Equal(t, 1.0, *prior.AICredits, "baseline must not be mutated")

	// A new scope must not inherit even a still-live engine's sessions.
	_, next := NewUsageScope(ctx)
	nextOutcome := &models.EvaluationOutcome{EvaluationUsage: next.Snapshot()}
	UpdateOutcomeUsage(nextOutcome, engine)
	require.Nil(t, nextOutcome.Digest.Usage)
}

func TestEvaluationSessionStatsIncompleteBaselinesAndResets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial *models.UsageStats
		current *models.UsageStats
		unknown bool
	}{
		{"unknown baseline", nil, accountedUsage(20, 2), true},
		{"missing baseline credits", &models.UsageStats{InputTokens: 10}, accountedUsage(20, 2), false},
		{"token reset", accountedUsage(100, 1), accountedUsage(20, 2), false},
		{"credit reset", accountedUsage(10, 3), accountedUsage(20, 2), false},
		{"missing current model", accountedUsage(10, 1), &models.UsageStats{InputTokens: 20, AICredits: utils.Ptr(2.0)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := EvaluationSessionStats(models.EvaluationSessionUsage{
				Usage: tc.current, InitialUsage: tc.initial, UnknownBaseline: tc.unknown, Cumulative: true,
			})
			require.NotNil(t, usage)
			require.Nil(t, usage.AICredits)
			for _, metric := range usage.ModelMetrics {
				require.Nil(t, metric.AICredits)
			}
			require.GreaterOrEqual(t, usage.InputTokens, 0)
		})
	}
	require.Nil(t, EvaluationSessionStats(models.EvaluationSessionUsage{}))
}

func TestUsageScopeCopiesSnapshotsAndConcurrentSessions(t *testing.T) {
	ctx, scope := NewUsageScope(t.Context())
	usage := accountedUsage(10, 1)
	engine := &accountingEngine{execute: func(req *ExecutionRequest) (*ExecutionResponse, error) {
		return &ExecutionResponse{SessionID: req.Message, Usage: usage}, nil
	}}
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			_, err := ExecuteRecorded(ctx, engine, &ExecutionRequest{Message: fmt.Sprint(i)})
			if err != nil {
				t.Error(err)
			}

		})
	}
	wg.Wait()
	snapshot := scope.Snapshot()
	require.Len(t, snapshot.Sessions, 32)
	*usage.AICredits = 99
	metric := usage.ModelMetrics["model"]
	*metric.AICredits = 99
	require.Equal(t, 1.0, *snapshot.Sessions[0].Usage.AICredits)
	require.Equal(t, 1.0, *snapshot.Sessions[0].Usage.ModelMetrics["model"].AICredits)
	outcome := &models.EvaluationOutcome{EvaluationUsage: snapshot}
	UpdateOutcomeUsage(outcome, engine)
	require.Equal(t, 32.0, *outcome.Digest.Usage.AICredits)
}

func TestUsageScopeRejectsLateOlderSnapshots(t *testing.T) {
	ctx, scope := NewUsageScope(t.Context())
	revision := uint64(2)
	engine := &accountingEngine{execute: func(*ExecutionRequest) (*ExecutionResponse, error) {
		return &ExecutionResponse{SessionID: "s", Usage: accountedUsage(int(revision)*100, float64(revision)), UsageIsCumulative: true, UsageRevision: revision}, nil
	}}
	_, err := ExecuteRecorded(ctx, engine, &ExecutionRequest{})
	require.NoError(t, err)
	revision = 1
	_, err = ExecuteRecorded(ctx, engine, &ExecutionRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(2), scope.Snapshot().Sessions[0].UsageRevision)
	require.Equal(t, 2.0, *scope.Snapshot().Sessions[0].Usage.AICredits)
}

func TestScopedUsageRevisionPreventsAbsorbingLaterEvaluation(t *testing.T) {
	collector := NewSessionUsageCollector()
	collector.SetMetrics(&rpc.UsageGetMetricsResult{TotalNanoAiu: utils.Ptr(1e9)})
	engine := &CopilotEngine{usageCollectors: map[string]*SessionUsageCollector{"s": collector}}
	outcome := &models.EvaluationOutcome{EvaluationUsage: &models.EvaluationUsage{Sessions: []models.EvaluationSessionUsage{{
		SessionID: "s", Usage: accountedUsage(10, 1), Cumulative: true, UsageRevision: collector.revision(),
	}}}}
	collector.beginTurn()
	collector.SetMetrics(&rpc.UsageGetMetricsResult{TotalNanoAiu: utils.Ptr(9e9)})
	UpdateOutcomeUsage(outcome, engine)
	require.Equal(t, 1.0, *outcome.Digest.Usage.AICredits)
	require.Equal(t, uint64(2), engine.SessionUsageRevision("s"))
	require.Zero(t, engine.SessionUsageRevision("absent"))
	outcome.EvaluationUsage.Sessions[0].UsageRevision = 2
	UpdateOutcomeUsage(outcome, engine)
	require.Equal(t, 9.0, *outcome.Digest.Usage.AICredits, "current revision can use final shutdown data")
}

func TestScopedUsageIncludesBaselineAndMissingAttribution(t *testing.T) {
	missingModel := &models.UsageStats{InputTokens: 20, AICredits: utils.Ptr(2.0)}
	outcome := &models.EvaluationOutcome{
		EvaluationUsage: &models.EvaluationUsage{Sessions: []models.EvaluationSessionUsage{
			{SessionID: "reported", Usage: accountedUsage(10, 1)}, {SessionID: "unattributed", Usage: missingModel},
		}},
		BaselineOutcome: &models.EvaluationOutcome{EvaluationUsage: &models.EvaluationUsage{
			Sessions: []models.EvaluationSessionUsage{{SessionID: "baseline", Usage: accountedUsage(40, 4)}},
		}},
	}
	UpdateOutcomeUsage(outcome, &stubUsageEngine{})
	require.Equal(t, 3.0, *outcome.Digest.Usage.AICredits)
	require.Nil(t, outcome.Digest.Usage.ModelMetrics["model"].AICredits)
	require.Equal(t, 4.0, *outcome.BaselineOutcome.Digest.Usage.AICredits)
	require.Equal(t, 30, outcome.Digest.Usage.InputTokens)
}

func TestMergeResponseUsageSemantics(t *testing.T) {
	for _, tc := range []struct {
		name       string
		initialID  string
		nextID     string
		cumulative bool
		want       int
	}{
		{"cumulative", "s", "s", true, 250},
		{"different cumulative", "s", "other", true, 350},
		{"anonymous cumulative", "", "", true, 350},
		{"generic same session", "s", "s", false, 350},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := &ExecutionResponse{SessionID: tc.initialID, Usage: accountedUsage(100, 1), UsageIsCumulative: tc.cumulative}
			MergeResponseUsage(initial, &ExecutionResponse{SessionID: tc.nextID, Usage: accountedUsage(250, 2), UsageIsCumulative: tc.cumulative})
			require.Equal(t, tc.want, initial.Usage.InputTokens)
		})
	}
	resp := &ExecutionResponse{SessionID: "s", Usage: accountedUsage(100, 1), UsageIsCumulative: true, UsageRevision: 2}
	MergeResponseUsage(resp, &ExecutionResponse{SessionID: "s", Usage: accountedUsage(20, 0.1), UsageIsCumulative: true, UsageRevision: 1})
	require.Equal(t, 100, resp.Usage.InputTokens, "late older response cannot replace a newer snapshot")
	MergeResponseUsage(resp, &ExecutionResponse{SessionID: "s", UsageIsCumulative: true, UsageRevision: 3})
	require.Equal(t, 100, resp.Usage.InputTokens)
	require.Nil(t, resp.Usage.AICredits, "missing snapshots retain diagnostics, not stale billing")
}

func TestExecuteRecordedWithoutScopePreservesResponseAndError(t *testing.T) {
	response := &ExecutionResponse{Usage: accountedUsage(1, 1)}
	wantErr := errors.New("execute failed")
	engine := &accountingEngine{execute: func(*ExecutionRequest) (*ExecutionResponse, error) { return response, wantErr }}
	actual, err := ExecuteRecorded(t.Context(), engine, &ExecutionRequest{})
	require.Same(t, response, actual)
	require.ErrorIs(t, err, wantErr)
}
