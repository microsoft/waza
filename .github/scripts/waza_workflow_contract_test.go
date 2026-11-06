package scripts

import (
	"encoding/json"
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
		"summary": map[string]any{
			"total_tests": 1, "succeeded": 1, "failed": 0, "errors": 0, "skipped": 0,
			"success_rate": 1.0, "aggregate_score": 0.0, "weighted_score": 0.0,
		},
		"tasks": []any{map[string]any{"test_id": "workflow-task", "status": "passed", "runs": []any{}}},
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
		{
			name: "completed failing grading",
			mutate: func(result map[string]any) {
				summary := result["summary"].(map[string]any)
				summary["succeeded"], summary["failed"], summary["success_rate"] = 0, 1, 0.0
				result["tasks"].([]any)[0].(map[string]any)["status"] = "failed"
			},
			runExit: 1, threshold: "0.8", wantValid: true, wantStatus: "failure",
		},
		{name: "runtime error with no artifact", runExit: 2, threshold: "0.8", wantValid: false},
		{
			name: "absent score",
			mutate: func(result map[string]any) {
				delete(result["summary"].(map[string]any), "aggregate_score")
			},
			runExit: 0, threshold: "0.8", wantValid: false,
		},
		{
			name: "inconsistent successful exit",
			mutate: func(result map[string]any) {
				summary := result["summary"].(map[string]any)
				summary["succeeded"], summary["errors"], summary["success_rate"] = 0, 1, 0.0
				result["tasks"].([]any)[0].(map[string]any)["status"] = "error"
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
		"releases/download/v${WAZA_VERSION}/waza-linux-amd64",
		"sha256sum --check",
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
	binary := filepath.Join(dir, "waza")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/waza")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))

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
  - type: regex
    name: must-fail
    config:
      must_match:
        - "NEVER_MATCH_WORKFLOW_CONTRACT"
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
			require.NoError(t, os.WriteFile(
				filepath.Join(workspace, "tasks", "task.yaml"),
				[]byte("id: workflow-task\nname: Workflow task\ninputs:\n  prompt: Explain this code\n"),
				0o600,
			))
			specPath := filepath.Join(workspace, "eval.yaml")
			resultPath := filepath.Join(workspace, "results.json")
			require.NoError(t, os.WriteFile(specPath, []byte(tt.spec), 0o600))

			run := exec.Command(binary, "run", specPath, "--output", resultPath, "--no-update-check")
			run.Dir = workspace
			runOutput, runErr := run.CombinedOutput()
			runExit := 0
			if runErr != nil {
				var exitErr *exec.ExitError
				require.ErrorAs(t, runErr, &exitErr, string(runOutput))
				runExit = exitErr.ExitCode()
			}
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
		})
	}
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
