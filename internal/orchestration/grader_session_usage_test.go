package orchestration

import (
	"fmt"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestGraderUsageKeepsLatestContinuedSessionSnapshot(t *testing.T) {
	for _, id := range []string{"task", "judge", ""} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprintf("session=%q/missing=%t", id, missing), func(t *testing.T) {
				initial := &models.UsageStats{AICredits: utils.Ptr(1.0)}
				resp := &execution.ExecutionResponse{SessionID: "task", Usage: initial}
				latest := &models.UsageStats{AICredits: utils.Ptr(2.0)}
				if missing {
					latest = nil
				}

				runner := NewEvalRunner(config.NewEvalConfig(&models.EvalSpec{}), &gradingUsageEngine{})
				ctx := runner.buildGraderContext(&models.TestCase{}, resp, nil)
				ctx.RecordUsage(models.SessionDigest{SessionID: id, Usage: latest})
				require.Len(t, resp.GraderSessions, 1)
				wantSnapshot := initial
				if id == "task" {
					wantSnapshot = latest
				}
				require.Equal(t, wantSnapshot, resp.Usage)
				usage := aggregateUsageFromOutcomes([]models.TestOutcome{{Runs: []models.RunResult{{
					SessionDigest: runner.buildSessionDigest(resp), GraderSessions: resp.GraderSessions,
				}}}})
				if missing {
					if usage != nil {
						require.Nil(t, usage.AICredits)
					}
					return
				}
				require.NotNil(t, usage)
				wantCredits := 3.0
				if id == "task" {
					wantCredits = 2.0
				}
				require.Equal(t, wantCredits, *usage.AICredits)
			})
		}
	}
}

func TestContinuedGraderPreservesEngineUsageSemanticsAcrossFollowUps(t *testing.T) {
	for _, cumulative := range []bool{false, true} {
		t.Run(fmt.Sprint(cumulative), func(t *testing.T) {
			resp := &execution.ExecutionResponse{SessionID: "task", Usage: &models.UsageStats{InputTokens: 100}, UsageIsCumulative: cumulative}
			runner := NewEvalRunner(config.NewEvalConfig(&models.EvalSpec{}), &gradingUsageEngine{})
			ctx := runner.buildGraderContext(&models.TestCase{}, resp, nil)
			ctx.RecordResponseUsage(&execution.ExecutionResponse{SessionID: "task", Usage: &models.UsageStats{InputTokens: 250}, UsageIsCumulative: cumulative})
			require.Len(t, resp.GraderSessions, 1)
			want := 350
			if cumulative {
				want = 250
			}
			require.Equal(t, want, resp.Usage.InputTokens)
			execution.MergeResponseUsage(resp, &execution.ExecutionResponse{SessionID: "task", Usage: &models.UsageStats{InputTokens: 500}, UsageIsCumulative: cumulative})
			want = 850
			if cumulative {
				want = 500
			}
			require.Equal(t, want, resp.Usage.InputTokens)
		})
	}
}
