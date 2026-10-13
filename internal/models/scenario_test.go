package models

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
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
		{"future-skill-context", "scenario: inventory\nskill: reader\nschemaVersion: \"2.1\"\n", "scenario requires"},
		{"no-scenario-v2", "skill: reader\nschemaVersion: \"2.0\"\n", "schema major"},
		{"empty-scenario", "scenario: \"\"\nschemaVersion: \"2.0\"\n", "non-empty"},
		{"blank-scenario", "scenario: \"  \"\nschemaVersion: \"2.0\"\n", "non-empty"},
		{"invalid-scenario-type", "scenario: {name: inventory}\nschemaVersion: \"2.0\"\n", "non-empty string"},
		{"unknown-scenario-field", "scenario: inventory\nschemaVersion: \"2.0\"\ncontext: {agent: reader}\n", "field context not found"},
		{"legacy-unknown-field", "skill: reader\nschemaVersion: \"1.99\"\nfuture_metadata: safe\n", ""},
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

func TestScenarioPresenceNativeSchemaMatrix(t *testing.T) {
	var document any
	require.NoError(t, json.Unmarshal([]byte(schemas.EvalSchemaJSON), &document))
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("eval.schema.json", document))
	schema, err := compiler.Compile("eval.schema.json")
	require.NoError(t, err)
	for _, format := range []string{"yaml", "json"} {
		for _, version := range []string{"", "1.0", "2.0", "2.1"} {
			for _, tc := range []struct {
				name    string
				present bool
				value   any
			}{
				{"absent", false, nil},
				{"null", true, nil},
				{"number", true, 42},
				{"boolean", true, true},
				{"object", true, map[string]any{}},
				{"array", true, []any{"recovery"}},
				{"blank", true, " \t\n"},
				{"string", true, "recovery"},
			} {
				t.Run(format+"/"+version+"/"+tc.name, func(t *testing.T) {
					fields := map[string]any{
						"name": "presence", "skill": "reader",
						"config":  map[string]any{"executor": "mock", "model": "harness", "trials_per_task": 1, "timeout_seconds": 60},
						"metrics": []any{map[string]any{"name": "accuracy", "weight": 1, "threshold": 0.8}},
						"tasks":   []any{"task.yaml"},
					}
					if version != "" {
						fields["schemaVersion"] = version
					}
					if tc.present {
						fields["scenario"] = tc.value
					}
					var data []byte
					var err error
					if format == "yaml" {
						data, err = yaml.Marshal(fields)
					} else {
						data, err = json.Marshal(fields)
					}
					require.NoError(t, err)
					path := filepath.Join(t.TempDir(), "eval."+format)
					require.NoError(t, os.WriteFile(path, data, 0o600))
					gotVersion, gotScenario, classErr := ClassifyEvalSpec(data, path)
					loaded, loadErr := LoadEvalSpec(path)
					valid := (!tc.present && (version == "" || version == "1.0")) ||
						(tc.name == "string" && version == "2.0")
					if valid {
						require.NoError(t, classErr)
						require.NoError(t, loadErr)
						require.NoError(t, schema.Validate(fields))
						wantVersion := version
						if wantVersion == "" {
							wantVersion = CurrentSchemaVersion
						}
						wantScenario := ""
						if tc.present {
							wantScenario = "recovery"
						}
						require.Equal(t, wantVersion, gotVersion)
						require.Equal(t, wantScenario, gotScenario)
						require.Equal(t, gotVersion, loaded.SchemaVersion)
						require.Equal(t, gotScenario, loaded.Scenario)
					} else {
						require.Error(t, classErr)
						require.EqualError(t, loadErr, classErr.Error())
						require.Nil(t, loaded)
						require.Empty(t, gotVersion)
						require.Empty(t, gotScenario)
						if !tc.present && version == "2.1" {
							require.NoError(t, schema.Validate(fields), "schema syntax does not replace the native supported-major fence")
						} else {
							require.Error(t, schema.Validate(fields))
						}
						if tc.present {
							require.Contains(t, classErr.Error(), "remove the scenario field for a legacy suite")
							require.Contains(t, classErr.Error(), "schemaVersion 2.0")
						}
					}
				})
			}
		}
	}
}

