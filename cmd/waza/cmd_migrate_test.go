package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/validation"
	"github.com/stretchr/testify/require"
)

func TestMigrateCommandNoOpForCurrentMajor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte("schemaVersion: \"1.0\"\n"), 0o644))

	var out bytes.Buffer
	err := runMigrate(&out, path)

	require.NoError(t, err)
	require.Contains(t, out.String(), "compatible with waza schemaVersion 1.4")
}

func TestMigrateCommandRejectsIncompatibleMajor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schemaVersion":"2.0","tasks":[]}`), 0o644))

	var out bytes.Buffer
	err := runMigrate(&out, path)

	require.Error(t, err)
	require.Contains(t, err.Error(), `waza migrate <file>`)
	require.Empty(t, out.String())
}

func TestMigrateCommandDefaultsMissingSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"eval_id":"run-1"}`), 0o644))

	var out bytes.Buffer
	err := runMigrate(&out, path)

	require.NoError(t, err)
	require.Contains(t, out.String(), "already compatible with schemaVersion 1.4")
}

func TestMigrateCommandReportsArtifactVersionsWithoutRewriting(t *testing.T) {
	for _, tc := range []struct {
		name, filename, contents, want string
	}{
		{"unversioned-results", "results.json", `{"tasks":[]}`,
			"results.json is already compatible with schemaVersion 1.4 (legacy reader default); current result writer schemaVersion 1.7; no migration needed.\n"},
		{"legacy-results", "history.json", `{"schemaVersion":"1.4","tasks":[]}`,
			"results.json is already compatible with schemaVersion 1.4 (legacy reader default); current result writer schemaVersion 1.7; no migration needed.\n"},
		{"prior-writer-results", "results.json", `{"schemaVersion":"1.5","tasks":[]}`,
			"results.json uses schemaVersion 1.5, which is compatible with waza schemaVersion 1.7; no migration needed.\n"},
		{"previous-results", "results.json", `{"schemaVersion":"1.6","tasks":[]}`,
			"results.json uses schemaVersion 1.6, which is compatible with waza schemaVersion 1.7; no migration needed.\n"},
		{"current-results", "results.json", `{"schemaVersion":"1.7","tasks":[]}`,
			"results.json is already compatible with schemaVersion 1.7; no migration needed.\n"},
		{"older-results", "history.json", `{"schemaVersion":"1.0","tasks":[]}`,
			"results.json uses schemaVersion 1.0, which is compatible with waza schemaVersion 1.7; no migration needed.\n"},
		{"future-minor-results", "results.json", `{"schemaVersion":"1.99","tasks":[]}`,
			"results.json uses schemaVersion 1.99, which is compatible with waza schemaVersion 1.7; no migration needed.\n"},
		{"legacy-eval", "eval.yaml", "schemaVersion: \"1.4\"\n",
			"eval.yaml is already compatible with schemaVersion 1.4; no migration needed.\n"},
		{"older-eval", "eval.yml", "schemaVersion: \"1.0\"\n",
			"eval.yaml uses schemaVersion 1.0, which is compatible with waza schemaVersion 1.4; no migration needed.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := migrateArtifactUnchanged(t, tc.filename, tc.contents)
			require.NoError(t, err)
			require.Equal(t, tc.want, output)
		})
	}
}

func TestMigrateCommandRejectsUnknownJSONShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schemaVersion":"1.0","name":"not-results"}`), 0o644))

	var out bytes.Buffer
	err := runMigrate(&out, path)

	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported JSON schema artifact")
	require.Contains(t, err.Error(), "expected a results.json object")
	require.Empty(t, out.String())
}

func TestMigrateCommandDetectsResultsJSONByShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom-name.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schemaVersion":"1.0","tasks":[]}`), 0o644))

	var out bytes.Buffer
	err := runMigrate(&out, path)

	require.NoError(t, err)
	require.Contains(t, out.String(), "results.json uses schemaVersion 1.0")
}

func TestMigrateCommandMissingFile(t *testing.T) {
	var out bytes.Buffer
	err := runMigrate(&out, filepath.Join(t.TempDir(), "missing.json"))

	require.Error(t, err)
	require.Contains(t, err.Error(), "reading")
}

