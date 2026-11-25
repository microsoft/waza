package graders

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type runAllErrorGrader struct {
	kind models.GraderKind
	err  error
}

func (g runAllErrorGrader) Name() string            { return "check" }
func (g runAllErrorGrader) Kind() models.GraderKind { return g.kind }
func (g runAllErrorGrader) Grade(context.Context, *Context) (*models.GraderResults, error) {
	if g.err != nil {
		return nil, g.err
	}
	return &models.GraderResults{Name: g.Name(), Score: 1, Passed: true}, nil
}

func TestRunAllPreservesFailingGraderKindAndCause(t *testing.T) {
	for _, scope := range []string{"eval", "task"} {
		for _, kind := range []models.GraderKind{models.GraderKindText, models.GraderKindPrompt} {
			for _, stage := range []string{"create", "run", "success"} {
				t.Run(scope+"/"+string(kind)+"/"+stage, func(t *testing.T) {
					cause := errors.New("original failure")
					config := models.GraderConfig{Identifier: "check", Kind: kind}
					var configs []models.GraderConfig
					task := &models.TestCase{}
					if scope == "eval" {
						configs = []models.GraderConfig{config}
					} else {
						task.Validators = []models.ValidatorInline{{Identifier: config.Identifier, Kind: kind}}
					}
					create := func(name string, _ models.GraderParameters) (Grader, error) {
						require.Equal(t, "check", name)
						if stage == "create" {
							return nil, cause
						}
						grader := runAllErrorGrader{kind: kind}
						if stage == "run" {
							grader.err = cause
						}
						return grader, nil
					}
					results, err := runAll(t.Context(), configs, task, &Context{}, "", "", false, create)
					if stage == "success" {
						require.NoError(t, err)
						require.Len(t, results, 1)
						require.True(t, results["check"].Passed)
						return
					}
					require.Nil(t, results)
					require.ErrorIs(t, err, cause)
					require.EqualError(t, err, "failed to "+stage+" grader check: original failure")
					failure, ok := errors.AsType[*RunError](fmt.Errorf("caller: %w", err))
					require.True(t, ok)
					require.Equal(t, kind, failure.Kind)
				})
			}
		}
	}
}

type runAllScriptedGrader struct {
	name   string
	result *models.GraderResults
	err    error
	calls  *[]string
}

func (g runAllScriptedGrader) Name() string            { return g.name }
func (g runAllScriptedGrader) Kind() models.GraderKind { return models.GraderKindProgram }
func (g runAllScriptedGrader) Grade(context.Context, *Context) (*models.GraderResults, error) {
	*g.calls = append(*g.calls, g.name)
	if g.err != nil {
		return nil, g.err
	}
	result := *g.result
	return &result, nil
}

