package metrics

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func TestComputeBehaviorMetrics(t *testing.T) {
	tests := []struct {
		name                string
		run                 models.RunResult
		rules               models.BehaviorRules
		wantToolCallCount   int
		wantIterationCount  int
		wantMaxToolsPassed  bool
		wantMaxIterPassed   bool
		wantReqToolsPassed  bool
		wantForbidPassed    bool
		wantReqUsedLen      int
		wantReqMissedLen    int
		wantForbidUsedLen   int
		wantAllPassed       bool
		wantEfficiencyScore float64
	}{
		{
			name: "no_constraints",
			run: models.RunResult{
				SessionDigest: models.SessionDigest{
					ToolCallCount: 3,
					Usage:         &models.UsageStats{Turns: 5},
					ToolsUsed:     []string{"grep", "edit"},
				},
			},
			rules:               models.BehaviorRules{},
			wantToolCallCount:   3,
			wantIterationCount:  5,
			wantMaxToolsPassed:  true,
			wantMaxIterPassed:   true,
			wantReqToolsPassed:  true,
			wantForbidPassed:    true,
			wantAllPassed:       true,
			wantEfficiencyScore: 1.0,
		},
		{
			name: "all_constraints_pass",
			run: models.RunResult{
				SessionDigest: models.SessionDigest{
					ToolCallCount: 3,
					Usage:         &models.UsageStats{Turns: 2},
					ToolsUsed:     []string{"grep", "edit", "bash"},
				},
			},
			rules: models.BehaviorRules{
				MaxToolInvocations: 5,
				MaxRounds:          3,
				MustUseTool:        []string{"grep", "edit"},
				ForbidTool:         []string{"rm"},
			},
			wantToolCallCount:   3,
			wantIterationCount:  2,
			wantMaxToolsPassed:  true,
			wantMaxIterPassed:   true,
			wantReqToolsPassed:  true,
			wantForbidPassed:    true,
			wantReqUsedLen:      2,
			wantReqMissedLen:    0,
			wantForbidUsedLen:   0,
			wantAllPassed:       true,
			wantEfficiencyScore: 1.0,
		},
		{
			name: "exceeds_max_tool_calls",
			run: models.RunResult{
				SessionDigest: models.SessionDigest{
					ToolCallCount: 10,
					Usage:         &models.UsageStats{Turns: 2},
					ToolsUsed:     []string{"grep"},
				},
			},
			rules: models.BehaviorRules{
				MaxToolInvocations: 5,
			},
			wantToolCallCount:   10,
			wantIterationCount:  2,
			wantMaxToolsPassed:  false,
			wantMaxIterPassed:   true,
			wantReqToolsPassed:  true,
			wantForbidPassed:    true,
			wantAllPassed:       false,
			wantEfficiencyScore: 0.75,
		},
		{
			name: "exceeds_max_iterations",
			run: models.RunResult{
				SessionDigest: models.SessionDigest{
					ToolCallCount: 2,
					Usage:         &models.UsageStats{Turns: 10},
					ToolsUsed:     []string{"edit"},
				},
			},
			rules: models.BehaviorRules{
				MaxRounds: 5,
			},
			wantToolCallCount:   2,
			wantIterationCount:  10,
			wantMaxToolsPassed:  true,
			wantMaxIterPassed:   false,
			wantReqToolsPassed:  true,
			wantForbidPassed:    true,
			wantAllPassed:       false,
			wantEfficiencyScore: 0.75,
		},
		{
			name: "missing_required_tool",
			run: models.RunResult{
				SessionDigest: models.SessionDigest{
					ToolCallCount: 3,
					Usage:         &models.UsageStats{Turns: 2},
					ToolsUsed:     []string{"grep"},
				},
			},
			rules: models.BehaviorRules{
				MustUseTool: []string{"grep", "edit", "bash"},
			},
			wantToolCallCount:   3,
			wantIterationCount:  2,
			wantMaxToolsPassed:  true,
			wantMaxIterPassed:   true,
			wantReqToolsPassed:  false,
			wantForbidPassed:    true,
			wantReqUsedLen:      1,
			wantReqMissedLen:    2,
			wantAllPassed:       false,
			wantEfficiencyScore: 0.75,
		},
		{
			name: "used_forbidden_tool",
			run: models.RunResult{
				SessionDigest: models.SessionDigest{
					ToolCallCount: 3,
					Usage:         &models.UsageStats{Turns: 2},
					ToolsUsed:     []string{"grep", "rm", "edit"},
				},
			},
			rules: models.BehaviorRules{
				ForbidTool: []string{"rm", "sudo"},
			},
			wantToolCallCount:   3,
			wantIterationCount:  2,
			wantMaxToolsPassed:  true,
			wantMaxIterPassed:   true,
			wantReqToolsPassed:  true,
			wantForbidPassed:    false,
			wantForbidUsedLen:   1,
			wantAllPassed:       false,
			wantEfficiencyScore: 0.75,
		},
		{
			name: "all_constraints_fail",
			run: models.RunResult{
				SessionDigest: models.SessionDigest{
					ToolCallCount: 20,
					Usage:         &models.UsageStats{Turns: 15},
					ToolsUsed:     []string{"rm"},
				},
			},
			rules: models.BehaviorRules{
				MaxToolInvocations: 5,
				MaxRounds:          3,
				MustUseTool:        []string{"grep"},
				ForbidTool:         []string{"rm"},
			},
			wantToolCallCount:   20,
			wantIterationCount:  15,
			wantMaxToolsPassed:  false,
			wantMaxIterPassed:   false,
			wantReqToolsPassed:  false,
			wantForbidPassed:    false,
			wantReqUsedLen:      0,
			wantReqMissedLen:    1,
			wantForbidUsedLen:   1,
			wantAllPassed:       false,
			wantEfficiencyScore: 0.0,
		},
		{
			name: "exact_boundary_tool_calls",
			run: models.RunResult{
				SessionDigest: models.SessionDigest{
					ToolCallCount: 5,
					Usage:         &models.UsageStats{Turns: 3},
					ToolsUsed:     []string{"grep"},
				},
			},
			rules: models.BehaviorRules{
				MaxToolInvocations: 5,
				MaxRounds:          3,
			},
			wantToolCallCount:   5,
			wantIterationCount:  3,
			wantMaxToolsPassed:  true,
			wantMaxIterPassed:   true,
			wantReqToolsPassed:  true,
			wantForbidPassed:    true,
			wantAllPassed:       true,
			wantEfficiencyScore: 1.0,
		},
		{
			name: "zero_tool_calls_and_turns",
			run: models.RunResult{
				SessionDigest: models.SessionDigest{
					ToolCallCount: 0,
					Usage:         &models.UsageStats{Turns: 0},
					ToolsUsed:     nil,
				},
			},
			rules: models.BehaviorRules{
				MaxToolInvocations: 5,
				MaxRounds:          3,
			},
			wantToolCallCount:   0,
			wantIterationCount:  0,
			wantMaxToolsPassed:  true,
			wantMaxIterPassed:   true,
			wantReqToolsPassed:  true,
			wantForbidPassed:    true,
			wantAllPassed:       true,
			wantEfficiencyScore: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ComputeBehaviorMetrics(&tt.run, &tt.rules)

			if m.ToolCallCount != tt.wantToolCallCount {
				t.Errorf("ToolCallCount = %d, want %d", m.ToolCallCount, tt.wantToolCallCount)
			}
			if m.IterationCount != tt.wantIterationCount {
				t.Errorf("IterationCount = %d, want %d", m.IterationCount, tt.wantIterationCount)
			}
			if m.MaxToolCallsPassed != tt.wantMaxToolsPassed {
				t.Errorf("MaxToolCallsPassed = %v, want %v", m.MaxToolCallsPassed, tt.wantMaxToolsPassed)
			}
			if m.MaxIterationsPassed != tt.wantMaxIterPassed {
				t.Errorf("MaxIterationsPassed = %v, want %v", m.MaxIterationsPassed, tt.wantMaxIterPassed)
			}
			if m.RequiredToolsPassed != tt.wantReqToolsPassed {
				t.Errorf("RequiredToolsPassed = %v, want %v", m.RequiredToolsPassed, tt.wantReqToolsPassed)
			}
			if m.ForbiddenToolsPassed != tt.wantForbidPassed {
				t.Errorf("ForbiddenToolsPassed = %v, want %v", m.ForbiddenToolsPassed, tt.wantForbidPassed)
			}
			if len(m.RequiredToolsUsed) != tt.wantReqUsedLen {
				t.Errorf("RequiredToolsUsed len = %d, want %d", len(m.RequiredToolsUsed), tt.wantReqUsedLen)
			}
			if len(m.RequiredToolsMissed) != tt.wantReqMissedLen {
				t.Errorf("RequiredToolsMissed len = %d, want %d", len(m.RequiredToolsMissed), tt.wantReqMissedLen)
			}
			if len(m.ForbiddenToolsUsed) != tt.wantForbidUsedLen {
				t.Errorf("ForbiddenToolsUsed len = %d, want %d", len(m.ForbiddenToolsUsed), tt.wantForbidUsedLen)
			}
			if m.AllConstraintsPassed() != tt.wantAllPassed {
				t.Errorf("AllConstraintsPassed() = %v, want %v", m.AllConstraintsPassed(), tt.wantAllPassed)
			}
			if !approxEqual(m.EfficiencyScore, tt.wantEfficiencyScore) {
				t.Errorf("EfficiencyScore = %f, want %f", m.EfficiencyScore, tt.wantEfficiencyScore)
			}
		})
	}
}
