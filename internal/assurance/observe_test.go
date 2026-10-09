package assurance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func referenceInput(parameters models.GraderParameters, evidence *graders.Context) ReferenceInput {
	return ReferenceInput{
		TaskID: "task", Check: models.RequirementCheck{Scope: "task", Grader: "check"},
		Parameters: parameters, Context: evidence,
	}
}

func TestObserveMechanicalActualVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		passed bool
	}{
		{"good", "state=ready", true},
		{"alternative", "The state=ready after a different valid path.", true},
		{"bad", "state=wrong", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := ObserveMechanical(t.Context(), referenceInput(
				models.TextGraderParameters{Contains: []string{"state=ready"}}, &graders.Context{Output: tc.output},
			))
			require.Equal(t, Observed, result.State)
			require.NoError(t, result.Err)
			require.Equal(t, tc.passed, result.Result.Passed)
			require.Equal(t, "check", result.Result.Name)
			if tc.passed {
				require.Equal(t, 1.0, result.Result.Score)
			} else {
				require.Zero(t, result.Result.Score)
				require.Contains(t, result.Result.Feedback, "Missing expected substring")
			}
		})
	}
}

func TestObserveMechanicalScopedIdentity(t *testing.T) {
	for _, check := range []models.RequirementCheck{
		{Scope: "eval", Grader: "same-name"},
		{Scope: "task", Grader: "same-name"},
		{Scope: "checkpoint", Grader: "same-name", AfterTurn: 1},
		{Scope: "checkpoint", Grader: "same-name", AfterTurn: 2},
	} {
		input := referenceInput(models.TextGraderParameters{Contains: []string{"ready"}}, &graders.Context{Output: "ready"})
		input.Check = check
		result := ObserveMechanical(t.Context(), input)
		require.Equal(t, Observed, result.State)
		require.Equal(t, "task", result.TaskID)
		require.Equal(t, check, result.Check)
	}
}

func TestObserveMechanicalInvalidIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		task  string
		check models.RequirementCheck
	}{
		{"empty-task", "", models.RequirementCheck{Scope: "task", Grader: "check"}},
		{"padded-task", " task", models.RequirementCheck{Scope: "task", Grader: "check"}},
		{"empty-name", "task", models.RequirementCheck{Scope: "task"}},
		{"padded-name", "task", models.RequirementCheck{Scope: "task", Grader: "check "}},
		{"unknown-scope", "task", models.RequirementCheck{Scope: "unknown", Grader: "check"}},
		{"eval-turn", "task", models.RequirementCheck{Scope: "eval", Grader: "check", AfterTurn: 1}},
		{"task-turn", "task", models.RequirementCheck{Scope: "task", Grader: "check", AfterTurn: 1}},
		{"checkpoint-zero", "task", models.RequirementCheck{Scope: "checkpoint", Grader: "check"}},
		{"checkpoint-negative", "task", models.RequirementCheck{Scope: "checkpoint", Grader: "check", AfterTurn: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := referenceInput(models.TextGraderParameters{Contains: []string{"ready"}}, &graders.Context{})
			input.TaskID, input.Check = tc.task, tc.check
			result := ObserveMechanical(t.Context(), input)
			require.Equal(t, Invalid, result.State)
			require.Error(t, result.Err)
			require.Nil(t, result.Result)
		})
	}
}

func TestObserveMechanicalNoAgentJudgeOrSubprocess(t *testing.T) {
	for _, parameters := range []models.GraderParameters{
		models.PromptGraderParameters{Prompt: "Judge", Model: "explicit-model"},
		models.ProgramGraderParameters{Command: "missing-executable"},
		models.InlineScriptGraderParameters{Assertions: []string{"True"}},
		models.TriggerHeuristicGraderParameters{SkillPath: "missing-skill"},
	} {
		executor := &executionGuard{}
		result := ObserveMechanical(t.Context(), referenceInput(parameters, &graders.Context{Executor: executor}))
		require.Equal(t, NotAssessed, result.State)
		require.ErrorContains(t, result.Err, "separately supported execution or calibration")
		require.Nil(t, result.Result)
		require.Zero(t, executor.calls)
	}
}

