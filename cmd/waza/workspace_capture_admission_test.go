package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceCaptureCLIAdmission(t *testing.T) {
	dir := t.TempDir()
	eval := `schemaVersion: "1.4"
name: capture
skill: example
config: {executor: copilot-sdk, model: mock, trials_per_task: 1, timeout_seconds: 10, workspace_capture: none}
tasks: [tasks/*.yaml]
`
	require.NoError(t, os.Mkdir(filepath.Join(dir, "tasks"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte(eval), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks", "good.yaml"), []byte("id: good\nname: good\ninputs: {prompt: hello}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks", "bad.yaml"), []byte("id: bad\nname: bad\ninputs: {prompt: hello}\ngraders:\n  - name: file\n    type: file\n    config: {must_exist: [report]}\n"), 0o600))
	path := filepath.Join(dir, "eval.yaml")
	preflight := newPreflightCommand()
	var output bytes.Buffer
	preflight.SetOut(&output)
	preflight.SetErr(&output)
	preflight.SetArgs([]string{path})
	require.Error(t, preflight.Execute())
	require.Contains(t, output.String(), "workspace.capture")

	for _, selection := range []string{"bad", "good"} {
		t.Run(selection, func(t *testing.T) {
			resetRunGlobals()
			engine := &taskAdmissionStartupEngine{}
			constructions := 0
			previous := newRunEngine
			t.Cleanup(func() { newRunEngine = previous })
			newRunEngine = func(models.Config) (execution.AgentEngine, error) {
				constructions++
				return engine, nil
			}
			cmd := newRunCommand()
			cmd.SetArgs([]string{path, "--context-dir", dir, "--task", selection})
			err := cmd.Execute()
			if selection == "bad" {
				require.ErrorContains(t, err, "workspace_capture none")
				require.Zero(t, constructions)
			} else {
				require.ErrorContains(t, err, "task admission startup sentinel")
				require.Equal(t, 1, constructions)
			}
		})
	}
}
