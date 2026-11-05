package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/judgeevidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

type evidenceSummaryFailure struct{}

func (evidenceSummaryFailure) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestEvidenceSavedGradeRetainsPaidObservationWhenSummaryCannotBeWritten(t *testing.T) {
	spec, results, original := savedEvidenceFixture(t)
	source, err := json.Marshal(original)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(results, source, 0600))
	output := filepath.Join(t.TempDir(), "graded.json")
	judge := savedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		_, err := request.Tools[0].Handler(copilot.ToolInvocation{ToolName: request.Tools[0].Name})
		zero := 0.0
		return &execution.ExecutionResponse{Success: true, SessionID: "observed-judge",
			Usage: &models.UsageStats{InputTokens: 2, OutputTokens: 1, AICredits: &zero}}, err
	})
	err = runGradeWithJudge(t.Context(), evidenceSummaryFailure{}, io.Discard, spec, "task", results, "", "", output, false, "", judge)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	saved, err := models.ParseEvaluationOutcome(data, output)
	require.NoError(t, err)
	require.Len(t, saved.RegradeUsage.Sessions, 1)
	require.Equal(t, "observed-judge", saved.RegradeUsage.Sessions[0].SessionID)
	require.Equal(t, 0.0, *saved.RegradeUsage.Sessions[0].Usage.AICredits)
	require.True(t, saved.TestOutcomes[0].Runs[0].PromptJudging[0].SubmissionObserved)
}

func TestEvidenceSavedGradePublicationFailureDoesNotPrintSuccessfulSummary(t *testing.T) {
	spec, results, original := savedEvidenceFixture(t)
	source, err := json.Marshal(original)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(results, source, 0600))
	var stdout bytes.Buffer
	judge := savedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		_, err := request.Tools[0].Handler(copilot.ToolInvocation{ToolName: request.Tools[0].Name})
		return &execution.ExecutionResponse{Success: true, SessionID: "judge"}, err
	})
	output := filepath.Join(t.TempDir(), "missing", "graded.json")
	err = runGradeWithJudge(t.Context(), &stdout, io.Discard, spec, "task", results, "", "", output, false, "", judge)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to save graded outcome")
	require.Empty(t, stdout.String())
	_, statErr := os.Stat(output)
	require.True(t, errors.Is(statErr, os.ErrNotExist))
}

func savedEvidencePairFixture(t *testing.T) (string, string, *models.EvaluationOutcome) {
	t.Helper()
	spec, source, original := mixedEvidenceFixture(t)
	config, err := os.ReadFile(spec)
	require.NoError(t, err)
	require.Contains(t, string(config), ", zz-broken.yaml")
	require.NoError(t, os.WriteFile(spec, []byte(strings.Replace(string(config), ", zz-broken.yaml", "", 1)), 0600))
	original.TestOutcomes = []models.TestOutcome{original.TestOutcomes[0], original.TestOutcomes[2]}
	require.NoError(t, saveOutcome(original, source))
	return spec, source, original
}