func TestRunAllPreservesCompletedResultsWhenLaterGraderErrors(t *testing.T) {
	cause := errors.New("operational failure")
	passing := models.GraderResults{Name: "passing", Type: models.GraderKindProgram, Score: 1, Passed: true, Feedback: "all checks passed"}
	failing := models.GraderResults{Name: "failing", Type: models.GraderKindProgram, Score: 0.25, Passed: false,
		Feedback: "missing required token", Details: map[string]any{"missing": []any{"token"}}}
	eval := func(names ...string) []models.GraderConfig {
		configs := make([]models.GraderConfig, 0, len(names))
		for _, name := range names {
			configs = append(configs, models.GraderConfig{Identifier: name, Kind: models.GraderKindProgram, Weight: 2})
		}
		return configs
	}
	task := func(names ...string) []models.ValidatorInline {
		validators := make([]models.ValidatorInline, 0, len(names))
		for _, name := range names {
			validators = append(validators, models.ValidatorInline{Identifier: name, Kind: models.GraderKindProgram})
		}
		return validators
	}
	tests := []struct {
		name       string
		configs    []models.GraderConfig
		validators []models.ValidatorInline
		want       []string
		wantCalls  []string
		wantErr    string
	}{
		{name: "all complete control", configs: eval("passing", "failing"), validators: task("task-passing"),
			want: []string{"passing", "failing", "task-passing", "_output_contains"}, wantCalls: []string{"passing", "failing", "task-passing"}},
		{name: "eval-level later run error", configs: eval("passing", "failing", "broken", "after"),
			want: []string{"passing", "failing"}, wantCalls: []string{"passing", "failing", "broken"},
			wantErr: "failed to run grader broken: operational failure"},
		{name: "eval-level later create error", configs: eval("passing", "failing", "uncreatable", "after"),
			want: []string{"passing", "failing"}, wantCalls: []string{"passing", "failing"},
			wantErr: "failed to create grader uncreatable: operational failure"},
		{name: "task-level later run error", configs: eval("passing"), validators: task("failing", "broken", "after"),
			want: []string{"passing", "failing"}, wantCalls: []string{"passing", "failing", "broken"},
			wantErr: "failed to run grader broken: operational failure"},
		{name: "task-level missing kind", configs: eval("passing", "failing"),
			validators: []models.ValidatorInline{{Identifier: "kindless"}},
			want:       []string{"passing", "failing"}, wantCalls: []string{"passing", "failing"},
			wantErr: "no kind associated with grader kindless"},
		{name: "first grader error", configs: eval("broken", "passing"),
			wantCalls: []string{"broken"}, wantErr: "failed to run grader broken: operational failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			create := func(name string, _ models.GraderParameters) (Grader, error) {
				grader := runAllScriptedGrader{name: name, calls: &calls}
				switch name {
				case "passing":
					grader.result = &passing
				case "failing":
					grader.result = &failing
				case "task-passing":
					grader.result = &models.GraderResults{Name: name, Score: 1, Passed: true}
				case "broken":
					grader.err = cause
				case "uncreatable":
					return nil, cause
				default:
					t.Fatalf("grader %q must not be created after an earlier grader error", name)
				}
				return grader, nil
			}
			testCase := &models.TestCase{Validators: tt.validators,
				Expectation: models.TaskExpectation{MustInclude: []string{"absent"}}}
			results, err := runAll(t.Context(), tt.configs, testCase, &Context{Output: "output"}, "", "", false, create)
			require.Equal(t, tt.wantCalls, calls)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tt.wantErr)
				_, ok := errors.AsType[*RunError](err)
				require.True(t, ok)
			}
			if len(tt.want) == 0 {
				require.Nil(t, results)
				return
			}
			require.Len(t, results, len(tt.want))
			for _, name := range tt.want {
				require.Contains(t, results, name)
			}
			require.Equal(t, 2.0, results["passing"].Weight)
			require.True(t, results["passing"].Passed)
			require.Equal(t, "all checks passed", results["passing"].Feedback)
			if want, ok := results["failing"]; ok {
				require.False(t, want.Passed)
				require.Equal(t, 0.25, want.Score)
				require.Equal(t, "missing required token", want.Feedback)
				require.Equal(t, failing.Details, want.Details)
			}
		})
	}
}

func TestRunAllMissingTaskKindRemainsClassifiable(t *testing.T) {
	_, err := RunAll(t.Context(), nil, &models.TestCase{Validators: []models.ValidatorInline{{Identifier: "missing"}}}, &Context{}, "", "", false)
	require.EqualError(t, err, "no kind associated with grader missing")
	failure, ok := errors.AsType[*RunError](err)
	require.True(t, ok)
	require.Empty(t, failure.Kind)
}

func TestRunAllRejectsPairwiseNameCollisionBeforeCreatingGraders(t *testing.T) {
	pairwise := models.GraderConfig{
		Identifier: "quality",
		Kind:       models.GraderKindPrompt,
		Parameters: models.PromptGraderParameters{Mode: models.PromptGraderModePairwise},
	}
	task := &models.TestCase{Validators: []models.ValidatorInline{{
		Identifier: "quality",
		Kind:       models.GraderKindText,
	}}}
	createCalls := 0
	_, err := runAll(t.Context(), []models.GraderConfig{pairwise}, task, &Context{}, "", "", false,
		func(string, models.GraderParameters) (Grader, error) {
			createCalls++
			return nil, errors.New("must not create graders")
		})
	require.ErrorContains(t, err, `pairwise grader name collision "quality"`)
	require.Zero(t, createCalls)
}