func TestMigrateScenarioHeaders(t *testing.T) {
	for _, filename := range []string{"eval.yaml", "eval.yml", "suite.yaml", "suite.yml"} {
		t.Run(filename, func(t *testing.T) {
			for _, tc := range []struct {
				name, header, wantError string
			}{
				{"valid", "scenario: inventory\nschemaVersion: \"2.0\"\n", ""},
				{"quoted-null", "scenario: \"null\"\nschemaVersion: \"2.0\"\n", ""},
				{"alias", "description: &identity inventory\nscenario: *identity\nschemaVersion: \"2.0\"\n", ""},
				{"merged", "<<: &header {scenario: inventory, schemaVersion: \"2.0\"}\n", ""},
				{"merged-legacy-version", "<<: &header {scenario: inventory, schemaVersion: \"1.4\"}\n", "scenario requires explicit schemaVersion 2.0"},
				{"null", "scenario: null\nschemaVersion: \"2.0\"\n", "scenario must be a non-empty string"},
				{"legacy-null", "scenario: null\nschemaVersion: \"1.4\"\n", "scenario must be a non-empty string"},
				{"number", "scenario: 123\nschemaVersion: \"2.0\"\n", "scenario must be a non-empty string"},
				{"boolean", "scenario: true\nschemaVersion: \"2.0\"\n", "scenario must be a non-empty string"},
				{"blank", "scenario: \" \\t \"\nschemaVersion: \"2.0\"\n", "scenario must be a non-empty string"},
				{"empty", "scenario: \"\"\nschemaVersion: \"2.0\"\n", "scenario must be a non-empty string"},
				{"sequence", "scenario: []\nschemaVersion: \"2.0\"\n", "scenario must be a non-empty string"},
				{"mapping", "scenario: {}\nschemaVersion: \"2.0\"\n", "scenario must be a non-empty string"},
				{"null-alias", "description: &identity null\nscenario: *identity\nschemaVersion: \"2.0\"\n", "scenario must be a non-empty string"},
				{"missing-alias", "scenario: *missing\nschemaVersion: \"2.0\"\n", "unknown anchor"},
				{"legacy-version", "scenario: inventory\nschemaVersion: \"1.4\"\n", "scenario requires explicit schemaVersion 2.0"},
				{"missing-version", "scenario: inventory\n", "scenario requires explicit schemaVersion 2.0"},
				{"null-version", "scenario: inventory\nschemaVersion: null\n", "scenario requires explicit schemaVersion 2.0"},
				{"future-minor", "scenario: inventory\nschemaVersion: \"2.1\"\n", "scenario requires explicit schemaVersion 2.0"},
				{"future-major", "scenario: inventory\nschemaVersion: \"3.0\"\n", "scenario requires explicit schemaVersion 2.0"},
				{"duplicate-scenario", "scenario: inventory\nscenario: other\nschemaVersion: \"2.0\"\n", "already defined"},
				{"duplicate-version", "scenario: inventory\nschemaVersion: \"1.4\"\nschemaVersion: \"2.0\"\n", "already defined"},
				{"strict-fields", "scenario: inventory\nschemaVersion: \"2.0\"\nunknown_field: true\n", "field unknown_field not found"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					output, err := migrateArtifactUnchanged(t, filename, "name: inventory\ntasks: []\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 300\n"+tc.header)
					if tc.wantError != "" {
						require.ErrorContains(t, err, tc.wantError)
						require.Empty(t, output)
					} else {
						require.NoError(t, err)
						require.Contains(t, output, "supported scenario eval with schemaVersion 2.0; no migration needed.")
					}
				})
			}
		})
	}
}

func TestMigrateScenarioConfiguredScaffolds(t *testing.T) {
	for _, template := range []string{"repository", "cli", "mcp"} {
		for _, filename := range []string{"suite.yaml", "suite.yml"} {
			for _, customOutput := range []bool{false, true} {
				t.Run(template+"/"+filename+"/output="+strconv.FormatBool(customOutput), func(t *testing.T) {
					t.Chdir(t.TempDir())
					require.NoError(t, os.WriteFile(".waza.yaml", []byte("files:\n  evalFile: "+filename+"\n  taskGlob: cases/*.task.yaml\n  taskFileSuffix: .task.yaml\n"), 0o600))
					path := filepath.Join("evals", "inventory", filename)
					args := []string{"new", "eval", "inventory", "--scenario", "--template", template}
					if customOutput {
						path = filepath.Join("custom", "inventory", filename)
						args = append(args, "--output", path)
					}
					root := newRootCommand()
					root.SetOut(&bytes.Buffer{})
					root.SetErr(&bytes.Buffer{})
					root.SetArgs(args)
					require.NoError(t, root.Execute())
					evalErrors, taskErrors, err := validation.ValidateEvalFile(path)
					require.NoError(t, err)
					require.Empty(t, evalErrors)
					require.Empty(t, taskErrors)
					spec, err := models.LoadEvalSpec(path)
					require.NoError(t, err)
					require.Equal(t, "inventory", spec.Scenario)
					tasks, err := spec.ResolveTestFiles(filepath.Dir(path))
					require.NoError(t, err)
					require.Equal(t, []string{filepath.Join(filepath.Dir(path), "cases", "workflow.task.yaml")}, tasks)
					_, err = models.LoadTestCase(tasks[0])
					require.NoError(t, err)
					output, err := migrateExistingFileUnchanged(t, path)
					require.NoError(t, err)
					require.Contains(t, output, path+" is a supported scenario eval with schemaVersion 2.0")
				})
			}
		}
	}
}

