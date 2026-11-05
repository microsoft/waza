package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

const preservedFileTask = `id: task-001
name: Preserved output
inputs:
  prompt: Write the output.
graders:
  - name: file-check
    type: file
    config:
      must_exist: [output.txt]
      content_patterns:
        - path: output.txt
          must_match: ['actual output']
          must_not_match: ['forbidden']
`

type preservedGradeFixture struct {
	dir, specPath, snapshotPath, workspace string
	run                                    models.RunResult
	snap                                   *snapshot.Snapshot
}

func newPreservedGradeFixture(t *testing.T, taskYAML string) preservedGradeFixture {
	t.Helper()
	fixture := preservedGradeFixture{dir: t.TempDir(), workspace: t.TempDir()}
	fixture.specPath = gradeSpec(t, fixture.dir, minimalSpec)
	writeTaskFile(t, fixture.dir, "task.yaml", taskYAML)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.workspace, "output.txt"), []byte("actual output"), 0o600))
	fixture.run = models.RunResult{RunNumber: 1, Attempts: 2, Status: models.StatusPassed,
		WorkspaceDir: fixture.workspace, FinalOutput: "original-recorded-output"}
	paths := []string{"output.txt"}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		// Rejected-mode callers must also reach admission on unsupported hosts.
		paths = nil
	}
	snap, err := snapshot.Capture(snapshot.CaptureInput{
		EvalID: "source-eval", Task: &models.TestCase{TestID: "task-001"}, Run: &fixture.run,
		ExecutionMode: "mock", WorkspacePaths: paths,
		WorkspaceLimits: snapshot.WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 1024},
	})
	require.NoError(t, err)
	fixture.snap = snap
	fixture.snapshotPath, err = snapshot.NewWriter(filepath.Join(fixture.dir, "snapshots")).Write(snap)
	require.NoError(t, err)
	fixture.run.Evidence = snap.Evidence
	return fixture
}

func TestGradeVerifiedSelectedFilesAndCachedSource(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("selected-file capture is explicitly unsupported on this platform")
	}
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "cached"}[cached], func(t *testing.T) {
			fixture := newPreservedGradeFixture(t, preservedFileTask)
			outcome := outcomeWithTasks(models.TestOutcome{TestID: "task-001", Cached: cached, Runs: []models.RunResult{fixture.run}})
			outcome.RunID = "source-eval"
			if cached {
				outcome.RunID = "cached-eval"
			}
			results := gradeResultsFile(t, fixture.dir, outcome)
			output := filepath.Join(fixture.dir, "graded.json")
			text, err := executeGrade(t, fixture.specPath, "--task", "task-001", "--results", results, "--evidence-snapshot", fixture.snapshotPath, "--output", output)
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
			require.Equal(t, fixture.snap.Evidence.SHA256, manifest.SourceManifestSHA256)
			ref, err := evidence.Reference(manifest, "validations")
			require.NoError(t, err)
			artifact, err := evidence.Resolve(manifest, ref)
			require.NoError(t, err)
			require.Equal(t, "unavailable", artifact.Availability)
			_, err = executeGrade(t, fixture.specPath, "--task", "task-001", "--results", results, "--evidence-snapshot", fixture.snapshotPath, "--workspace", fixture.workspace)
			require.ErrorContains(t, err, "cannot be combined")
			_, err = executeGrade(t, fixture.specPath, "--results", results, "--evidence-snapshot", fixture.snapshotPath)
			require.ErrorContains(t, err, "requires --task")
			if cached {
				outcome.TestOutcomes[0].Cached = false
				results = gradeResultsFile(t, fixture.dir, outcome)
				_, err = executeGrade(t, fixture.specPath, "--task", "task-001", "--results", results, "--evidence-snapshot", fixture.snapshotPath)
				require.ErrorContains(t, err, "attribution")
			}
		})
	}
}

func TestGradeEvidenceSnapshotHelpStatesSupportedHosts(t *testing.T) {
	cmd := newGradeCommand()
	require.Contains(t, cmd.Long, "only on Linux/macOS")
	require.Contains(t, cmd.Flags().Lookup("evidence-snapshot").Usage, "on Linux/macOS")
}