func TestEvidenceSavedGradeMixedValidatorsStopOnMechanicalErrors(t *testing.T) {
	for _, evidenceFirst := range []bool{false, true} {
		for _, broken := range []bool{false, true} {
			name := "mechanical-first"
			if evidenceFirst {
				name = "evidence-first"
			}
			if broken {
				name += "/missing-schema"
			} else {
				name += "/valid-schema"
			}
			t.Run(name, func(t *testing.T) {
				spec, source, original := savedEvidencePairFixture(t)
				path := filepath.Join(filepath.Dir(spec), "task.yaml")
				task, err := os.ReadFile(path)
				require.NoError(t, err)
				head, judgeYAML, found := strings.Cut(string(task), "graders:\n")
				require.True(t, found)
				schema := "schema: {type: object}"
				if broken {
					schema = "schema_file: missing-schema.json"
				}
				mechanical := "  - name: schema\n    type: json_schema\n    config: {" + schema + "}\n"
				validators := mechanical + judgeYAML
				if evidenceFirst {
					validators = judgeYAML + mechanical
				}
				require.NoError(t, os.WriteFile(path, []byte(head+"graders:\n"+validators), 0600))
				run := &original.TestOutcomes[0].Runs[0]
				old := run.JudgeEvidence
				run.FinalOutput = "{}"
				run.JudgeEvidence = judgeevidence.Capture(judgeevidence.CaptureInput{
					Origin: old.Origin, Task: *old.Task.Items, Output: run.FinalOutput, OutputPresent: true, ExecutionMode: "mock",
					Events: []copilot.SessionEvent{
						{Data: &copilot.UserMessageData{Content: *old.Task.Items}},
						{Data: &copilot.AssistantMessageData{Content: run.FinalOutput}},
					},
				})
				require.NoError(t, judgeevidence.Verify(run.JudgeEvidence, old.Origin))
				require.NoError(t, saveOutcome(original, source))
				parsed, err := models.LoadEvalSpec(spec)
				require.NoError(t, err)
				tasks, err := loadGradeTasks(parsed, spec, "")
				require.NoError(t, err)
				require.Len(t, tasks, 2)
				require.Equal(t, "task", tasks[0].TestID)
				require.Len(t, tasks[0].Validators, 2)
				output := filepath.Join(t.TempDir(), "graded.json")
				var summary bytes.Buffer
				calls := 0
				err = runGradeWithJudge(t.Context(), &summary, io.Discard, spec, "", source, filepath.Dir(spec), "", output, false, "", successfulSavedEvidenceJudge(t, &calls))
				wantCalls := 2
				if broken {
					require.ErrorContains(t, err, "missing-schema.json")
					wantCalls = 0
					if evidenceFirst {
						wantCalls = 1
					}
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, wantCalls, calls, "a mechanical error must not start the later task's judge")
				data, err := os.ReadFile(output)
				require.NoError(t, err)
				graded, err := models.ParseEvaluationOutcome(data, output)
				require.NoError(t, err)
				require.Equal(t, original.EvaluationUsage, graded.EvaluationUsage)
				require.NotNil(t, graded.RegradeUsage)
				require.Len(t, graded.RegradeUsage.Sessions, wantCalls)
				for _, session := range graded.RegradeUsage.Sessions {
					require.Equal(t, 0.25, *session.Usage.AICredits)
				}
				if broken {
					require.Equal(t, models.StatusError, graded.TestOutcomes[0].Status)
					require.Equal(t, models.StatusError, graded.TestOutcomes[0].Runs[0].Status)
					require.Len(t, graded.TestOutcomes[0].Runs[0].PromptJudging, wantCalls)
					require.Equal(t, original.TestOutcomes[1].Runs, graded.TestOutcomes[1].Runs)
					var fields map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(summary.Bytes(), &fields))
					require.NotContains(t, fields, "overall_score")
				} else {
					require.Equal(t, models.StatusPassed, graded.TestOutcomes[0].Status)
					require.Len(t, graded.TestOutcomes[0].Runs[0].Validations, 2)
					require.Len(t, graded.TestOutcomes[1].Runs[0].PromptJudging, 1)
				}
			})
		}
	}
}

func TestEvidenceSavedGradePromptErrorsRetainIndependentTaskAggregation(t *testing.T) {
	spec, source, original := savedEvidencePairFixture(t)
	output := filepath.Join(t.TempDir(), "graded.json")
	calls := 0
	success := successfulSavedEvidenceJudge(t, &calls)
	judge := savedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		response, err := success(request)
		if calls == 1 {
			return response, io.ErrUnexpectedEOF
		}
		return response, err
	})
	err := runGradeWithJudge(t.Context(), io.Discard, io.Discard, spec, "", source, "", "", output, false, "", judge)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, 2, calls)
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	graded, err := models.ParseEvaluationOutcome(data, output)
	require.NoError(t, err)
	require.Equal(t, original.EvaluationUsage, graded.EvaluationUsage)
	require.Len(t, graded.RegradeUsage.Sessions, 2)
	require.Equal(t, models.StatusError, graded.TestOutcomes[0].Status)
	require.Equal(t, "failed", graded.TestOutcomes[0].Runs[0].PromptJudging[0].State)
	require.Equal(t, models.StatusPassed, graded.TestOutcomes[1].Status)
	require.True(t, graded.TestOutcomes[1].Runs[0].PromptJudging[0].SubmissionObserved)
}

