package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/azure/azure-dev/cli/azd/pkg/extensions"
	"github.com/stretchr/testify/require"
)

func TestMetadataCommand_OutputsValidJSON(t *testing.T) {
	rootCmd := newRootCommand()

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"metadata"})

	err := rootCmd.Execute()
	require.NoError(t, err)

	var metadata extensions.ExtensionCommandMetadata
	err = json.Unmarshal(buf.Bytes(), &metadata)
	require.NoError(t, err, "metadata output should be valid JSON matching ExtensionCommandMetadata")

	require.Equal(t, "1.0", metadata.SchemaVersion)
	require.Equal(t, "microsoft.azd.waza", metadata.ID)
}

func TestMetadataCommand_ContainsExpectedCommands(t *testing.T) {
	rootCmd := newRootCommand()

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"metadata"})

	err := rootCmd.Execute()
	require.NoError(t, err)

	var metadata extensions.ExtensionCommandMetadata
	err = json.Unmarshal(buf.Bytes(), &metadata)
	require.NoError(t, err)

	// Collect top-level command names
	cmdNames := make(map[string]bool)
	for _, cmd := range metadata.Commands {
		if len(cmd.Name) > 0 {
			cmdNames[cmd.Name[0]] = true
		}
	}

	expectedCmds := []string{"run", "init", "generate", "compare", "tokens"}
	for _, name := range expectedCmds {
		require.True(t, cmdNames[name], "expected command %q in metadata output", name)
	}

	// metadata command itself should be present but hidden
	require.True(t, cmdNames["metadata"], "metadata command should appear in output")
	for _, cmd := range metadata.Commands {
		if len(cmd.Name) > 0 && cmd.Name[0] == "metadata" {
			require.True(t, cmd.Hidden, "metadata command should be hidden")
		}
	}
}

func TestMetadataCommand_FlagsPopulated(t *testing.T) {
	rootCmd := newRootCommand()

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"metadata"})

	err := rootCmd.Execute()
	require.NoError(t, err)

	var metadata extensions.ExtensionCommandMetadata
	err = json.Unmarshal(buf.Bytes(), &metadata)
	require.NoError(t, err)

	// Find the "run" command and check it has flags
	for _, cmd := range metadata.Commands {
		if len(cmd.Name) > 0 && cmd.Name[0] == "run" {
			flagNames := make(map[string]bool)
			for _, f := range cmd.Flags {
				flagNames[f.Name] = true
			}
			require.True(t, flagNames["verbose"], "run command should have --verbose flag")
			require.True(t, flagNames["output"], "run command should have --output flag")
			require.True(t, flagNames["context-dir"], "run command should have --context-dir flag")
			return
		}
	}
	t.Fatal("run command not found in metadata")
}

func TestMetadataCommand_IsHidden(t *testing.T) {
	rootCmd := newRootCommand()

	metadataCmd, _, err := rootCmd.Find([]string{"metadata"})
	require.NoError(t, err)
	require.True(t, metadataCmd.Hidden, "metadata command should be hidden from help")
}
