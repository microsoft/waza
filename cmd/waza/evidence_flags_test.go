package main

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunSnapshotHelpQualifiesWorkspaceCapture(t *testing.T) {
	resetRunGlobals()
	t.Cleanup(resetRunGlobals)
	command := newRunCommand()
	require.Contains(t, command.Flags().Lookup("snapshot").Usage, "only with explicit --snapshot-file selections")
}

func TestRunSnapshotExclusionRequiresCapture(t *testing.T) {
	for _, flag := range []string{"--snapshot-file", "--snapshot-evaluator-only"} {
		t.Run(flag, func(t *testing.T) {
			resetRunGlobals()
			t.Cleanup(resetRunGlobals)
			command := newRunCommand()
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetArgs([]string{"missing-eval.yaml", flag, "rubric/report.txt"})
			require.ErrorContains(t, command.Execute(), flag+" requires --snapshot")
		})
	}
	t.Run("capture without file selection", func(t *testing.T) {
		resetRunGlobals()
		t.Cleanup(resetRunGlobals)
		command := newRunCommand()
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		command.SetArgs([]string{"missing-eval.yaml", "--snapshot", t.TempDir(),
			"--snapshot-evaluator-only", "rubric/report.txt"})
		require.ErrorContains(t, command.Execute(), "--snapshot-evaluator-only requires --snapshot-file")
	})
	t.Run("capture and file selection present", func(t *testing.T) {
		resetRunGlobals()
		t.Cleanup(resetRunGlobals)
		command := newRunCommand()
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		command.SetArgs([]string{"missing-eval.yaml", "--snapshot", t.TempDir(),
			"--snapshot-file", "report.txt", "--snapshot-evaluator-only", "rubric/report.txt"})
		err := command.Execute()
		require.Error(t, err)
		require.NotContains(t, err.Error(), "requires --snapshot")
	})
}
