package reporting

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestOutcome() *models.EvaluationOutcome {
	return &models.EvaluationOutcome{
		RunID:       "run-1",
		SkillTested: "code-explainer",
		BenchName:   "Code Explainer Eval",
		Timestamp:   time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		Setup: models.OutcomeSetup{
			RunsPerTest: 1,
			ModelID:     "gpt-4o",
			EngineType:  "mock",
			TimeoutSec:  60,
		},
		Digest: models.OutcomeDigest{
			TotalTests:     3,
			Succeeded:      2,
			Failed:         1,
			Errors:         0,
			SuccessRate:    0.67,
			AggregateScore: 0.75,
			DurationMs:     3500,
		},
		TestOutcomes: []models.TestOutcome{
			{
				TestID:      "task-1",
				DisplayName: "explain-function",
				Status:      models.StatusPassed,
				Stats:       &models.TestStats{AvgScore: 0.95, AvgDurationMs: 1000},
				Runs: []models.RunResult{
					{
						RunNumber: 1, Status: models.StatusPassed, DurationMs: 1000,
						Validations: map[string]models.GraderResults{
							"regex": {Name: "regex", Type: "regex", Score: 1.0, Passed: true, Feedback: "ok"},
						},
					},
				},
			},
			{
				TestID:      "task-2",
				DisplayName: "explain-class",
				Status:      models.StatusFailed,
				Stats:       &models.TestStats{AvgScore: 0.40, AvgDurationMs: 1500},
				Runs: []models.RunResult{
					{
						RunNumber: 1, Status: models.StatusFailed, DurationMs: 1500,
						Validations: map[string]models.GraderResults{
							"regex":   {Name: "regex", Type: "regex", Score: 0.0, Passed: false, Feedback: "pattern not found"},
							"keyword": {Name: "keyword", Type: "keyword", Score: 0.8, Passed: true, Feedback: "ok"},
						},
					},
				},
			},
			{
				TestID:      "task-3",
				DisplayName: "explain-module",
				Status:      models.StatusPassed,
				Stats:       &models.TestStats{AvgScore: 0.90, AvgDurationMs: 1000},
				Runs: []models.RunResult{
					{
						RunNumber: 1, Status: models.StatusPassed, DurationMs: 1000,
						Validations: map[string]models.GraderResults{
							"code": {Name: "code", Type: "code", Score: 0.9, Passed: true, Feedback: "good"},
						},
					},
				},
			},
		},
	}
}

func TestConvertToJUnit_Structure(t *testing.T) {
	outcome := newTestOutcome()
	suites := ConvertToJUnit(outcome)

	assert.Equal(t, 3, suites.Tests)
	assert.Equal(t, 1, suites.Failures)
	assert.Equal(t, 0, suites.Errors)
	assert.InDelta(t, 3.5, suites.Time, 0.01)

	require.Len(t, suites.TestSuites, 1)
	suite := suites.TestSuites[0]

	assert.Equal(t, "Code Explainer Eval", suite.Name)
	assert.Equal(t, 3, suite.Tests)
	assert.Equal(t, 1, suite.Failures)
	assert.Equal(t, "2025-06-15T12:00:00Z", suite.Timestamp)
	require.Len(t, suite.TestCases, 3)
}

func TestConvertToJUnit_PassedTestCase(t *testing.T) {
	outcome := newTestOutcome()
	suites := ConvertToJUnit(outcome)
	tc := suites.TestSuites[0].TestCases[0]

	assert.Equal(t, "explain-function", tc.Name)
	assert.Equal(t, "code-explainer", tc.Classname)
	assert.InDelta(t, 1.0, tc.Time, 0.01)
	assert.Nil(t, tc.Failure)
	assert.Nil(t, tc.Error)
}

func TestConvertToJUnit_FailedTestCase(t *testing.T) {
	outcome := newTestOutcome()
	suites := ConvertToJUnit(outcome)
	tc := suites.TestSuites[0].TestCases[1]

	assert.Equal(t, "explain-class", tc.Name)
	require.NotNil(t, tc.Failure)
	assert.Equal(t, "GraderFailure", tc.Failure.Type)
	assert.Contains(t, tc.Failure.Message, "score=0.40")
	assert.Contains(t, tc.Failure.Body, "[FAIL] regex")
	assert.Contains(t, tc.Failure.Body, "pattern not found")
	// keyword passed, so it should NOT appear in failure body
	assert.NotContains(t, tc.Failure.Body, "[FAIL] keyword")
}

