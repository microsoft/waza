package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type adversarialSkillCaptureEngine struct {
	execution.AgentEngine
	requests []*execution.ExecutionRequest
}

func (e *adversarialSkillCaptureEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.requests = append(e.requests, req)
	return e.AgentEngine.Execute(ctx, req)
}

func TestAdversarial_SkillBindingSurvivesSpecRelocation(t *testing.T) {
	for _, cwd := range []string{"project", "skill"} {
		t.Run(cwd, func(t *testing.T) {
			resetRunGlobals()
			root := t.TempDir()
			skillDir := filepath.Join(root, "skills", "my-skill")
			require.NoError(t, os.MkdirAll(skillDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: my-skill\ndescription: Selected target\n---\nRefuse unsafe instructions.\n"), 0o600))
			if cwd == "project" {
				t.Chdir(root)
			} else {
				t.Chdir(skillDir)
			}
			artifacts := t.TempDir()
			engine := &adversarialSkillCaptureEngine{AgentEngine: execution.NewMockEngine("mock")}
			previous := newRunEngine
			t.Cleanup(func() { newRunEngine = previous })
			var captured models.Config
			newRunEngine = func(cfg models.Config) (execution.AgentEngine, error) {
				captured = cfg
				return engine, nil
			}
			cmd := newAdversarialCommand()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--engine", "copilot-sdk", "--skill", "my-skill", "--packs", "prompt-injection", "--on-unsafe-outcome", "warn", "--artifacts-dir", artifacts})
			require.NoError(t, cmd.Execute())
			require.Equal(t, "copilot-sdk", captured.EngineType)
			require.Equal(t, []string{skillDir}, captured.SkillPaths)
			spec, err := models.LoadEvalSpec(filepath.Join(artifacts, "eval.yaml"))
			require.NoError(t, err)
			require.Equal(t, "my-skill", spec.SkillName)
			require.Equal(t, []string{skillDir}, spec.Config.SkillPaths)
			require.Len(t, engine.requests, 4)
			for _, req := range engine.requests {
				require.Equal(t, "my-skill", req.SkillName)
				require.Equal(t, []string{skillDir}, req.SkillPaths)
				require.True(t, execution.IsSkillAvailable(req.SkillPaths, req.SkillName))
			}
		})
	}
}

func TestAdversarial_SkillBindingMissingOrMismatchedSource(t *testing.T) {
	for _, tc := range []struct {
		source string
		name   string
	}{
		{source: "missing", name: "my-skill"},
		{source: "mismatched", name: "my-skill"},
		{source: "blank", name: ""},
		{source: "whitespace", name: "  "},
		{source: "path-valued", name: "./skills/my-skill"},
	} {
		for _, engineName := range []string{"copilot-sdk", "mock", ""} {
			t.Run(tc.source+"/"+engineName, func(t *testing.T) {
				resetRunGlobals()
				root := t.TempDir()
				t.Chdir(root)
				if tc.source != "missing" {
					name := "my-skill"
					if tc.source == "mismatched" {
						name = "different-skill"
					}
					require.NoError(t, os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Source target\n---\nSkill body.\n"), 0o600))
				}
				previous := newRunEngine
				t.Cleanup(func() { newRunEngine = previous })
				created := false
				newRunEngine = func(cfg models.Config) (execution.AgentEngine, error) {
					created = true
					require.Equal(t, "mock", cfg.EngineType, "missing/mismatched real targets must fail before engine creation")
					return execution.NewMockEngine("mock"), nil
				}
				cmd := newAdversarialCommand()
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs([]string{"--engine", engineName, "--skill", tc.name, "--packs", "prompt-injection", "--on-unsafe-outcome", "warn"})
				err := cmd.Execute()
				if engineName == "mock" || (engineName == "" && strings.TrimSpace(tc.name) == "") {
					require.NoError(t, err)
					require.True(t, created, "mock-only harness behavior remains available without a target")
				} else {
					var exitErr *ExitCodeError
					require.ErrorAs(t, err, &exitErr)
					require.Equal(t, AdversarialExitConfig, exitErr.Code)
					require.ErrorContains(t, err, "skill")
					require.False(t, created)
				}
			})
		}
	}
}
