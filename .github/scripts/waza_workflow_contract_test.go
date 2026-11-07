package scripts

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestWazaResultValidator(t *testing.T) {
	t.Parallel()

	valid := map[string]any{
		"schemaVersion": "1.8",
		"eval_id":       "workflow-contract",
		"config":        map[string]any{"runs_per_test": 1},
		"summary": map[string]any{
			"total_tests": 1, "succeeded": 1, "failed": 0, "errors": 0, "skipped": 0,
			"success_rate": 1.0, "aggregate_score": 0.0, "weighted_score": 0.0,
		},
		"tasks": []any{map[string]any{
			"test_id": "workflow-task", "status": "passed",
			"runs": []any{map[string]any{
				"run_number": 1, "attempts": 1, "status": "passed",
				"duration_ms": 0, "final_output": "",
			}},
		}},
	}

	tests := []struct {
		name       string
		mutate     func(map[string]any)
		runExit    int
		threshold  string
		wantValid  bool
		wantStatus string
	}{
		{name: "passing grading with explicit zero scores", runExit: 0, threshold: "0.8", wantValid: true, wantStatus: "success"},
		{name: "invalid threshold", runExit: 0, threshold: "invalid", wantValid: false},
		{name: "threshold below zero", runExit: 0, threshold: "-0.1", wantValid: false},
		{name: "threshold above one", runExit: 0, threshold: "1.1", wantValid: false},
		{
			name: "complete multiple trials",
			mutate: func(result map[string]any) {
				jsonObject(t, result["config"])["runs_per_test"] = 2
				task := jsonObject(t, jsonItems(t, result["tasks"])[0])
				runs := jsonItems(t, task["runs"])
				second := cloneJSONMap(t, jsonObject(t, runs[0]))
				second["run_number"] = 2
				task["runs"] = append(runs, second)
			},
			runExit: 0, threshold: "1", wantValid: true, wantStatus: "success",
		},
		{
			name: "skipped diagnostic is not successful grading",
			mutate: func(result map[string]any) {
				summary := jsonObject(t, result["summary"])
				summary["succeeded"], summary["skipped"], summary["success_rate"] = 0, 1, 0.0
				task := jsonObject(t, jsonItems(t, result["tasks"])[0])
				task["status"], task["runs"] = "skipped", []any{}
			},
			runExit: 0, threshold: "0", wantValid: true, wantStatus: "failure",
		},
		{
			name: "error diagnostic without trials",
			mutate: func(result map[string]any) {
				summary := jsonObject(t, result["summary"])
				summary["succeeded"], summary["errors"], summary["success_rate"] = 0, 1, 0.0
				task := jsonObject(t, jsonItems(t, result["tasks"])[0])
				task["status"], task["runs"] = "error", []any{}
			},
			runExit: 1, threshold: "0", wantValid: true, wantStatus: "failure",
		},
		{
			name: "completed failing grading",
			mutate: func(result map[string]any) {
				summary := jsonObject(t, result["summary"])
				summary["succeeded"], summary["failed"], summary["success_rate"] = 0, 1, 0.0
				task := jsonObject(t, jsonItems(t, result["tasks"])[0])
				task["status"] = "failed"
				jsonObject(t, jsonItems(t, task["runs"])[0])["status"] = "failed"
			},
			runExit: 1, threshold: "0.8", wantValid: true, wantStatus: "failure",
		},
		{name: "runtime error with no artifact", runExit: 2, threshold: "0.8", wantValid: false},
		{
			name: "absent score",
			mutate: func(result map[string]any) {
				delete(jsonObject(t, result["summary"]), "aggregate_score")
			},
			runExit: 0, threshold: "0.8", wantValid: false,
		},
		{
			name: "inconsistent successful exit",
			mutate: func(result map[string]any) {
				summary := jsonObject(t, result["summary"])
				summary["succeeded"], summary["errors"], summary["success_rate"] = 0, 1, 0.0
				jsonObject(t, jsonItems(t, result["tasks"])[0])["status"] = "error"
			},
			runExit: 0, threshold: "0.8", wantValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			resultPath := filepath.Join(dir, "results.json")
			outputPath := filepath.Join(dir, "github-output")

			if tt.name != "runtime error with no artifact" {
				data, err := json.Marshal(cloneJSONMap(t, valid))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(resultPath, data, 0o600))
				if tt.mutate != nil {
					var result map[string]any
					require.NoError(t, json.Unmarshal(data, &result))
					tt.mutate(result)
					data, err = json.Marshal(result)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(resultPath, data, 0o600))
				}
			}

			command := exec.Command("bash", "validate-waza-result.sh", resultPath, tt.threshold, strconv.Itoa(tt.runExit))
			command.Env = append(os.Environ(), "GITHUB_OUTPUT="+outputPath)
			output, err := command.CombinedOutput()
			if tt.wantValid {
				require.NoError(t, err, string(output))
				recorded, readErr := os.ReadFile(outputPath)
				require.NoError(t, readErr)
				require.Contains(t, string(recorded), "status="+tt.wantStatus)
			} else {
				require.Error(t, err, string(output))
			}
		})
	}

	t.Run("malformed artifact", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "results.json")
		require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
		command := exec.Command("bash", "validate-waza-result.sh", path, "0.8", "0")
		require.Error(t, command.Run())
	})
	t.Run("multiple JSON documents", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "results.json")
		data, err := json.Marshal(valid)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(append(data, '\n'), data...), 0o600))
		command := exec.Command("bash", "validate-waza-result.sh", path, "0.8", "0")
		require.Error(t, command.Run())
	})
	t.Run("literal dash-prefixed result paths", func(t *testing.T) {
		script, err := filepath.Abs("validate-waza-result.sh")
		require.NoError(t, err)
		for _, name := range []string{"-results.json", "-"} {
			dir := t.TempDir()
			data, err := json.Marshal(valid)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
			outputPath := filepath.Join(dir, "github-output")
			command := exec.Command("bash", script, name, "0.8", "0")
			command.Dir = dir
			command.Stdin = strings.NewReader("invalid stdin must not be used")
			command.Env = append(os.Environ(), "GITHUB_OUTPUT="+outputPath)
			output, err := command.CombinedOutput()
			require.NoError(t, err, string(output))
			recorded, err := os.ReadFile(outputPath)
			require.NoError(t, err)
			require.Contains(t, string(recorded), "status=success")
			require.Contains(t, string(recorded), "success_rate=1")
			require.Contains(t, string(recorded), "aggregate_score=0")
		}
	})
}

