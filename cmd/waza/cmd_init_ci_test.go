package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type initCIStep struct {
	ID   string
	If   string
	Run  string
	Uses string
	Env  map[string]string
	With map[string]string
	// ContinueOnError is untyped so any emitted value, including an expression, is observed.
	ContinueOnError any `yaml:"continue-on-error"`
}

type initCIJob struct {
	If             string
	Needs          string
	Environment    string
	TimeoutMinutes int `yaml:"timeout-minutes"`
	Steps          []initCIStep
}

type initCIWorkflowSpec struct {
	On struct {
		PullRequest map[string]any `yaml:"pull_request"`
		Dispatch    struct {
			Inputs map[string]struct {
				Default any
			}
		} `yaml:"workflow_dispatch"`
	}
	Permissions map[string]string
	Env         map[string]string
	Jobs        map[string]initCIJob
}

func initCIFixture(t *testing.T) (string, initCIWorkflowSpec) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, validateCLIIsolation(dir))
	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	data, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "eval.yml"))
	require.NoError(t, err)
	var workflow initCIWorkflowSpec
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	require.NoError(t, err)
	require.Contains(t, string(readme), "checksum-verified standalone Waza "+workflow.Env["WAZA_VERSION"])
	require.Contains(t, string(readme), "Fixture\nisolation is not sandboxing")
	require.Contains(t, string(readme), "unresolved runtime")
	require.Contains(t, string(readme), "WAZA_COPILOT_TOKEN")
	cfg, err := projectconfig.Load(dir)
	require.NoError(t, err)
	require.Equal(t, projectconfig.DefaultEngine, cfg.Defaults.Engine)
	return dir, workflow
}

func initCIStepByName(t *testing.T, job initCIJob, name string) initCIStep {
	t.Helper()
	for _, step := range job.Steps {
		if step.ID == name || strings.Contains(step.Run, name) {
			return step
		}
	}
	t.Fatalf("generated step %q not found", name)
	return initCIStep{}
}

func initCIShell(t *testing.T, dir, script string, env map[string]string) (string, error) {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	prefix := "waza() { \"$WAZA_TEST_BINARY\" -test.run='^TestInitCIHelperProcess$' -- --no-telemetry \"$@\"; }\n"
	command := exec.Command("bash", "-euo", "pipefail", "-c", prefix+script)
	command.Dir = dir
	command.Env = append(os.Environ(), "WAZA_TEST_BINARY="+filepath.ToSlash(binary),
		"WAZA_INIT_CI_HELPER=1", "WAZA_NO_UPDATE_CHECK=1", "NO_COLOR=1",
		"TMP="+dir, "TEMP="+dir, "TMPDIR="+dir,
		"COPILOT_GITHUB_TOKEN=", "GH_TOKEN=", "GITHUB_TOKEN=", "BASH_ENV=")
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	if runtime.GOOS == "windows" {
		// The consumer runs on Ubuntu. A fixture-local startup file gives
		// every nested Bash process LF jq output without exported functions.
		bashEnv := filepath.Join(dir, "init-ci-bash-env.sh")
		require.NoError(t, os.WriteFile(bashEnv, []byte("jq() { command jq \"$@\" | tr -d '\\r'; }\n"), 0o600))
		command.Env = append(command.Env, "BASH_ENV="+filepath.ToSlash(bashEnv))
	}
	out, err := command.CombinedOutput()
	return string(out), err
}

func initCIWriteEval(t *testing.T, dir, grader string) string {
	t.Helper()
	initCIAddSkill(t, dir, "reviewed")
	evalDir := filepath.Join(dir, "evals", "reviewed")
	return initCIWriteEvalAt(t, evalDir, "eval.yaml", grader)
}

func initCIAddSkill(t *testing.T, dir, name string) {
	t.Helper()
	skillDir := filepath.Join(dir, "skills", name)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+name+"\n"), 0o600))
}

func initCIWriteEvalAt(t *testing.T, evalDir, evalFile, grader string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(evalDir, "fixtures"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "task.yaml"), []byte("id: task\nname: Task\ninputs: {prompt: ready}\n"), 0o600))
	path := filepath.Join(evalDir, evalFile)
	spec := "schemaVersion: '1.4'\nname: reviewed\nskill: reviewed\nversion: '1.0'\nconfig: {executor: copilot-sdk, model: never-call, trials_per_task: 1, timeout_seconds: 5}\nmetrics: [{name: accuracy, weight: 1, threshold: 0.8}]\ntasks: [task.yaml]\n" + grader
	require.NoError(t, os.WriteFile(path, []byte(spec), 0o600))
	return filepath.ToSlash(path)
}

