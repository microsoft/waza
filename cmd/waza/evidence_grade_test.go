package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func TestGradeVerifiedSelectedFilesAndCachedSource(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("selected-file capture is explicitly unsupported on this platform")
	}
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "cached"}[cached], func(t *testing.T) {
			dir := t.TempDir()
			specPath := gradeSpec(t, dir, minimalSpec)
			writeTaskFile(t, dir, "task.yaml", `id: task-001
name: Preserved output
inputs:
  prompt: Write the output.
graders:
  - name: file-check
    type: file
    config:
      must_exist: [output.txt]
`)
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "output.txt"), []byte("actual output"), 0o600))
			run := models.RunResult{RunNumber: 1, Attempts: 2, Status: models.StatusPassed, WorkspaceDir: root}
			snap, err := snapshot.Capture(snapshot.CaptureInput{
				EvalID: "source-eval", Task: &models.TestCase{TestID: "task-001"}, Run: &run,
				ExecutionMode: "mock", WorkspacePaths: []string{"output.txt"},
				WorkspaceLimits: snapshot.WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 1024},
			})
			require.NoError(t, err)
			snapshotPath, err := snapshot.NewWriter(filepath.Join(dir, "snapshots")).Write(snap)
			require.NoError(t, err)
			run.Evidence = snap.Evidence
			outcome := outcomeWithTasks(models.TestOutcome{TestID: "task-001", Cached: cached, Runs: []models.RunResult{run}})
			outcome.RunID = "source-eval"
			if cached {
				outcome.RunID = "cached-eval"
			}
			results := gradeResultsFile(t, dir, outcome)
			output := filepath.Join(dir, "graded.json")
			text, err := executeGrade(t, specPath, "--task", "task-001", "--results", results, "--evidence-snapshot", snapshotPath, "--output", output)
			require.NoError(t, err)
			require.Contains(t, text, `"passed": true`)
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			graded, err := models.ParseEvaluationOutcome(data, output)
			require.NoError(t, err)
			require.Equal(t, cached, graded.TestOutcomes[0].Cached)
			manifest := graded.TestOutcomes[0].Runs[0].Evidence
			require.NoError(t, evidence.Validate(manifest))
			require.Equal(t, "source-eval", manifest.Origin.EvalID)
			require.Equal(t, snap.Evidence.SHA256, manifest.SourceManifestSHA256)
			ref, err := evidence.Reference(manifest, "validations")
			require.NoError(t, err)
			artifact, err := evidence.Resolve(manifest, ref)
			require.NoError(t, err)
			require.Equal(t, "unavailable", artifact.Availability)
			_, err = executeGrade(t, specPath, "--task", "task-001", "--results", results, "--evidence-snapshot", snapshotPath, "--workspace", root)
			require.ErrorContains(t, err, "cannot be combined")
			_, err = executeGrade(t, specPath, "--results", results, "--evidence-snapshot", snapshotPath)
			require.ErrorContains(t, err, "requires --task")
			if cached {
				outcome.TestOutcomes[0].Cached = false
				results = gradeResultsFile(t, dir, outcome)
				_, err = executeGrade(t, specPath, "--task", "task-001", "--results", results, "--evidence-snapshot", snapshotPath)
				require.ErrorContains(t, err, "attribution")
			}
		})
	}
}

func TestPreservedGradeRejectsUncertifiableInputs(t *testing.T) {
	for _, test := range []struct {
		name   string
		kind   models.GraderKind
		params models.GraderParameters
	}{
		{"absence", models.GraderKindFile, models.FileGraderParameters{MustNotExist: []string{"secret.txt"}}},
		{"wildcard", models.GraderKindFile, models.FileGraderParameters{MustExist: []string{"*.txt"}}},
		{"judge", models.GraderKindPrompt, models.PromptGraderParameters{}},
		{"program", models.GraderKindProgram, models.ProgramGraderParameters{}},
		{"code", models.GraderKindInlineScript, models.InlineScriptGraderParameters{}},
		{"diff", models.GraderKindDiff, models.DiffGraderParameters{}},
		{"no files", models.GraderKindText, models.TextGraderParameters{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := preservedGradeInputs(&models.EvalSpec{Graders: []models.GraderConfig{{Kind: test.kind, Parameters: test.params}}}, &models.TestCase{})
			require.Error(t, err)
		})
	}
}

func TestEvidenceSerializationRemainsAdditive(t *testing.T) {
	data, err := json.Marshal(models.RunResult{})
	require.NoError(t, err)
	require.NotContains(t, string(data), `"evidence"`)
	require.NotContains(t, string(data), `"requirement_explanations"`)
}
