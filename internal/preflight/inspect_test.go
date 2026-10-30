package preflight

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/santhosh-tekuri/jsonschema/v6"
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
		{"unmatched pattern", strings.ReplaceAll(validEval, "task.yaml", "absent*.yaml"), validTask, "tasks.empty"},
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cases.csv"), []byte("prompt\nFirst\nSecond\n"), 0600))
	r = Inspect(path, Options{})
	require.False(t, r.Failed(true), "%+v", r.Diagnostics)
	require.Equal(t, "row-2", r.Tasks[0].ID)
	require.Equal(t, "row-2", r.Tasks[0].Name)
}

func TestInspectCSVSourcePathParity(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(fmt.Sprintf("absolute=%t", absolute), func(t *testing.T) {
			path, dir := writeSuite(t, validEval, validTask)
			csvPath := filepath.Join(dir, "cases.csv")
			require.NoError(t, os.WriteFile(csvPath, []byte("id,name,prompt\nfirst,First,Inspect\n"), 0600))
			source := "cases.csv"
			if absolute {
				source = csvPath
			}
			eval := strings.ReplaceAll(validEval, "tasks:\n  - task.yaml", fmt.Sprintf("tasks: []\ntasks_from: %q", source))
			require.NoError(t, os.WriteFile(path, []byte(eval), 0600))
			report := Inspect(path, Options{})
			require.False(t, report.Failed(true), "%+v", report.Diagnostics)
			require.Len(t, report.Tasks, 1)
			require.Equal(t, csvPath, report.Tasks[0].Source)
			spec, err := models.LoadEvalSpec(path)
			require.NoError(t, err)
			spec.Config.InstructionFiles = []string{"missing.txt"}
			diagnostics := &Report{Complete: true}
			tasks, sources, _ := discoverTasks(diagnostics, spec, dir)
			require.Len(t, tasks, 1)
			inspectTask(diagnostics, tasks[0], spec, sources[0], dir, filepath.Join(dir, "fixtures"))
			require.True(t, hasDiagnostic(diagnostics, "instruction.file", Invalid))
			for _, diagnostic := range diagnostics.Diagnostics {
				require.Equal(t, csvPath, diagnostic.Source)
			}
		})
	}
}

func TestInspectCSVRejectsSymlinkEscape(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "tasks:\n  - task.yaml", "tasks: []\ntasks_from: cases.csv")
	path, dir := writeSuite(t, eval, validTask)
	outside := filepath.Join(t.TempDir(), "outside.csv")
	require.NoError(t, os.WriteFile(outside, []byte("prompt\nprivate-outside-data\n"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "cases.csv")))
	r := Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "tasks.csv", Invalid), "%+v", r.Diagnostics)
	require.False(t, r.Complete)
	require.Empty(t, r.Tasks)
	data, err := json.Marshal(r)
	require.NoError(t, err)
	require.NotContains(t, string(data), "private-outside-data")
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

func TestInspectLiveMCPConfigurationStates(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		state        State
	}{
		{"stdio", "{command: never-run}", Unresolved},
		{"HTTP", "{type: http, url: 'https://never-contact.invalid'}", Unresolved},
		{"SSE", "{type: sse, url: 'https://never-contact.invalid'}", Unresolved},
		{"missing command", "{type: stdio}", Invalid},
		{"missing URL", "{type: http}", Invalid},
		{"unknown transport", "{type: custom, url: 'https://never-contact.invalid'}", Unsupported},
		{"non-object configuration", "[]", Invalid},
		{"malformed headers", "{type: http, url: 'https://never-contact.invalid', headers: [secret-value]}", Invalid},
		{"malformed environment", "{command: never-run, env: [secret-value]}", Invalid},
		{"malformed args", "{command: never-run, args: {unexpected: secret-value}}", Invalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk")
			eval = strings.ReplaceAll(eval, "  model: harness", "  model: harness\n  mcp_servers:\n    live: "+tc.config)
			path, _ := writeSuite(t, eval, validTask)
			r := Inspect(path, Options{})
			require.Contains(t, r.Dependencies, Dependency{Kind: "mcp", Name: "live", Mode: "live", State: tc.state})
			require.True(t, hasDiagnostic(r, "dependency.live", Unresolved))
			require.Equal(t, tc.state != Unresolved, hasDiagnostic(r, "dependency.mcp_configuration", tc.state))
			require.Equal(t, tc.state == Invalid, r.Failed(false), "%+v", r.Diagnostics)
			require.True(t, r.Failed(true))
			data, err := json.Marshal(r)
			require.NoError(t, err)
			require.NotContains(t, string(data), "never-contact.invalid")
			require.NotContains(t, string(data), "never-run")
			require.NotContains(t, string(data), "secret-value")
		})
	}
}

