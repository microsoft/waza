package graders

import (
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/stretchr/testify/require"
)

func TestToolConstraintRegexRunAllLifecycle(t *testing.T) {
	for _, scope := range []string{"eval", "task"} {
		t.Run(scope, func(t *testing.T) {
			params := models.ToolConstraintGraderParameters{
				ExpectTools: []models.ToolSpecParameters{{
					Tool: "^bash$", CommandPattern: "^same$", PathPattern: "^same$", SkillPattern: "^same$",
				}},
			}
			config := models.GraderConfig{
				Identifier: "constraints", Kind: models.GraderKindToolConstraint, Parameters: params,
			}
			tc := &models.TestCase{}
			var configs []models.GraderConfig
			if scope == "eval" {
				configs = []models.GraderConfig{config}
			} else {
				tc.Validators = []models.ValidatorInline{{
					Identifier: config.Identifier, Kind: config.Kind, Parameters: params,
				}}
			}
			grade := func(args models.ToolCallArgs) models.GraderResults {
				results, err := RunAll(t.Context(), configs, tc, &Context{Session: &models.SessionDigest{
					ToolCalls: []models.ToolCall{{Name: "BASH", Arguments: args}},
				}}, "", "", false)
				require.NoError(t, err)
				require.Len(t, results, 1)
				return results["constraints"]
			}
			matching := models.ToolCallArgs{Command: "SAME", Path: "same", Skill: "Same"}
			for range 2 {
				result := grade(matching)
				require.True(t, result.Passed)
				require.Equal(t, 1.0, result.Score)
				require.Equal(t, 1.0, result.Weight)
				require.Equal(t, []string{"^bash$ (command_pattern: ^same$, skill_pattern: ^same$, path_pattern: ^same$)"}, result.Details["expect_tools"])
			}
			for _, field := range []string{"command", "path", "skill"} {
				args := matching
				switch field {
				case "command":
					args.Command = "same-extra"
				case "path":
					args.Path = "same-extra"
				case "skill":
					args.Skill = "same-extra"
				}
				result := grade(args)
				require.False(t, result.Passed, field)
				require.Zero(t, result.Score, field)
			}
			params.ExpectTools[0].CommandPattern = "^changed$"
			require.False(t, grade(matching).Passed, "a new RunAll pass must compile current declarations, not reuse an earlier cache")
			matching.Command = "CHANGED"
			require.True(t, grade(matching).Passed)
		})
	}
}

func TestToolConstraintRegexOwnedState(t *testing.T) {
	for _, group := range []string{"expect", "reject", "allow"} {
		t.Run(group, func(t *testing.T) {
			args := map[string]argmatcher.Matcher{
				"command": {Kind: argmatcher.KindRegex, Regex: "^same$"},
			}
			specs := []models.ToolSpecParameters{{
				Tool: " bash ", CommandPattern: "^same$", PathPattern: "^same$", SkillPattern: "^same$", Args: args,
			}}
			var params models.ToolConstraintGraderParameters
			switch group {
			case "expect":
				params.ExpectTools = specs
			case "reject":
				params.RejectTools = specs
			case "allow":
				params.AllowOnly = &specs
			}
			created, err := Create("owned", params)
			require.NoError(t, err)
			grader, ok := created.(*toolConstraintGrader)
			require.True(t, ok)
			var compiled compiledToolSpec
			switch group {
			case "expect":
				compiled = grader.compiledExpect[0]
			case "reject":
				compiled = grader.compiledReject[0]
			case "allow":
				compiled = (*grader.compiledAllowOnly)[0]
				require.Nil(t, compiled.tool, "allow-only tool names are literal, not regexes")
			}
			require.Equal(t, " bash ", specs[0].Tool, "construction must not normalize caller-owned declarations")
			require.NotSame(t, compiled.commandPattern, compiled.pathPattern)
			require.NotSame(t, compiled.commandPattern, compiled.skillPattern)
			require.NotSame(t, compiled.pathPattern, compiled.skillPattern)
			for _, matcher := range args {
				require.False(t, matcher.IsCompiled())
			}
			specs[0] = models.ToolSpecParameters{Tool: "different", CommandPattern: "["}
			args["command"] = argmatcher.Matcher{Kind: argmatcher.KindRegex, Regex: "^different$"}
			gCtx := &Context{Session: &models.SessionDigest{ToolCalls: []models.ToolCall{{
				Name: "BASH", Arguments: models.ToolCallArgs{Command: "same", Path: "SAME", Skill: "Same"},
			}}}}
			var workers sync.WaitGroup
			for range 8 {
				workers.Go(func() {
					result, err := created.Grade(t.Context(), gCtx)
					if err != nil {
						t.Error(err)
						return
					}
					if result.Passed != (group != "reject") {
						t.Errorf("owned %s grader changed after caller mutation: %+v", group, result)
					}
				})
			}
			workers.Wait()
			require.True(t, compiled.commandPattern.MatchString("same"))
			_, err = Create("new", params)
			require.ErrorContains(t, err, "command_pattern: invalid regex", "another construction must inspect the mutated declarations")
		})
	}
}

func TestToolConstraintRegexRunAllDiagnostics(t *testing.T) {
	for _, group := range []string{"expect_tools", "reject_tools", "allow_only"} {
		for _, field := range []string{"tool", "command_pattern", "path_pattern", "skill_pattern"} {
			t.Run(group+"/"+field, func(t *testing.T) {
				spec := models.ToolSpecParameters{Tool: "bash"}
				switch field {
				case "tool":
					spec.Tool = " "
				case "command_pattern":
					spec.CommandPattern = "["
				case "path_pattern":
					spec.PathPattern = "["
				case "skill_pattern":
					spec.SkillPattern = "["
				}
				specs := []models.ToolSpecParameters{spec}
				var params models.ToolConstraintGraderParameters
				switch group {
				case "expect_tools":
					params.ExpectTools = specs
				case "reject_tools":
					params.RejectTools = specs
				case "allow_only":
					params.AllowOnly = &specs
				}
				config := models.GraderConfig{
					Identifier: "invalid", Kind: models.GraderKindToolConstraint, Parameters: params,
				}
				results, err := RunAll(t.Context(), []models.GraderConfig{config}, &models.TestCase{}, &Context{}, "", "", false)
				require.Nil(t, results)
				var runErr *RunError
				require.ErrorAs(t, err, &runErr)
				require.Equal(t, models.GraderKindToolConstraint, runErr.Kind)
				require.ErrorContains(t, err, "config."+group+"[0]."+field+":")
				validateErr := ValidateConfig("invalid", params)
				require.Error(t, validateErr)
				require.EqualError(t, err, "failed to create grader invalid: "+validateErr.Error())
				invalid, external := schemaloader.ClassifyError(err)
				require.True(t, invalid)
				require.False(t, external)
			})
		}
	}
}
