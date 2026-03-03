package main

import (
	"fmt"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
)

func TestFormatGitHubComment_PassedEval(t *testing.T) {
	outcome := &models.EvaluationOutcome{
		BenchName:   "Test Eval",
		SkillTested: "code-explainer",
		Setup: models.OutcomeSetup{
			ModelID: "gpt-4o",
		},
		Digest: models.OutcomeDigest{
			TotalTests:     2,
			Succeeded:      2,
			Failed:         0,
			Errors:         0,
			SuccessRate:    1.0,
			AggregateScore: 0.87,
			MinScore:       0.81,
			MaxScore:       0.92,
			StdDev:         0.055,
			DurationMs:     45000,
		},
		TestOutcomes: []models.TestOutcome{
			{
				TestID:      "tc-001",
				DisplayName: "code-explain-python",
				Status:      models.StatusPassed,
				Stats: &models.TestStats{
					AvgScore:      0.92,
					PassRate:      1.0,
					AvgDurationMs: 20000,
				},
				Runs: []models.RunResult{
					{
						RunNumber:  1,
						Status:     models.StatusPassed,
						DurationMs: 20000,
						Validations: map[string]models.GraderResults{
							"text": {
								Name:   "text",
								Type:   models.GraderKindText,
								Score:  0.95,
								Passed: true,
							},
							"behavior": {
								Name:   "behavior",
								Type:   models.GraderKindBehavior,
								Score:  0.89,
								Passed: true,
							},
						},
					},
				},
			},
			{
				TestID:      "tc-002",
				DisplayName: "code-explain-go",
				Status:      models.StatusPassed,
				Stats: &models.TestStats{
					AvgScore:      0.81,
					PassRate:      1.0,
					AvgDurationMs: 25000,
				},
				Runs: []models.RunResult{
					{
						RunNumber:  1,
						Status:     models.StatusPassed,
						DurationMs: 25000,
						Validations: map[string]models.GraderResults{
							"text": {
								Name:   "text",
								Type:   models.GraderKindText,
								Score:  0.85,
								Passed: true,
							},
							"behavior": {
								Name:   "behavior",
								Type:   models.GraderKindBehavior,
								Score:  0.77,
								Passed: true,
							},
						},
					},
				},
			},
		},
	}

	result := FormatGitHubComment(outcome)

	// Check header
	assert.Contains(t, result, "## 🧪 Waza Eval Results")
	assert.Contains(t, result, "**Status:** ✅ Passed")
	assert.Contains(t, result, "**Score:** 0.87")
	assert.Contains(t, result, "**Duration:** 45s")

	// Check summary stats
	assert.Contains(t, result, "**Tests:** 2 total, 2 passed, 0 failed, 0 errors")
	assert.Contains(t, result, "**Success Rate:** 100.0%")

	// Check table
	assert.Contains(t, result, "### Task Results")
	assert.Contains(t, result, "| Task | Score | Status | Graders |")
	assert.Contains(t, result, "code-explain-python")
	assert.Contains(t, result, "code-explain-go")

	// Check footer
	assert.Contains(t, result, "**Benchmark:** Test Eval")
	assert.Contains(t, result, "**Skill:** code-explainer")
	assert.Contains(t, result, "**Model:** gpt-4o")

	// Should NOT have flaky or failed sections
	assert.NotContains(t, result, "⚠️ Flaky Tasks")
	assert.NotContains(t, result, "Failed Task Details")
}