func TestEvidenceSavedGradeLocalFailuresDoNotStartLaterJudges(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		verbose, warning, missing bool
		failAt, judgeCalls        int
	}{
		{"initial verbose write", true, false, false, 1, 0},
		{"grader verbose write", true, false, false, 2, 1},
		{"legacy manifest warning", false, true, false, 1, 0},
		{"missing frozen evidence", false, false, true, 99, 0},
		{"healthy control", true, false, false, 99, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, source, original := savedEvidencePairFixture(t)
			if tc.warning {
				config, err := models.LoadEvalSpec(spec)
				require.NoError(t, err)
				tasks, err := loadGradeTasks(config, spec, "task")
				require.NoError(t, err)
				require.Len(t, tasks, 1)
				snap, err := snapshot.Capture(snapshot.CaptureInput{
					EvalID: original.RunID, Task: tasks[0], Run: &original.TestOutcomes[0].Runs[0], Spec: config, ExecutionMode: "mock",
				})
				require.NoError(t, err)
				original.TestOutcomes[0].Runs[0].Evidence = snap.Evidence
				original.TestOutcomes[0].Runs[0].Evidence.SHA256 = strings.Repeat("a", 64)
			}
			if tc.missing {
				original.TestOutcomes[0].Runs[0].JudgeEvidence = nil
			}
			require.NoError(t, saveOutcome(original, source))
			output := filepath.Join(t.TempDir(), "graded.json")
			calls := 0
			writer := &preflightFailWriter{failAt: tc.failAt}
			err := runGradeWithJudge(t.Context(), io.Discard, writer, spec, "", source, "", "", output, tc.verbose, "", successfulSavedEvidenceJudge(t, &calls))
			if tc.name == "healthy control" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				if !tc.missing {
					require.ErrorContains(t, err, "write failed")
				}
			}
			require.Equal(t, tc.judgeCalls, calls, "local failures must stop later paid judges even when diagnostic output would recover")
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			graded, err := models.ParseEvaluationOutcome(data, output)
			require.NoError(t, err)
			require.Equal(t, original.EvaluationUsage, graded.EvaluationUsage)
			require.NotNil(t, graded.RegradeUsage)
			require.Len(t, graded.RegradeUsage.Sessions, tc.judgeCalls)
			run := graded.TestOutcomes[0].Runs[0]
			if tc.name == "healthy control" {
				require.Equal(t, models.StatusPassed, run.Status)
				require.Equal(t, models.StatusPassed, graded.TestOutcomes[1].Status)
			} else {
				require.Equal(t, models.StatusError, run.Status)
				require.Equal(t, original.TestOutcomes[1].Runs, graded.TestOutcomes[1].Runs)
				wantObservations := tc.judgeCalls
				if tc.missing {
					wantObservations = 1
				}
				require.Len(t, run.PromptJudging, wantObservations)
			}
		})
	}
}

