package preflight

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"github.com/stretchr/testify/require"
)

const validEval = `schemaVersion: "1.4"
name: offline-example
skill: example
config:
  executor: mock
  model: harness
  trials_per_task: 1
  timeout_seconds: 10
metrics:
  - name: accuracy
    weight: 1
    threshold: 0.8
tasks:
  - task.yaml
`

const validTask = `id: task
name: Task
inputs:
  prompt: Inspect local evidence.
graders:
  - name: state
    type: text
    config:
      contains: ["ready"]
requirements:
  - id: observable
    category: outcome
    description: The selected check observes the declared artifact.
    checks:
      - scope: task
        grader: state
`

func writeSuite(t *testing.T, eval, task string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte(eval), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(task), 0600))
	return path, dir
}

func hasDiagnostic(r *Report, code string, state State) bool {
	for _, d := range r.Diagnostics {
		if d.Code == code && d.State == state {
			return true
		}
	}
	return false
}

func TestInspectValidDeterministicAndDescriptive(t *testing.T) {
	path, _ := writeSuite(t, validEval, validTask)
	first := Inspect(path, Options{})
	require.False(t, first.Failed(true))
	require.True(t, first.Complete)
	require.Len(t, first.Tasks, 1)
	require.Equal(t, Verified, first.Tasks[0].Requirements[0].State)
	require.Equal(t, ReportKind, first.Kind)
	require.Equal(t, "1.0", first.SchemaVersion)
	require.Contains(t, first.Tasks[0].Requirements[0].Meaning, "not enforced or assessed")
	require.Equal(t, first, Inspect(path, Options{}))
}

func TestInspectInvalidConfigurations(t *testing.T) {
	tests := []struct {
		name, eval, task, code string
	}{
		{"unmatched pattern", strings.ReplaceAll(validEval, "task.yaml", "absent*.yaml"), validTask, "tasks.pattern"},
		{"malformed pattern", strings.ReplaceAll(validEval, "task.yaml", "["), validTask, "eval.configuration"},
		{"invalid eval schema", strings.ReplaceAll(validEval, "accuracy", ""), validTask, "eval.schema"},
		{"timeout", strings.ReplaceAll(validEval, "timeout_seconds: 10", "timeout_seconds: 0"), validTask, "eval.configuration"},
		{"task missing name", validEval, strings.ReplaceAll(validTask, "name: Task\n", ""), "task.schema"},
		{"duplicate task id", strings.ReplaceAll(validEval, "  - task.yaml", "  - task.yaml\n  - task.yaml"), validTask, "task.id"},
		{"prompt conflict", validEval, strings.ReplaceAll(validTask, "  prompt:", "  prompt_file: absent.txt\n  prompt:"), "task.configuration"},
		{"missing prompt file", validEval, strings.ReplaceAll(validTask, "  prompt: Inspect local evidence.", "  prompt_file: absent.txt"), "task.configuration"},
		{"unsafe resource", validEval, strings.ReplaceAll(validTask, "  prompt:", "  files: [{path: ../outside.txt}]\n  prompt:"), "resource.path"},
		{"missing resource", validEval, strings.ReplaceAll(validTask, "  prompt:", "  files: [{path: missing.txt}]\n  prompt:"), "resource.file"},
		{"missing instruction", validEval, validTask + "\ninstruction_files: [absent.md]\n", "instruction.file"},
		{"unsafe instruction", validEval, validTask + "\ninstruction_files: [../absent.md]\n", "instruction.path"},
		{"missing context", validEval, validTask + "\ncontext_dir: /nonexistent/offline-preflight\n", "context.directory"},
		{"missing skill directory", validEval, validTask + "\nskill_directories: [/nonexistent/offline-preflight]\n", "skill.directory"},
		{"unknown requirement reference", validEval, strings.ReplaceAll(validTask, "grader: state", "grader: unknown"), "requirement.invalid"},
		{"duplicate declaration", validEval, strings.ReplaceAll(validTask, "requirements:", "  - name: state\n    type: text\n    config: {}\nrequirements:"), "requirement.invalid"},
		{"invalid regex", validEval, strings.ReplaceAll(validTask, "contains: [\"ready\"]", "regex_match: ['[']"), "grader.configuration"},
		{"invalid checkpoint", validEval, validTask + "\ncheckpoints: [{after_turn: 2, graders: [{name: state, type: text, config: {}}]}]\n", "task.configuration"},
		{"bad lock", validEval + "\ngraders: [{name: external, ref: 'github.com/example/checks@v1#check'}]\n", validTask, "grader.lock"},
		{"malformed context fixture", validEval, strings.ReplaceAll(validTask, "  prompt:", "  context: {fixture: 42}\n  prompt:"), "context.fixture"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, _ := writeSuite(t, tt.eval, tt.task)
			r := Inspect(path, Options{})
			require.True(t, r.Failed(false), "%+v", r.Diagnostics)
			require.True(t, hasDiagnostic(r, tt.code, Invalid), "%+v", r.Diagnostics)
		})
	}
}