func TestRunAllRejectsPairwiseExpectationNameCollisionBeforeCreatingGraders(t *testing.T) {
	tests := []struct {
		name        string
		expectation models.TaskExpectation
	}{
		{name: "_output_contains", expectation: models.TaskExpectation{MustInclude: []string{"required"}}},
		{name: "_output_not_contains", expectation: models.TaskExpectation{MustExclude: []string{"forbidden"}}},
		{name: "_output_contains_any", expectation: models.TaskExpectation{MayInclude: []string{"accepted"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pairwise := models.GraderConfig{
				Identifier: tt.name,
				Kind:       models.GraderKindPrompt,
				Parameters: models.PromptGraderParameters{Mode: models.PromptGraderModePairwise},
			}
			createCalls := 0
			_, err := runAll(t.Context(), []models.GraderConfig{pairwise},
				&models.TestCase{Expectation: tt.expectation}, &Context{}, "", "", false,
				func(string, models.GraderParameters) (Grader, error) {
					createCalls++
					return nil, errors.New("must not create graders")
				})
			require.ErrorContains(t, err, fmt.Sprintf("pairwise grader name collision %q", tt.name))
			require.Zero(t, createCalls)
		})
	}
}

func TestApplyDefaults_PromptGrader(t *testing.T) {
	t.Run("sets judge model when empty", func(t *testing.T) {
		p := models.PromptGraderParameters{Prompt: "check"}
		result := applyDefaults(p, "gpt-4o", "", false)
		pp, ok := result.(models.PromptGraderParameters)
		assert.True(t, ok)
		assert.Equal(t, "gpt-4o", pp.Model)
		assert.Equal(t, "check", pp.Prompt)
	})

	t.Run("preserves existing model", func(t *testing.T) {
		p := models.PromptGraderParameters{Model: "existing"}
		result := applyDefaults(p, "gpt-4o", "", false)
		pp, ok := result.(models.PromptGraderParameters)
		assert.True(t, ok)
		assert.Equal(t, "existing", pp.Model)
	})

	t.Run("no judge model", func(t *testing.T) {
		p := models.PromptGraderParameters{Prompt: "check"}
		result := applyDefaults(p, "", "", false)
		pp, ok := result.(models.PromptGraderParameters)
		assert.True(t, ok)
		assert.Equal(t, "", pp.Model)
	})

	t.Run("sets judge reasoning effort when empty", func(t *testing.T) {
		result := applyDefaults(models.PromptGraderParameters{Prompt: "check"}, "", "low", false)
		pp, ok := result.(models.PromptGraderParameters)
		assert.True(t, ok)
		assert.Equal(t, "low", pp.ReasoningEffort)
	})

	t.Run("preserves grader reasoning effort", func(t *testing.T) {
		result := applyDefaults(models.PromptGraderParameters{ReasoningEffort: "high"}, "", "low", false)
		pp, ok := result.(models.PromptGraderParameters)
		assert.True(t, ok)
		assert.Equal(t, "high", pp.ReasoningEffort)
	})

	t.Run("applies judge reasoning effort when continuing session", func(t *testing.T) {
		result := applyDefaults(models.PromptGraderParameters{Prompt: "check", ContinueSession: true}, "", "low", false)
		pp, ok := result.(models.PromptGraderParameters)
		assert.True(t, ok)
		assert.Equal(t, "low", pp.ReasoningEffort)
	})
}

func TestApplyDefaults_DiffGrader(t *testing.T) {
	t.Run("sets update snapshots", func(t *testing.T) {
		p := models.DiffGraderParameters{}
		result := applyDefaults(p, "", "", true)
		dp, ok := result.(models.DiffGraderParameters)
		assert.True(t, ok)
		assert.True(t, dp.UpdateSnapshots)
	})

	t.Run("no update snapshots", func(t *testing.T) {
		p := models.DiffGraderParameters{}
		result := applyDefaults(p, "", "", false)
		dp, ok := result.(models.DiffGraderParameters)
		assert.True(t, ok)
		assert.False(t, dp.UpdateSnapshots)
	})
}

func TestApplyDefaults_OtherGrader(t *testing.T) {
	p := models.TextGraderParameters{Contains: []string{"hello"}}
	result := applyDefaults(p, "gpt-4o", "", true)
	tp, ok := result.(models.TextGraderParameters)
	assert.True(t, ok)
	assert.Equal(t, []string{"hello"}, tp.Contains)
}

// --- Expectation evaluation tests ---

func TestEvaluateExpectations_MayInclude(t *testing.T) {
	t.Run("passes when any match found", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MayInclude: []string{"alpha", "beta", "gamma"},
			},
		}
		gCtx := &Context{Output: "The result is Beta plus delta"}
		results := evaluateExpectations(tc, gCtx)
		r, ok := results["_output_contains_any"]
		assert.True(t, ok)
		assert.Equal(t, 1.0, r.Score)
		assert.True(t, r.Passed)
		assert.Contains(t, r.Feedback, "beta")
	})

	t.Run("fails when none match", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MayInclude: []string{"alpha", "beta"},
			},
		}
		gCtx := &Context{Output: "nothing relevant here"}
		results := evaluateExpectations(tc, gCtx)
		r, ok := results["_output_contains_any"]
		assert.True(t, ok)
		assert.Equal(t, 0.0, r.Score)
		assert.False(t, r.Passed)
	})

	t.Run("skipped when empty", func(t *testing.T) {
		tc := &models.TestCase{}
		gCtx := &Context{Output: "anything"}
		results := evaluateExpectations(tc, gCtx)
		_, ok := results["_output_contains_any"]
		assert.False(t, ok)
	})
}