func TestConvertToJUnit_ErrorTestCase(t *testing.T) {
	outcome := &models.EvaluationOutcome{
		BenchName: "err-test",
		Timestamp: time.Now(),
		Digest:    models.OutcomeDigest{TotalTests: 1, Errors: 1, DurationMs: 500},
		TestOutcomes: []models.TestOutcome{
			{
				DisplayName: "broken-task",
				Status:      models.StatusError,
				Runs: []models.RunResult{
					{Status: models.StatusError, ErrorMsg: "timeout after 60s"},
				},
			},
		},
	}

	suites := ConvertToJUnit(outcome)
	tc := suites.TestSuites[0].TestCases[0]

	assert.Nil(t, tc.Failure)
	require.NotNil(t, tc.Error)
	assert.Equal(t, "ExecutionError", tc.Error.Type)
	assert.Equal(t, "timeout after 60s", tc.Error.Message)
}

func TestConvertToJUnit_Properties(t *testing.T) {
	outcome := newTestOutcome()
	suites := ConvertToJUnit(outcome)
	props := suites.TestSuites[0].Properties

	propMap := make(map[string]string)
	for _, p := range props {
		propMap[p.Name] = p.Value
	}

	assert.Equal(t, "code-explainer", propMap["skill"])
	assert.Equal(t, "gpt-4o", propMap["model"])
	assert.Equal(t, "mock", propMap["engine"])
	assert.Contains(t, propMap["score"], "0.75")
}

func TestConvertToJUnit_EmptyOutcome(t *testing.T) {
	outcome := &models.EvaluationOutcome{
		BenchName: "empty",
		Timestamp: time.Now(),
		Digest:    models.OutcomeDigest{},
	}

	suites := ConvertToJUnit(outcome)
	assert.Equal(t, 0, suites.Tests)
	require.Len(t, suites.TestSuites, 1)
	assert.Empty(t, suites.TestSuites[0].TestCases)
}

func TestWriteJUnitXML_ValidXML(t *testing.T) {
	outcome := newTestOutcome()
	dir := t.TempDir()
	path := filepath.Join(dir, "results.xml")

	err := WriteJUnitXML(outcome, path)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	content := string(data)
	assert.True(t, strings.HasPrefix(content, "<?xml"))

	// Verify it parses as valid XML
	var parsed JUnitTestSuites
	err = xml.Unmarshal(data, &parsed)
	require.NoError(t, err)
	assert.Equal(t, 3, parsed.Tests)
	assert.Equal(t, 1, parsed.Failures)
	require.Len(t, parsed.TestSuites, 1)
	assert.Len(t, parsed.TestSuites[0].TestCases, 3)
}

func TestWriteJUnitXML_FailedGraderDetails(t *testing.T) {
	outcome := newTestOutcome()
	dir := t.TempDir()
	path := filepath.Join(dir, "results.xml")

	err := WriteJUnitXML(outcome, path)
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "pattern not found")
	assert.Contains(t, content, "GraderFailure")
}

func TestConvertToJUnit_DurationFromRuns(t *testing.T) {
	// Test that duration is computed from runs when stats are nil
	outcome := &models.EvaluationOutcome{
		BenchName: "dur-test",
		Timestamp: time.Now(),
		Digest:    models.OutcomeDigest{TotalTests: 1, Succeeded: 1, DurationMs: 2000},
		TestOutcomes: []models.TestOutcome{
			{
				DisplayName: "task-a",
				Status:      models.StatusPassed,
				Runs: []models.RunResult{
					{DurationMs: 1000},
					{DurationMs: 3000},
				},
			},
		},
	}

	suites := ConvertToJUnit(outcome)
	tc := suites.TestSuites[0].TestCases[0]
	// Average of 1000ms and 3000ms = 2000ms = 2.0s
	assert.InDelta(t, 2.0, tc.Time, 0.01)
}