func TestInspectOptionalUnmatchedTaskPatternsRetainRuntimeParity(t *testing.T) {
	eval := strings.ReplaceAll(validEval, "  - task.yaml", "  - task.yaml\n  - optional/*.yaml")
	path, _ := writeSuite(t, eval, validTask)
	r := Inspect(path, Options{})
	require.True(t, r.Complete)
	require.False(t, r.Failed(true), "%+v", r.Diagnostics)
	require.Len(t, r.Tasks, 1)
	require.False(t, hasDiagnostic(r, "tasks.pattern", Invalid))
	eval = strings.ReplaceAll(validEval, "  - task.yaml", "  - task.yaml\n  - optional/[")
	path, _ = writeSuite(t, eval, validTask)
	r = Inspect(path, Options{})
	require.True(t, hasDiagnostic(r, "tasks.pattern", Invalid))
	require.False(t, r.Complete)
}

func TestInspectDeclaredScenarioAdmission(t *testing.T) {
	for _, version := range []string{"", "1.0", "2.0", "2.1"} {
		for _, value := range []string{"null", "42", "true", "{}", "[]", "'   '", "recovery"} {
			t.Run(version+"/"+value, func(t *testing.T) {
				eval := strings.ReplaceAll(validEval, "schemaVersion: \"1.4\"\n", "")
				if version != "" {
					eval = "schemaVersion: '" + version + "'\n" + eval
				}
				eval = "scenario: " + value + "\n" + eval
				path, _ := writeSuite(t, eval, validTask)
				r := Inspect(path, Options{})
				if version == "2.0" && value == "recovery" {
					require.False(t, r.Failed(true), "%+v", r.Diagnostics)
					require.True(t, r.Complete)
				} else {
					require.True(t, r.Failed(false))
					require.True(t, hasDiagnostic(r, "eval.configuration", Invalid))
					require.False(t, r.Complete)
					require.Empty(t, r.Tasks)
				}
			})
		}
	}
}

func TestSchemaStateRequiresExplicitOfflineSentinel(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want State
	}{
		{"direct sentinel", schemaloader.ErrExternalReference, Unresolved},
		{"wrapped sentinel", fmt.Errorf("schema: %w", schemaloader.ErrExternalReference), Unresolved},
		{"load sentinel", &jsonschema.LoadURLError{URL: "file:///schema.json", Err: schemaloader.ErrExternalReference}, Unresolved},
		{"wrapped load sentinel", fmt.Errorf("compile: %w", &jsonschema.LoadURLError{URL: "file:///schema.json", Err: fmt.Errorf("offline: %w", schemaloader.ErrExternalReference)}), Unresolved},
		{"unrelated load failure", &jsonschema.LoadURLError{URL: "file:///schema.json", Err: errors.New("malformed schema resource")}, Invalid},
		{"missing load cause", &jsonschema.LoadURLError{URL: "file:///schema.json"}, Invalid},
		{"ordinary error", errors.New("invalid schema"), Invalid},
		{"nil", nil, Invalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := schemaState(tc.err)
			require.Equal(t, tc.want, state)
			report := &Report{}
			report.add("grader.schema", state, "eval.yaml", "", "", "Schema diagnosis.", "Inspect schema.")
			require.Equal(t, tc.want == Invalid, report.Failed(false))
			require.True(t, report.Failed(true))
		})
	}
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