func TestRetainedWazaWorkflowsUseCurrentContract(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"waza.yaml", "waza-eval.yml", "skills-ci-example.yml"} {
		source, err := os.ReadFile(filepath.Join("..", "workflows", name))
		require.NoError(t, err)
		var parsed any
		require.NoError(t, yaml.Unmarshal(source, &parsed), "%s must remain valid YAML", name)

		text := string(source)
		for _, obsolete := range []string{
			"pip install waza",
			"go install github.com/microsoft/waza",
			".summary.pass_rate",
			".summary.composite_score",
			"--fail-threshold",
			"waza report",
			"if-no-files-found: warn",
		} {
			require.NotContains(t, text, obsolete, "%s retains obsolete contract %q", name, obsolete)
		}
		require.Contains(t, text, "if-no-files-found: error")
	}

	reusable, err := os.ReadFile(filepath.Join("..", "workflows", "waza.yaml"))
	require.NoError(t, err)
	for _, required := range []string{
		"default: '0.41.0'",
		"install-waza-release.sh",
		"validate-waza-result.sh",
		"steps.validate.outputs.success_rate",
		"steps.validate.outputs.aggregate_score",
	} {
		require.Contains(t, string(reusable), required)
	}

	automatic, err := os.ReadFile(filepath.Join("..", "workflows", "waza-eval.yml"))
	require.NoError(t, err)
	for _, required := range []string{
		"id: effective",
		"output-file=",
		"artifact-name=",
		"rm -f -- \"$OUTPUT_FILE\"",
		"validate-waza-result.sh",
	} {
		require.Contains(t, string(automatic), required)
	}
}

func TestRunHelpMatchesWorkflowFlags(t *testing.T) {
	command := exec.Command("go", "run", "../../cmd/waza", "run", "--help")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	help := string(output)
	for _, flag := range []string{"--context-dir", "--format", "--output", "--trials", "--verbose"} {
		require.Contains(t, help, flag)
	}
	for _, obsolete := range []string{"--fail-threshold", "json, markdown, github"} {
		require.NotContains(t, help, obsolete)
	}
}