func TestObserveMechanicalMissingAndUnreadableState(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(workspace, "directory"), 0o700))
	parameters := models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{
		Path: "state.txt", MustMatch: []string{"^ready$"},
	}}}
	for _, tc := range []struct {
		name     string
		evidence *graders.Context
		state    ObservationState
	}{
		{"nil-context", nil, InsufficientEvidence},
		{"missing-workspace", &graders.Context{}, InsufficientEvidence},
		{"nonexistent-workspace", &graders.Context{WorkspaceDir: filepath.Join(workspace, "missing")}, InsufficientEvidence},
		{"not-a-directory", &graders.Context{WorkspaceDir: filepath.Join(workspace, "file")}, InsufficientEvidence},
		{"missing-required-state", &graders.Context{WorkspaceDir: workspace}, InsufficientEvidence},
		{"captured-only-file-grader", &graders.Context{WorkspaceFiles: map[string][]byte{"state.txt": []byte("ready")}}, InsufficientEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "file"), []byte("ready"), 0o600))
			result := ObserveMechanical(t.Context(), referenceInput(parameters, tc.evidence))
			require.Equal(t, tc.state, result.State)
			require.Error(t, result.Err)
			require.Nil(t, result.Result)
		})
	}
	parameters.ContentPatterns[0].Path = "directory"
	result := ObserveMechanical(t.Context(), referenceInput(parameters, &graders.Context{WorkspaceDir: workspace}))
	require.Equal(t, OperationalError, result.State)
	require.Error(t, result.Err)
	require.Nil(t, result.Result)
}

func TestObserveMechanicalStateVersusMissingEvidence(t *testing.T) {
	workspace := t.TempDir()
	parameters := models.FileGraderParameters{MustExist: []string{"state.txt"}, ContentPatterns: []models.FileContentPatternParameters{{
		Path: "state.txt", MustMatch: []string{"^ready$"},
	}}}
	input := referenceInput(parameters, &graders.Context{Output: "Successfully completed.", WorkspaceDir: workspace})
	missing := ObserveMechanical(t.Context(), input)
	require.Equal(t, InsufficientEvidence, missing.State)
	require.Nil(t, missing.Result)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "state.txt"), []byte("wrong"), 0o600))
	wrong := ObserveMechanical(t.Context(), input)
	require.Equal(t, Observed, wrong.State)
	require.False(t, wrong.Result.Passed)

	require.NoError(t, os.WriteFile(filepath.Join(workspace, "state.txt"), []byte("ready"), 0o600))
	good := ObserveMechanical(t.Context(), input)
	require.Equal(t, Observed, good.State)
	require.True(t, good.Result.Passed)
}

func TestObserveMechanicalCapturedDiffAndSnapshots(t *testing.T) {
	references := t.TempDir()
	parameters := models.DiffGraderParameters{ContextDir: references, ExpectedFiles: []models.DiffExpectedFileParameters{{
		Path: "state.txt", Snapshot: "expected.txt",
	}}}
	input := referenceInput(parameters, &graders.Context{WorkspaceFiles: map[string][]byte{"state.txt": []byte("ready")}})
	missing := ObserveMechanical(t.Context(), input)
	require.Equal(t, InsufficientEvidence, missing.State)
	require.Nil(t, missing.Result)

	require.NoError(t, os.WriteFile(filepath.Join(references, "expected.txt"), []byte("ready"), 0o600))
	good := ObserveMechanical(t.Context(), input)
	require.Equal(t, Observed, good.State)
	require.True(t, good.Result.Passed)

	input.Context.WorkspaceFiles["state.txt"] = []byte("wrong")
	bad := ObserveMechanical(t.Context(), input)
	require.Equal(t, Observed, bad.State)
	require.False(t, bad.Result.Passed)

	parameters.UpdateSnapshots = true
	input.Parameters = parameters
	mutation := ObserveMechanical(t.Context(), input)
	require.Equal(t, Invalid, mutation.State)
	require.ErrorContains(t, mutation.Err, "cannot update")
	snapshot, err := os.ReadFile(filepath.Join(references, "expected.txt"))
	require.NoError(t, err)
	require.Equal(t, "ready", string(snapshot))
}