func TestOrdinaryGradeBindsEveryEvidenceBearingRun(t *testing.T) {
	for _, test := range []struct {
		name       string
		cached     bool
		mutate     func(*models.EvidenceManifest)
		wantError  string
		noEvidence bool
		secondOnly bool
		tampered   bool
	}{
		{name: "matching"},
		{name: "cached source eval", cached: true},
		{name: "foreign eval", mutate: func(m *models.EvidenceManifest) { m.Origin.EvalID = "other" }, wantError: "attribution"},
		{name: "foreign task", mutate: func(m *models.EvidenceManifest) { m.Origin.TaskID = "other" }, wantError: "attribution"},
		{name: "foreign task cached", cached: true, mutate: func(m *models.EvidenceManifest) { m.Origin.TaskID = "other" }, wantError: "attribution"},
		{name: "foreign run", mutate: func(m *models.EvidenceManifest) { m.Origin.RunNumber = 7 }, wantError: "attribution"},
		{name: "foreign run cached", cached: true, mutate: func(m *models.EvidenceManifest) { m.Origin.RunNumber = 7 }, wantError: "attribution"},
		{name: "foreign attempt", mutate: func(m *models.EvidenceManifest) { m.Origin.AttemptCount = 7 }, wantError: "attribution"},
		{name: "foreign attempt cached", cached: true, mutate: func(m *models.EvidenceManifest) { m.Origin.AttemptCount = 7 }, wantError: "attribution"},
		{name: "foreign later run", secondOnly: true, mutate: func(m *models.EvidenceManifest) { m.Origin.TaskID = "other" }, wantError: "attribution"},
		{name: "tampered digest", tampered: true, wantError: "identity does not match"},
		{name: "legacy absent", noEvidence: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPreservedGradeFixture(t, preservedFileTask)
			validManifest := *fixture.run.Evidence
			if test.mutate != nil {
				test.mutate(fixture.run.Evidence)
				require.NoError(t, evidence.Seal(fixture.run.Evidence))
			}
			if test.noEvidence {
				fixture.run.Evidence = nil
			}
			if test.tampered {
				fixture.run.Evidence.Origin.TaskID = "tampered"
			}
			outcome := outcomeWithTasks(models.TestOutcome{TestID: "task-001", Cached: test.cached,
				Runs: []models.RunResult{fixture.run, fixture.run}})
			if test.secondOnly {
				outcome.TestOutcomes[0].Runs[0].Evidence = &validManifest
			}
			outcome.RunID = "source-eval"
			if test.cached {
				outcome.RunID = "current-eval"
			}
			results := gradeResultsFile(t, fixture.dir, outcome)
			original, err := os.ReadFile(results)
			require.NoError(t, err)
			output := filepath.Join(fixture.dir, "graded.json")
			text, err := executeGrade(t, fixture.specPath, "--task", "task-001", "--results", results,
				"--workspace", fixture.workspace, "--output", output)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				require.NotContains(t, text, `"overall_score"`)
				_, err = os.Stat(output)
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, err)
				data, err := os.ReadFile(output)
				require.NoError(t, err)
				graded, err := models.ParseEvaluationOutcome(data, output)
				require.NoError(t, err)
				for _, run := range graded.TestOutcomes[0].Runs {
					if test.noEvidence {
						require.Nil(t, run.Evidence)
					} else {
						require.NoError(t, evidence.Bind(run.Evidence, graded.RunID, "task-001", run.RunNumber, run.Attempts, graded.TestOutcomes[0].Cached))
						require.Equal(t, "source-eval", run.Evidence.Origin.EvalID)
					}
				}
			}
			unchanged, err := os.ReadFile(results)
			require.NoError(t, err)
			require.Equal(t, original, unchanged)
		})
	}
}

