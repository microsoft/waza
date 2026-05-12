package execution

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexEngineExecuteUsesCLIWorkspaceAndSkillContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake codex shell script is POSIX-only")
	}

	fakeCodex := writeFakeCodex(t, 0)
	sourceDir := t.TempDir()
	skillDir := filepath.Join(sourceDir, "skills", "demo")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo\n---\nAlways mention workspace facts."), 0o644))

	engine := NewCodexEngine("test-model", WithCodexBinary(fakeCodex))
	require.NoError(t, engine.Initialize(context.Background()))
	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:              "Inspect the fixture.",
		ModelReasoningEffort: "high",
		Resources:            []ResourceFile{{Path: "input.txt", Content: []byte("fixture data")}},
		SourceDir:            sourceDir,
		SkillName:            "demo",
		TaskName:             "Codex task",
		TaskDescription:      "Verify fake execution.",
		SkillPaths:           []string{filepath.Join(sourceDir, "skills")},
		Timeout:              10 * time.Second,
	})

	require.NoError(t, err)
	require.True(t, resp.Success)
	require.Equal(t, "final from fake codex", resp.FinalOutput)
	require.Equal(t, "test-model", resp.ModelID)
	require.Contains(t, resp.WorkspaceFiles, "created.txt")
	require.Equal(t, []byte("fixture data"), resp.WorkspaceFiles["input.txt"])

	prompt := string(resp.WorkspaceFiles["prompt.txt"])
	require.Contains(t, prompt, "<skill_context>")
	require.Contains(t, prompt, "Always mention workspace facts.")
	require.Contains(t, prompt, "Name: Codex task")

	args := string(resp.WorkspaceFiles["args.txt"])
	require.Contains(t, args, "--model test-model")
	require.Contains(t, args, `approval_policy="never"`)
	require.Contains(t, args, `model_reasoning_effort="high"`)
	require.Contains(t, args, "--sandbox workspace-write")
	require.NotContains(t, args, "--ephemeral")
	require.Len(t, resp.ToolCalls, 1)
	require.Equal(t, "bash", resp.ToolCalls[0].Name)
	require.Equal(t, "codex-test-session", resp.SessionID)
	require.NotNil(t, resp.Usage)
	require.Equal(t, 12, resp.Usage.InputTokens)
}

func TestCodexEngineExecuteReportsCLIError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake codex shell script is POSIX-only")
	}

	fakeCodex := writeFakeCodex(t, 7)
	engine := NewCodexEngine("", WithCodexBinary(fakeCodex))
	require.NoError(t, engine.Initialize(context.Background()))
	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message: "fail",
		Timeout: 10 * time.Second,
	})

	require.NoError(t, err)
	require.False(t, resp.Success)
	require.Contains(t, resp.ErrorMsg, "fake codex failed")
	require.Equal(t, "final from fake codex", resp.FinalOutput)
}

func TestCodexEngineExecuteResumesSessionForFollowUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake codex shell script is POSIX-only")
	}

	fakeCodex := writeFakeCodex(t, 0)
	engine := NewCodexEngine("", WithCodexBinary(fakeCodex))
	require.NoError(t, engine.Initialize(context.Background()))
	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	first, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message: "Remember apple.",
		Timeout: 10 * time.Second,
	})
	require.NoError(t, err)
	require.Equal(t, "codex-test-session", first.SessionID)

	second, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:      "What did I ask you to remember?",
		SessionID:    first.SessionID,
		WorkspaceDir: first.WorkspaceDir,
		Timeout:      10 * time.Second,
	})
	require.NoError(t, err)

	args := string(second.WorkspaceFiles["args.txt"])
	require.Contains(t, args, "exec resume")
	require.Contains(t, args, "codex-test-session")
	require.NotContains(t, args, "--ephemeral")
	require.Equal(t, first.WorkspaceDir, second.WorkspaceDir)
}

func TestCodexEngineExecuteRejectsSkillTriggerTelemetry(t *testing.T) {
	fakeCodex := writeFakeCodex(t, 0)
	engine := NewCodexEngine("", WithCodexBinary(fakeCodex))
	require.NoError(t, engine.Initialize(context.Background()))
	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	_, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:                 "trigger?",
		Timeout:                 10 * time.Second,
		CancelOnSkillInvocation: true,
	})
	require.ErrorContains(t, err, "does not support skill invocation telemetry")
}

func writeFakeCodex(t *testing.T, exitCode int) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "codex")
	script := `#!/bin/sh
set -u
work=""
out=""
args=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --cd)
      work="$2"
      args="$args $1 $2"
      shift 2
      ;;
    --output-last-message)
      out="$2"
      args="$args $1 $2"
      shift 2
      ;;
    *)
      args="$args $1"
      shift
      ;;
  esac
done
if [ -n "$work" ]; then
  cd "$work"
fi
cat > prompt.txt
printf "%s" "$args" > args.txt
printf "created by fake codex" > created.txt
if [ -n "$out" ]; then
  printf "final from fake codex" > "$out"
else
  printf "final from fake codex"
fi
cat <<'JSON'
{"type":"thread.started","thread_id":"codex-test-session"}
{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"/bin/sh -c pwd","aggregated_output":"","exit_code":null,"status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_0","type":"command_execution","command":"/bin/sh -c pwd","aggregated_output":"fake pwd\n","exit_code":0,"status":"completed"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"final from fake codex"}}
{"type":"turn.completed","usage":{"input_tokens":12,"cached_input_tokens":3,"output_tokens":4,"reasoning_output_tokens":1}}
JSON
if [ ` + strconv.Itoa(exitCode) + ` -ne 0 ]; then
  printf "fake codex failed\n" >&2
  exit ` + strconv.Itoa(exitCode) + `
fi
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}