func TestObserveMechanicalUnavailableDiff(t *testing.T) {
	parameters := models.DiffGraderParameters{ExpectedFiles: []models.DiffExpectedFileParameters{{Path: "state.txt", Contains: []string{"+ready"}}}}
	for _, evidence := range []*graders.Context{{}, {WorkspaceDir: t.TempDir()}} {
		result := ObserveMechanical(t.Context(), referenceInput(parameters, evidence))
		require.Equal(t, InsufficientEvidence, result.State)
		require.Nil(t, result.Result)
	}
}

func TestObserveMechanicalMissingSessionEvidence(t *testing.T) {
	for _, parameters := range []models.GraderParameters{
		models.ToolCallsGraderParameters{ForbiddenTools: []string{"delete"}},
		models.ToolConstraintGraderParameters{RejectTools: []models.ToolSpecParameters{{Tool: "delete"}}},
		models.ActionSequenceGraderParameters{ExpectedActions: []string{"edit"}, MatchingMode: models.ActionSequenceMatchingModeExact},
		models.BehaviorGraderParameters{MaxToolCalls: 2},
		models.BehaviorGraderParameters{MaxTokens: 20},
		models.SkillInvocationGraderParameters{ForbiddenSkills: []string{"delete"}},
	} {
		result := ObserveMechanical(t.Context(), referenceInput(parameters, &graders.Context{}))
		require.Equal(t, InsufficientEvidence, result.State)
		require.Nil(t, result.Result)
	}
	result := ObserveMechanical(t.Context(), referenceInput(models.BehaviorGraderParameters{MaxTokens: 20},
		&graders.Context{Session: &models.SessionDigest{}}))
	require.Equal(t, InsufficientEvidence, result.State)
	require.ErrorContains(t, result.Err, "token usage")
}

func TestObserveMechanicalRawEmptyHistoryIsNotAssurance(t *testing.T) {
	result := ObserveMechanical(t.Context(), referenceInput(
		models.ToolCallsGraderParameters{ForbiddenTools: []string{"delete"}}, &graders.Context{Session: &models.SessionDigest{}},
	))
	require.Equal(t, Observed, result.State)
	require.True(t, result.Result.Passed)
}

func TestObserveMechanicalInvalidAndOperationalErrors(t *testing.T) {
	for _, parameters := range []models.GraderParameters{nil, models.FileGraderParameters{}, models.GenericGraderParameters{}} {
		result := ObserveMechanical(t.Context(), referenceInput(parameters, &graders.Context{}))
		require.Equal(t, Invalid, result.State)
		require.Error(t, result.Err)
		require.Nil(t, result.Result)
	}
	result := ObserveMechanical(t.Context(), referenceInput(
		models.JSONSchemaGraderParameters{Schema: map[string]any{"type": "not-a-schema-type"}}, &graders.Context{Output: "{}"},
	))
	require.Equal(t, Invalid, result.State)
	require.Error(t, result.Err)
	require.Nil(t, result.Result)
}

