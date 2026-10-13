package jsonrpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/validation"
	"github.com/stretchr/testify/require"
)

func TestScenarioAPI(t *testing.T) {
	dir := t.TempDir()
	files, err := scaffold.ScenarioFiles("inventory", "model", "eval.yaml", "tasks/*.yaml", ".yaml", "repository")
	require.NoError(t, err)
	path := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte(files["eval.yaml"]), 0o600))
	server := newTestServer()
	resp := rpcCall(t, server, "eval.get", map[string]string{"path": path})
	require.Nil(t, resp.Error)
	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalGetResult
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, "inventory", result.Scenario)
	require.Empty(t, result.SkillName)
	resp = rpcCall(t, server, "eval.list", map[string]string{"dir": dir})
	require.Nil(t, resp.Error)
	data, err = json.Marshal(resp.Result)
	require.NoError(t, err)
	var list EvalListResult
	require.NoError(t, json.Unmarshal(data, &list))
	require.Len(t, list.Evals, 1)
	require.Equal(t, "inventory", list.Evals[0].Scenario)
	resp = rpcCall(t, server, "eval.validate", map[string]string{"path": path})
	require.Nil(t, resp.Error)
	data, err = json.Marshal(resp.Result)
	require.NoError(t, err)
	var valid EvalValidateResult
	require.NoError(t, json.Unmarshal(data, &valid))
	require.True(t, valid.Valid)
}

func TestScenarioAPIListConfiguredScaffolds(t *testing.T) {
	for _, template := range []string{"repository", "cli", "mcp"} {
		for _, filename := range []string{"suite.yaml", "suite.yml"} {
			for _, customOutput := range []bool{false, true} {
				t.Run(template+"/"+filename+"/output="+strconv.FormatBool(customOutput), func(t *testing.T) {
					root := t.TempDir()
					dir := filepath.Join(root, "evals", "inventory")
					if customOutput {
						dir = filepath.Join(root, "custom", "nested")
					}
					require.NoError(t, os.WriteFile(filepath.Join(root, ".waza.yaml"), []byte("files:\n  evalFile: "+filename+"\n  taskGlob: cases/*.task.yaml\n  taskFileSuffix: .task.yaml\n"), 0o600))
					files, err := scaffold.ScenarioFiles("inventory", "model", filename, "cases/*.task.yaml", ".task.yaml", template)
					require.NoError(t, err)
					for name, contents := range files {
						path := filepath.Join(dir, filepath.FromSlash(name))
						require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
						require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
					}
					before := readScenarioAPIFiles(t, root)
					path := filepath.Join(dir, filename)
					evalErrors, taskErrors, err := validation.ValidateEvalFile(path)
					require.NoError(t, err)
					require.Empty(t, evalErrors)
					require.Empty(t, taskErrors)
					spec, err := models.LoadEvalSpec(path)
					require.NoError(t, err)
					tasks, err := spec.ResolveTestFiles(dir)
					require.NoError(t, err)
					require.Equal(t, []string{filepath.Join(dir, "cases", "workflow.task.yaml")}, tasks)
					_, err = models.LoadTestCase(tasks[0])
					require.NoError(t, err)
					server := newTestServer()
					get := rpcCall(t, server, "eval.get", map[string]string{"path": path})
					require.Nil(t, get.Error)
					valid := rpcCall(t, server, "eval.validate", map[string]string{"path": path})
					require.Nil(t, valid.Error)
					data, err := json.Marshal(valid.Result)
					require.NoError(t, err)
					var checked EvalValidateResult
					require.NoError(t, json.Unmarshal(data, &checked))
					require.True(t, checked.Valid)
					list := scenarioAPIList(t, server, root)
					require.Equal(t, []EvalSummary{{Path: path, Name: spec.Name, Scenario: "inventory"}}, list.Evals)
					require.Equal(t, before, readScenarioAPIFiles(t, root))
				})
			}
		}
	}
}