func TestInitCI_NestedNativeJQ(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows jq output normalization")
	}
	t.Setenv("BASH_ENV", "")
	dir, _ := initCIFixture(t)
	native := exec.Command("jq", "-nr", "true")
	raw, err := native.Output()
	require.NoError(t, err)
	require.Contains(t, []string{"true\n", "true\r\n"}, string(raw), "accept valid native jq line endings")
	// A native process boundary can strip exported Bash functions. The
	// fixture must initialize jq normalization in the nested shell itself.
	out, err := initCIShell(t, dir,
		`env -u 'BASH_FUNC_jq%%' bash -euo pipefail -c 'jq -nr true'`, nil)
	require.NoError(t, err, out)
	require.Equal(t, "true\n", out)
	out, err = initCIShell(t, dir,
		`env -u 'BASH_FUNC_jq%%' bash -euo pipefail -c 'jq -en false >/dev/null'`, nil)
	require.Error(t, err, out)
}

func TestInitCI_OfflineCommandsAndTrust(t *testing.T) {
	dir, workflow := initCIFixture(t)
	require.Equal(t, map[string]string{"contents": "read"}, workflow.Permissions)
	require.NotContains(t, workflow.On.PullRequest, "branches")
	require.NotContains(t, workflow.On.PullRequest, "branches-ignore")
	require.Equal(t, false, workflow.On.Dispatch.Inputs["run-agent"].Default)
	validate := workflow.Jobs["validate"]
	require.Equal(t, 10, validate.TimeoutMinutes)
	require.Empty(t, validate.If)
	for _, step := range validate.Steps {
		for _, value := range step.Env {
			require.NotContains(t, value, "secrets.")
		}
		require.NotContains(t, step.Run, "waza run")
	}
	offline := initCIStepByName(t, validate, "waza preflight")
	env := offline.Env
	env["WAZA_INIT_CI_MODE"] = "reject"
	out, err := initCIShell(t, dir, offline.Run, env)
	require.NoError(t, err, out)
	require.Contains(t, out, "No evaluation files found")
	path := initCIWriteEval(t, dir, "")
	out, err = initCIShell(t, dir, offline.Run, env)
	require.NoError(t, err, out)
	require.Contains(t, out, "unresolved")
	require.Contains(t, out, "not runtime readiness or evaluation success")
	require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
	require.NoFileExists(t, filepath.Join(dir, "results.json"))
	require.NoError(t, os.WriteFile(filepath.FromSlash(path), []byte("config: [invalid]\n"), 0o600))
	out, err = initCIShell(t, dir, offline.Run, env)
	require.Error(t, err, out)
	require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))

	agent := workflow.Jobs["agent"]
	require.Equal(t, "validate", agent.Needs)
	require.Equal(t, "waza-agent", agent.Environment)
	require.Equal(t, 45, agent.TimeoutMinutes)
	// Evaluate the emitted condition with deterministic event values, not a
	// separately authored copy of the desired gate. GitHub expression string
	// comparison ignores case, so evaluate it with nocasematch.
	condition := strings.NewReplacer(
		"github.event_name", `"$EVENT"`,
		"inputs.run-agent", `"$OPT_IN" == true`,
		"github.ref", `"$REF"`,
		"format('refs/heads/{0}', github.event.repository.default_branch)", `"$DEFAULT_REF"`,
	).Replace(agent.If)
	// Git refs are case-sensitive; the first job step must enforce the exact
	// ref before any step can receive the environment secret.
	guard := initCIStepByName(t, agent, "exact-default-ref")
	require.Equal(t, guard.ID, agent.Steps[0].ID, "exact ref guard must precede all other agent steps")
	require.Empty(t, guard.If)
	require.Nil(t, guard.ContinueOnError, "a failed exact ref guard must stop the job")
	for _, step := range agent.Steps[1:] {
		for _, value := range step.Env {
			if strings.Contains(value, "secrets.") {
				require.Empty(t, step.If, "secret-bearing step %q must not run after a failed ref guard", step.ID)
			}
		}
	}
	require.Equal(t, "refs/heads/${{ github.event.repository.default_branch }}", guard.Env["DEFAULT_REF"])
	for _, tc := range []struct {
		event, optIn, ref, defaultRef string
		jobSelected, admitted         bool
	}{
		{"pull_request", "true", "refs/heads/main", "refs/heads/main", false, false},
		{"push", "true", "refs/heads/main", "refs/heads/main", false, false},
		{"workflow_dispatch", "false", "refs/heads/main", "refs/heads/main", false, false},
		{"workflow_dispatch", "true", "refs/heads/topic", "refs/heads/main", false, false},
		{"workflow_dispatch", "true", "refs/heads/main", "refs/heads/main", true, true},
		{"pull_request", "true", "refs/heads/develop", "refs/heads/develop", false, false},
		{"workflow_dispatch", "true", "refs/heads/main", "refs/heads/develop", false, false},
		{"workflow_dispatch", "true", "refs/heads/develop", "refs/heads/develop", true, true},
		// A distinct case-variant branch passes the case-insensitive job
		// condition and must be rejected by the exact guard.
		{"workflow_dispatch", "true", "refs/heads/MAIN", "refs/heads/main", true, false},
		{"workflow_dispatch", "true", "refs/heads/Develop", "refs/heads/develop", true, false},
	} {
		out, err = initCIShell(t, dir, "shopt -s nocasematch\n[[ "+condition+" ]]", map[string]string{
			"EVENT": tc.event, "OPT_IN": tc.optIn, "REF": tc.ref, "DEFAULT_REF": tc.defaultRef,
		})
		require.Equal(t, tc.jobSelected, err == nil, "job condition %+v: %s", tc, out)
		if !tc.jobSelected {
			continue
		}
		defaultBranch := strings.TrimPrefix(tc.defaultRef, "refs/heads/")
		out, err = initCIShell(t, dir, guard.Run, map[string]string{
			"GITHUB_REF":  tc.ref,
			"DEFAULT_REF": strings.ReplaceAll(guard.Env["DEFAULT_REF"], "${{ github.event.repository.default_branch }}", defaultBranch),
		})
		require.Equal(t, tc.admitted, err == nil, "exact ref guard %+v: %s", tc, out)
		if !tc.admitted {
			require.Contains(t, out, "Trusted evaluations run only from "+tc.defaultRef)
		}
	}
}

