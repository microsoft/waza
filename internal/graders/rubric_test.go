package graders

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// shippedRubrics is the closed set of starter rubrics promised by the rubric
// library (issue #360). Updating this list is intentional — adding to it
// without updating docs/PRD should fail review.
var shippedRubrics = []string{
	"groundedness",
	"helpfulness",
	"instruction-following",
	"refusal-correctness",
	"tool-use-appropriateness",
}

func TestBuiltinRubricNames_ShipsExpectedSet(t *testing.T) {
	require.ElementsMatch(t, shippedRubrics, BuiltinRubricNames())
}

func TestLoadAllBuiltinRubrics_ParsesAndValidates(t *testing.T) {
	rubrics, err := LoadAllBuiltinRubrics()
	require.NoError(t, err)
	require.Len(t, rubrics, len(shippedRubrics))

	for _, r := range rubrics {
		t.Run(r.Name, func(t *testing.T) {
			require.NoError(t, r.Validate())
			require.NotEmpty(t, r.Body, "rubric body must be non-empty")
			require.True(t, strings.HasPrefix(r.Source, "builtin:"), "source should be tagged builtin")
			require.GreaterOrEqual(t, len(r.Goldens), 2, "each shipped rubric must have at least 2 goldens")

			var hasPass, hasFail bool
			for _, g := range r.Goldens {
				switch g.Expected {
				case RubricExpectedPass:
					hasPass = true
				case RubricExpectedFail:
					hasFail = true
				}
			}
			require.True(t, hasPass, "rubric must have at least one passing golden")
			require.True(t, hasFail, "rubric must have at least one failing golden")
		})
	}
}

func TestResolveRubric_BuiltinByName(t *testing.T) {
	r, err := ResolveRubric("groundedness")
	require.NoError(t, err)
	require.Equal(t, "groundedness", r.Name)
	require.Equal(t, "builtin:groundedness", r.Source)
}

func TestResolveRubric_UnknownNameMentionsAvailable(t *testing.T) {
	_, err := ResolveRubric("does-not-exist")
	require.Error(t, err)
	require.Contains(t, err.Error(), "groundedness")
}

