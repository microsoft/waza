package orchestration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func TestNoCaptureRepositoryAndFollowUps(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "%s", out)
	}
	git("init", "--initial-branch=main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Waza Test")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "recording"), []byte(strings.Repeat("x", 16*1024*1024+1)), 0o600))
	git("add", "recording")
	git("commit", "-m", "fixture")

	for _, mode := range []string{"", "full", "none"} {
		t.Run("capture_"+mode, func(t *testing.T) {
			spec := &models.EvalSpec{Config: models.Config{TimeoutSec: 30, ModelID: "mock", WorkspaceCapture: mode}}
			text := models.ValidatorInline{Identifier: "output", Kind: models.GraderKindText, Parameters: models.TextGraderParameters{Contains: []string{"Mock response"}}}
			tool := models.ValidatorInline{Identifier: "tools", Kind: models.GraderKindToolCalls, Parameters: models.ToolCallsGraderParameters{ForbiddenTools: []string{"delete"}}}
			tc := &models.TestCase{TestID: "repo", DisplayName: "repo", Stimulus: models.TaskStimulus{
				Message: "inspect", FollowUps: []string{"continue"}, WorkDir: "repository",
				Repos: []models.GitResource{{Type: models.GitResourceTypeWorktree, Source: repo, Dest: "repository"}},
			}, Validators: []models.ValidatorInline{text, tool}, Checkpoints: []models.Checkpoint{{AfterTurn: 1, Graders: []models.ValidatorInline{text, tool}}}}
			engine := &workspaceCaptureRecordingEngine{MockEngine: execution.NewMockEngine("mock")}
			require.NoError(t, engine.Initialize(t.Context()))
			t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir())), engine)
			run := runner.executeRun(t.Context(), tc, 1)
			if mode != "none" {
				require.Equal(t, models.StatusError, run.Status)
				require.Contains(t, run.ErrorMsg, "workspace capture incomplete")
				require.Len(t, engine.requests, 1)
			} else {
				require.Equal(t, models.StatusPassed, run.Status, run.ErrorMsg)
				require.Len(t, engine.requests, 2)
				require.Len(t, run.Checkpoints, 1)
				require.True(t, run.Validations["tools"].Passed)
				for _, req := range engine.requests {
					require.True(t, req.SkipWorkspaceCapture)
				}
				for _, resp := range engine.responses {
					require.Empty(t, resp.WorkspaceCaptureError)
					require.Nil(t, resp.WorkspaceFiles)
				}
				info, err := os.Stat(filepath.Join(engine.responses[0].WorkspaceDir, "repository", "recording"))
				require.NoError(t, err)
				require.Greater(t, info.Size(), int64(16*1024*1024))
			}
		})
	}
}

func TestNoCaptureRejectsBeforeExecutionAndRespectsSelection(t *testing.T) {
	dir := t.TempDir()
	good := "id: good\nname: good\ninputs:\n  prompt: hello\n"
	bad := "id: bad\nname: bad\ninputs:\n  prompt: hello\ngraders:\n  - name: file\n    type: file\n    config:\n      must_exist: [report.txt]\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "good.yaml"), []byte(good), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(bad), 0o600))
	spec := &models.EvalSpec{Config: models.Config{TimeoutSec: 30, TrialsPerTask: 1, ModelID: "mock", WorkspaceCapture: "none"}, Tasks: []string{"*.yaml"}}
	for _, filter := range []string{"", "good", "bad"} {
		t.Run("filter_"+filter, func(t *testing.T) {
			engine := &workspaceCaptureRecordingEngine{MockEngine: execution.NewMockEngine("mock")}
			var opts []RunnerOption
			if filter != "" {
				opts = append(opts, WithTaskFilters(filter))
			}

			runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, opts...)
			outcome, err := runner.RunBenchmark(t.Context())
			t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
			if filter == "good" {
				require.NoError(t, err)
				require.NotNil(t, outcome)
				require.Len(t, engine.requests, 1)
			} else {
				require.ErrorContains(t, err, "workspace_capture none")
				require.Empty(t, engine.requests)
			}
		})
	}
}

func TestNoCaptureRejectsEachUnsupportedGraderBeforeExecute(t *testing.T) {
	cases := []struct {
		kind   models.GraderKind
		params models.GraderParameters
	}{
		{models.GraderKindFile, models.FileGraderParameters{MustExist: []string{"out"}}},
		{models.GraderKindDiff, models.DiffGraderParameters{ExpectedFiles: []models.DiffExpectedFileParameters{{Path: "out"}}}},
		{models.GraderKindInlineScript, models.InlineScriptGraderParameters{Assertions: []string{"True"}}},
		{models.GraderKindProgram, models.ProgramGraderParameters{Command: "check"}},
		{models.GraderKindPrompt, models.PromptGraderParameters{EvidenceContext: &models.PromptEvidenceContext{Files: []string{"out"}}}},
		{"unknown", models.GenericGraderParameters{}},
	}

	for _, c := range cases {
		for _, scope := range []string{"eval", "task", "checkpoint"} {
			t.Run(string(c.kind)+"/"+scope, func(t *testing.T) {
				spec := &models.EvalSpec{Config: models.Config{TimeoutSec: 30, WorkspaceCapture: "none"}}
				tc := &models.TestCase{TestID: "test", Stimulus: models.TaskStimulus{Message: "hello"}}
				g := models.ValidatorInline{Identifier: "unsafe", Kind: c.kind, Parameters: c.params}
				switch scope {
				case "eval":
					spec.Graders = []models.GraderConfig{{Identifier: g.Identifier, Kind: g.Kind, Parameters: g.Parameters}}
				case "task":
					tc.Validators = []models.ValidatorInline{g}
				case "checkpoint":
					tc.Checkpoints = []models.Checkpoint{{AfterTurn: 1, Graders: []models.ValidatorInline{g}}}
				}
				engine := &workspaceCaptureRecordingEngine{MockEngine: execution.NewMockEngine("mock")}
				require.NoError(t, engine.Initialize(t.Context()))
				t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
				runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir())), engine)
				run := runner.executeRun(t.Context(), tc, 1)
				require.Equal(t, models.StatusError, run.Status)
				require.Contains(t, run.ErrorMsg, "workspace_capture none")
				require.Empty(t, engine.requests)
			})
		}
	}
}

func TestNoCaptureRejectsSnapshotEvidenceAndMismatchedPreparedRequest(t *testing.T) {
	spec := &models.EvalSpec{Config: models.Config{TimeoutSec: 30, WorkspaceCapture: "none"}}
	tc := &models.TestCase{TestID: "test", Stimulus: models.TaskStimulus{Message: "hello"}}
	engine := &workspaceCaptureRecordingEngine{MockEngine: execution.NewMockEngine("mock")}
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(context.Background())) })
	cfg := config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir()))
	runner := NewEvalRunner(cfg, engine, WithWorkspaceEvidence([]string{"report"}, nil, snapshot.WorkspaceLimits{}))
	run := runner.executeRun(t.Context(), tc, 1)
	require.Contains(t, run.ErrorMsg, "workspace snapshot files")
	require.Empty(t, engine.requests)
	runner = NewEvalRunner(cfg, engine)
	run = runner.executeRunWithAttempt(t.Context(), tc, 1, 1, &execution.ExecutionRequest{Message: "hello"})
	require.Contains(t, run.ErrorMsg, "prepared request workspace capture")
	require.Empty(t, engine.requests)
}