func TestInitCI_WorkspaceInventoryUsesNativeEvalLayouts(t *testing.T) {
	for _, layout := range []string{"separated", "nested", "co-located", "linked directory"} {
		t.Run(layout, func(t *testing.T) {
			dir, workflow := initCIFixture(t)
			initCIAddSkill(t, dir, "reviewed")
			evalPath := filepath.Join(dir, "evals", "reviewed", "eval.yaml")
			switch layout {
			case "nested":
				evalPath = filepath.Join(dir, "skills", "reviewed", "evals", "eval.yaml")
			case "co-located":
				evalPath = filepath.Join(dir, "skills", "reviewed", "eval.yaml")
			case "linked directory":
				targetDir := filepath.Join(dir, "linked-evals", "reviewed")
				require.NoError(t, os.MkdirAll(targetDir, 0o755))
				evalPath = filepath.Join(targetDir, "eval.yaml")
				linkPath := filepath.Join(dir, "evals", "reviewed")
				if err := os.Symlink(filepath.Dir(evalPath), linkPath); err != nil {
					t.Skipf("directory symlink unavailable: %v", err)
				}
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(evalPath), 0o755))
			require.NoError(t, os.WriteFile(evalPath, []byte("config: [invalid]\n"), 0o600))

			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			step.Env["WAZA_INIT_CI_MODE"] = "reject"
			out, err := initCIShell(t, dir, step.Run, step.Env)
			require.Error(t, err, out)
			require.Contains(t, out, "invalid eval.schema")
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_EmptyEvaluationRootSurvivesCheckout(t *testing.T) {
	dir := t.TempDir()
	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	evalsDir := filepath.Join(dir, "evals")
	require.FileExists(t, filepath.Join(evalsDir, ".gitkeep"))

	// Git checkouts do not retain empty, untracked directories.
	require.NoError(t, os.Remove(filepath.Join(dir, "skills")))
	data, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "eval.yml"))
	require.NoError(t, err)
	var workflow initCIWorkflowSpec
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
	out, err := initCIShell(t, dir, step.Run, step.Env)
	require.NoError(t, err, out)
	require.Contains(t, out, "No evaluation files found")

	require.NoError(t, os.Remove(filepath.Join(evalsDir, ".gitkeep")))
	require.NoError(t, os.Remove(evalsDir))
	out, err = initCIShell(t, dir, step.Run, step.Env)
	require.Error(t, err, out)
	require.Contains(t, out, "Evaluation directory is missing")
}

func TestInitCI_EmptySymlinkedEvaluationRootIsKept(t *testing.T) {
	dir, _ := initCIFixture(t)
	evalsDir := filepath.Join(dir, "evals")
	require.NoError(t, os.Remove(filepath.Join(evalsDir, ".gitkeep")))
	require.NoError(t, os.Remove(evalsDir))
	inside := filepath.Join(dir, "shared-evals")
	require.NoError(t, os.MkdirAll(inside, 0o755))
	if err := os.Symlink(inside, evalsDir); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	require.FileExists(t, filepath.Join(inside, ".gitkeep"), "an empty in-project symlink target must survive checkout")

	outside := t.TempDir()
	require.NoError(t, os.Remove(evalsDir))
	require.NoError(t, os.Symlink(outside, evalsDir))
	cmd = newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	require.NoFileExists(t, filepath.Join(outside, ".gitkeep"), "init must not write outside the project")
}

func TestInitCI_WorkspaceInventoryFollowsEvalRootSymlink(t *testing.T) {
	dir, _ := initCIFixture(t)
	cfg := projectconfig.New()
	cfg.Paths.Evals = "suites"
	config, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), config, 0o600))
	workflowPath := filepath.Join(dir, ".github", "workflows", "eval.yml")
	require.NoError(t, os.Remove(workflowPath))
	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill"})
	require.NoError(t, cmd.Execute())
	initCIAddSkill(t, dir, "reviewed")

	suitesDir := filepath.Join(dir, "suites")
	require.NoError(t, os.Remove(filepath.Join(suitesDir, ".gitkeep")))
	require.NoError(t, os.Remove(suitesDir))
	targetDir := filepath.Join(dir, "shared-evals", "reviewed")
	require.NoError(t, os.MkdirAll(targetDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "eval.yaml"), []byte("config: [invalid]\n"), 0o600))
	if err := os.Symlink(filepath.Dir(targetDir), suitesDir); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}

	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	var workflow initCIWorkflowSpec
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
	step.Env["WAZA_INIT_CI_MODE"] = "reject"
	out, err := initCIShell(t, dir, step.Run, step.Env)
	require.Error(t, err, out)
	require.Contains(t, out, "invalid eval.schema")
	require.NotContains(t, out, "No evaluation files found")
}

