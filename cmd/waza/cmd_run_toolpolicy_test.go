package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/stretchr/testify/require"
)

type policyLogRunner struct {
	listeners []orchestration.ProgressListener
}

func (r *policyLogRunner) OnProgress(listener orchestration.ProgressListener) {
	r.listeners = append(r.listeners, listener)
}

func (r *policyLogRunner) RunBenchmark(context.Context) (*models.EvaluationOutcome, error) {
	for _, listener := range r.listeners {
		listener(orchestration.ProgressEvent{
			EventType: orchestration.EventRunComplete, TestName: "reader", RunNum: 1,
			Details: map[string]any{"session_digest": models.SessionDigest{
				ToolPolicyMode:    "allow_list",
				ToolPolicyDenials: []models.ToolPolicyDenial{{Tool: "bash", Kind: "shell", Reason: "undeclared"}},
			}},
		})
	}
	return &models.EvaluationOutcome{}, nil
}

func TestRunCommandSessionLogIncludesToolPolicy(t *testing.T) {
	resetRunGlobals()
	t.Cleanup(resetRunGlobals)
	newBenchmarkRunner = func(*config.EvalConfig, execution.AgentEngine, ...orchestration.RunnerOption) benchmarkRunner {
		return &policyLogRunner{}
	}
	dir := t.TempDir()
	cmd := newRunCommand()
	cmd.SetArgs([]string{createTestSpec(t, "mock"), "--session-log", "--session-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.Execute())
	paths, err := filepath.Glob(filepath.Join(dir, "*-session.jsonl"))
	require.NoError(t, err)
	require.Len(t, paths, 1)
	data, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	var event struct {
		Type string `json:"type"`
		Data struct {
			TaskName      string               `json:"task_name"`
			RunNumber     int                  `json:"run_number"`
			SessionDigest models.SessionDigest `json:"session_digest"`
		} `json:"data"`
	}
	// The stub emits one run event before the command's session-end event.
	line := data
	for i, b := range data {
		if b == '\n' {
			line = data[:i]
			break
		}
	}
	require.NoError(t, json.Unmarshal(line, &event))
	require.Equal(t, "run_complete", event.Type)
	require.Equal(t, "reader", event.Data.TaskName)
	require.Equal(t, 1, event.Data.RunNumber)
	require.Equal(t, "allow_list", event.Data.SessionDigest.ToolPolicyMode)
	require.Equal(t, []models.ToolPolicyDenial{{Tool: "bash", Kind: "shell", Reason: "undeclared"}}, event.Data.SessionDigest.ToolPolicyDenials)
}
