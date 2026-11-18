package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestOptimizeNoCaptureRejectsQualificationBeforeAnyWork(t *testing.T) {
	for _, scope := range []string{"grader", "checkpoint", "grader_unknown", "checkpoint_unknown"} {
		t.Run(scope, func(t *testing.T) {
			suite := copyOptimizerExample(t)
			eval := filepath.Join(suite, "eval.yaml")
			data, err := os.ReadFile(eval)
			require.NoError(t, err)
			content := strings.ReplaceAll(string(data), "\r\n", "\n")
			content = strings.Replace(content, "  executor: mock\n", "  workspace_capture: none\n  executor: mock\n", 1)
			require.NoError(t, os.WriteFile(eval, []byte(content), 0o600))
			task := filepath.Join(suite, "tasks", "bug-fix.yaml")
			data, err = os.ReadFile(task)
			require.NoError(t, err)
			content = strings.ReplaceAll(string(data), "\r\n", "\n")
			switch scope {
			case "grader":
				content += "  - name: file\n    type: file\n    config: {must_exist: [report.txt]}\n"
			case "checkpoint":
				content += "checkpoints:\n  - after_turn: 1\n    graders:\n      - name: file\n        type: file\n        config: {must_exist: [report.txt]}\n"
			case "grader_unknown":
				content += "  - name: unknown\n    type: text\n    config: {contains: [ready], future_evidence: true}\n"
			case "checkpoint_unknown":
				content += "checkpoints:\n  - after_turn: 1\n    graders:\n      - name: unknown\n        type: text\n        config: {contains: [ready], future_evidence: true}\n"
			}
			require.NoError(t, os.WriteFile(task, []byte(content), 0o600))

			actorCalls, optimizerCalls := 0, 0
			oldActor, oldOptimizer := newRunEngine, newReflectEngine
			t.Cleanup(func() { newRunEngine, newReflectEngine = oldActor, oldOptimizer })
			newRunEngine = func(models.Config) (execution.AgentEngine, error) {
				actorCalls++
				return execution.NewMockEngine("mock"), nil
			}
			newReflectEngine = func(string) execution.AgentEngine {
				optimizerCalls++
				return execution.NewMockEngine("mock")
			}
			outDir := filepath.Join(t.TempDir(), "output")
			_, err = executeOptimize(t, "run", eval, "--seed", filepath.Join(suite, "seed"),
				"--target", "context-overlay", "--optimization-tag", "optimization", "--qualification-tag", "qualification",
				"--output-dir", outDir, "--adapter", "copilot-reflect", "--reflect-model", "mock-model", "--allow-paid-calls")
			require.ErrorContains(t, err, "workspace_capture none")
			require.Zero(t, actorCalls)
			require.Zero(t, optimizerCalls)
			require.NoDirExists(t, outDir)
		})
	}
}