func TestInitCI_SelectedExecutionResultContract(t *testing.T) {
	dir, workflow := initCIFixture(t)
	agent := workflow.Jobs["agent"]
	run := initCIStepByName(t, agent, "run-eval")
	validate := initCIStepByName(t, agent, "validate-result")
	enforce := initCIStepByName(t, agent, "VALIDATION_OUTCOME")
	require.Equal(t, "always()", validate.If)
	require.Equal(t, "always()", enforce.If)
	require.Equal(t, "${{ secrets.WAZA_COPILOT_TOKEN }}", run.Env["COPILOT_GITHUB_TOKEN"])
	for _, step := range agent.Steps {
		if strings.Contains(step.Uses, "upload-artifact@") {
			require.Equal(t, "always()", step.If)
			require.Equal(t, "error", step.With["if-no-files-found"])
			require.Equal(t, "results.json", step.With["path"])
		}
	}
	validator, err := filepath.Abs(filepath.Join("..", "..", ".github", "scripts", "validate-waza-result.sh"))
	require.NoError(t, err)
	// The exact shared consumer command, using the local copy of the
	// release-matched script rather than fetching code during offline tests.
	_, validateCommand, found := strings.Cut(validate.Run, "bash ")
	require.True(t, found)
	validateCommand = "bash " + strings.ReplaceAll(validateCommand, `"$RUNNER_TEMP/validate-waza-result.sh"`, `"$VALIDATOR"`)
	for _, tc := range []struct {
		name, mode, grader, artifact string
		token, success               bool
		executor                     string
		nonAgent, gradingFails       bool
	}{
		{name: "passing grading", token: true, success: true, grader: "graders:\n  - type: text\n    name: pass\n    config: {contains: [ready]}\n"},
		{name: "completed failing grading", token: true, gradingFails: true, grader: "graders:\n  - type: text\n    name: fail\n    config: {contains: [never-match]}\n"},
		{name: "task runtime error", token: true, mode: "task-error"},
		{name: "runtime launch rejected", token: true, mode: "reject"},
		{name: "missing authentication"},
		{name: "missing artifact", token: true, artifact: "missing"},
		{name: "malformed artifact", token: true, artifact: "malformed"},
		{name: "passing mock is not agent evidence", token: true, executor: "mock", nonAgent: true, grader: "graders:\n  - type: text\n    name: pass\n    config: {contains: [ready]}\n"},
		{name: "missing executor identity", token: true, artifact: "missing engine", nonAgent: true},
		{name: "unknown executor identity", token: true, artifact: "unknown engine", nonAgent: true},
		{name: "invalid executor identity", token: true, artifact: "invalid engine", nonAgent: true},
		{name: "inherited mock is not agent evidence", token: true, executor: "inherited mock", nonAgent: true, grader: "graders:\n  - type: text\n    name: pass\n    config: {contains: [ready]}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := initCIWriteEval(t, dir, tc.grader)
			if tc.executor != "" {
				spec, err := os.ReadFile(filepath.FromSlash(path))
				require.NoError(t, err)
				replacement := "executor: mock,"
				if tc.executor == "inherited mock" {
					replacement = ""
					require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("defaults: {engine: mock, model: never-call}\n"), 0o600))
				}
				require.NoError(t, os.WriteFile(filepath.FromSlash(path), []byte(strings.ReplaceAll(string(spec), "executor: copilot-sdk,", replacement)), 0o600))
			}
			output := filepath.Join(dir, "github-output")
			require.NoError(t, os.WriteFile(output, nil, 0o600))
			token := ""
			if tc.token {
				token = "synthetic-offline-token"
			}
			out, runErr := initCIShell(t, dir, run.Run, map[string]string{
				"EVAL_PATH": path, "COPILOT_GITHUB_TOKEN": token,
				"GITHUB_OUTPUT": filepath.ToSlash(output), "WAZA_INIT_CI_MODE": tc.mode,
			})
			if tc.token {
				require.NoError(t, runErr, out)
			} else {
				require.Error(t, runErr, out)
				require.Contains(t, out, "Configure WAZA_COPILOT_TOKEN")
			}
			record, err := os.ReadFile(output)
			require.NoError(t, err)
			runExit := strings.TrimSpace(strings.TrimPrefix(string(record), "exit_code="))
			if tc.token && tc.mode == "" && !tc.gradingFails {
				require.Equal(t, "0", runExit, out)
			}
			if tc.grader != "" || tc.mode == "task-error" {
				data, err := os.ReadFile(filepath.Join(dir, "results.json"))
				require.NoError(t, err)
				var result struct {
					Summary struct{ Failed, Errors int }
				}
				require.NoError(t, json.Unmarshal(data, &result))
				if tc.mode == "task-error" {
					require.Equal(t, 1, result.Summary.Errors)
				} else {
					require.Zero(t, result.Summary.Errors, string(data))
					if tc.gradingFails {
						require.Equal(t, 1, result.Summary.Failed)
					} else {
						require.Zero(t, result.Summary.Failed)
					}
				}
			}
			if tc.artifact == "missing" {
				require.NoError(t, os.Remove(filepath.Join(dir, "results.json")))
			} else if tc.artifact == "malformed" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "results.json"), []byte("{"), 0o600))
			} else if tc.artifact != "" {
				data, err := os.ReadFile(filepath.Join(dir, "results.json"))
				require.NoError(t, err)
				var result map[string]any
				require.NoError(t, json.Unmarshal(data, &result))
				config, ok := result["config"].(map[string]any)
				require.True(t, ok)
				switch tc.artifact {
				case "missing engine":
					delete(config, "engine_type")
				case "unknown engine":
					config["engine_type"] = "unknown"
				case "invalid engine":
					config["engine_type"] = map[string]string{"type": "copilot-sdk"}
				default:
					t.Fatalf("unhandled artifact case %q", tc.artifact)
				}
				data, err = json.Marshal(result)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "results.json"), data, 0o600))
			}
			require.NoError(t, os.WriteFile(output, nil, 0o600))
			out, validationErr := initCIShell(t, dir, validateCommand, map[string]string{
				"VALIDATOR": filepath.ToSlash(validator), "RUN_EXIT_CODE": runExit, "GITHUB_OUTPUT": filepath.ToSlash(output),
			})
			record, err = os.ReadFile(output)
			require.NoError(t, err)
			if tc.success {
				require.NoError(t, validationErr, out)
				require.Contains(t, string(record), "status=success")
			} else if tc.nonAgent {
				require.Error(t, validationErr, out)
				require.Contains(t, out, "cannot satisfy the agent-evidence gate")
				require.FileExists(t, filepath.Join(dir, "results.json"))
			} else if tc.gradingFails || tc.mode == "task-error" {
				require.NoError(t, validationErr, out)
				require.Contains(t, string(record), "status=failure")
				require.NotEqual(t, "0", runExit)
				require.FileExists(t, filepath.Join(dir, "results.json"))
			} else {
				require.Error(t, validationErr, out)
			}
			outcome, status := "failure", "failure"
			if validationErr == nil {
				outcome = "success"
			}
			if strings.Contains(string(record), "status=success") {
				status = "success"
			}
			out, err = initCIShell(t, dir, enforce.Run, map[string]string{
				"VALIDATION_OUTCOME": outcome, "EVALUATION_STATUS": status,
			})
			require.Equal(t, tc.success, err == nil, out)
		})
	}
}