func TestPreservedGradeAdmitsOnlyDeclaredConcreteFileGraders(t *testing.T) {
	fileParams := models.FileGraderParameters{MustExist: []string{"output.txt"}}
	type admissionCase struct {
		name, wantError string
		kind            models.GraderKind
		params          models.GraderParameters
	}
	var cases []admissionCase
	for _, name := range models.AllGraderKinds() {
		wantError := "only supports file graders"
		if name == string(models.GraderKindFile) {
			wantError = ""
		}
		cases = append(cases, admissionCase{name: name, kind: models.GraderKind(name), params: fileParams, wantError: wantError})
	}
	cases = append(cases,
		admissionCase{name: "unknown", kind: "future", params: fileParams, wantError: "only supports file graders"},
		admissionCase{name: "empty kind", params: fileParams, wantError: "only supports file graders"},
		admissionCase{name: "nil parameters", kind: models.GraderKindFile, wantError: "parameters are missing or incompatible"},
		admissionCase{name: "wrong parameters", kind: models.GraderKindFile, params: models.TextGraderParameters{}, wantError: "parameters are missing or incompatible"},
		admissionCase{name: "pointer parameters", kind: models.GraderKindFile, params: &fileParams, wantError: "parameters are missing or incompatible"},
		admissionCase{name: "nil pointer parameters", kind: models.GraderKindFile, params: (*models.FileGraderParameters)(nil), wantError: "parameters are missing or incompatible"},
		admissionCase{name: "uncaptured absence", kind: models.GraderKindFile, params: models.FileGraderParameters{MustNotExist: []string{"excluded.txt"}}, wantError: "cannot prove file absence"},
		admissionCase{name: "wildcard content", kind: models.GraderKindFile, params: models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{Path: "*.txt", MustNotMatch: []string{"forbidden"}}}}, wantError: "cannot certify wildcard inventory"},
	)
	for _, scope := range []string{"eval", "task"} {
		for _, test := range cases {
			t.Run(scope+"/"+test.name, func(t *testing.T) {
				spec := &models.EvalSpec{Graders: []models.GraderConfig{{Kind: models.GraderKindFile, Parameters: fileParams}}}
				task := &models.TestCase{}
				if scope == "eval" {
					spec.Graders = append(spec.Graders, models.GraderConfig{Kind: test.kind, Parameters: test.params})
				} else {
					task.Validators = []models.ValidatorInline{{Kind: test.kind, Parameters: test.params}}
				}
				paths, err := preservedGradeInputs(spec, task)
				if test.wantError != "" {
					require.ErrorContains(t, err, test.wantError)
					require.Nil(t, paths)
				} else {
					require.NoError(t, err)
					require.Equal(t, []string{"output.txt"}, paths)
				}
			})
		}
	}
}

func TestPreservedGradeOutputExpectationCombinations(t *testing.T) {
	spec := &models.EvalSpec{Graders: []models.GraderConfig{{Kind: models.GraderKindFile,
		Parameters: models.FileGraderParameters{MustExist: []string{"output.txt"}}}}}
	for mask := 0; mask < 8; mask++ {
		t.Run(fmt.Sprintf("mask-%d", mask), func(t *testing.T) {
			task := &models.TestCase{}
			fields := []*[]string{&task.Expectation.MustInclude, &task.Expectation.MustExclude, &task.Expectation.MayInclude}
			for i, field := range fields {
				*field = []string{}
				if mask&(1<<i) != 0 {
					*field = []string{"unverified"}
				}
			}
			paths, err := preservedGradeInputs(spec, task)
			if mask == 0 {
				require.NoError(t, err)
				require.Equal(t, []string{"output.txt"}, paths)
			} else {
				require.ErrorContains(t, err, "does not support expected.output_")
				require.Nil(t, paths)
			}
		})
	}
	for _, expectation := range []models.TaskExpectation{
		{MustInclude: []string{""}}, {MustExclude: []string{""}}, {MayInclude: []string{""}},
	} {
		_, err := preservedGradeInputs(spec, &models.TestCase{Expectation: expectation})
		require.ErrorContains(t, err, "does not support expected.output_")
	}
}

func TestPreservedGradeRequiredPathsRemainSortedAndDeduplicated(t *testing.T) {
	params := models.FileGraderParameters{MustExist: []string{"b.txt", "a.txt", "a.txt"},
		ContentPatterns: []models.FileContentPatternParameters{{Path: "b.txt", MustNotMatch: []string{"forbidden"}}}}
	paths, err := preservedGradeInputs(
		&models.EvalSpec{Graders: []models.GraderConfig{{Kind: models.GraderKindFile, Parameters: params}}},
		&models.TestCase{Validators: []models.ValidatorInline{{Kind: models.GraderKindFile, Parameters: params}}})
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt", "b.txt"}, paths)
}