func TestFormatGitHubComment_FailedEval(t *testing.T) {
	outcome := &models.EvaluationOutcome{
		BenchName:   "Test Eval",
		SkillTested: "code-explainer",
		Setup: models.OutcomeSetup{
			ModelID: "gpt-4o",
		},
		Digest: models.OutcomeDigest{
			TotalTests:     2,
			Succeeded:      1,
			Failed:         1,
			Errors:         0,
			SuccessRate:    0.5,
			AggregateScore: 0.45,
			MinScore:       0.10,
			MaxScore:       0.80,
			StdDev:         0.35,
			DurationMs:     30000,
		},
		TestOutcomes: []models.TestOutcome{
			{
				TestID:      "tc-001",
				DisplayName: "passing-task",
				Status:      models.StatusPassed,
				Stats: &models.TestStats{
					AvgScore: 0.80,
					PassRate: 1.0,
				},
				Runs: []models.RunResult{
					{
						RunNumber:  1,
						Status:     models.StatusPassed,
						DurationMs: 15000,
						Validations: map[string]models.GraderResults{
							"text": {
								Name:     "text",
								Score:    0.80,
								Passed:   true,
								Feedback: "Pattern matched",
							},
						},
					},
				},
			},
			{
				TestID:      "tc-002",
				DisplayName: "failing-task",
				Status:      models.StatusFailed,
				Stats: &models.TestStats{
					AvgScore: 0.10,
					PassRate: 0.0,
				},
				Runs: []models.RunResult{
					{
						RunNumber:  1,
						Status:     models.StatusFailed,
						DurationMs: 15000,
						Validations: map[string]models.GraderResults{
							"code": {
								Name:     "code",
								Score:    0.10,
								Passed:   false,
								Feedback: "Assertion failed: expected True",
							},
						},
					},
				},
			},
		},
	}

	result := FormatGitHubComment(outcome)

	// Check failed status
	assert.Contains(t, result, "**Status:** ❌ Failed")
	assert.Contains(t, result, "**Score:** 0.45")

	// Check summary
	assert.Contains(t, result, "**Tests:** 2 total, 1 passed, 1 failed, 0 errors")
	assert.Contains(t, result, "**Success Rate:** 50.0%")

	// Check failed task details section
	assert.Contains(t, result, "### Failed Task Details")
	assert.Contains(t, result, "#### failing-task")
	assert.Contains(t, result, "**Run 1/1** (failed)")
	assert.Contains(t, result, "❌ **code** (0.10): Assertion failed: expected True")

	// Check that passing task is not in failed details
	assert.NotContains(t, result, "#### passing-task")
}

func TestFormatGitHubComment_FlakyTasks(t *testing.T) {
	outcome := &models.EvaluationOutcome{
		BenchName:   "Test Eval",
		SkillTested: "code-explainer",
		Setup: models.OutcomeSetup{
			ModelID: "gpt-4o",
		},
		Digest: models.OutcomeDigest{
			TotalTests:     1,
			Succeeded:      0,
			Failed:         1,
			Errors:         0,
			SuccessRate:    0.0,
			AggregateScore: 0.50,
			MinScore:       0.50,
			MaxScore:       0.50,
			StdDev:         0.20,
			DurationMs:     30000,
		},
		TestOutcomes: []models.TestOutcome{
			{
				TestID:      "tc-001",
				DisplayName: "flaky-task",
				Status:      models.StatusFailed,
				Stats: &models.TestStats{
					AvgScore:    0.50,
					PassRate:    0.60,
					StdDevScore: 0.20,
					Flaky:       true,
				},
				Runs: []models.RunResult{
					{
						RunNumber:  1,
						Status:     models.StatusFailed,
						DurationMs: 15000,
						Validations: map[string]models.GraderResults{
							"code": {
								Name:     "code",
								Score:    0.50,
								Passed:   false,
								Feedback: "Intermittent failure",
							},
						},
					},
				},
			},
		},
	}

	result := FormatGitHubComment(outcome)

	// Check flaky warning
	assert.Contains(t, result, "### ⚠️ Flaky Tasks")
	assert.Contains(t, result, "The following tasks showed inconsistent results")
	assert.Contains(t, result, "**flaky-task**: 60% pass rate, score=0.50±0.20")
}

func TestFormatGitHubComment_EmptyGraders(t *testing.T) {
	outcome := &models.EvaluationOutcome{
		BenchName:   "Test Eval",
		SkillTested: "test-skill",
		Setup: models.OutcomeSetup{
			ModelID: "gpt-4o",
		},
		Digest: models.OutcomeDigest{
			TotalTests:     1,
			Succeeded:      1,
			Failed:         0,
			Errors:         0,
			SuccessRate:    1.0,
			AggregateScore: 1.0,
			MinScore:       1.0,
			MaxScore:       1.0,
			StdDev:         0.0,
			DurationMs:     10000,
		},
		TestOutcomes: []models.TestOutcome{
			{
				TestID:      "tc-001",
				DisplayName: "task-no-graders",
				Status:      models.StatusPassed,
				Stats: &models.TestStats{
					AvgScore: 1.0,
					PassRate: 1.0,
				},
				Runs: []models.RunResult{
					{
						RunNumber:   1,
						Status:      models.StatusPassed,
						DurationMs:  10000,
						Validations: map[string]models.GraderResults{},
					},
				},
			},
		},
	}

	result := FormatGitHubComment(outcome)

	// Check that empty graders shows "-"
	assert.Contains(t, result, "task-no-graders")
	assert.Contains(t, result, "| 1.00 | ✅ | - |")
}