func TestInitCI_CustomEvalPaths(t *testing.T) {
	dir, _ := initCIFixture(t)
	configPath := filepath.Join(dir, ".waza.yaml")
	cfg := projectconfig.New()
	cfg.Paths.Evals = "configured suites"
	cfg.Files.EvalFile = "suite.yml"
	config, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, config, 0o600))
	workflowPath := filepath.Join(dir, ".github", "workflows", "eval.yml")
	require.NoError(t, os.Remove(workflowPath))
	cmd := newInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", "generated suites"})
	require.NoError(t, cmd.Execute())
	initCIAddSkill(t, dir, "reviewed")
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err)
	var workflow initCIWorkflowSpec
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	require.Equal(t, "generated suites/my-skill/suite.yml", workflow.On.Dispatch.Inputs["eval-path"].Default)
	step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
	require.Equal(t, "generated suites", step.Env["EVALS_PATH"])
	require.Equal(t, "suite.yml", step.Env["EVAL_FILE"])
	evalDir := filepath.Join(dir, "generated suites", "reviewed")
	require.NoError(t, os.MkdirAll(evalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evalDir, "suite.yml"), []byte("config: [invalid]\n"), 0o600))
	step.Env["WAZA_INIT_CI_MODE"] = "reject"
	out, err := initCIShell(t, dir, step.Run, step.Env)
	require.Error(t, err, out)
	require.NotContains(t, out, "No evaluation files found")
	require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
}