func TestPreservedGradeRejectsRecordedOutputChecks(t *testing.T) {
	for _, test := range []struct {
		name, extra, wantError string
	}{
		{"text", `  - name: output-check
    type: text
    config:
      contains: [unverified-pass-token]
`, "only supports file graders"},
		{"output_contains", "expected:\n  output_contains: [unverified-pass-token]\n", "expected.output_contains"},
		{"output_not_contains", "expected:\n  output_not_contains: [original-recorded-output]\n", "expected.output_not_contains"},
		{"output_contains_any", "expected:\n  output_contains_any: [unverified-pass-token]\n", "expected.output_contains_any"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPreservedGradeFixture(t, preservedFileTask+test.extra)
			originalSnapshot, err := os.ReadFile(fixture.snapshotPath)
			require.NoError(t, err)
			for _, altered := range []bool{false, true} {
				run := fixture.run
				if altered {
					run.FinalOutput = "unverified-pass-token"
				}
				outcome := outcomeWithTasks(models.TestOutcome{TestID: "task-001", Runs: []models.RunResult{run}})
				outcome.RunID = "source-eval"
				results := gradeResultsFile(t, fixture.dir, outcome)
				output := filepath.Join(fixture.dir, "rejected.json")
				text, err := executeGrade(t, fixture.specPath, "--task", "task-001", "--results", results,
					"--evidence-snapshot", fixture.snapshotPath, "--output", output)
				require.ErrorContains(t, err, test.wantError)
				require.NotContains(t, text, `"overall_score"`, "rejection must not emit grading JSON")
				_, err = os.Stat(output)
				require.ErrorIs(t, err, os.ErrNotExist)
				snapshotBytes, err := os.ReadFile(fixture.snapshotPath)
				require.NoError(t, err)
				require.Equal(t, originalSnapshot, snapshotBytes)

				text, err = executeGrade(t, fixture.specPath, "--task", "task-001", "--results", results, "--workspace", fixture.workspace)
				require.NoError(t, err)
				var ordinary struct {
					Passed bool `json:"passed"`
				}
				require.NoError(t, json.Unmarshal([]byte(text), &ordinary))
				require.Equal(t, altered, ordinary.Passed, "ordinary grading retains output-check behavior")
			}
			if len(fixture.snap.WorkspaceFiles) != 0 {
				fixture.snap.WorkspaceFiles[0].Content = "unverified corruption"
			}
			data, err := json.Marshal(fixture.snap)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(fixture.snapshotPath, data, 0o600))
			results := gradeResultsFile(t, fixture.dir, &models.EvaluationOutcome{RunID: "source-eval",
				TestOutcomes: []models.TestOutcome{{TestID: "task-001", Runs: []models.RunResult{fixture.run}}}})
			_, err = executeGrade(t, fixture.specPath, "--task", "task-001", "--results", results, "--evidence-snapshot", fixture.snapshotPath)
			require.ErrorContains(t, err, test.wantError, "unsupported checks reject before workspace verification/materialization")
		})
	}
}

func gradeSentinelTask(t *testing.T, marker string) string {
	t.Helper()
	program, err := os.Executable()
	require.NoError(t, err)
	return preservedFileTask + fmt.Sprintf(`  - name: executable-sentinel
    type: program
    config:
      command: %q
      args: ['-test.run=^TestPreservedGradeExecutionSentinel$', '--', '--grader-admission-sentinel', %q]
`, program, marker)
}

func TestGradeRejectsDuplicateTaskRowsBeforeExecutingAnyGrader(t *testing.T) {
	for _, test := range []struct {
		name         string
		firstInvalid bool
		lastAbsent   bool
		firstEmpty   bool
		lastCached   bool
		unselected   bool
	}{
		{name: "first invalid later absent", firstInvalid: true, lastAbsent: true},
		{name: "both valid"},
		{name: "first empty", firstEmpty: true},
		{name: "cached overwrite", firstInvalid: true, lastCached: true},
		{name: "unselected duplicate", unselected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPreservedGradeFixture(t, preservedFileTask)
			marker := filepath.Join(fixture.dir, "executed")
			writeTaskFile(t, fixture.dir, "task.yaml", gradeSentinelTask(t, marker))
			first := fixture.run
			last := fixture.run
			if test.firstInvalid {
				manifest := *fixture.run.Evidence
				manifest.Origin.TaskID = "foreign"
				require.NoError(t, evidence.Seal(&manifest))
				first.Evidence = &manifest
			}
			if test.lastAbsent {
				last.Evidence = nil
			}
			duplicateID := "task-001"
			if test.unselected {
				duplicateID = "task-002"
			}
			outcome := &models.EvaluationOutcome{RunID: "source-eval", TestOutcomes: []models.TestOutcome{
				{TestID: duplicateID, Runs: []models.RunResult{first}},
				{TestID: duplicateID, Cached: test.lastCached, Runs: []models.RunResult{last}},
			}}
			if test.firstEmpty {
				outcome.TestOutcomes[0].Runs = nil
			}
			if test.unselected {
				outcome.TestOutcomes = append([]models.TestOutcome{{TestID: "task-001", Runs: []models.RunResult{fixture.run}}}, outcome.TestOutcomes...)
			}
			results := gradeResultsFile(t, fixture.dir, outcome)
			original, err := os.ReadFile(results)
			require.NoError(t, err)
			output := filepath.Join(fixture.dir, "graded.json")
			text, err := executeGrade(t, fixture.specPath, "--task", "task-001", "--results", results,
				"--workspace", fixture.workspace, "--output", output)
			require.ErrorContains(t, err, fmt.Sprintf("duplicate task ID %q", duplicateID))
			require.NotContains(t, text, `"overall_score"`)
			for _, path := range []string{marker, output} {
				_, err := os.Stat(path)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			unchanged, err := os.ReadFile(results)
			require.NoError(t, err)
			require.Equal(t, original, unchanged)
		})
	}
}

