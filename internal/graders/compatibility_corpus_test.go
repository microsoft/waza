package graders

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCompatibilityGraderInventory(t *testing.T) {
	tc, err := models.LoadTestCase(testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1", "graders.yaml"))
	require.NoError(t, err)
	var kinds []string
	for _, v := range tc.Validators {
		if p, ok := v.Parameters.(models.TriggerHeuristicGraderParameters); ok {
			p.SkillPath = testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1", "SKILL.md")
			v.Parameters = p
		}
		g, err := Create(v.Identifier, v.Parameters)
		require.NoError(t, err, v.Identifier)
		require.Equal(t, v.Kind, g.Kind())
		kinds = append(kinds, string(g.Kind()))
	}
	require.ElementsMatch(t, models.AllGraderKinds(), kinds, "extend the fixture and preservation inventory when adding a grader")
}

func TestCompatibilityProgramGraderProcess(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("WAZA_COMPAT_PROGRAM_HELPER", "1")
	for _, c := range []struct {
		name    string
		mode    string
		missing bool
		cancel  bool
		passed  bool
	}{
		{name: "success", mode: "success", passed: true},
		{name: "nonzero exit with stderr", mode: "failure"},
		{name: "missing executable", missing: true},
		{name: "canceled context", mode: "success", cancel: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			command := binary
			if c.missing {
				command = filepath.Join(t.TempDir(), "missing-program")
			}
			args := []string{"-test.run=^TestCompatibilityProgramHelper$", "--", c.mode}
			g, err := Create("compat-program", models.ProgramGraderParameters{Command: command, Args: args, Timeout: 10})
			require.NoError(t, err)
			workspace := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if c.cancel {
				cancel()
			}
			result, err := g.Grade(ctx, &Context{Output: "recorded input", WorkspaceDir: workspace})
			require.NoError(t, err)
			require.Equal(t, "compat-program", result.Name)
			require.Equal(t, models.GraderKindProgram, result.Type)
			require.Equal(t, c.passed, result.Passed)
			require.Equal(t, command, result.Details["command"])
			require.Equal(t, args, result.Details["args"])
			require.Equal(t, workspace, result.Details["workspace_dir"])
			if c.passed {
				require.Equal(t, 1.0, result.Score)
				require.Equal(t, "recorded input|"+workspace, result.Feedback)
				require.Equal(t, result.Feedback, result.Details["stdout"])
				require.Equal(t, "", result.Details["stderr"])
			} else {
				require.Equal(t, 0.0, result.Score)
				require.Contains(t, result.Feedback, "Program exited with error:")
				require.NotEmpty(t, result.Details["exit_error"])
				if c.mode == "failure" {
					require.Equal(t, "rejected input", result.Details["stdout"])
					require.Equal(t, "fixture failure", result.Details["stderr"])
					require.Contains(t, result.Feedback, "fixture failure")
				}
			}
		})
	}
}

func TestCompatibilityProgramHelper(t *testing.T) {
	if os.Getenv("WAZA_COMPAT_PROGRAM_HELPER") != "1" {
		return
	}
	if len(os.Args) != 4 || os.Args[1] != "-test.run=^TestCompatibilityProgramHelper$" || os.Args[2] != "--" {
		t.Fatal("invalid program helper invocation")
	}
	switch os.Args[3] {
	case "success":
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Printf("%s|%s", input, os.Getenv("WAZA_WORKSPACE_DIR"))
		os.Exit(0)
	case "failure":
		_, err := fmt.Fprint(os.Stdout, "rejected input")
		require.NoError(t, err)
		fmt.Fprint(os.Stderr, "fixture failure")
		os.Exit(7)
	default:
		t.Fatal("unknown program helper mode")
	}
}
func TestCompatibilityScenariosRepeatable(t *testing.T) {
	type sample struct {
		Name           string             `json:"name"`
		Pass           bool               `json:"pass"`
		Output         string             `json:"output"`
		ToolEvents     []models.ToolEvent `json:"tool_events"`
		WorkspaceFiles map[string]string  `json:"workspace_files"`
	}
	type verdict struct {
		Passed bool
		Score  float64
		Type   models.GraderKind
		Name   string
	}
	for _, scenario := range []string{"mcp", "cli", "repository"} {
		t.Run(scenario, func(t *testing.T) {
			root := testutil.RepoFile(t, "examples", "compatibility", scenario)
			tc, err := models.LoadTestCase(filepath.Join(root, "task.yaml"))
			require.NoError(t, err)
			require.True(t, tc.Golden)
			data, err := os.ReadFile(filepath.Join(root, "cases.json"))
			require.NoError(t, err)
			var cases []sample
			require.NoError(t, json.Unmarshal(data, &cases))
			require.Len(t, cases, 3)
			require.Equal(t, []string{"known-good", "deliberately-bad", "alternative-valid"}, []string{cases[0].Name, cases[1].Name, cases[2].Name})
			for _, c := range cases {
				t.Run(c.Name, func(t *testing.T) {
					var previous []verdict
					for iteration := range 2 {
						workspace := t.TempDir()
						for path, content := range c.WorkspaceFiles {
							require.NoError(t, os.WriteFile(filepath.Join(workspace, path), []byte(content), 0o600))
						}
						var current []verdict
						passed := true
						digest := &models.SessionDigest{}
						for _, event := range c.ToolEvents {
							digest.ToolCalls = append(digest.ToolCalls, models.ToolCall{
								ID: event.ToolCallID, Name: event.ToolName, Success: event.Success,
							})
							digest.ToolCallCount++
							digest.ToolsUsed = append(digest.ToolsUsed, event.ToolName)
						}
						for _, v := range tc.Validators {
							g, err := Create(v.Identifier, v.Parameters)
							require.NoError(t, err)
							result, err := g.Grade(t.Context(), &Context{
								TestCase: tc, Output: c.Output, ToolEvents: c.ToolEvents,
								WorkspaceDir: workspace, Session: digest,
							})
							require.NoError(t, err)
							require.NotNil(t, result)
							current = append(current, verdict{result.Passed, result.Score, g.Kind(), result.Name})
							passed = passed && result.Passed
						}
						require.Equal(t, c.Pass, passed, "known outcome must be asserted on both iterations")
						if iteration > 0 {
							require.Equal(t, previous, current, "only timing/diagnostic paths are excluded from verdict projection")
						}
						previous = current
					}
				})
			}
		})
	}
}