func TestObserveDeclaredMechanicalUsesSharedScopedLookup(t *testing.T) {
	task := &models.TestCase{
		TestID: "task",
		Validators: []models.ValidatorInline{{Identifier: "same", Kind: models.GraderKindText,
			Parameters: models.TextGraderParameters{Contains: []string{"task-ready"}}}},
		Checkpoints: []models.Checkpoint{{AfterTurn: 2, Graders: []models.ValidatorInline{
			{Identifier: "same", Kind: models.GraderKindText, Parameters: models.TextGraderParameters{Contains: []string{"turn-ready"}}},
		}}},
	}
	spec := &models.EvalSpec{Graders: []models.GraderConfig{{Identifier: "same", Kind: models.GraderKindText,
		Parameters: models.TextGraderParameters{Contains: []string{"eval-ready"}}}}}
	for _, tc := range []struct {
		scope string
		turn  int
		text  string
	}{
		{"eval", 0, "eval-ready"},
		{"task", 0, "task-ready"},
		{"checkpoint", 2, "turn-ready"},
	} {
		check := models.RequirementCheck{Scope: tc.scope, Grader: "same", AfterTurn: tc.turn}
		good := ObserveDeclaredMechanical(t.Context(), task, spec, check, &graders.Context{Output: tc.text})
		require.Equal(t, Observed, good.State)
		require.True(t, good.Result.Passed)
		require.Equal(t, check, good.Check)
		bad := ObserveDeclaredMechanical(t.Context(), task, spec, check, &graders.Context{Output: "wrong"})
		require.Equal(t, Observed, bad.State)
		require.False(t, bad.Result.Passed)
	}
	for _, check := range []models.RequirementCheck{
		{Scope: "task", Grader: "missing"},
		{Scope: "checkpoint", Grader: "same", AfterTurn: 1},
	} {
		result := ObserveDeclaredMechanical(t.Context(), task, spec, check, &graders.Context{})
		require.Equal(t, Invalid, result.State)
		require.Error(t, result.Err)
	}
	task.Validators = append(task.Validators, task.Validators[0])
	duplicate := ObserveDeclaredMechanical(t.Context(), task, spec, models.RequirementCheck{Scope: "task", Grader: "same"}, &graders.Context{})
	require.Equal(t, Invalid, duplicate.State)
	require.ErrorContains(t, duplicate.Err, "2 declarations")
	require.Equal(t, Invalid, ObserveDeclaredMechanical(t.Context(), nil, spec, models.RequirementCheck{}, nil).State)
	require.Equal(t, Invalid, ObserveDeclaredMechanical(t.Context(), task, nil, models.RequirementCheck{}, nil).State)
}

func TestObserveMechanicalFileSchemaIsNotAssessed(t *testing.T) {
	for _, output := range []string{"{}", "not-json"} {
		for _, path := range []string{"missing.json", "../outside.json", filepath.Join(t.TempDir(), "absolute.json")} {
			result := ObserveMechanical(t.Context(), referenceInput(models.JSONSchemaGraderParameters{SchemaFile: path}, &graders.Context{Output: output}))
			require.Equal(t, NotAssessed, result.State)
			require.Nil(t, result.Result)
			require.ErrorContains(t, result.Err, "confined schema loader")
		}
	}
}

func TestObserveMechanicalMalformedArgumentsAreOperational(t *testing.T) {
	parameters := models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{
		Tool: "^update$", Args: map[string]argmatcher.Matcher{"state": {Kind: argmatcher.KindEquals, Equals: "ready"}},
	}}}
	for _, arguments := range []any{[]string{"not-an-object"}, make(chan int)} {
		evidence := &graders.Context{
			Session:    &models.SessionDigest{ToolCalls: []models.ToolCall{{ID: "call", Name: "update"}}},
			ToolEvents: []models.ToolEvent{{ToolCallID: "call", ToolName: "update", Args: arguments}},
		}
		result := ObserveMechanical(t.Context(), referenceInput(parameters, evidence))
		require.Equal(t, OperationalError, result.State)
		require.Error(t, result.Err)
		require.Nil(t, result.Result)
	}
	reject := models.ToolConstraintGraderParameters{RejectTools: []models.ToolSpecParameters{{
		Tool: "update", Args: map[string]argmatcher.Matcher{"state": {Kind: argmatcher.KindEquals, Equals: "forbidden"}},
	}}}
	result := ObserveMechanical(t.Context(), referenceInput(reject, &graders.Context{Session: &models.SessionDigest{
		ToolCalls: []models.ToolCall{{Name: "update", Arguments: models.ToolCallArgs{Extra: map[string]any{"state": make(chan int)}}}},
	}}))
	require.Equal(t, OperationalError, result.State)
	require.Nil(t, result.Result)
}