func TestInspectRefOnlyLockedGraderMissingCache(t *testing.T) {
	for _, scenario := range []bool{false, true} {
		for _, scope := range []string{"eval", "task", "checkpoint"} {
			t.Run(fmt.Sprintf("scenario=%t/%s", scenario, scope), func(t *testing.T) {
				cache := t.TempDir()
				t.Setenv("WAZA_MODULE_CACHE", cache)
				ref := "github.com/example/checks/check.yaml@v1"
				commit := strings.Repeat("a", 40)
				eval, task := validEval, validTask
				if scenario {
					eval = strings.Replace(eval, `schemaVersion: "1.4"`, "schemaVersion: \"2.0\"\nscenario: offline-cache", 1)
					eval = strings.Replace(eval, "skill: example\n", "", 1)
				}
				declaration := "{ref: '" + ref + "'}"
				switch scope {
				case "eval":
					eval += "\ngraders: [" + declaration + "]\n"
				case "task":
					task = strings.Replace(task, "\nrequirements:", "\n  - "+declaration+"\nrequirements:", 1)
				case "checkpoint":
					task += "\ncheckpoints: [{after_turn: 1, graders: [" + declaration + "]}]\n"
				}
				path, dir := writeSuite(t, eval, task)
				lock := models.NewLockfile()
				entry := models.LockfileGrader{Ref: ref, Commit: commit, Digest: "sha256:" + strings.Repeat("b", 64), URL: "https://not-fetched.invalid"}
				lock.UpsertGrader(entry)
				require.NoError(t, models.WriteLockfile(filepath.Join(dir, models.LockfileName), lock))
				report := Inspect(path, Options{})
				if scope != "eval" {
					// Task and checkpoint schemas support inline validators, not refs.
					require.True(t, hasDiagnostic(report, "task.schema", Invalid))
					require.True(t, report.Failed(false))
					require.False(t, hasDiagnostic(report, "grader.cache_missing", Unresolved))
					return
				}
				require.True(t, hasDiagnostic(report, "grader.cache_missing", Unresolved), "%+v", report.Diagnostics)
				require.False(t, hasDiagnostic(report, "grader.name", Invalid), "%+v", report.Diagnostics)
				require.False(t, report.Failed(false), "%+v", report.Diagnostics)
				require.True(t, report.Failed(true))
				require.False(t, report.Complete)
				require.Equal(t, Dependency{Kind: "grader", Mode: "locked-cache", State: Unresolved}, report.Dependencies[0])

				module := filepath.Join(cache, "github.com", "example", "checks", commit)
				require.NoError(t, os.MkdirAll(module, 0o700))
				for _, identity := range []struct{ preset, alias, want string }{
					{"cached", "", "cached"},
					{"", "", "check"},
					{"cached", "explicit", "explicit"},
					{"", "explicit", "explicit"},
				} {
					t.Run(fmt.Sprintf("preset=%q/alias=%q", identity.preset, identity.alias), func(t *testing.T) {
						selected := eval
						if identity.alias != "" {
							selected = strings.Replace(eval, declaration, "{name: '"+identity.alias+"', ref: '"+ref+"'}", 1)
						}
						require.NoError(t, os.WriteFile(path, []byte(selected), 0o600))
						presetData := "type: text\nconfig: {contains: [ready]}\n"
						if identity.preset != "" {
							presetData = "name: " + identity.preset + "\n" + presetData
						}
						presetPath := filepath.Join(module, "check.yaml")
						require.NoError(t, os.WriteFile(presetPath, []byte(presetData), 0o600))
						digest, err := registry.DigestDirectory(module)
						require.NoError(t, err)
						entry.Digest = digest
						lock.UpsertGrader(entry)
						require.NoError(t, models.WriteLockfile(filepath.Join(dir, models.LockfileName), lock))
						report = Inspect(path, Options{})
						require.False(t, report.Failed(true), "%+v", report.Diagnostics)
						require.True(t, report.Complete)
						require.Equal(t, Dependency{Kind: "grader", Name: identity.want, Mode: "locked-cache", State: Verified}, report.Dependencies[0])
						after, err := os.ReadFile(presetPath)
						require.NoError(t, err)
						require.Equal(t, presetData, string(after))
					})
				}
			})
		}
	}
}

func TestInspectGradersPreservesKnownNameErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		configs []models.GraderConfig
	}{
		{"unnamed-inline", []models.GraderConfig{{}}},
		{"duplicate-explicit-ref-names", []models.GraderConfig{
			{Identifier: "duplicate", Ref: "github.com/example/checks/a.yaml@v1"},
			{Identifier: "duplicate", Ref: "github.com/example/checks/b.yaml@v1"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := &Report{}
			inspectGraders(report, tc.configs, "eval.yaml", "", "")
			require.True(t, hasDiagnostic(report, "grader.name", Invalid))
		})
	}
}