func TestFormatGitHubComment_WithErrors(t *testing.T) {
	outcome := &models.EvaluationOutcome{
		BenchName:   "Test Eval",
		SkillTested: "test-skill",
		Setup: models.OutcomeSetup{
			ModelID: "gpt-4o",
		},
		Digest: models.OutcomeDigest{
			TotalTests:     1,
			Succeeded:      0,
			Failed:         0,
			Errors:         1,
			SuccessRate:    0.0,
			AggregateScore: 0.0,
			MinScore:       0.0,
			MaxScore:       0.0,
			StdDev:         0.0,
			DurationMs:     5000,
		},
		TestOutcomes: []models.TestOutcome{
			{
				TestID:      "tc-001",
				DisplayName: "error-task",
				Status:      models.StatusError,
				Stats: &models.TestStats{
					AvgScore: 0.0,
					PassRate: 0.0,
				},
				Runs: []models.RunResult{
					{
						RunNumber:  1,
						Status:     models.StatusError,
						DurationMs: 5000,
						ErrorMsg:   "Timeout exceeded",
						Validations: map[string]models.GraderResults{
							"code": {
								Name:     "code",
								Score:    0.0,
								Passed:   false,
								Feedback: "Execution error",
							},
						},
					},
				},
			},
		},
	}

	result := FormatGitHubComment(outcome)

	// Check error status
	assert.Contains(t, result, "**Status:** ❌ Failed")
	assert.Contains(t, result, "0 passed, 0 failed, 1 errors")

	// Check failed task details (errors show up here too)
	assert.Contains(t, result, "### Failed Task Details")
	assert.Contains(t, result, "#### error-task")
	assert.Contains(t, result, "**Run 1/1** (error)")
}

func TestFormatGitHubComment_DurationFormatting(t *testing.T) {
	tests := []struct {
		name       string
		durationMs int64
		expected   string
	}{
		{"seconds", 45000, "45s"},
		{"minutes", 125000, "2m5s"},
		{"milliseconds", 500, "500ms"},
		{"hours", 3665000, "1h1m5s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome := &models.EvaluationOutcome{
				BenchName:   "Test",
				SkillTested: "skill",
				Setup:       models.OutcomeSetup{ModelID: "model"},
				Digest: models.OutcomeDigest{
					TotalTests:     1,
					Succeeded:      1,
					SuccessRate:    1.0,
					AggregateScore: 1.0,
					DurationMs:     tt.durationMs,
				},
				TestOutcomes: []models.TestOutcome{},
			}

			result := FormatGitHubComment(outcome)
			assert.Contains(t, result, fmt.Sprintf("**Duration:** %s", tt.expected))
		})
	}
}

func TestFormatGitHubComment_MultipleRunsInFailedTask(t *testing.T) {
	outcome := &models.EvaluationOutcome{
		BenchName:   "Test Eval",
		SkillTested: "test-skill",
		Setup: models.OutcomeSetup{
			ModelID: "gpt-4o",
		},
		Digest: models.OutcomeDigest{
			TotalTests:     1,
			Succeeded:      0,
			Failed:         1,
			Errors:         0,
			SuccessRate:    0.0,
			AggregateScore: 0.33,
			DurationMs:     30000,
		},
		TestOutcomes: []models.TestOutcome{
			{
				TestID:      "tc-001",
				DisplayName: "multi-run-task",
				Status:      models.StatusFailed,
				Stats: &models.TestStats{
					AvgScore: 0.33,
					PassRate: 0.33,
				},
				Runs: []models.RunResult{
					{
						RunNumber:  1,
						Status:     models.StatusFailed,
						DurationMs: 10000,
						Validations: map[string]models.GraderResults{
							"code": {
								Name:     "code",
								Score:    0.0,
								Passed:   false,
								Feedback: "Run 1 failed",
							},
						},
					},
					{
						RunNumber:  2,
						Status:     models.StatusPassed,
						DurationMs: 10000,
						Validations: map[string]models.GraderResults{
							"code": {
								Name:     "code",
								Score:    1.0,
								Passed:   true,
								Feedback: "Run 2 passed",
							},
						},
					},
					{
						RunNumber:  3,
						Status:     models.StatusFailed,
						DurationMs: 10000,
						Validations: map[string]models.GraderResults{
							"code": {
								Name:     "code",
								Score:    0.0,
								Passed:   false,
								Feedback: "Run 3 failed",
							},
						},
					},
				},
			},
		},
	}

	result := FormatGitHubComment(outcome)

	// Check that multiple failed runs are shown
	assert.Contains(t, result, "**Run 1/3** (failed)")
	assert.Contains(t, result, "Run 1 failed")
	assert.Contains(t, result, "**Run 3/3** (failed)")
	assert.Contains(t, result, "Run 3 failed")

	// Passed run should not be shown
	assert.NotContains(t, result, "**Run 2/3** (passed)")
	assert.NotContains(t, result, "Run 2 passed")
}