func TestWorkflowContractAgainstActualCLIArtifacts(t *testing.T) {
	dir := t.TempDir()
	guard := exec.Command("bash", "go_offline_env.sh")
	guard.Env = append(os.Environ(), "RUNNER_TEMP="+dir)
	guardOutput, err := guard.Output()
	require.NoError(t, err, string(guardOutput))
	offlineEnv := strings.Split(strings.TrimSpace(string(guardOutput)), "\n")
	t.Cleanup(func() {
		entries, err := filepath.Glob(filepath.Join(dir, "*.invoked"))
		require.NoError(t, err)
		require.Empty(t, entries, "all production fixtures must avoid launching Copilot")
	})
	binary := filepath.Join(dir, "waza")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/waza")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	runStep := evalWorkflowStepNamed(t, readEvalWorkflowSteps(t, "waza-eval.yml", "run-evaluation"), "Run Evaluation")
	stageStep := evalWorkflowStepNamed(t, readEvalWorkflowSteps(t, "waza-eval.yml", "run-evaluation"), "Stage exact result artifact")

	tests := []struct {
		name          string
		spec          string
		wantRunExit   int
		wantValidated bool
		wantStatus    string
	}{
		{
			name: "passing grading",
			spec: `name: workflow-pass
skill: workflow-contract
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  parallel: false
  executor: mock
  model: test-model
tasks:
  - "tasks/*.yaml"
`,
			wantRunExit: 0, wantValidated: true, wantStatus: "success",
		},
		{
			name: "completed failing grading",
			spec: `name: workflow-fail
skill: workflow-contract
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  parallel: false
  executor: mock
  model: test-model
graders:
  - type: text
    name: must-fail
    config:
      regex_match:
        - "NEVER_MATCH_WORKFLOW_CONTRACT"
tasks:
  - "tasks/*.yaml"
`,
			wantRunExit: 1, wantValidated: true, wantStatus: "failure",
		},
		{
			name: "task runtime error",
			spec: `name: workflow-error
skill: workflow-contract
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  parallel: false
  executor: mock
  model: test-model
tasks:
  - "tasks/*.yaml"
`,
			wantRunExit: 1, wantValidated: true, wantStatus: "failure",
		},
		{
			name:          "configuration error",
			spec:          "name: invalid\nconfig: [\n",
			wantRunExit:   2,
			wantValidated: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(workspace, "tasks"), 0o700))
			taskYAML := "id: workflow-task\nname: Workflow task\ninputs:\n  prompt: Explain this code\n"
			if tt.name == "task runtime error" {
				taskYAML += "instruction_files:\n  - missing-workflow-instruction.md\n"
			}
			require.NoError(t, os.WriteFile(
				filepath.Join(workspace, "tasks", "task.yaml"),
				[]byte(taskYAML),
				0o600,
			))
			specPath := filepath.Join(workspace, "eval.yaml")
			resultPath := filepath.Join(workspace, "results.json")
			require.NoError(t, os.WriteFile(specPath, []byte(tt.spec), 0o600))

			runOutputs := filepath.Join(workspace, "run-output")
			run := exec.Command("bash", "-e", "-o", "pipefail", "-c", runStep.Run)
			run.Dir = workspace
			run.Env = append(os.Environ(), offlineEnv...)
			run.Env = append(run.Env, "EVAL_FILE="+specPath, "OUTPUT_FILE="+resultPath,
				"CONTEXT_DIR=", "VERBOSE=false", "GITHUB_OUTPUT="+runOutputs,
				"WAZA_NO_UPDATE_CHECK=1", "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			runOutput, runErr := run.CombinedOutput()
			require.NoError(t, runErr, string(runOutput))
			exitOutput, readErr := os.ReadFile(runOutputs)
			require.NoError(t, readErr)
			runExit, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(string(exitOutput), "exit_code=")))
			require.NoError(t, parseErr)
			require.Equal(t, tt.wantRunExit, runExit, string(runOutput))

			githubOutput := filepath.Join(workspace, "github-output")
			validate := exec.Command("bash", filepath.Join("validate-waza-result.sh"), resultPath, "0.8", strconv.Itoa(runExit))
			validate.Env = append(os.Environ(), "GITHUB_OUTPUT="+githubOutput)
			validateOutput, validateErr := validate.CombinedOutput()
			if !tt.wantValidated {
				require.Error(t, validateErr, string(validateOutput))
				return
			}
			require.NoError(t, validateErr, string(validateOutput))
			recorded, readErr := os.ReadFile(githubOutput)
			require.NoError(t, readErr)
			require.Contains(t, string(recorded), "status="+tt.wantStatus)
			data, readErr := os.ReadFile(resultPath)
			require.NoError(t, readErr)
			var produced map[string]any
			require.NoError(t, json.Unmarshal(data, &produced))
			tasks := jsonItems(t, produced["tasks"])
			require.Len(t, tasks, 1)
			task := jsonObject(t, tasks[0])
			runs := jsonItems(t, task["runs"])
			require.Len(t, runs, 1, "production fixture must contain a real trial")
			trial := jsonObject(t, runs[0])
			if tt.name == "completed failing grading" {
				require.Equal(t, "failed", task["status"])
				require.Equal(t, "failed", trial["status"])
				require.Empty(t, trial["error_msg"])
				require.NotEmpty(t, trial["final_output"])
				require.Empty(t, jsonObject(t, trial["session_digest"])["errors"])
				require.Equal(t, float64(0), jsonObject(t, produced["summary"])["errors"])
				validations := jsonObject(t, trial["validations"])
				require.Contains(t, validations, "must-fail")
				grading := jsonObject(t, validations["must-fail"])
				require.Equal(t, "text", grading["type"])
				require.Equal(t, false, grading["passed"])
				require.Equal(t, float64(0), grading["score"])
				if details, present := grading["details"]; present {
					require.Empty(t, jsonObject(t, details)["evaluation_errors"])
				}
			}
			if tt.name == "task runtime error" {
				require.Equal(t, "error", trial["status"])
				require.Contains(t, trial["error_msg"], "missing-workflow-instruction.md")
			}
			stageOutputs := filepath.Join(workspace, "stage-output")
			runEvalWorkflowScript(t, stageStep.Run, workspace, []string{
				"OUTPUT_FILE=" + resultPath, "RUNNER_TEMP=" + dir, "GITHUB_OUTPUT=" + stageOutputs,
			})
			staged, readErr := os.ReadFile(stageOutputs)
			require.NoError(t, readErr)
			stagedDir := strings.TrimSpace(strings.TrimPrefix(string(staged), "artifact-path="))
			stagedData, readErr := os.ReadFile(filepath.Join(stagedDir, filepath.Base(resultPath)))
			require.NoError(t, readErr)
			require.Equal(t, data, stagedData, "upload must contain the actual production result")
			if tt.wantRunExit == 0 {
				for _, mutation := range []struct {
					name   string
					change func(map[string]any)
				}{
					{"missing trials", func(r map[string]any) { jsonObject(t, jsonItems(t, r["tasks"])[0])["runs"] = []any{} }},
					{"null trial", func(r map[string]any) { jsonObject(t, jsonItems(t, r["tasks"])[0])["runs"] = []any{nil} }},
					{"incomplete trial", func(r map[string]any) {
						jsonObject(t, jsonItems(t, r["tasks"])[0])["runs"] = []any{map[string]any{"run_number": 1, "status": "passed"}}
					}},
					{"missing expected trial", func(r map[string]any) { jsonObject(t, r["config"])["runs_per_test"] = 2 }},
					{"wrong trial index", func(r map[string]any) {
						task := jsonObject(t, jsonItems(t, r["tasks"])[0])
						jsonObject(t, jsonItems(t, task["runs"])[0])["run_number"] = 2
					}},
					{"incomplete task", func(r map[string]any) { r["tasks"] = []any{map[string]any{}} }},
					{"null score", func(r map[string]any) { jsonObject(t, r["summary"])["aggregate_score"] = nil }},
					{"string rate", func(r map[string]any) { jsonObject(t, r["summary"])["success_rate"] = "invalid" }},
					{"inflated skipped rate", func(r map[string]any) {
						s := jsonObject(t, r["summary"])
						s["succeeded"], s["skipped"] = 0, 1
						jsonObject(t, jsonItems(t, r["tasks"])[0])["status"] = "skipped"
					}},
				} {
					t.Run(mutation.name, func(t *testing.T) {
						changed := cloneJSONMap(t, produced)
						mutation.change(changed)
						changedData, marshalErr := json.Marshal(changed)
						require.NoError(t, marshalErr)
						require.NotEqual(t, string(data), string(changedData))
						path := filepath.Join(t.TempDir(), "mutated.json")
						require.NoError(t, os.WriteFile(path, changedData, 0o600))
						check := exec.Command("bash", "validate-waza-result.sh", path, "0", "0")
						require.Error(t, check.Run())
					})
				}
			}
		})
	}
}