func TestInspectMissingEvalAndUnreadableTask(t *testing.T) {
	r := Inspect(filepath.Join(t.TempDir(), "absent.yaml"), Options{})
	require.False(t, r.Complete)
	require.True(t, r.Failed(false))
	require.True(t, hasDiagnostic(r, "eval.read", Invalid))
	path, dir := writeSuite(t, strings.ReplaceAll(validEval, "task.yaml", "directory"), validTask)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "directory"), 0700))
	r = Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "task.read", Invalid))
}

func TestInspectCrossScopeResultCollisionIsUnresolved(t *testing.T) {
	path, _ := writeSuite(t, validEval+"\ngraders: [{name: state, type: text, config: {contains: [ready]}}]\n", validTask)
	r := Inspect(path, Options{})
	require.Equal(t, Verified, r.Tasks[0].Requirements[0].State)
	require.True(t, hasDiagnostic(r, "grader.result_collision", Unresolved))
	require.False(t, r.Failed(false), "%+v", r.Diagnostics)
	require.True(t, r.Failed(true))
	require.True(t, r.Complete)
}

func TestInspectUncoveredAndDisabledTasks(t *testing.T) {
	path, _ := writeSuite(t, validEval, strings.ReplaceAll(validTask, "    checks:\n      - scope: task\n        grader: state", "    checks: []")+"\nenabled: false\n")
	r := Inspect(path, Options{})
	require.False(t, r.Tasks[0].Enabled)
	require.Equal(t, Unresolved, r.Tasks[0].Requirements[0].State)
	require.True(t, hasDiagnostic(r, "requirement.uncovered", Unresolved))
	require.False(t, r.Failed(false))
}

func TestInspectCSVParity(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "tasks:\n  - task.yaml", "tasks: []\ntasks_from: cases.csv\nrange: [2, 2]\ninputs: {target: fixture}")
	path, dir := writeSuite(t, eval, validTask)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cases.csv"), []byte("id,name,prompt\nfirst,First,Inspect {{.Vars.target}}\nsecond,Second,Inspect {{.Vars.target}}\n"), 0600))
	r := Inspect(path, Options{})
	require.False(t, r.Failed(true), "%+v", r.Diagnostics)
	require.Len(t, r.Tasks, 1)
	require.Equal(t, "second", r.Tasks[0].ID)
	require.Empty(t, r.Tasks[0].Requirements, "CSV has no implicit assurance or invented requirements")
}