func TestInitCI_RejectsActionsExpressionsInLiteralPaths(t *testing.T) {
	const expression = "suites-${{ github.token }}"
	t.Run("init evals dir", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, validateCLIIsolation(dir))
		cmd := newInitCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{dir, "--no-skill", "--evals-dir", expression})
		err := cmd.Execute()
		require.ErrorContains(t, err, `generated CI cannot use path "`+expression+`"`)
		require.NoFileExists(t, filepath.Join(dir, ".github", "workflows", "eval.yml"))
		require.NoDirExists(t, filepath.Join(dir, expression))
	})
	t.Run("new skill eval file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, validateCLIIsolation(dir))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("files:\n  evalFile: '${{ github.token }}.yml'\n"), 0o600))
		cmd := newNewSkillCommand()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{"reviewed", "--output-dir", dir})
		err := cmd.Execute()
		require.ErrorContains(t, err, "GitHub Actions would evaluate")
		require.NoFileExists(t, filepath.Join(dir, "reviewed", ".github", "workflows", "eval.yml"))
		require.NoFileExists(t, filepath.Join(dir, "reviewed", "SKILL.md"))
	})
}

func TestInitCI_InventoryUsesLiteralFilenameAndFallbackPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name             string
		evalFile         string
		configured       string
		legacy           string
		globDecoy        string
		wantPreflightErr bool
	}{
		{
			name:             "legacy fallback when configured file is absent",
			evalFile:         "suite.yml",
			legacy:           "config: [invalid]\n",
			wantPreflightErr: true,
		},
		{
			name:             "metacharacters are literal in configured filename",
			evalFile:         "suite[1].yml",
			configured:       "config: [invalid]\n",
			globDecoy:        "valid",
			wantPreflightErr: true,
		},
		{
			name:       "configured file takes precedence over legacy fallback",
			evalFile:   "suite.yml",
			configured: "valid",
			legacy:     "config: [invalid]\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := initCIFixture(t)
			cfg := projectconfig.New()
			cfg.Paths.Evals = "custom suites"
			cfg.Files.EvalFile = tc.evalFile
			config, err := yaml.Marshal(cfg)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), config, 0o600))
			workflowPath := filepath.Join(dir, ".github", "workflows", "eval.yml")
			require.NoError(t, os.Remove(workflowPath))
			cmd := newInitCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetArgs([]string{dir, "--no-skill"})
			require.NoError(t, cmd.Execute())
			initCIAddSkill(t, dir, "reviewed")
			data, err := os.ReadFile(workflowPath)
			require.NoError(t, err)
			var workflow initCIWorkflowSpec
			require.NoError(t, yaml.Unmarshal(data, &workflow))
			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			step.Env["WAZA_INIT_CI_MODE"] = "reject"

			evalDir := filepath.Join(dir, "custom suites", "reviewed")
			if tc.configured == "valid" {
				initCIWriteEvalAt(t, evalDir, tc.evalFile, "")
			} else if tc.configured != "" {
				require.NoError(t, os.MkdirAll(evalDir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(evalDir, tc.evalFile), []byte(tc.configured), 0o600))
			}
			if tc.legacy != "" {
				require.NoError(t, os.MkdirAll(evalDir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(evalDir, "eval.yaml"), []byte(tc.legacy), 0o600))
			}
			if tc.globDecoy == "valid" {
				initCIWriteEvalAt(t, evalDir, "suite1.yml", "")
			}

			out, err := initCIShell(t, dir, step.Run, step.Env)
			require.Equal(t, tc.wantPreflightErr, err != nil, out)
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func TestInitCI_StandaloneGeneratedPaths(t *testing.T) {
	for _, evalFile := range []string{"eval.yaml", "suite.yml"} {
		t.Run(evalFile, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, validateCLIIsolation(dir), "standalone fixture must not inherit repository configuration")
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("files:\n  evalFile: "+evalFile+"\n"), 0o600))
			cmd := newNewSkillCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetArgs([]string{"reviewed", "--output-dir", dir})
			require.NoError(t, cmd.Execute())
			root := filepath.Join(dir, "reviewed")
			workflowPath := filepath.Join(root, ".github", "workflows", "eval.yml")
			data, err := os.ReadFile(workflowPath)
			require.NoError(t, err)
			require.NotContains(t, string(data), "/main/")
			var workflow initCIWorkflowSpec
			require.NoError(t, yaml.Unmarshal(data, &workflow))
			require.Equal(t, filepath.ToSlash(filepath.Join("evals", evalFile)), workflow.On.Dispatch.Inputs["eval-path"].Default)
			require.Equal(t, false, workflow.On.Dispatch.Inputs["run-agent"].Default)
			require.Contains(t, workflow.Jobs["agent"].If, "github.event.repository.default_branch")
			require.Equal(t, "waza-agent", workflow.Jobs["agent"].Environment)
			require.NotContains(t, workflow.On.PullRequest, "branches")
			readme, err := os.ReadFile(filepath.Join(root, "README.md"))
			require.NoError(t, err)
			require.Contains(t, string(readme), "checksum-verified standalone Waza "+workflow.Env["WAZA_VERSION"])
			require.NoError(t, os.Remove(filepath.Join(dir, ".waza.yaml")), "standalone checkout must not inherit parent configuration")
			require.NoError(t, validateCLIIsolation(root))
			// Preflight and run must both resolve the standalone skill for required_skills.
			evalPath := filepath.Join(root, "evals", evalFile)
			evalData, err := os.ReadFile(evalPath)
			require.NoError(t, err)
			required := strings.Replace(string(evalData), "config:\n", "config:\n  required_skills: [reviewed]\n", 1)
			require.NotEqual(t, string(evalData), required, "fixture must declare required_skills")
			require.NoError(t, os.WriteFile(evalPath, []byte(required), 0o600))
			step := initCIStepByName(t, workflow.Jobs["validate"], "waza preflight")
			step.Env["WAZA_INIT_CI_MODE"] = "reject"
			out, err := initCIShell(t, root, step.Run, step.Env)
			require.NoError(t, err, out)
			require.Contains(t, out, "Offline preflight: "+filepath.Join(root, "evals", evalFile))
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(root, "runtime-invoked"))
			require.NoFileExists(t, filepath.Join(root, "results.json"))
			// Explicit single-file preflight from the checkout must agree with run.
			t.Chdir(root)
			single := newPreflightCommand()
			single.SetOut(&bytes.Buffer{})
			single.SilenceUsage = true
			single.SetArgs([]string{filepath.Join("evals", evalFile)})
			require.NoError(t, single.Execute())

			// The trusted job must load the skill under test without a .waza.yaml.
			run := initCIStepByName(t, workflow.Jobs["agent"], "waza run")
			output := filepath.Join(root, "github-output")
			require.NoError(t, os.WriteFile(output, nil, 0o600))
			out, err = initCIShell(t, root, run.Run, map[string]string{
				"EVAL_PATH": filepath.ToSlash(filepath.Join("evals", evalFile)), "COPILOT_GITHUB_TOKEN": "synthetic-offline-token",
				"GITHUB_OUTPUT": filepath.ToSlash(output), "WAZA_INIT_CI_MODE": "record-skills",
			})
			require.NoError(t, err, out)
			require.FileExists(t, filepath.Join(root, "results.json"), out)
			sessions, err := os.ReadFile(filepath.Join(root, "session-skill-paths"))
			require.NoError(t, err, out)
			wantRoot, err := filepath.EvalSymlinks(root)
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(string(sessions), "\r\n", "\n"), "\n"), "\n")
			require.Len(t, lines, 3, "each scaffolded task must start one recorded session")
			for _, line := range lines {
				var resolved []string
				for _, p := range strings.Split(line, "\x1f") {
					if p == "" {
						continue
					}
					r, err := filepath.EvalSymlinks(p)
					require.NoError(t, err)
					resolved = append(resolved, r)
				}
				require.Equal(t, []string{wantRoot}, resolved, "session must receive exactly the standalone skill root")
			}
			for _, name := range []string{"results.json", "runtime-invoked", "session-skill-paths"} {
				require.NoError(t, os.Remove(filepath.Join(root, name)))
			}

			require.NoError(t, os.WriteFile(filepath.Join(root, "evals", evalFile), []byte("config: [invalid]\n"), 0o600))
			out, err = initCIShell(t, root, step.Run, step.Env)
			require.Error(t, err, out)
			require.NotContains(t, out, "No evaluation files found")
			require.NoFileExists(t, filepath.Join(root, "runtime-invoked"))
			// Repairing a standalone scaffold must preserve a user-owned workflow.
			require.NoError(t, os.WriteFile(workflowPath, []byte("name: User workflow\n"), 0o600))
			require.NoError(t, cmd.Execute())
			data, err = os.ReadFile(workflowPath)
			require.NoError(t, err)
			require.Equal(t, "name: User workflow\n", string(data))
		})
	}
}

