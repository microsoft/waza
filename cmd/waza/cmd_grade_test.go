package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func gradeSpec(t *testing.T, dir string, specYAML string) string {
	t.Helper()
	p := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(p, []byte(specYAML), 0o644))
	return p
}

func writeTaskFile(t *testing.T, dir, filename, yaml string) {
	t.Helper()
	taskDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(taskDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, filename), []byte(yaml), 0o644))
}

func gradeResultsFile(t *testing.T, dir string, outcome *models.EvaluationOutcome) string {
	t.Helper()
	data, err := json.Marshal(outcome)
	require.NoError(t, err)
	p := filepath.Join(dir, "results.json")
	require.NoError(t, os.WriteFile(p, data, 0o644))
	return p
}

func outcomeWithTasks(tasks ...models.TestOutcome) *models.EvaluationOutcome {
	return &models.EvaluationOutcome{TestOutcomes: tasks}
}

func taskOutcome(taskID, output string) models.TestOutcome {
	return models.TestOutcome{
		TestID: taskID,
		Runs: []models.RunResult{{
			FinalOutput:   output,
			DurationMs:    1000,
			SessionDigest: models.SessionDigest{SessionID: "s-" + taskID},
		}},
	}
}

func parseMap(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	raw, ok := m[key]
	require.True(t, ok, "missing key %q", key)
	result, ok := raw.(map[string]any)
	require.True(t, ok, "%q is not a map", key)
	return result
}

func executeGrade(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := newGradeCommand()
	cmd.SetArgs(args)
	cmd.SetOut(&buf)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	return buf.String(), err
}

const (
	minimalSpec = `name: grade-test
skill: test
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
tasks:
  - "tasks/*.yaml"
`

	taskWithCodeGrader = `id: task-001
name: Task One
inputs:
  prompt: "Do something"
graders:
  - name: length_check
    type: code
    config:
      assertions:
        - "len(output) > 5"
`

	taskWithTextGrader = `id: task-002
name: Task Two
inputs:
  prompt: "Say hello"
graders:
  - name: hello_check
    type: text
    config:
      contains:
        - "hello"
`
)

func TestGradeCommand_NoArgs(t *testing.T) {
	cmd := newGradeCommand()
	cmd.SetArgs([]string{})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	require.Error(t, err)
}

func TestGradeCommand_MissingResults(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	_, err := executeGrade(t, specPath)
	require.ErrorContains(t, err, "required flag")
}

func TestGradeCommand_NonexistentSpec(t *testing.T) {
	_, err := executeGrade(t, "/nonexistent/eval.yaml", "--results", "/tmp/r.json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to load spec")
}

func TestGradeCommand_NonexistentResultsFile(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	_, err := executeGrade(t, specPath, "--results", "/nonexistent/results.json")
	require.ErrorContains(t, err, "failed to read results file")
}

func TestGradeCommand_InvalidResultsJSON(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	badFile := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(badFile, []byte("{invalid"), 0o644))

	_, err := executeGrade(t, specPath, "--results", badFile)
	require.ErrorContains(t, err, "failed to parse results JSON")
}

func TestGradeCommand_TaskNotInSpec(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-001", "some output")))
	_, err := executeGrade(t, specPath, "--task", "nonexistent-task", "--results", results)
	require.ErrorContains(t, err, "not found in spec")
}

func TestGradeCommand_TaskNotInResults(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("other-task", "output")))
	_, err := executeGrade(t, specPath, "--task", "task-001", "--results", results)
	require.ErrorContains(t, err, "not found in results file")
}

func TestGradeCommand_TaskZeroRuns(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	results := gradeResultsFile(t, dir, outcomeWithTasks(models.TestOutcome{
		TestID: "task-001",
		Runs:   []models.RunResult{},
	}))
	_, err := executeGrade(t, specPath, "--task", "task-001", "--results", results)
	require.ErrorContains(t, err, "has no runs")
}

func TestGradeCommand_SingleTask_Passing(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-001", "this is a long enough output")))
	output, err := executeGrade(t, specPath, "--task", "task-001", "--results", results)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))

	require.Equal(t, 1.0, parsed["overall_score"])
	require.Equal(t, true, parsed["passed"])
	tasks := parseMap(t, parsed, "tasks")
	require.Contains(t, tasks, "task-001")
}