func TestMigrateScenarioRetainsLegacyClassification(t *testing.T) {
	for _, tc := range []struct {
		name, filename, contents, wantOutput, wantError string
	}{
		{"canonical-missing-version", "eval.yaml", "{}", "already compatible with schemaVersion 1.4", ""},
		{"canonical-older-version", "eval.yml", "schemaVersion: \"1.0\"\n", "compatible with waza schemaVersion 1.4", ""},
		{"canonical-uninspected-fields", "eval.yaml", "schemaVersion: \"1.4\"\nunknown_field: true\n", "already compatible with schemaVersion 1.4", ""},
		{"canonical-scalar-diagnostic", "eval.yaml", "true", "", `cannot unmarshal !!bool ` + "`true`" + ` into struct { SchemaVersion string "yaml:\"schemaVersion\"" }`},
		{"canonical-sequence-diagnostic", "eval.yml", "[]", "", `cannot unmarshal !!seq into struct { SchemaVersion string "yaml:\"schemaVersion\"" }`},
		{"custom-legacy", "suite.yml", "schemaVersion: \"1.4\"\nname: inventory\ntasks: []\n", "", "unsupported schema artifact"},
		{"custom-task", "suite.yaml", "id: task\nname: Task\ninputs:\n  prompt: Test\n", "", "unsupported schema artifact"},
		{"custom-unrelated", "suite.yml", "settings: true\n", "", "unsupported schema artifact"},
		{"custom-future-absent", "suite.yaml", "schemaVersion: \"2.0\"\nname: inventory\ntasks: []\n", "", "unsupported schema artifact"},
		{"other-extension", "suite.txt", "scenario: inventory\nschemaVersion: \"2.0\"\n", "", "unsupported schema artifact"},
		{"extensionless", "suite", "scenario: inventory\nschemaVersion: \"2.0\"\n", "", "unsupported schema artifact"},
		{"scenario-json", "suite.json", `{"scenario":"inventory","schemaVersion":"2.0","tasks":[]}`, "", "waza migrate <file>"},
		{"results-missing-version", "custom.json", `{"eval_id":"run-1"}`, "already compatible with schemaVersion 1.4", ""},
		{"results-scenario-metadata", "custom.json", `{"schemaVersion":"1.4","eval_id":"run-1","scenario":null}`, "already compatible with schemaVersion 1.4", ""},
		{"results-future", "results.json", `{"schemaVersion":"2.0","tasks":[]}`, "", "waza migrate <file>"},
		{"unknown-json", "settings.json", `{"schemaVersion":"1.0","name":"not-results"}`, "", "unsupported JSON schema artifact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := migrateArtifactUnchanged(t, tc.filename, tc.contents)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Empty(t, output)
			} else {
				require.NoError(t, err)
				require.Contains(t, output, tc.wantOutput)
			}
		})
	}
}

func migrateArtifactUnchanged(t *testing.T, filename, contents string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return migrateExistingFileUnchanged(t, path)
}

func migrateExistingFileUnchanged(t *testing.T, path string) (string, error) {
	t.Helper()
	before := readMigrateFiles(t, filepath.Dir(path))
	var output bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"migrate", path})
	migrateErr := cmd.Execute()
	after := readMigrateFiles(t, filepath.Dir(path))
	require.Equal(t, before, after)
	return output.String(), migrateErr
}

func readMigrateFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = data
		return nil
	})
	require.NoError(t, err)
	return files
}