func TestResolveRubric_LocalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "my-rubric.md")
	body := strings.Join([]string{
		"---",
		"name: my-custom",
		"version: 0.1.0",
		"scale: pass-fail",
		"description: A user-supplied rubric.",
		"---",
		"",
		"Judge the response. Call set_waza_grade_pass or set_waza_grade_fail.",
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	r, err := ResolveRubric(path)
	require.NoError(t, err)
	require.Equal(t, "my-custom", r.Name)
	require.True(t, strings.HasPrefix(r.Source, "file:"))
}

func TestResolveRubric_RejectsEmpty(t *testing.T) {
	_, err := ResolveRubric("")
	require.Error(t, err)
}

func TestLoadRubricFile_ExpandsTildeHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	// Place a rubric inside a temp dir nested under the home directory so we
	// can reference it via "~/<subpath>" and verify expansion.
	relDir, err := os.MkdirTemp(home, "waza-rubric-tilde-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(relDir) })

	rubricPath := filepath.Join(relDir, "tilde-rubric.md")
	body := strings.Join([]string{
		"---",
		"name: tilde-rubric",
		"version: 0.1.0",
		"scale: pass-fail",
		"description: Verifies ~/ expansion preserves the separator.",
		"---",
		"",
		"Judge it. Call set_waza_grade_pass or set_waza_grade_fail.",
	}, "\n")
	require.NoError(t, os.WriteFile(rubricPath, []byte(body), 0o600))

	rel, err := filepath.Rel(home, rubricPath)
	require.NoError(t, err)
	tildePath := "~/" + filepath.ToSlash(rel)

	r, err := LoadRubricFile(tildePath)
	require.NoError(t, err, "tilde path %q should expand to %q", tildePath, rubricPath)
	require.Equal(t, "tilde-rubric", r.Name)
}

func TestParseRubric_RequiresFrontmatter(t *testing.T) {
	_, err := ParseRubric([]byte("no frontmatter here"))
	require.ErrorContains(t, err, "frontmatter")
}

func TestParseRubric_RequiresClosingDelimiter(t *testing.T) {
	_, err := ParseRubric([]byte("---\nname: x\nversion: 1.0.0\nscale: pass-fail\ndescription: nope"))
	require.ErrorContains(t, err, "closing")
}

func TestRubricValidate_RequiresSemver(t *testing.T) {
	r := &Rubric{
		RubricFrontmatter: RubricFrontmatter{
			Name: "x", Version: "v-not-semver", Scale: RubricScalePassFail, Description: "d",
		},
		Body: "body",
	}
	require.ErrorContains(t, r.Validate(), "semver")
}

func TestRubricValidate_RejectsUnknownScale(t *testing.T) {
	r := &Rubric{
		RubricFrontmatter: RubricFrontmatter{
			Name: "x", Version: "1.0.0", Scale: "thumbs-up", Description: "d",
		},
		Body: "body",
	}
	require.ErrorContains(t, r.Validate(), "scale")
}

func TestRubricRenderPrompt_InjectsOutput(t *testing.T) {
	r := &Rubric{Body: "RUBRIC_BODY"}
	got := r.RenderPrompt("task input", "src ctx", "candidate output")
	require.Contains(t, got, "RUBRIC_BODY")
	require.Contains(t, got, "task input")
	require.Contains(t, got, "src ctx")
	require.Contains(t, got, "candidate output")
}

func TestRubricRenderPrompt_NoInjectionsWhenAllEmpty(t *testing.T) {
	r := &Rubric{Body: "RUBRIC_BODY"}
	got := r.RenderPrompt("", "", "")
	require.Equal(t, "RUBRIC_BODY", got)
}

func TestRenderJudgePrompt_SkipsOutputInjectionWhenContinueSession(t *testing.T) {
	// With continue_session: true the judge resumes the agent session and
	// reads context directly, so the rubric body must be sent untouched.
	g, err := NewPromptGrader("grounded-continue", models.PromptGraderParameters{
		Rubric:          "groundedness",
		ContinueSession: true,
	})
	require.NoError(t, err)

	got := g.renderJudgePrompt(&Context{Output: "this should NOT be injected"})
	require.NotContains(t, got, "this should NOT be injected")
	require.NotContains(t, got, "## Candidate output")
	require.Equal(t, g.args.Prompt, got, "continue_session must leave the rubric body untouched")
}

func TestRenderJudgePrompt_InjectsOutputWhenNotContinueSession(t *testing.T) {
	g, err := NewPromptGrader("grounded-fresh", models.PromptGraderParameters{
		Rubric: "groundedness",
	})
	require.NoError(t, err)

	got := g.renderJudgePrompt(&Context{Output: "candidate-marker-xyz"})
	require.Contains(t, got, "candidate-marker-xyz")
	require.Contains(t, got, "## Candidate output")
}

func TestNewPromptGrader_AcceptsBuiltinRubric(t *testing.T) {
	g, err := NewPromptGrader("groundedness-grader", models.PromptGraderParameters{
		Rubric: "groundedness",
	})
	require.NoError(t, err)
	require.NotNil(t, g.rubric)
	require.Equal(t, "groundedness", g.rubric.Name)
	// The rubric body becomes the seed prompt when no inline prompt is set.
	require.Contains(t, g.args.Prompt, "Groundedness")
}

func TestNewPromptGrader_InlinePromptStillWorks(t *testing.T) {
	// No rubric; inline prompt path must be unchanged.
	g, err := NewPromptGrader("inline", models.PromptGraderParameters{
		Prompt: "grade this",
	})
	require.NoError(t, err)
	require.Nil(t, g.rubric)
	require.Equal(t, "grade this", g.args.Prompt)
}

func TestNewPromptGrader_InlinePromptOverridesRubricBody(t *testing.T) {
	g, err := NewPromptGrader("override", models.PromptGraderParameters{
		Prompt: "explicit prompt wins",
		Rubric: "helpfulness",
	})
	require.NoError(t, err)
	require.NotNil(t, g.rubric)
	require.Equal(t, "explicit prompt wins", g.args.Prompt)
}

func TestNewPromptGrader_UnknownRubricFails(t *testing.T) {
	_, err := NewPromptGrader("bad", models.PromptGraderParameters{
		Rubric: "no-such-rubric",
	})
	require.ErrorContains(t, err, "no-such-rubric")
}

func TestNewPromptGrader_MissingPromptAndRubricFails(t *testing.T) {
	_, err := NewPromptGrader("bad", models.PromptGraderParameters{})
	require.ErrorContains(t, err, "prompt")
	require.ErrorContains(t, err, "rubric")
}

func TestPairwiseMode_IncludesRubricMetadata(t *testing.T) {
	// Pairwise grading must also surface rubric metadata in Details so the
	// dashboard can attribute the verdict to the right rubric, just like
	// independent grading does.
	executor := &fakePromptExecutor{}
	executor.execute = func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		winner := "B"
		if executor.calls == 2 {
			winner = "A"
		}
		require.Len(t, req.Tools, 1)
		_, err := req.Tools[0].Handler(copilot.ToolInvocation{
			Arguments: map[string]any{
				"winner":    winner,
				"magnitude": "much-better",
				"reasoning": "more complete",
			},
		})
		require.NoError(t, err)
		return &execution.ExecutionResponse{Success: true}, nil
	}

	grader, err := NewPromptGrader("pairwise-rubric-grader", models.PromptGraderParameters{
		Rubric: "helpfulness",
		Mode:   models.PromptGraderModePairwise,
	})
	require.NoError(t, err)

	results, err := grader.Grade(context.Background(), &Context{
		Output:         "better output",
		BaselineOutput: "worse output",
		Executor:       executor,
	})
	require.NoError(t, err)
	require.NotNil(t, results)

	meta, ok := results.Details["rubric"].(map[string]any)
	require.True(t, ok, "pairwise Details must include rubric metadata map")
	require.Equal(t, "helpfulness", meta["name"])
	require.NotEmpty(t, meta["version"])
	require.Equal(t, "builtin:helpfulness", meta["source"])
}