func TestEvaluateExpectations_MustInclude(t *testing.T) {
	t.Run("all present", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MustInclude: []string{"hello", "world"},
			},
		}
		gCtx := &Context{Output: "Hello World"}
		results := evaluateExpectations(tc, gCtx)
		r := results["_output_contains"]
		assert.Equal(t, 1.0, r.Score)
		assert.True(t, r.Passed)
	})

	t.Run("partial match", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MustInclude: []string{"hello", "world"},
			},
		}
		gCtx := &Context{Output: "Hello there"}
		results := evaluateExpectations(tc, gCtx)
		r := results["_output_contains"]
		assert.Equal(t, 0.5, r.Score)
		assert.False(t, r.Passed)
	})
}

func TestEvaluateExpectations_MustExclude(t *testing.T) {
	t.Run("none present passes", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MustExclude: []string{"error", "fail"},
			},
		}
		gCtx := &Context{Output: "all good"}
		results := evaluateExpectations(tc, gCtx)
		r := results["_output_not_contains"]
		assert.Equal(t, 1.0, r.Score)
		assert.True(t, r.Passed)
	})

	t.Run("some present fails", func(t *testing.T) {
		tc := &models.TestCase{
			Expectation: models.TaskExpectation{
				MustExclude: []string{"error", "fail"},
			},
		}
		gCtx := &Context{Output: "error occurred"}
		results := evaluateExpectations(tc, gCtx)
		r := results["_output_not_contains"]
		assert.Equal(t, 0.5, r.Score)
		assert.False(t, r.Passed)
	})
}

func TestEvaluateExpectations_Combined(t *testing.T) {
	tc := &models.TestCase{
		Expectation: models.TaskExpectation{
			MustInclude: []string{"result"},
			MustExclude: []string{"error"},
			MayInclude:  []string{"option_a", "option_b"},
		},
	}
	gCtx := &Context{Output: "The result is option_b"}
	results := evaluateExpectations(tc, gCtx)
	assert.Len(t, results, 3)
	assert.True(t, results["_output_contains"].Passed)
	assert.True(t, results["_output_not_contains"].Passed)
	assert.True(t, results["_output_contains_any"].Passed)
}
