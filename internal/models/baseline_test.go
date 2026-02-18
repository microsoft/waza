package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestBenchmarkSpec_BaselineYAMLSerialization(t *testing.T) {
	tests := []struct {
		name           string
		yamlContent    string
		expectBaseline bool
	}{
		{
			name: "baseline true",
			yamlContent: `
name: test-eval
skill: test-skill
baseline: true
config:
  executor: mock
  model: gpt-4
  trials_per_task: 3
  timeout_seconds: 60
graders: []
tasks: []
`,
			expectBaseline: true,
		},
		{
			name: "baseline false",
			yamlContent: `
name: test-eval
skill: test-skill
baseline: false
config:
  executor: mock
  model: gpt-4
  trials_per_task: 3
  timeout_seconds: 60
graders: []
tasks: []
`,
			expectBaseline: false,
		},
		{
			name: "baseline omitted defaults to false",
			yamlContent: `
name: test-eval
skill: test-skill
config:
  executor: mock
  model: gpt-4
  trials_per_task: 3
  timeout_seconds: 60
graders: []
tasks: []
`,
			expectBaseline: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var spec BenchmarkSpec
			err := yaml.Unmarshal([]byte(tt.yamlContent), &spec)
			require.NoError(t, err)
			assert.Equal(t, tt.expectBaseline, spec.Baseline)
		})
	}
}

func TestSkillImpactMetric_JSONSerialization(t *testing.T) {
	metric := &SkillImpactMetric{
		PassRateWithSkills: 0.667,
		PassRateBaseline:   0.333,
		Delta:              0.334,
		PercentChange:      100.3,
	}

	data, err := json.Marshal(metric)
	require.NoError(t, err)

	var decoded SkillImpactMetric
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.InDelta(t, metric.PassRateWithSkills, decoded.PassRateWithSkills, 0.001)
	assert.InDelta(t, metric.PassRateBaseline, decoded.PassRateBaseline, 0.001)
	assert.InDelta(t, metric.Delta, decoded.Delta, 0.001)
	assert.InDelta(t, metric.PercentChange, decoded.PercentChange, 0.1)
}

func TestTestOutcome_SkillImpactOmittedWhenNil(t *testing.T) {
	outcome := TestOutcome{
		TestID:      "test-001",
		DisplayName: "Test Case",
		Status:      StatusPassed,
		SkillImpact: nil,
	}

	data, err := json.Marshal(outcome)
	require.NoError(t, err)

	var raw map[string]any
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	_, hasSkillImpact := raw["skill_impact"]
	assert.False(t, hasSkillImpact, "skill_impact should be omitted when nil")
}

func TestTestOutcome_SkillImpactIncludedWhenPresent(t *testing.T) {
	outcome := TestOutcome{
		TestID:      "test-001",
		DisplayName: "Test Case",
		Status:      StatusPassed,
		SkillImpact: &SkillImpactMetric{
			PassRateWithSkills: 1.0,
			PassRateBaseline:   0.5,
			Delta:              0.5,
			PercentChange:      100.0,
		},
	}

	data, err := json.Marshal(outcome)
	require.NoError(t, err)

	var decoded TestOutcome
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	require.NotNil(t, decoded.SkillImpact)
	assert.InDelta(t, 1.0, decoded.SkillImpact.PassRateWithSkills, 0.001)
	assert.InDelta(t, 0.5, decoded.SkillImpact.PassRateBaseline, 0.001)
	assert.InDelta(t, 0.5, decoded.SkillImpact.Delta, 0.001)
	assert.InDelta(t, 100.0, decoded.SkillImpact.PercentChange, 0.1)
}

func TestEvaluationOutcome_BaselineFieldsSerialization(t *testing.T) {
	outcome := EvaluationOutcome{
		RunID:       "eval-001",
		SkillTested: "test-skill",
		BenchName:   "test-eval",
		IsBaseline:  true,
		BaselineOutcome: &EvaluationOutcome{
			RunID:       "eval-001-baseline",
			SkillTested: "test-skill",
			BenchName:   "test-eval (baseline)",
		},
	}

	data, err := json.Marshal(outcome)
	require.NoError(t, err)

	var decoded EvaluationOutcome
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.True(t, decoded.IsBaseline)
	require.NotNil(t, decoded.BaselineOutcome)
	assert.Equal(t, "eval-001-baseline", decoded.BaselineOutcome.RunID)
}

func TestEvaluationOutcome_BaselineFieldsOmittedWhenFalse(t *testing.T) {
	outcome := EvaluationOutcome{
		RunID:       "eval-001",
		SkillTested: "test-skill",
		BenchName:   "test-eval",
		IsBaseline:  false,
	}

	data, err := json.Marshal(outcome)
	require.NoError(t, err)

	var raw map[string]any
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	_, hasIsBaseline := raw["is_baseline"]
	assert.False(t, hasIsBaseline, "is_baseline should be omitted when false")

	_, hasBaselineOutcome := raw["baseline_outcome"]
	assert.False(t, hasBaselineOutcome, "baseline_outcome should be omitted when nil")
}
