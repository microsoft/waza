package models

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestScenarioVersionBoundary(t *testing.T) {
	for _, tt := range []struct {
		name, fields, want string
	}{
		{"generic", "scenario: inventory\nschemaVersion: \"2.0\"\n", ""},
		{"skill-context", "scenario: inventory\nskill: reader\nschemaVersion: \"2.0\"\n", ""},
		{"legacy", "skill: reader\nschemaVersion: \"1.0\"\n", ""},
		{"future-v1", "skill: reader\nschemaVersion: \"1.99\"\n", ""},
		{"old-scenario", "scenario: inventory\nschemaVersion: \"1.5\"\n", "scenario requires"},
		{"missing-version", "scenario: inventory\n", "scenario requires"},
		{"future-minor", "scenario: inventory\nschemaVersion: \"2.1\"\n", "scenario requires"},
		{"future-major", "scenario: inventory\nschemaVersion: \"3.0\"\n", "scenario requires"},
		{"no-scenario-v2", "skill: reader\nschemaVersion: \"2.0\"\n", "schema major"},
		{"empty-scenario", "scenario: \"\"\nschemaVersion: \"2.0\"\n", "non-empty"},
		{"blank-scenario", "scenario: \"  \"\nschemaVersion: \"2.0\"\n", "non-empty"},
		{"required-without-context", "scenario: inventory\nschemaVersion: \"2.0\"\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 30\n  required_skills: [reader]\n", "explicit skill_directories"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := "name: suite\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 30\n" + tt.fields
			// Avoid duplicate config mappings for the invalid-context case.
			if tt.name == "required-without-context" {
				data = "name: suite\n" + tt.fields
			}
			path := filepath.Join(t.TempDir(), "eval.yaml")
			require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
			spec, err := LoadEvalSpec(path)
			if tt.want != "" {
				require.ErrorContains(t, err, tt.want)
				return
			}
			require.NoError(t, err)
			if spec.Scenario != "" {
				require.Equal(t, ScenarioSchemaVersion, spec.SchemaVersion)
			}
		})
	}
}

func TestScenarioTaskDiscovery(t *testing.T) {
	for _, tt := range []struct {
		name      string
		scenario  string
		skill     string
		evalPaths []string
		taskPaths []string
		disabled  []string
		want      bool
	}{
		{name: "legacy-ambient"},
		{name: "generic", scenario: "inventory", want: true},
		{name: "explicit-skill", scenario: "inventory", skill: "reader"},
		{name: "explicit-directories", scenario: "inventory", evalPaths: []string{"skills"}},
		{name: "explicit-task", scenario: "inventory", taskPaths: []string{"skills"}},
		{name: "empty-task-override", scenario: "inventory", skill: "reader", evalPaths: []string{"skills"}, taskPaths: []string{}, want: true},
		{name: "legacy-empty-inherits", evalPaths: []string{"skills"}, taskPaths: []string{}},
		{name: "no-skills-wins", scenario: "inventory", taskPaths: []string{"skills"}, disabled: []string{"*"}, want: true},
		{name: "none-wins", scenario: "inventory", evalPaths: []string{"none"}, taskPaths: []string{"skills"}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := &EvalSpec{Scenario: tt.scenario, SkillName: tt.skill, Config: Config{SkillPaths: tt.evalPaths, DisabledSkills: tt.disabled}}
			require.Equal(t, tt.want, spec.SkillsDisabledForTask(tt.taskPaths))
		})
	}
	var task TestCase
	require.NoError(t, yaml.Unmarshal([]byte("skill_directories: []\ninputs:\n  prompt: hello\n"), &task))
	require.NotNil(t, task.SkillPaths)
}