func TestEvidenceSavedGradeInvalidatesPortableSnapshotAssessments(t *testing.T) {
	for _, manifestState := range []string{"valid", "tampered", "foreign", "absent", "valid-verbose-failure", "tampered-warning-failure"} {
		t.Run(manifestState, func(t *testing.T) {
			specPath, source, original := savedEvidenceFixture(t)
			taskPath := filepath.Join(filepath.Dir(specPath), "task.yaml")
			taskYAML, err := os.ReadFile(taskPath)
			require.NoError(t, err)
			taskYAML = append(taskYAML, []byte(`requirements:
  - id: judged
    category: outcome
    description: Judge the recorded output.
    checks:
      - scope: eval
        grader: judge
`)...)
			require.NoError(t, os.WriteFile(taskPath, taskYAML, 0600))
			spec, err := models.LoadEvalSpec(specPath)
			require.NoError(t, err)
			tasks, err := loadGradeTasks(spec, specPath, "task")
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			run := &original.TestOutcomes[0].Runs[0]
			run.Validations = map[string]models.GraderResults{"judge": {Name: "judge", Score: 1, Passed: true}}
			snap, err := snapshot.Capture(snapshot.CaptureInput{
				EvalID: original.RunID, Task: tasks[0], Run: run, Spec: spec, ExecutionMode: "mock",
			})
			require.NoError(t, err)
			run.SnapshotPath, err = snapshot.NewWriter(filepath.Join(t.TempDir(), "snapshots")).Write(snap)
			require.NoError(t, err)
			run.Evidence = snap.Evidence
			run.RequirementExplanations = evidence.ExplainRequirements(spec, tasks[0], run)
			require.Len(t, run.RequirementExplanations, 1)
			require.Len(t, run.RequirementExplanations[0].Checks[0].References, 1, "the old verdict must initially reference the snapshot assessment")
			originalSHA := run.Evidence.SHA256
			switch manifestState {
			case "tampered", "tampered-warning-failure":
				run.Evidence.SHA256 = strings.Repeat("a", 64)
			case "foreign":
				run.Evidence.Origin.EvalID = "foreign-evaluation"
				require.NoError(t, evidence.Seal(run.Evidence))
			case "absent":
				run.Evidence = nil
			}
			require.NoError(t, saveOutcome(original, source))
			before, err := os.ReadFile(source)
			require.NoError(t, err)
			calls := 0
			judge := savedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				calls++
				_, err := request.Tools[1].Handler(copilot.ToolInvocation{ToolName: request.Tools[1].Name})
				return &execution.ExecutionResponse{Success: true, SessionID: "changed-verdict"}, err
			})
			output := filepath.Join(t.TempDir(), "graded.json")
			var notices bytes.Buffer
			var noticeWriter io.Writer = &notices
			writeFailure := strings.HasSuffix(manifestState, "-failure")
			if writeFailure {
				noticeWriter = io.MultiWriter(&notices, evidenceSummaryFailure{})
			}
			err = runGradeWithJudge(t.Context(), io.Discard, noticeWriter, specPath, "task", source, "", "", output, manifestState == "valid-verbose-failure", "", judge)
			if writeFailure {
				require.ErrorIs(t, err, io.ErrClosedPipe)
				require.Zero(t, calls)
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, calls)
			}
			data, err := os.ReadFile(output)
			require.NoError(t, err)
			graded, err := models.ParseEvaluationOutcome(data, output)
			require.NoError(t, err)
			updated := graded.TestOutcomes[0].Runs[0]
			require.Len(t, updated.RequirementExplanations, 1)
			check := updated.RequirementExplanations[0].Checks[0]
			if writeFailure {
				require.Equal(t, models.StatusError, updated.Status)
				require.Empty(t, updated.Validations)
				require.Equal(t, "operational_error", check.Category)
			} else {
				require.Equal(t, models.StatusFailed, updated.Status)
				require.False(t, updated.Validations["judge"].Passed)
				require.Equal(t, "grader_recorded_failure", check.Observation)
			}
			require.Empty(t, check.References, "the new verdict must not cite an old snapshot assessment")
			if strings.HasPrefix(manifestState, "valid") {
				require.NotNil(t, updated.Evidence)
				require.NoError(t, evidence.ValidateNative(updated.Evidence))
				require.Equal(t, originalSHA, updated.Evidence.SourceManifestSHA256)
				for _, id := range []string{"validations", "result", "grader-config", "checkpoints"} {
					ref, err := evidence.Reference(updated.Evidence, id)
					require.NoError(t, err)
					artifact, err := evidence.Resolve(updated.Evidence, ref)
					require.NoError(t, err)
					require.Equal(t, "unavailable", artifact.Availability, id)
					require.Nil(t, artifact.ContentDigest, id)
					require.Empty(t, artifact.Document, id)
					require.Empty(t, artifact.Pointer, id)
				}
			} else {
				require.Nil(t, updated.Evidence)
				if manifestState != "absent" {
					require.Contains(t, notices.String(), "legacy snapshot manifest")
				}
			}
			after, err := os.ReadFile(source)
			require.NoError(t, err)
			require.Equal(t, before, after, "regrading must not rewrite the source artifact")
		})
	}
}