func TestScenarioAPIListCustomHeaders(t *testing.T) {
	for _, filename := range []string{"suite.yaml", "suite.yml"} {
		t.Run(filename, func(t *testing.T) {
			for _, tc := range []struct {
				name, header string
				listed       bool
			}{
				{"valid", "scenario: inventory\nschemaVersion: \"2.0\"\n", true},
				{"quoted-null", "scenario: \"null\"\nschemaVersion: \"2.0\"\n", true},
				{"alias", "description: &identity inventory\nscenario: *identity\nschemaVersion: \"2.0\"\n", true},
				{"merge", "<<: &header {scenario: inventory, schemaVersion: \"2.0\"}\n", true},
				{"optional-skill", "scenario: inventory\nschemaVersion: \"2.0\"\nskill: reader\n", true},
				{"null", "scenario: null\nschemaVersion: \"2.0\"\n", false},
				{"number", "scenario: 123\nschemaVersion: \"2.0\"\n", false},
				{"boolean", "scenario: true\nschemaVersion: \"2.0\"\n", false},
				{"blank", "scenario: \" \\t \"\nschemaVersion: \"2.0\"\n", false},
				{"empty", "scenario: \"\"\nschemaVersion: \"2.0\"\n", false},
				{"sequence", "scenario: []\nschemaVersion: \"2.0\"\n", false},
				{"mapping", "scenario: {}\nschemaVersion: \"2.0\"\n", false},
				{"null-alias", "description: &identity null\nscenario: *identity\nschemaVersion: \"2.0\"\n", false},
				{"missing-alias", "scenario: *missing\nschemaVersion: \"2.0\"\n", false},
				{"legacy", "scenario: inventory\nschemaVersion: \"1.4\"\n", false},
				{"versionless", "scenario: inventory\n", false},
				{"null-version", "scenario: inventory\nschemaVersion: null\n", false},
				{"future-version", "scenario: inventory\nschemaVersion: \"2.1\"\n", false},
				{"duplicate-scenario", "scenario: inventory\nscenario: other\nschemaVersion: \"2.0\"\n", false},
				{"duplicate-version", "scenario: inventory\nschemaVersion: \"1.4\"\nschemaVersion: \"2.0\"\n", false},
				{"unknown-body-field", "scenario: inventory\nschemaVersion: \"2.0\"\nunknown_field: true\n", false},
				{"invalid-body", "scenario: inventory\nschemaVersion: \"2.0\"\nconfig:\n  trials_per_task: 0\n", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					dir := t.TempDir()
					path := filepath.Join(dir, filename)
					contents := "name: inventory\ntasks: []\n"
					if tc.name != "invalid-body" {
						contents += "config:\n  trials_per_task: 1\n  timeout_seconds: 300\n"
					}
					require.NoError(t, os.WriteFile(path, []byte(contents+tc.header), 0o600))
					before := readScenarioAPIFiles(t, dir)
					list := scenarioAPIList(t, newTestServer(), dir)
					if tc.listed {
						require.Len(t, list.Evals, 1)
						spec, err := models.LoadEvalSpec(path)
						require.NoError(t, err)
						require.Equal(t, EvalSummary{Path: path, Name: spec.Name, SkillName: spec.SkillName, Scenario: spec.Scenario}, list.Evals[0])
					} else {
						require.Nil(t, list.Evals)
						get := rpcCall(t, newTestServer(), "eval.get", map[string]string{"path": path})
						require.NotNil(t, get.Error)
					}
					require.Equal(t, before, readScenarioAPIFiles(t, dir))
				})
			}
		})
	}
}