func TestInspectMockOverridesAndSecretFreeOutput(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk") + `
command_mocks:
  - name: offline-tool
    responses:
      - args: []
        fixture: absent.txt
        environment: {TOKEN: super-secret-payload}
config_unused: super-secret-payload
`
	task := validTask + "\ncommand_mocks: []\n"
	path, _ := writeSuite(t, eval, task)
	r := Inspect(path, Options{})
	for _, dep := range r.Dependencies {
		require.NotEqual(t, "command", dep.Kind, "explicit [] disables inherited command mocks")
	}
	require.False(t, hasDiagnostic(r, "mock.command_fixture", Invalid))
	data, err := json.Marshal(r)
	require.NoError(t, err)
	require.NotContains(t, string(data), "super-secret-payload")
}

func TestInspectMCPMockAndLiveModes(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk")
	eval = strings.ReplaceAll(eval, "  model: harness", "  model: harness\n  mcp_servers:\n    mocked: {command: never-run}\n    live: {type: http, url: 'https://invalid.example', headers: {Authorization: secret-token}}")
	eval += `
mcp_mocks:
  - name: mocked
    tools:
      inspect:
        responses: [{return: {state: ready}}]
`
	path, _ := writeSuite(t, eval, validTask)
	r := Inspect(path, Options{})
	require.False(t, r.Failed(false), "%+v", r.Diagnostics)
	require.Contains(t, r.Dependencies, Dependency{Kind: "mcp", Name: "mocked", Mode: "mocked", State: Verified})
	require.Contains(t, r.Dependencies, Dependency{Kind: "mcp", Name: "live", Mode: "live", State: Unresolved})
	require.True(t, hasDiagnostic(r, "mock.mcp_override", Verified))
	data, err := json.Marshal(r)
	require.NoError(t, err)
	require.NotContains(t, string(data), "secret-token")
	require.NotContains(t, string(data), "invalid.example")
}

func TestInspectCommandFixtureInvalidIsNotVerified(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk") + `
command_mocks:
  - name: offline-tool
    responses: [{args: [], fixture: absent.txt}]
`
	path, _ := writeSuite(t, eval, validTask)
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "mock.command_fixture", Invalid))
	require.Contains(t, r.Dependencies, Dependency{Kind: "command", Name: "offline-tool", Mode: "mocked", State: Invalid, TaskID: "task"})
}

func TestOfflineSchemaStates(t *testing.T) {
	require.NoError(t, compileOfflineSchema(map[string]any{"type": "object"}, "memory://schema.json"))
	require.Equal(t, Invalid, schemaState(compileOfflineSchema(map[string]any{"type": "invalid"}, "memory://schema.json")))
	err := compileOfflineSchema(map[string]any{"$ref": "https://invalid.example/schema"}, "memory://schema.json")
	require.Error(t, err)
	require.Equal(t, Unresolved, schemaState(err))
}

func TestPreflightArtifactNotResults(t *testing.T) {
	data, err := json.Marshal(&Report{Kind: ReportKind, SchemaVersion: "1.0", Tasks: []TaskPlan{}})
	require.NoError(t, err)
	_, ok, err := models.ProbeEvaluationOutcomeSchemaVersion(data)
	require.NoError(t, err)
	require.False(t, ok)
	_, err = models.ParseEvaluationOutcome(data, "preflight.json")
	require.ErrorContains(t, err, "not evaluation results")
	_, ok, err = models.ProbeEvaluationOutcomeSchemaVersion([]byte(`{"eval_id":"legacy","tasks":[]}`))
	require.NoError(t, err)
	require.True(t, ok)
}

func TestInspectLockedGraderCacheMatrix(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("WAZA_MODULE_CACHE", cache)
	ref := "github.com/example/checks/check.yaml@v1"
	commit := strings.Repeat("a", 40)
	eval := validEval + "\ngraders: [{name: locked, ref: '" + ref + "'}]\n"
	path, dir := writeSuite(t, eval, validTask)
	lock := models.NewLockfile()
	lockPath := filepath.Join(dir, models.LockfileName)
	entry := models.LockfileGrader{Ref: ref, Commit: commit, Digest: "sha256:" + strings.Repeat("b", 64), URL: "https://not-fetched.invalid"}
	lock.UpsertGrader(entry)
	require.NoError(t, models.WriteLockfile(lockPath, lock))
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "grader.cache_missing", Unresolved), "%+v", r.Diagnostics)
	require.False(t, r.Failed(false))
	require.False(t, r.Complete)
	module := filepath.Join(cache, "github.com", "example", "checks", commit)
	require.NoError(t, os.MkdirAll(module, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(module, "check.yaml"), []byte("name: cached\ntype: text\nconfig: {contains: [ready]}\n"), 0600))
	r = Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "grader.cache_integrity", Invalid))
	digest, err := registry.DigestDirectory(module)
	require.NoError(t, err)
	entry.Digest = digest
	lock.UpsertGrader(entry)
	require.NoError(t, models.WriteLockfile(lockPath, lock))
	r = Inspect(path, Options{})
	require.False(t, r.Failed(true), "%+v", r.Diagnostics)
	require.True(t, r.Complete)
	require.Contains(t, r.Dependencies, Dependency{Kind: "grader", Name: "locked", Mode: "locked-cache", State: Verified})
	lock.Graders = nil
	require.NoError(t, models.WriteLockfile(lockPath, lock))
	r = Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "grader.lock_entry", Invalid))
}