func TestGradeCommand_SingleTask_Failing(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-001", "hi")))
	output, err := executeGrade(t, specPath, "--task", "task-001", "--results", results)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))

	require.Equal(t, 0.0, parsed["overall_score"])
	require.Equal(t, false, parsed["passed"])
}

func TestGradeCommand_AllTasks(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task1.yaml", taskWithCodeGrader)
	writeTaskFile(t, dir, "task2.yaml", taskWithTextGrader)

	results := gradeResultsFile(t, dir, outcomeWithTasks(
		taskOutcome("task-001", "this is a long enough output"),
		taskOutcome("task-002", "hello world"),
	))
	output, err := executeGrade(t, specPath, "--results", results)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))

	require.Equal(t, true, parsed["passed"])
	require.InDelta(t, 1.0, parsed["overall_score"], 0.001)

	tasks := parseMap(t, parsed, "tasks")
	require.Len(t, tasks, 2)
	require.Contains(t, tasks, "task-001")
	require.Contains(t, tasks, "task-002")
}

func TestGradeCommand_AllTasks_MixedPassFail(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task1.yaml", taskWithCodeGrader)
	writeTaskFile(t, dir, "task2.yaml", taskWithTextGrader)

	results := gradeResultsFile(t, dir, outcomeWithTasks(
		taskOutcome("task-001", "this is long enough"), // passes code grader
		taskOutcome("task-002", "goodbye world"),       // fails text grader (no "hello")
	))
	output, err := executeGrade(t, specPath, "--results", results)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))

	require.Equal(t, false, parsed["passed"])

	tasks := parseMap(t, parsed, "tasks")
	t1 := parseMap(t, tasks, "task-001")
	t2 := parseMap(t, tasks, "task-002")
	require.Equal(t, true, t1["passed"])
	require.Equal(t, false, t2["passed"])
}

func TestGradeCommand_AggregatesAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, `name: grade-test
skill: test
version: "1.0"
config:
  trials_per_task: 2
  timeout_seconds: 30
  executor: mock
  model: test
tasks:
  - "tasks/*.yaml"
`)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	results := gradeResultsFile(t, dir, outcomeWithTasks(models.TestOutcome{
		TestID: "task-001",
		Runs: []models.RunResult{
			{
				RunNumber:     1,
				FinalOutput:   "this is a long enough output",
				DurationMs:    1000,
				SessionDigest: models.SessionDigest{SessionID: "s-task-001-1"},
			},
			{
				RunNumber:     2,
				FinalOutput:   "no",
				DurationMs:    1000,
				SessionDigest: models.SessionDigest{SessionID: "s-task-001-2"},
			},
		},
	}))

	output, err := executeGrade(t, specPath, "--task", "task-001", "--results", results)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	require.InDelta(t, 0.5, parsed["overall_score"], 0.001)
	require.Equal(t, false, parsed["passed"])

	tasks := parseMap(t, parsed, "tasks")
	task := parseMap(t, tasks, "task-001")
	require.InDelta(t, 0.5, task["overall_score"], 0.001)
	require.Equal(t, false, task["passed"])
	graderAvgs := parseMap(t, task, "grader_averages")
	require.InDelta(t, 0.5, graderAvgs["length_check"], 0.001)
}

func TestGradeCommand_SkipsDisabledTasks(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "enabled.yaml", taskWithCodeGrader)
	writeTaskFile(t, dir, "disabled.yaml", `id: task-disabled
name: Disabled Task
enabled: false
inputs:
  prompt: "Do nothing"
graders:
  - name: disabled_check
    type: text
    config:
      contains:
        - "never used"
`)

	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-001", "this is a long enough output")))
	output, err := executeGrade(t, specPath, "--results", results)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	tasks := parseMap(t, parsed, "tasks")
	require.Len(t, tasks, 1)
	require.Contains(t, tasks, "task-001")
	require.NotContains(t, tasks, "task-disabled")
}