// TestRubricGoldens_OracleJudge runs each shipped rubric against its bundled
// goldens with a *mocked* judge ("oracle") that always returns the golden's
// expected outcome. This is the contract test for the rubric library: it
// guarantees that each shipped rubric file (a) parses, (b) is wired into the
// prompt grader by name, (c) is rendered into a judge prompt that contains the
// candidate output, and (d) scores correctly when the judge cooperates. It
// does NOT call any real LLM, so it is fast and free.
func TestRubricGoldens_OracleJudge(t *testing.T) {
	rubrics, err := LoadAllBuiltinRubrics()
	require.NoError(t, err)

	for _, r := range rubrics {
		t.Run(r.Name, func(t *testing.T) {
			for _, g := range r.Goldens {
				golden := g
				t.Run(golden.Name, func(t *testing.T) {
					grader, err := NewPromptGrader(r.Name+"-grader", models.PromptGraderParameters{
						Rubric: r.Name,
					})
					require.NoError(t, err)

					var toolInvoked bool
					executor := &fakePromptExecutor{
						execute: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
							// Sanity-check that the rubric was actually rendered:
							// rubric prose must reach the judge.
							require.Contains(t, req.Message, strings.TrimSpace(strings.SplitN(r.Body, "\n", 2)[0]))
							require.Contains(t, req.Message, strings.TrimSpace(golden.Output))

							// Find the right tool to call based on the golden's expected outcome.
							var toolName string
							switch golden.Expected {
							case RubricExpectedPass:
								toolName = wazaPassToolName
							case RubricExpectedFail:
								toolName = wazaFailToolName
							default:
								t.Fatalf("invalid expected: %q", golden.Expected)
							}
							for _, tool := range req.Tools {
								if tool.Name == toolName {
									_, err := tool.Handler(copilot.ToolInvocation{
										Arguments: map[string]any{
											"description": golden.Name,
											"reason":      "oracle judge",
										},
									})
									require.NoError(t, err)
									toolInvoked = true
									break
								}
							}
							require.True(t, toolInvoked, "oracle judge could not find expected tool %q in request tools", toolName)
							return &execution.ExecutionResponse{Success: true, FinalOutput: "ok"}, nil
						},
					}

					results, err := grader.Grade(context.Background(), &Context{
						Output:   golden.Output,
						Executor: executor,
					})
					require.NoError(t, err)
					require.NotNil(t, results)
					require.True(t, toolInvoked, "judge handler must have been called and matched the expected tool")

					switch golden.Expected {
					case RubricExpectedPass:
						require.True(t, results.Passed, "golden %q expected pass", golden.Name)
						require.Equal(t, 1.0, results.Score)
					case RubricExpectedFail:
						require.False(t, results.Passed, "golden %q expected fail", golden.Name)
						require.Equal(t, 0.0, results.Score)
					}

					// Rubric metadata must travel back in the results so the
					// dashboard/eval-runner can attribute the verdict.
					meta, ok := results.Details["rubric"].(map[string]any)
					require.True(t, ok, "results.Details should include rubric metadata map")
					require.Equal(t, r.Name, meta["name"])
					require.Equal(t, r.Version, meta["version"])
				})
			}
		})
	}
}
