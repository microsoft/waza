package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCommandCSVTaskIdentityParity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.csv")
	require.NoError(t, os.WriteFile(path, []byte("id,name,prompt,value\nexplicit,Named,{{.TaskName}} {{.Vars.value}} {{.Vars.global}},row\n,NameOnly,{{.TaskName}},\n,,{{.TaskName}},\n"), 0o644))
	for _, absolute := range []bool{false, true} {
		for _, bounds := range [][2]int{{}, {2, 3}, {3, 99}, {4, 99}} {
			t.Run(fmt.Sprintf("absolute=%t/range=%v", absolute, bounds), func(t *testing.T) {
				source := "data.csv"
				if absolute {
					source = path
				}
				spec := &models.EvalSpec{
					TasksFrom: source, Range: bounds,
					Inputs: map[string]string{"value": "default", "global": "shared"},
				}
				tasks, err := loadTestCases(spec, filepath.Join(dir, "eval.yaml"))
				require.NoError(t, err)
				full := []struct{ id, name, prompt string }{
					{"explicit", "Named", "Named row shared"},
					{"NameOnly", "NameOnly", "NameOnly"},
					{"row-3", "row-3", "row-3"},
				}
				start, end := 0, len(full)
				if bounds != [2]int{} {
					start = bounds[0] - 1
					end = min(bounds[1], len(full))
				}
				require.Len(t, tasks, end-start)
				for i, task := range tasks {
					want := full[start+i]
					require.Equal(t, want.id, task.TestID)
					require.Equal(t, want.name, task.DisplayName)
					require.Equal(t, want.prompt, task.Stimulus.Message)
				}
				definitions, err := loadTestDefinitionYAML(spec, filepath.Join(dir, "eval.yaml"))
				require.NoError(t, err)
				require.Len(t, definitions, len(tasks))
				for _, task := range tasks {
					require.Contains(t, definitions, task.TestID)
					require.Contains(t, definitions[task.TestID], "id: "+task.TestID)
				}
			})
		}
	}
}

func TestRunAnalysis_RangedCSVIdentity(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "data.csv"), []byte("prompt\nfirst\n{{.TaskName}}\n"), 0o644))
	spec := &models.EvalSpec{
		TasksFrom: "data.csv", Range: [2]int{2, 2},
		Config: models.Config{EngineType: "mock"},
	}
	specPath := filepath.Join(dir, "eval.yaml")
	definitions, err := loadTestDefinitionYAML(spec, specPath)
	require.NoError(t, err)
	require.Len(t, definitions, 1)
	require.Contains(t, definitions, "row-2")
	require.Contains(t, definitions["row-2"], "prompt: row-2")
	outcome := outcomeWithTasks(taskOutcome("row-2", "failed output"))
	outcome.TestOutcomes[0].Status = models.StatusFailed
	report, err := generateEvalAnalysis(context.Background(), nil, spec, specPath, outcome, nil)
	require.NoError(t, err)
	require.NotEmpty(t, report)
}