func TestGradeCommand_GlobalGraders(t *testing.T) {
	const specWithGlobal = `name: grade-test
skill: test
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
graders:
  - type: text
    name: no_errors
    config:
      regex_not_match:
        - "(?i)fatal error"
tasks:
  - "tasks/*.yaml"
`
	const plainTask = `id: task-001
name: Task One
inputs:
  prompt: "Do something"
`
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, specWithGlobal)
	writeTaskFile(t, dir, "task.yaml", plainTask)

	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-001", "all good")))
	output, err := executeGrade(t, specPath, "--task", "task-001", "--results", results)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	require.Equal(t, true, parsed["passed"])

	tasks := parseMap(t, parsed, "tasks")
	task := parseMap(t, tasks, "task-001")
	graderAvgs := parseMap(t, task, "grader_averages")
	_, hasNoErrors := graderAvgs["no_errors"]
	require.True(t, hasNoErrors, "global grader result should appear in output")
}

func TestGradeCommand_GlobalGraderFailing(t *testing.T) {
	const specWithGlobal = `name: grade-test
skill: test
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
graders:
  - type: text
    name: no_errors
    config:
      regex_not_match:
        - "(?i)fatal error"
tasks:
  - "tasks/*.yaml"
`
	const plainTask = `id: task-001
name: Task One
inputs:
  prompt: "Do something"
`
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, specWithGlobal)
	writeTaskFile(t, dir, "task.yaml", plainTask)

	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-001", "Fatal Error occurred")))
	output, err := executeGrade(t, specPath, "--task", "task-001", "--results", results)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	require.Equal(t, false, parsed["passed"])
}

func TestGradeCommand_CodeExplainerIntegration(t *testing.T) {
	evalPath := filepath.Join("../..", "examples", "code-explainer", "eval.yaml")
	if _, err := os.Stat(evalPath); err != nil {
		t.Skip("examples/code-explainer/eval.yaml not found")
	}

	dir := t.TempDir()
	results := gradeResultsFile(t, dir, outcomeWithTasks(
		taskOutcome("explain-python-recursion-001",
			"This recursive factorial function uses recursion with a base case where n <= 1"),
	))

	output, err := executeGrade(t, evalPath, "--task", "explain-python-recursion-001", "--results", results)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))
	require.Equal(t, true, parsed["passed"])
	require.Equal(t, 1.0, parsed["overall_score"])
}

func TestGradeCommand_Verbose(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-001", "long enough output here")))

	var errBuf bytes.Buffer
	cmd := newGradeCommand()
	cmd.SetArgs([]string{specPath, "--task", "task-001", "--results", results, "-v"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(&errBuf)

	err := cmd.Execute()
	require.NoError(t, err)
	require.Contains(t, errBuf.String(), "grading task-001")
}

func TestGradeCommand_OutputStructure(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	results := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-001", "sufficient output text")))
	output, err := executeGrade(t, specPath, "--results", results)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &parsed))

	_, hasScore := parsed["overall_score"]
	_, hasPassed := parsed["passed"]
	_, hasTasks := parsed["tasks"]
	require.True(t, hasScore, "output must contain overall_score")
	require.True(t, hasPassed, "output must contain passed")
	require.True(t, hasTasks, "output must contain tasks")

	tasks := parseMap(t, parsed, "tasks")
	for _, v := range tasks {
		taskMap, ok := v.(map[string]any)
		require.True(t, ok)
		_, hasTaskScore := taskMap["overall_score"]
		_, hasTaskPassed := taskMap["passed"]
		require.True(t, hasTaskScore, "each task must have overall_score")
		require.True(t, hasTaskPassed, "each task must have passed")
	}
}

func TestGradeCommand_OutputFlag_WritesEvaluationOutcome(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)

	resultsPath := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-001", "this is a long enough output")))
	outputFile := filepath.Join(dir, "graded.json")

	_, err := executeGrade(t, specPath, "--results", resultsPath, "--output", outputFile)
	require.NoError(t, err)

	data, err := os.ReadFile(outputFile)
	require.NoError(t, err)

	var outcome models.EvaluationOutcome
	require.NoError(t, json.Unmarshal(data, &outcome))

	require.Len(t, outcome.TestOutcomes, 1)
	require.Equal(t, "task-001", outcome.TestOutcomes[0].TestID)
	require.Equal(t, models.StatusPassed, outcome.TestOutcomes[0].Status)
	require.NotEmpty(t, outcome.TestOutcomes[0].Runs)
	require.NotEmpty(t, outcome.TestOutcomes[0].Runs[0].Validations)
	require.NotNil(t, outcome.TestOutcomes[0].Stats, "graded outcome must have computed stats")
	require.InDelta(t, 1.0, outcome.TestOutcomes[0].Stats.PassRate, 0.001)

	require.Equal(t, 1, outcome.Digest.TotalTests)
	require.Equal(t, 1, outcome.Digest.Succeeded)
	require.Equal(t, 0, outcome.Digest.Failed)
	require.InDelta(t, 1.0, outcome.Digest.SuccessRate, 0.001)
	require.InDelta(t, 1.0, outcome.Digest.AggregateScore, 0.001)
}