func TestOrdinaryGradePreflightsAllSelectedTasksBeforeExecutingProgram(t *testing.T) {
	fixture := newPreservedGradeFixture(t, preservedFileTask)
	marker := filepath.Join(fixture.dir, "executed")
	writeTaskFile(t, fixture.dir, "task.yaml", gradeSentinelTask(t, marker))
	writeTaskFile(t, fixture.dir, "task2.yaml", strings.Replace(preservedFileTask, "task-001", "task-002", 1))
	manifest := *fixture.run.Evidence
	manifest.Origin.TaskID = "foreign"
	require.NoError(t, evidence.Seal(&manifest))
	second := fixture.run
	second.Evidence = &manifest
	outcome := &models.EvaluationOutcome{RunID: "source-eval", TestOutcomes: []models.TestOutcome{
		{TestID: "task-001", Runs: []models.RunResult{fixture.run}},
		{TestID: "task-002", Runs: []models.RunResult{second}},
	}}
	results := gradeResultsFile(t, fixture.dir, outcome)
	original, err := os.ReadFile(results)
	require.NoError(t, err)
	output := filepath.Join(fixture.dir, "graded.json")
	text, err := executeGrade(t, fixture.specPath, "--results", results, "--workspace", fixture.workspace, "--output", output)
	require.ErrorContains(t, err, `task "task-002"`)
	require.NotContains(t, text, `"overall_score"`)
	for _, path := range []string{marker, output} {
		_, err := os.Stat(path)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	unchanged, err := os.ReadFile(results)
	require.NoError(t, err)
	require.Equal(t, original, unchanged)
	_, err = executeGrade(t, fixture.specPath, "--task", "task-001", "--results", results, "--workspace", fixture.workspace)
	require.NoError(t, err, "unselected bad evidence does not block a valid selected task")
	require.FileExists(t, marker)
	require.NoError(t, os.Remove(marker))
	manifest.Origin.TaskID = "task-002"
	require.NoError(t, evidence.Seal(&manifest))
	results = gradeResultsFile(t, fixture.dir, outcome)
	_, err = executeGrade(t, fixture.specPath, "--results", results, "--workspace", fixture.workspace, "--output", output)
	require.NoError(t, err)
	executed, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "executed", string(executed))
	require.FileExists(t, output)
}

func TestPreservedGradeRejectsBeforeExecutingProgram(t *testing.T) {
	fixture := newPreservedGradeFixture(t, preservedFileTask)
	marker := filepath.Join(fixture.dir, "executed")
	writeTaskFile(t, fixture.dir, "task.yaml", gradeSentinelTask(t, marker))
	results := gradeResultsFile(t, fixture.dir, &models.EvaluationOutcome{RunID: "source-eval",
		TestOutcomes: []models.TestOutcome{{TestID: "task-001", Runs: []models.RunResult{fixture.run}}}})
	text, err := executeGrade(t, fixture.specPath, "--task", "task-001", "--results", results, "--evidence-snapshot", fixture.snapshotPath)
	require.ErrorContains(t, err, "only supports file graders")
	require.NotContains(t, text, `"overall_score"`, "rejection must not emit grading JSON")
	_, err = os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = executeGrade(t, fixture.specPath, "--task", "task-001", "--results", results, "--workspace", fixture.workspace)
	require.NoError(t, err)
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "executed", string(data), "ordinary grading exercises the real executable control")
}

func TestPreservedGradeExecutionSentinel(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--grader-admission-sentinel" {
			require.Less(t, i+1, len(os.Args))
			require.NoError(t, os.WriteFile(os.Args[i+1], []byte("executed"), 0o600))
			return
		}
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
		{"no explicit file checks", models.GraderKindFile, models.FileGraderParameters{}},
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