type initCIEngine struct{ preflightEngineSpy }

// initCISkillRecorder records the skill directories each session receives.
type initCISkillRecorder struct{ execution.AgentEngine }

func (e *initCISkillRecorder) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	f, err := os.OpenFile("session-skill-paths", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintln(f, strings.Join(req.SkillPaths, "\x1f")); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return e.AgentEngine.Execute(ctx, req)
}

func (*initCIEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return nil, errors.New("synthetic task runtime error")
}

func TestInitCIHelperProcess(t *testing.T) {
	if os.Getenv("WAZA_INIT_CI_HELPER") != "1" {
		return
	}
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		if err := os.WriteFile("runtime-invoked", nil, 0o600); err != nil {
			return nil, err
		}
		if os.Getenv("WAZA_INIT_CI_MODE") == "reject" {
			return nil, errors.New("offline runtime launch rejected")
		}
		if os.Getenv("WAZA_INIT_CI_MODE") == "task-error" {
			return &initCIEngine{}, nil
		}
		if os.Getenv("WAZA_INIT_CI_MODE") == "record-skills" {
			return &initCISkillRecorder{execution.NewMockEngine("never-call")}, nil
		}
		return execution.NewMockEngine("never-call"), nil
	}
	for i, arg := range os.Args {
		if arg == "--" {
			cmd := newRootCommand()
			cmd.SetArgs(os.Args[i+1:])
			if err := cmd.Execute(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				var exitErr *ExitCodeError
				if errors.As(err, &exitErr) {
					os.Exit(exitErr.Code)
				}
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	t.Fatal("missing CLI arguments")
}