func TestScenarioAbsentWithoutSkillNativeBoundary(t *testing.T) {
	for _, version := range []string{"", "1.0"} {
		t.Run(version, func(t *testing.T) {
			data := "name: legacy\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 30\n"
			if version != "" {
				data += "schemaVersion: \"" + version + "\"\n"
			}
			path := filepath.Join(t.TempDir(), "eval.yaml")
			require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
			gotVersion, gotScenario, err := ClassifyEvalSpec([]byte(data), path)
			require.NoError(t, err)
			loaded, err := LoadEvalSpec(path)
			require.NoError(t, err)
			require.Empty(t, gotScenario)
			require.Empty(t, loaded.SkillName)
			require.Equal(t, gotVersion, loaded.SchemaVersion)
		})
	}
}

func TestScenarioPresenceAliases(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
	}{
		{"string", `"inventory"`, "inventory"},
		{"quoted-number", `"42"`, "42"},
		{"null", "null", ""},
		{"number", "42", ""},
		{"blank", `"  "`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := "name: &identity " + tc.value + "\nscenario: *identity\nschemaVersion: \"2.0\"\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 30\n"
			path := filepath.Join(t.TempDir(), "eval.yaml")
			require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
			version, scenario, classErr := ClassifyEvalSpec([]byte(data), path)
			loaded, loadErr := LoadEvalSpec(path)
			if tc.want == "" {
				require.ErrorContains(t, classErr, "non-empty string")
				require.EqualError(t, loadErr, classErr.Error())
				require.Nil(t, loaded)
				return
			}
			require.NoError(t, classErr)
			require.NoError(t, loadErr)
			require.Equal(t, ScenarioSchemaVersion, version)
			require.Equal(t, tc.want, scenario)
			require.Equal(t, scenario, loaded.Scenario)
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
		{name: "whitespace-target", scenario: "inventory", skill: " \t ", want: true},
		{name: "legacy-whitespace-target", skill: " \t "},
		{name: "explicit-skill", scenario: "inventory", skill: "reader"},
		{name: "explicit-directories", scenario: "inventory", evalPaths: []string{"skills"}},
		{name: "explicit-task", scenario: "inventory", taskPaths: []string{"skills"}},
		{name: "last-eval-path-disabled", scenario: "inventory", evalPaths: []string{"skills/foo"}, disabled: []string{"foo"}, want: true},
		{name: "path-disabled-by-full-name", scenario: "inventory", evalPaths: []string{"skills/foo"}, disabled: []string{"skills/foo"}, want: true},
		{name: "remaining-eval-path", scenario: "inventory", evalPaths: []string{"skills/foo", "skills/bar"}, disabled: []string{"foo"}},
		{name: "task-opt-in-after-filter", scenario: "inventory", evalPaths: []string{"skills/foo"}, disabled: []string{"foo"}, taskPaths: []string{"skills/bar"}},
		{name: "named-target-after-filter", scenario: "inventory", skill: "reader", evalPaths: []string{"skills/foo"}, disabled: []string{"foo"}},
		{name: "legacy-filtered-path-keeps-discovery", evalPaths: []string{"skills/foo"}, disabled: []string{"foo"}},
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

func TestScenarioRequiresEnabledSkillPaths(t *testing.T) {
	for _, tt := range []struct {
		name     string
		scenario string
		disabled []string
		wantErr  bool
	}{
		{name: "enabled", scenario: "inventory"},
		{name: "disabled-name", scenario: "inventory", disabled: []string{"foo"}, wantErr: true},
		{name: "disabled-path", scenario: "inventory", disabled: []string{"skills/foo"}, wantErr: true},
		{name: "disabled-all", scenario: "inventory", disabled: []string{"*"}, wantErr: true},
		{name: "legacy-preserved", disabled: []string{"foo"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := &EvalSpec{Scenario: tt.scenario, SchemaVersion: ScenarioSchemaVersion, SkillName: " \t ", Config: Config{
				TrialsPerTask: 1, TimeoutSec: 30,
				SkillPaths: []string{"skills/foo"}, DisabledSkills: tt.disabled, RequiredSkills: []string{"foo"},
			}}
			err := spec.Validate()
			if tt.wantErr {
				require.ErrorContains(t, err, "explicit skill_directories")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestScenarioRejectsUnknownConfigField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte("name: suite\nscenario: inventory\nschemaVersion: \"2.0\"\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 30\n  future_setting: true\n"), 0o600))
	_, err := LoadEvalSpec(path)
	require.ErrorContains(t, err, "field future_setting not found")
}