func TestPreparedFileEvidenceIsIndependentOfSource(t *testing.T) {
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "state.txt"), []byte("ready"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "evaluator-only.txt"), []byte("do not copy"), 0o600))
	parameters := models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{Path: "state.txt", MustMatch: []string{"^ready$"}}}}
	evidence, prepared, cleanup, state, err := prepareLocalEvidence(parameters, &graders.Context{WorkspaceDir: source})
	require.NoError(t, err)
	require.Equal(t, Observed, state)
	require.NotEqual(t, source, evidence.WorkspaceDir)
	t.Cleanup(func() { require.NoError(t, cleanup()) })
	_, err = os.Stat(filepath.Join(evidence.WorkspaceDir, "evaluator-only.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.Remove(filepath.Join(source, "state.txt")))
	require.NoError(t, os.Mkdir(filepath.Join(source, "state.txt"), 0o700))
	g, err := graders.Create("check", prepared)
	require.NoError(t, err)
	result, err := g.Grade(t.Context(), evidence)
	require.NoError(t, err)
	require.True(t, result.Passed)
}

func TestPreparedDiffEvidenceCopiesBytesAndSnapshot(t *testing.T) {
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "snapshot.txt"), []byte("ready"), 0o600))
	original := []byte("ready")
	parameters := models.DiffGraderParameters{ContextDir: source, ExpectedFiles: []models.DiffExpectedFileParameters{{Path: "state.txt", Snapshot: "snapshot.txt"}}}
	evidence, prepared, cleanup, state, err := prepareLocalEvidence(parameters, &graders.Context{WorkspaceFiles: map[string][]byte{"state.txt": original}})
	require.NoError(t, err)
	require.Equal(t, Observed, state)
	t.Cleanup(func() { require.NoError(t, cleanup()) })
	original[0] = 'X'
	require.NoError(t, os.Remove(filepath.Join(source, "snapshot.txt")))
	g, err := graders.Create("check", prepared)
	require.NoError(t, err)
	result, err := g.Grade(t.Context(), evidence)
	require.NoError(t, err)
	require.True(t, result.Passed)
	require.Empty(t, evidence.WorkspaceDir)
}

func TestObserveMechanicalRejectsEscapingEvidencePaths(t *testing.T) {
	for _, parameters := range []models.GraderParameters{
		models.FileGraderParameters{MustExist: []string{"../reference.txt"}},
		models.FileGraderParameters{MustNotExist: []string{"../reference.txt"}},
		models.DiffGraderParameters{ExpectedFiles: []models.DiffExpectedFileParameters{{Path: "../reference.txt", Contains: []string{"ready"}}}},
		models.DiffGraderParameters{ExpectedFiles: []models.DiffExpectedFileParameters{{Path: "state.txt", Snapshot: "../reference.txt"}}},
	} {
		evidence := &graders.Context{WorkspaceDir: t.TempDir(), WorkspaceFiles: map[string][]byte{"state.txt": []byte("ready")}}
		result := ObserveMechanical(t.Context(), referenceInput(parameters, evidence))
		require.Equal(t, Invalid, result.State)
		require.ErrorContains(t, result.Err, "must stay within")
		require.Nil(t, result.Result)
	}
}

func TestObserveMechanicalRejectsSymlinkEscape(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "state.txt"), []byte("ready"), 0o600))
	if err := os.Symlink(filepath.Join(outside, "state.txt"), filepath.Join(workspace, "state.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	result := ObserveMechanical(t.Context(), referenceInput(
		models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{Path: "state.txt", MustMatch: []string{"ready"}}}},
		&graders.Context{WorkspaceDir: workspace},
	))
	require.Equal(t, OperationalError, result.State)
	require.Error(t, result.Err)
	require.Nil(t, result.Result)
}

func TestObserveMechanicalCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result := ObserveMechanical(ctx, referenceInput(models.TextGraderParameters{}, &graders.Context{}))
	require.Equal(t, OperationalError, result.State)
	require.ErrorIs(t, result.Err, context.Canceled)
	require.Nil(t, result.Result)

	ctxAfter := &cancelAfterGradeContext{Context: t.Context()}
	result = ObserveMechanical(ctxAfter, referenceInput(models.TextGraderParameters{Contains: []string{"ready"}}, &graders.Context{Output: "ready"}))
	require.Equal(t, OperationalError, result.State)
	require.ErrorIs(t, result.Err, context.Canceled)
	require.True(t, result.Result.Passed)
}

type executionGuard struct{ calls int }

func (e *executionGuard) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.calls++
	return nil, errors.New("unexpected model execution")
}

type cancelAfterGradeContext struct {
	context.Context
	calls int
}

func (c *cancelAfterGradeContext) Err() error {
	c.calls++
	if c.calls > 1 {
		return context.Canceled
	}
	return nil
}