func TestCommandCSVPathAndErrorParity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.csv")
	require.NoError(t, os.WriteFile(path, []byte("prompt\nfirst\nsecond\n"), 0o644))
	outside := filepath.Join(t.TempDir(), "external.csv")
	require.NoError(t, os.WriteFile(outside, []byte("prompt\nexternal\n"), 0o644))
	internal := filepath.Join(dir, "internal.csv")
	external := filepath.Join(dir, "external.csv")
	dangling := filepath.Join(dir, "dangling.csv")
	require.NoError(t, os.Symlink(path, internal))
	require.NoError(t, os.Symlink(outside, external))
	require.NoError(t, os.Symlink(filepath.Join(dir, "absent.csv"), dangling))
	for _, tc := range []struct {
		name, source, csv, want string
		bounds                  [2]int
	}{
		{name: "contained link", source: internal},
		{name: "lexical escape", source: outside, want: "escapes spec directory"},
		{name: "resolved escape", source: external, want: "escapes spec directory through symlinks"},
		{name: "dangling link", source: dangling, want: "loading CSV dataset"},
		{name: "missing", source: "absent.csv", want: "loading CSV dataset"},
		{name: "zero range", source: path, bounds: [2]int{0, 1}, want: "both values must be > 0"},
		{name: "reversed range", source: path, bounds: [2]int{2, 1}, want: "must be <= end"},
		{name: "invalid CSV", source: "bad.csv", csv: "prompt\n\"unclosed\n", want: "loading CSV dataset"},
		{name: "ranged template error", source: "bad.csv", csv: "prompt\nfirst\n{{bad\n", bounds: [2]int{2, 2}, want: "resolving prompt template for row 2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.csv != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, tc.source), []byte(tc.csv), 0o644))
			}
			tasks, err := loadTestCasesFromCSV(&models.EvalSpec{TasksFrom: tc.source, Range: tc.bounds}, filepath.Join(dir, "eval.yaml"))
			if tc.want != "" {
				require.ErrorContains(t, err, tc.want)
				require.Nil(t, tasks)
				return
			}
			require.NoError(t, err)
			require.Len(t, tasks, 2)
			require.Equal(t, "row-1", tasks[0].TestID)
		})
	}
	root := filepath.VolumeName(dir) + string(filepath.Separator)
	tasks, err := loadTestCasesFromCSV(&models.EvalSpec{TasksFrom: path}, filepath.Join(root, "eval.yaml"))
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	alias := filepath.Join(t.TempDir(), "suite")
	require.NoError(t, os.Symlink(dir, alias))
	for _, tc := range []struct {
		name, source string
		rejected     bool
	}{
		{"relative", "data.csv", false},
		{"lexical absolute", filepath.Join(alias, "data.csv"), false},
		{"physical absolute", path, true},
	} {
		t.Run("aliased base/"+tc.name, func(t *testing.T) {
			spec := &models.EvalSpec{TasksFrom: tc.source}
			specPath := filepath.Join(alias, "eval.yaml")
			gradeTasks, gradeErr := loadGradeTasks(spec, specPath, "")
			definitions, analysisErr := loadTestDefinitionYAML(spec, specPath)
			if tc.rejected {
				require.ErrorContains(t, gradeErr, "escapes spec directory")
				require.Nil(t, gradeTasks)
				require.ErrorContains(t, analysisErr, "escapes spec directory")
				require.Nil(t, definitions)
				return
			}
			require.NoError(t, gradeErr)
			require.Len(t, gradeTasks, 2)
			require.NoError(t, analysisErr)
			require.Len(t, definitions, 2)
			require.Contains(t, definitions, "row-2")
		})
	}
}

func TestGradeCommand_RangedCSVRunIdentity(t *testing.T) {
	resetRunGlobals()
	t.Cleanup(resetRunGlobals)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "data.csv"), []byte("prompt\nfirst\nsecond\n"), 0o644))
	specPath := gradeSpec(t, dir, `name: csv-regrade
skill: test
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
tasks_from: data.csv
range: [2, 2]
`)
	results := filepath.Join(dir, "run.json")
	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath, "--output", results})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.Execute())
	data, err := os.ReadFile(results)
	require.NoError(t, err)
	var outcome models.EvaluationOutcome
	require.NoError(t, json.Unmarshal(data, &outcome))
	require.Len(t, outcome.TestOutcomes, 1)
	require.Equal(t, "row-2", outcome.TestOutcomes[0].TestID)
	for _, selected := range []bool{false, true} {
		t.Run(fmt.Sprintf("selected=%t", selected), func(t *testing.T) {
			args := []string{specPath, "--results", results}
			if selected {
				args = append(args, "--task", "row-2")
			}
			output, err := executeGrade(t, args...)
			require.NoError(t, err)
			var graded models.GradeOutcome
			require.NoError(t, json.Unmarshal([]byte(output), &graded))
			require.Len(t, graded.Tasks, 1)
			require.Contains(t, graded.Tasks, "row-2")
		})
	}
}