func TestGradeCommand_OutputFlag_PreservesUnmodifiedTasks(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task1.yaml", taskWithCodeGrader)
	// Even if task2 is in eval.yaml, if we specify --task task-001, we should preserve task-002
	writeTaskFile(t, dir, "task2.yaml", taskWithTextGrader)

	// Create a results file with two tasks
	resultsPath := gradeResultsFile(t, dir, outcomeWithTasks(
		taskOutcome("task-001", "this is a long enough output"), // passes length
		taskOutcome("task-002", "some un-regraded output"),      // won't be regraded, wasn't graded before
	))
	outputFile := filepath.Join(dir, "graded.json")

	_, err := executeGrade(t, specPath, "--task", "task-001", "--results", resultsPath, "--output", outputFile)
	require.NoError(t, err)

	data, err := os.ReadFile(outputFile)
	require.NoError(t, err)

	var outcome models.EvaluationOutcome
	require.NoError(t, json.Unmarshal(data, &outcome))

	// Should have both tasks in the outcome
	require.Len(t, outcome.TestOutcomes, 2)

	// One should be regraded and passed
	// The other should be preserved as-is
	var found1, found2 bool
	for _, to := range outcome.TestOutcomes {
		switch to.TestID {
		case "task-001":
			found1 = true
			require.Equal(t, models.StatusPassed, to.Status)
			require.NotEmpty(t, to.Runs[0].Validations)
		case "task-002":
			found2 = true
			require.Empty(t, to.Runs[0].Validations, "should not have regraded task-002")
		}
	}
	require.True(t, found1, "missing task-001")
	require.True(t, found2, "missing task-002")
}

func TestGradeCommand_WorkspaceNotExist(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)
	resultsPath := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-001", "hello world")))

	_, err := executeGrade(t, specPath, "--results", resultsPath, "--workspace", "/nonexistent/path")
	require.ErrorContains(t, err, "--workspace path")
}

func TestGradeCommand_WorkspaceIsFile(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)
	resultsPath := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("task-001", "hello world")))

	filePath := filepath.Join(dir, "afile.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("x"), 0o644))

	_, err := executeGrade(t, specPath, "--results", resultsPath, "--workspace", filePath)
	require.ErrorContains(t, err, "not a directory")
}

func TestGradeCommand_NoTasksGraded(t *testing.T) {
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithCodeGrader)
	resultsPath := gradeResultsFile(t, dir, outcomeWithTasks(taskOutcome("unrelated-task", "output")))

	_, err := executeGrade(t, specPath, "--results", resultsPath)
	require.ErrorContains(t, err, "no tasks were graded")
}

func TestGradeCommand_SkillInvocationGrader(t *testing.T) {
	const taskWithSkillInvocation = `id: task-skill
name: Skill Task
inputs:
  prompt: "Do something"
graders:
  - name: skill_check
    type: skill_invocation
    config:
      required_skills:
        - my-skill
      mode: any_order
`
	dir := t.TempDir()
	specPath := gradeSpec(t, dir, minimalSpec)
	writeTaskFile(t, dir, "task.yaml", taskWithSkillInvocation)

	outcome := outcomeWithTasks(models.TestOutcome{
		TestID: "task-skill",
		Runs: []models.RunResult{{
			FinalOutput:   "output",
			DurationMs:    1000,
			SessionDigest: models.SessionDigest{SessionID: "s-1"},
			SkillInvocations: []models.SkillInvocation{
				{Name: "my-skill", Path: "/skills/my-skill/SKILL.md"},
			},
		}},
	})
	resultsPath := gradeResultsFile(t, dir, outcome)

	out, err := executeGrade(t, specPath, "--results", resultsPath)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	passed, ok := result["passed"].(bool)
	require.True(t, ok)
	require.True(t, passed)
}
