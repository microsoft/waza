package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTaskCommand_HasRecordSubcommand(t *testing.T) {
	cmd := newNewTaskCommand()

	found := false
	for _, c := range cmd.Commands() {
		if c.Name() == "record" {
			found = true
			break
		}
	}

	assert.True(t, found, "new task command should include the record subcommand")
}

func TestNewTaskRecordCommand_RequiresTwoArgs(t *testing.T) {
	cmd := newTaskFromPromptCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"only-prompt"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 2 arg(s)")
}

func TestNewTaskRecordCommand_ExistingFileNeedsOverwrite(t *testing.T) {
	dir := t.TempDir()
	taskPath := filepath.Join(dir, "existing-task.yaml")
	require.NoError(t, os.WriteFile(taskPath, []byte("id: existing\n"), 0o644))

	root := newRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"new", "task", "record", "collect telemetry", taskPath})

	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "--overwrite")
}