func TestScenarioAPIListRetainsCanonicalBehavior(t *testing.T) {
	root := t.TempDir()
	legacy := "schemaVersion: \"1.4\"\nname: legacy\nskill: reader\ntasks: []\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 30\n"
	validScenario := "schemaVersion: \"2.0\"\nscenario: inventory\nname: inventory\ntasks: []\nconfig:\n  trials_per_task: 1\n  timeout_seconds: 30\n"
	files := map[string]string{
		"a/eval.yaml":          legacy,
		"b/suite.yml":          validScenario,
		"c/eval.yml":           "true",
		"d/eval.yaml":          "scenario: null\nschemaVersion: \"2.0\"\n",
		"e/eval.yml":           legacy,
		"task.yaml":            "id: task\nname: Task\ninputs:\n  prompt: Test\n",
		"legacy.yaml":          legacy,
		"metadata.yml":         "settings: true\n",
		"missing-scenario.yml": "schemaVersion: \"2.0\"\nname: not-scenario\n",
		"malformed.yaml":       "[",
		"scenario.json":        `{"scenario":"inventory","schemaVersion":"2.0","name":"inventory","tasks":[]}`,
		"scenario":             validScenario,
		"scenario.txt":         validScenario,
		"scenario.YAML":        validScenario,
	}
	for name, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	}
	before := readScenarioAPIFiles(t, root)
	list := scenarioAPIList(t, newTestServer(), root)
	require.Equal(t, []EvalSummary{
		{Path: filepath.Join(root, "a", "eval.yaml"), Name: "legacy", SkillName: "reader"},
		{Path: filepath.Join(root, "b", "suite.yml"), Name: "inventory", Scenario: "inventory"},
		{Path: filepath.Join(root, "c", "eval.yml"), Name: filepath.Join(root, "c", "eval.yml")},
		{Path: filepath.Join(root, "d", "eval.yaml"), Name: filepath.Join(root, "d", "eval.yaml")},
		{Path: filepath.Join(root, "e", "eval.yml"), Name: "legacy", SkillName: "reader"},
	}, list.Evals)
	require.Equal(t, before, readScenarioAPIFiles(t, root))
}

func TestScenarioAPIListEmptyAndMissingDirectories(t *testing.T) {
	for _, dir := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		resp := rpcCall(t, newTestServer(), "eval.list", map[string]string{"dir": dir})
		require.Nil(t, resp.Error)
		data, err := json.Marshal(resp.Result)
		require.NoError(t, err)
		require.JSONEq(t, `{"evals":null}`, string(data))
	}
}

func TestScenarioAPIListUnreadableCustomCandidate(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "suite.yaml")); err != nil {
		var linkErr *os.LinkError
		if runtime.GOOS == "windows" && errors.As(err, &linkErr) && linkErr.Op == "symlink" &&
			(errors.Is(linkErr.Err, fs.ErrPermission) || errors.Is(linkErr.Err, syscall.Errno(1314))) {
			t.Skipf("symlink privileges unavailable: %v", err)
		}
		require.NoError(t, err)
	}
	list := scenarioAPIList(t, newTestServer(), dir)
	require.Nil(t, list.Evals)
}

func scenarioAPIList(t *testing.T, server *Server, dir string) EvalListResult {
	t.Helper()
	resp := rpcCall(t, server, "eval.list", map[string]string{"dir": dir})
	require.Nil(t, resp.Error)
	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var list EvalListResult
	require.NoError(t, json.Unmarshal(data, &list))
	return list
}

func readScenarioAPIFiles(t *testing.T, dir string) map[string][]byte {
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

func TestScenarioAPIOptionalSkillAndLegacyOmission(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			dir := t.TempDir()
			files, err := scaffold.ScenarioFiles("inventory", "model", "eval.yaml", "tasks/*.yaml", ".yaml", "repository")
			require.NoError(t, err)
			eval := files["eval.yaml"] + "skill: reader\n"
			if legacy {
				eval = strings.ReplaceAll(eval, "scenario: \"inventory\"\n", "")
				eval = strings.ReplaceAll(eval, "schemaVersion: \"2.0\"", "schemaVersion: \"1.4\"")
			}
			path := filepath.Join(dir, "eval.yaml")
			require.NoError(t, os.WriteFile(path, []byte(eval), 0o600))
			server := newTestServer()
			resp := rpcCall(t, server, "eval.get", map[string]string{"path": path})
			require.Nil(t, resp.Error)
			data, err := json.Marshal(resp.Result)
			require.NoError(t, err)
			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &wire))
			require.JSONEq(t, `"reader"`, string(wire["skill"]))
			if legacy {
				require.NotContains(t, wire, "scenario")
			} else {
				require.JSONEq(t, `"inventory"`, string(wire["scenario"]))
			}
		})
	}
}