func TestInspectLockedGraderMergeFailureNames(t *testing.T) {
	refs, requests := offlineFixtureCacheReferences(t)
	for _, scenario := range []bool{false, true} {
		for _, identity := range []struct{ preset, alias, want string }{
			{"cached", "", "cached"},
			{"", "", "check"},
			{"cached", "explicit", "explicit"},
			{"", "explicit", "explicit"},
		} {
			for _, problem := range []string{"invalid", refs[0], refs[1]} {
				t.Run(fmt.Sprintf("scenario=%t/preset=%q/alias=%q/%s", scenario, identity.preset, identity.alias, problem), func(t *testing.T) {
					const moduleRef = "github.com/example/checks/check.yaml@v1"
					override := map[string]any{"ref": moduleRef}
					if identity.alias != "" {
						override["name"] = identity.alias
					}
					state := Unresolved
					if problem == "invalid" {
						state = Invalid
						override["type"] = "text"
						override["config"] = map[string]any{"contains": []string{"ready"}}
					} else {
						override["config"] = map[string]any{"expect": []any{map[string]any{
							"tool": "read", "args": map[string]any{"value": map[string]any{
								"json_schema": map[string]any{"$ref": problem},
							}},
						}}}
					}
					eval := validEval
					if scenario {
						eval = strings.Replace(eval, `schemaVersion: "1.4"`, "schemaVersion: \"2.0\"\nscenario: offline-cache", 1)
						eval = strings.Replace(eval, "skill: example\n", "", 1)
					}
					eval += pipelineYAML(t, map[string]any{"graders": []any{override}})
					evalPath, dir := writeSuite(t, eval, validTask)
					cache := t.TempDir()
					t.Setenv("WAZA_MODULE_CACHE", cache)
					commit := strings.Repeat("a", 40)
					module := filepath.Join(cache, "github.com", "example", "checks", commit)
					require.NoError(t, os.MkdirAll(module, 0o700))
					preset := map[string]any{"type": "tool_calls", "config": map[string]any{
						"expect": []any{map[string]any{"tool": "read"}},
					}}
					if identity.preset != "" {
						preset["name"] = identity.preset
					}
					presetData := []byte(pipelineYAML(t, preset))
					presetPath := filepath.Join(module, "check.yaml")
					require.NoError(t, os.WriteFile(presetPath, presetData, 0o600))
					digest, err := registry.DigestDirectory(module)
					require.NoError(t, err)
					lock := models.NewLockfile()
					lock.UpsertGrader(models.LockfileGrader{
						Ref: moduleRef, Commit: commit, Digest: digest, URL: "https://not-contacted.invalid",
					})
					lockPath := filepath.Join(dir, models.LockfileName)
					require.NoError(t, models.WriteLockfile(lockPath, lock))
					lockData, err := os.ReadFile(lockPath)
					require.NoError(t, err)
					report := Inspect(evalPath, Options{})
					require.True(t, hasDiagnostic(report, "grader.merge", state), "%+v", report.Diagnostics)
					require.Equal(t, state == Invalid, report.Failed(false), "%+v", report.Diagnostics)
					require.True(t, report.Failed(true))
					require.False(t, report.Complete)
					require.Equal(t, Dependency{Kind: "grader", Name: identity.want, Mode: "locked-cache", State: state}, report.Dependencies[0])
					spec, err := models.ParseEvalSpecOffline([]byte(eval), evalPath, nil)
					require.NoError(t, err)
					authored, err := models.ParseEvalSpecOffline([]byte(eval), evalPath, nil)
					require.NoError(t, err)
					inspectLockedGraders(&Report{Complete: true}, spec, evalPath)
					require.Equal(t, authored.Graders, spec.Graders, "failed merges must not rewrite the authored configuration")
					after, err := os.ReadFile(evalPath)
					require.NoError(t, err)
					require.Equal(t, eval, string(after))
					after, err = os.ReadFile(presetPath)
					require.NoError(t, err)
					require.Equal(t, presetData, after)
					after, err = os.ReadFile(lockPath)
					require.NoError(t, err)
					require.Equal(t, lockData, after)
					require.Zero(t, requests.Load())
				})
			}
		}
	}
}
