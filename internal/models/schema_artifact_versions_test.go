package models

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/schemas"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestArtifactVersionSeparation(t *testing.T) {
	require.Equal(t, "1.4", CurrentSchemaVersion)
	require.Equal(t, "1.5", CurrentResultSchemaVersion)
	require.Equal(t, "2.0", ScenarioSchemaVersion)
	require.Equal(t, "1.4", defaultSchemaVersion(""))

	for _, tc := range []struct {
		name, fields, want, wantError string
	}{
		{"legacy-default", "skill: reader\n", "1.4", ""},
		{"legacy-pinned", "skill: reader\nschemaVersion: \"1.4\"\n", "1.4", ""},
		{"legacy-forward-minor", "skill: reader\nschemaVersion: \"1.5\"\n", "1.5", ""},
		{"scenario", "scenario: inventory\nschemaVersion: \"2.0\"\n", "2.0", ""},
		{"scenario-with-result-version", "scenario: inventory\nschemaVersion: \"1.5\"\n", "", "scenario requires"},
		{"scenario-without-version", "scenario: inventory\n", "", "scenario requires"},
		{"scenario-future-minor", "scenario: inventory\nschemaVersion: \"2.1\"\n", "", "scenario requires"},
		{"legacy-with-scenario-version", "skill: reader\nschemaVersion: \"2.0\"\n", "", "schema major"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("name: versions\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 30\n" + tc.fields)
			path := filepath.Join(t.TempDir(), "eval.yaml")
			require.NoError(t, os.WriteFile(path, data, 0o600))
			spec, err := LoadEvalSpec(path)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Nil(t, spec)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, spec.SchemaVersion)
		})
	}

	var schema struct {
		Properties struct {
			SchemaVersion struct {
				Default string `json:"default"`
			} `json:"schemaVersion"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(schemas.EvalSchemaJSON), &schema))
	require.Equal(t, "1.4", schema.Properties.SchemaVersion.Default)

	// Task YAML never negotiated an independent schemaVersion. The writer
	// bump must not introduce one or alter its existing loading defaults.
	path := filepath.Join(t.TempDir(), "task.yaml")
	require.NoError(t, os.WriteFile(path, []byte("id: legacy\nname: Legacy\ninputs:\n  prompt: Read inventory.\n"), 0o600))
	task, err := LoadTestCase(path)
	require.NoError(t, err)
	require.Equal(t, "legacy", task.TestID)
	require.Nil(t, task.Active)
	require.Nil(t, task.SkillPaths)
	taskData, err := yaml.Marshal(task)
	require.NoError(t, err)
	require.NotContains(t, string(taskData), "schemaVersion")
}

func TestHistoricalResultVersionsSurviveWriterVersionBump(t *testing.T) {
	for _, version := range []string{"", "1.0", "1.1", "1.4", "1.5", "1.99"} {
		t.Run("version="+version, func(t *testing.T) {
			outcome, err := ParseEvaluationOutcome([]byte(minimalOutcomeJSON(version)), "historical.json")
			require.NoError(t, err)
			want := version
			if want == "" {
				want = "1.4"
			}
			require.Equal(t, want, outcome.SchemaVersion)
			data, err := json.Marshal(outcome)
			require.NoError(t, err)
			var wire EvaluationOutcome
			require.NoError(t, json.Unmarshal(data, &wire))
			require.Equal(t, want, wire.SchemaVersion)
			require.NotContains(t, string(data), "pass_rate_ci")
			require.NotContains(t, string(data), "success_rate_ci")
			require.NotContains(t, string(data), "pass_hat_k")
		})
	}
	for _, version := range []string{"2.0", "2.1", "3.0"} {
		t.Run("reject-result-version="+version, func(t *testing.T) {
			_, err := ParseEvaluationOutcome([]byte(minimalOutcomeJSON(version)), "historical.json")
			require.ErrorContains(t, err, "waza migrate")
		})
	}

	data, err := json.Marshal(EvaluationOutcome{RunID: "new-result"})
	require.NoError(t, err)
	var emitted EvaluationOutcome
	require.NoError(t, json.Unmarshal(data, &emitted))
	require.Equal(t, "1.5", emitted.SchemaVersion)
}