func TestPinnedReleaseInstaller(t *testing.T) {
	script, err := filepath.Abs("install-waza-release.sh")
	require.NoError(t, err)
	for _, control := range []string{"valid", "bad checksum", "missing checksum", "download error", "invalid version"} {
		t.Run(control, func(t *testing.T) {
			dir := t.TempDir()
			payload := filepath.Join(dir, "payload")
			require.NoError(t, os.WriteFile(payload, []byte("#!/bin/sh\necho exact-release\n"), 0o600))
			curl := `#!/bin/bash
			set -euo pipefail
			destination=
			while [ "$#" -gt 0 ]; do
			  if [ "$1" = -o ]; then destination="$2"; shift; fi
			  url="$1"
			  shift
			done
			case "$url" in
			  */waza-linux-amd64)
			    if [ "$CONTROL" = "download error" ]; then exit 22; fi
			    cp "$PAYLOAD" "$destination"
			    ;;
			  */checksums.txt)
			    hash="$(sha256sum "$PAYLOAD" | cut -d' ' -f1)"
			    if [ "$CONTROL" = "bad checksum" ]; then hash=0000000000000000000000000000000000000000000000000000000000000000; fi
			    asset=waza-linux-amd64
			    if [ "$CONTROL" = "missing checksum" ]; then asset=another-asset; fi
			    printf '%s  %s\n' "$hash" "$asset" > "$destination"
			    ;;
			  *) exit 22 ;;
			esac
			`
			require.NoError(t, os.WriteFile(filepath.Join(dir, "curl"), []byte(curl), 0o700))
			version := "0.41.0"
			if control == "invalid version" {
				version = "latest"
			}
			command := exec.Command("bash", script, version)
			command.Env = append(os.Environ(), "RUNNER_TEMP="+dir, "GITHUB_PATH="+filepath.Join(dir, "github-path"),
				"CONTROL="+control, "PAYLOAD="+payload, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, runErr := command.CombinedOutput()
			if control != "valid" {
				require.Error(t, runErr, string(output))
				_, statErr := os.Stat(filepath.Join(dir, "waza-bin", "waza"))
				require.True(t, os.IsNotExist(statErr), "failed installation must not publish a binary")
				return
			}
			require.NoError(t, runErr, string(output))
			installed, readErr := os.ReadFile(filepath.Join(dir, "waza-bin", "waza"))
			require.NoError(t, readErr)
			require.Equal(t, "#!/bin/sh\necho exact-release\n", string(installed))
			path, readErr := os.ReadFile(filepath.Join(dir, "github-path"))
			require.NoError(t, readErr)
			require.Equal(t, filepath.Join(dir, "waza-bin")+"\n", string(path))
		})
	}
}

func TestAzureWorkflowNumericGate(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "docs", "CI-CD-GUIDE.md"))
	require.NoError(t, err)
	var script string
	for _, block := range strings.Split(string(source), "```yaml\n")[1:] {
		block, _, _ = strings.Cut(block, "```")
		if !strings.Contains(block, "displayName: Check results") {
			continue
		}
		var pipeline struct {
			Steps []struct {
				Script      string `yaml:"script"`
				DisplayName string `yaml:"displayName"`
			} `yaml:"steps"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(block), &pipeline))
		for _, step := range pipeline.Steps {
			if step.DisplayName == "Check results" {
				script = step.Script
			}
		}
	}
	require.NotEmpty(t, script, "test must execute the copied Azure script")
	for _, value := range []string{"1", "0", `"invalid"`, "null"} {
		t.Run(value, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "results.json"),
				[]byte(`{"summary":{"success_rate":`+value+`}}`), 0o600))
			command := exec.Command("bash", "-c", script)
			command.Dir = dir
			output, runErr := command.CombinedOutput()
			if value == "1" {
				require.NoError(t, runErr, string(output))
			} else {
				require.Error(t, runErr, string(output))
			}
		})
	}
}

type evalWorkflowStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]string `yaml:"with"`
}

func readEvalWorkflowSteps(t *testing.T, name, job string) []evalWorkflowStep {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "workflows", name))
	require.NoError(t, err)
	var workflow struct {
		Jobs map[string]struct {
			Steps []evalWorkflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(source, &workflow))
	steps := workflow.Jobs[job].Steps
	require.NotEmpty(t, steps)
	return steps
}

func evalWorkflowStepNamed(t *testing.T, steps []evalWorkflowStep, name string) evalWorkflowStep {
	t.Helper()
	for _, step := range steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("missing workflow step %q", name)
	return evalWorkflowStep{}
}

func runEvalWorkflowScript(t *testing.T, script, directory string, environment []string) string {
	t.Helper()
	command := exec.Command("bash", "-e", "-o", "pipefail", "-c", script)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return string(output)
}

func declaredWorkflowDefaults(t *testing.T, name, event string) map[string]string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "workflows", name))
	require.NoError(t, err)
	var workflow struct {
		On map[string]struct {
			Inputs map[string]struct {
				Default any `yaml:"default"`
			} `yaml:"inputs"`
		} `yaml:"on"`
	}
	require.NoError(t, yaml.Unmarshal(source, &workflow))
	defaults := map[string]string{}
	for name, input := range workflow.On[event].Inputs {
		if input.Default != nil {
			defaults[name] = fmt.Sprint(input.Default)
		}
	}
	return defaults
}

func TestAutomaticWorkflowEffectiveInputs(t *testing.T) {
	steps := readEvalWorkflowSteps(t, "waza-eval.yml", "run-evaluation")
	resolve := evalWorkflowStepNamed(t, steps, "Resolve effective inputs")
	require.Equal(t, map[string]string{
		"EVAL_INPUT": "${{ inputs.eval-yaml }}", "CONTEXT_INPUT": "${{ inputs.context-dir }}",
		"VERBOSE_INPUT": "${{ inputs.verbose }}", "OUTPUT_INPUT": "${{ inputs.output-file }}",
		"ARTIFACT_INPUT": "${{ inputs.artifact-name }}",
	}, resolve.Env)
	run := evalWorkflowStepNamed(t, steps, "Run Evaluation")
	upload := evalWorkflowStepNamed(t, steps, "Upload Results Artifact")
	require.Equal(t, "${{ steps.effective.outputs.artifact-name }}", upload.With["name"])
	require.Equal(t, "${{ steps.stage-result.outputs.artifact-path }}", upload.With["path"])
	require.Equal(t, "always() && steps.stage-result.outcome == 'success'", upload.If)
	require.Equal(t, "true", upload.With["include-hidden-files"])
	require.Equal(t, "error", upload.With["if-no-files-found"])
	transcripts := evalWorkflowStepNamed(t, steps, "Upload optional transcripts")
	require.Equal(t, "transcripts/", transcripts.With["path"])
	require.NotEqual(t, "true", transcripts.With["include-hidden-files"])
	require.Equal(t, "ignore", transcripts.With["if-no-files-found"], "hidden-only optional inventory must not fail the required JSON upload")
	require.Equal(t, "${{ steps.effective.outputs.output-file }}", run.Env["OUTPUT_FILE"])
	for _, event := range []string{"push", "pull_request", "workflow_dispatch", "workflow_call"} {
		for _, custom := range []bool{false, true} {
			t.Run(event+strconv.FormatBool(custom), func(t *testing.T) {
				dir := t.TempDir()
				outputPath := filepath.Join(dir, "outputs")
				argvPath := filepath.Join(dir, "argv")
				require.NoError(t, os.WriteFile(filepath.Join(dir, "waza"), []byte("#!/bin/bash\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\nexit 1\n"), 0o700))
				defaults := declaredWorkflowDefaults(t, "waza-eval.yml", event)
				if event == "workflow_call" {
					defaults["eval-yaml"] = "examples/code-explainer/eval.yaml"
				}
				env := []string{"GITHUB_EVENT_NAME=" + event, "GITHUB_OUTPUT=" + outputPath,
					"EVAL_INPUT=" + defaults["eval-yaml"], "CONTEXT_INPUT=" + defaults["context-dir"],
					"VERBOSE_INPUT=" + defaults["verbose"], "OUTPUT_INPUT=" + defaults["output-file"],
					"ARTIFACT_INPUT=" + defaults["artifact-name"]}
				customOutput := "custom results.json"
				if event == "workflow_dispatch" {
					customOutput = ".results.json"
				}
				if custom {
					env = []string{"GITHUB_EVENT_NAME=" + event, "GITHUB_OUTPUT=" + outputPath,
						"EVAL_INPUT=custom eval.yaml", "CONTEXT_INPUT=missing context", "VERBOSE_INPUT=false",
						"OUTPUT_INPUT=" + customOutput, "ARTIFACT_INPUT=custom evidence"}
				}

				runEvalWorkflowScript(t, resolve.Run, dir, env)
				resolved, err := os.ReadFile(outputPath)
				require.NoError(t, err)
				values := map[string]string{}
				for line := range strings.SplitSeq(strings.TrimSpace(string(resolved)), "\n") {
					key, value, ok := strings.Cut(line, "=")
					require.True(t, ok)
					values[key] = value
				}
				wantEval, wantOutput, wantArtifact := "examples/code-explainer/eval.yaml", "results.json", "waza-evaluation-results"
				wantArgs := []string{"run", wantEval, "--verbose", "--output", wantOutput}
				if custom {
					wantEval, wantOutput, wantArtifact = "custom eval.yaml", customOutput, "custom evidence"
					wantArgs = []string{"run", wantEval, "--context-dir", "missing context", "--output", wantOutput}
				}
				require.Equal(t, wantEval, values["eval-file"])
				require.Equal(t, wantOutput, values["output-file"])
				require.Equal(t, wantArtifact, values["artifact-name"])
				require.NoError(t, os.WriteFile(filepath.Join(dir, wantOutput), []byte("stale"), 0o600))
				runEvalWorkflowScript(t, run.Run, dir, []string{
					"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
					"GITHUB_OUTPUT=" + outputPath, "CAPTURE_ARGS=" + argvPath, "EVAL_FILE=" + values["eval-file"],
					"CONTEXT_DIR=" + values["context-dir"], "VERBOSE=" + values["verbose"], "OUTPUT_FILE=" + values["output-file"],
				})
				arguments, err := os.ReadFile(argvPath)
				require.NoError(t, err)
				require.Equal(t, wantArgs, strings.Split(strings.TrimSpace(string(arguments)), "\n"))
				_, err = os.Stat(filepath.Join(dir, wantOutput))
				require.True(t, os.IsNotExist(err), "execution must remove the selected stale artifact")
				resolved, err = os.ReadFile(outputPath)
				require.NoError(t, err)
				require.Contains(t, string(resolved), "exit_code=1\n")
			})
		}
	}
	t.Run("multiline input rejected", func(t *testing.T) {
		command := exec.Command("bash", "-e", "-c", resolve.Run)
		command.Env = append(os.Environ(), "EVAL_INPUT=bad\npath", "CONTEXT_INPUT=", "VERBOSE_INPUT=", "OUTPUT_INPUT=", "ARTIFACT_INPUT=")
		require.Error(t, command.Run())
	})
}

func TestWorkflowStagesOnlyExactResult(t *testing.T) {
	stage := evalWorkflowStepNamed(t, readEvalWorkflowSteps(t, "waza-eval.yml", "run-evaluation"), "Stage exact result artifact")
	require.Equal(t, "always() && steps.run-eval.outputs.exit_code != ''", stage.If)
	require.Equal(t, "${{ steps.effective.outputs.output-file }}", stage.Env["OUTPUT_FILE"])
	for _, name := range []string{"results.json", ".results.json", "custom results.json", "*.json", "!results.json", "nested/*/result.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte("fresh result"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "unrelated.json"), []byte("stale"), 0o600))
			outputPath := filepath.Join(dir, "stage-output")
			runEvalWorkflowScript(t, stage.Run, dir, []string{
				"OUTPUT_FILE=" + name, "RUNNER_TEMP=" + dir, "GITHUB_OUTPUT=" + outputPath,
			})
			output, err := os.ReadFile(outputPath)
			require.NoError(t, err)
			stagedDir := strings.TrimSpace(strings.TrimPrefix(string(output), "artifact-path="))
			require.Equal(t, dir, filepath.Dir(stagedDir))
			entries, err := os.ReadDir(stagedDir)
			require.NoError(t, err)
			require.Len(t, entries, 1, "upload directory must exclude unrelated and stale evidence")
			require.Equal(t, filepath.Base(name), entries[0].Name())
			staged, err := os.ReadFile(filepath.Join(stagedDir, entries[0].Name()))
			require.NoError(t, err)
			require.Equal(t, "fresh result", string(staged))
		})
	}
	for _, control := range []string{"missing", "directory", "symlink", "copy failure", "temporary directory failure"} {
		t.Run(control, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "result.json")
			target := filepath.Join(dir, "unrelated.json")
			require.NoError(t, os.WriteFile(target, []byte("user-owned stale evidence"), 0o600))
			switch control {
			case "directory":
				require.NoError(t, os.Mkdir(path, 0o700))
			case "symlink":
				require.NoError(t, os.Symlink(target, path))
			case "copy failure", "temporary directory failure":
				require.NoError(t, os.WriteFile(path, []byte("fresh result"), 0o600))
				command := "cp"
				if control == "temporary directory failure" {
					command = "mktemp"
				}
				require.NoError(t, os.WriteFile(filepath.Join(dir, command), []byte("#!/bin/bash\necho 'injected staging failure' >&2\nexit 1\n"), 0o700))
			}
			outputPath := filepath.Join(dir, "stage-output")
			command := exec.Command("bash", "-e", "-o", "pipefail", "-c", stage.Run)
			command.Dir = dir
			command.Env = append(os.Environ(), "OUTPUT_FILE="+path, "RUNNER_TEMP="+dir,
				"GITHUB_OUTPUT="+outputPath, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			require.Error(t, err, string(output))
			_, err = os.Stat(outputPath)
			require.True(t, os.IsNotExist(err), "failed staging must not export an upload path")
			retained, err := os.ReadFile(target)
			require.NoError(t, err)
			require.Equal(t, "user-owned stale evidence", string(retained))
		})
	}
}

func TestSkillsWorkflowDeclaredDispatchDefault(t *testing.T) {
	step := evalWorkflowStepNamed(t, readEvalWorkflowSteps(t, "skills-ci-example.yml", "evaluate-skill"), "Determine Eval File")
	defaults := declaredWorkflowDefaults(t, "skills-ci-example.yml", "workflow_dispatch")
	for _, repository := range []string{"microsoft/waza", "example/skill"} {
		t.Run(repository, func(t *testing.T) {
			for _, override := range []string{"", "custom eval.yaml"} {
				dir := t.TempDir()
				want := "eval/eval.yaml"
				if repository == "microsoft/waza" {
					want = "examples/code-explainer/eval.yaml"
				}
				if override != "" {
					want = override
				}
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, want)), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, want), []byte("fixture"), 0o600))
				input := defaults["eval-yaml"]
				if override != "" {
					input = override
				}
				path := filepath.Join(dir, "outputs")
				runEvalWorkflowScript(t, step.Run, dir, []string{"REPOSITORY=" + repository,
					"EVAL_INPUT=" + input, "GITHUB_OUTPUT=" + path})
				output, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "eval-file="+want+"\n", string(output))
			}
		})
	}
}

func TestReusableWorkflowTrialOverride(t *testing.T) {
	run := evalWorkflowStepNamed(t, readEvalWorkflowSteps(t, "waza.yaml", "eval"), "Run Evaluation")
	for _, trials := range []string{"", "0", "2", "1.5", "-1"} {
		t.Run("trials="+trials, func(t *testing.T) {
			dir := t.TempDir()
			argvPath := filepath.Join(dir, "argv")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "waza"),
				[]byte("#!/bin/bash\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\n"), 0o700))
			command := exec.Command("bash", "-e", "-o", "pipefail", "-c", run.Run)
			command.Dir = dir
			command.Env = append(os.Environ(), "EVAL_PATH=eval path.yaml", "OUTPUT_FORMAT=default",
				"TRIALS="+trials, "CAPTURE_ARGS="+argvPath, "GITHUB_OUTPUT="+filepath.Join(dir, "outputs"),
				"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			output, err := command.CombinedOutput()
			if trials == "1.5" || trials == "-1" {
				require.Error(t, err, string(output))
				_, statErr := os.Stat(argvPath)
				require.True(t, os.IsNotExist(statErr))
				return
			}
			require.NoError(t, err, string(output))
			arguments, err := os.ReadFile(argvPath)
			require.NoError(t, err)
			want := []string{"run", "eval path.yaml", "--output", "results.json", "--format", "default"}
			if trials == "2" {
				want = append(want, "--trials", "2")
			}
			require.Equal(t, want, strings.Split(strings.TrimSpace(string(arguments)), "\n"))
		})
	}
}

func jsonObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok, "expected JSON object, got %T", value)
	return object
}

func jsonItems(t *testing.T, value any) []any {
	t.Helper()
	items, ok := value.([]any)
	require.True(t, ok, "expected JSON array, got %T", value)
	return items
}

func cloneJSONMap(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(input)
	require.NoError(t, err)
	var output map[string]any
	require.NoError(t, json.Unmarshal(data, &output))
	return output
}

func TestWazaWorkflowDocsDoNotAdvertiseLegacySummaryFields(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		filepath.Join("..", "..", "docs", "CI-CD-GUIDE.md"),
		filepath.Join("..", "..", "site", "src", "content", "docs", "guides", "ci-cd.mdx"),
	} {
		source, err := os.ReadFile(path)
		require.NoError(t, err)
		text := string(source)
		require.False(t, strings.Contains(text, ".summary.pass_rate"), path)
		require.False(t, strings.Contains(text, "upload-artifact@v3"), path)
	}
}

func TestDocumentedWazaResultContract(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "docs", "CI-CD-GUIDE.md"))
	require.NoError(t, err)
	_, section, found := strings.Cut(string(source), "### Result JSON Structure")
	require.True(t, found)
	_, section, found = strings.Cut(section, "```json\n")
	require.True(t, found)
	example, _, found := strings.Cut(section, "```")
	require.True(t, found)
	resultPath := filepath.Join(t.TempDir(), "documented-result.json")
	require.NoError(t, os.WriteFile(resultPath, []byte(example), 0o600))
	command := exec.Command("bash", "validate-waza-result.sh", resultPath, "0.8", "0")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}
