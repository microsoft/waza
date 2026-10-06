package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServeLabFlag(t *testing.T) {
	cmd := newServeCommand()
	flag := cmd.Flags().Lookup("lab")
	require.NotNil(t, flag)
	require.Equal(t, "false", flag.DefValue)
	require.NoError(t, cmd.ParseFlags([]string{"--lab", "--no-browser", "--port", "4188"}))
	enabled, err := cmd.Flags().GetBool("lab")
	require.NoError(t, err)
	require.True(t, enabled)
	port, err := cmd.Flags().GetInt("port")
	require.NoError(t, err)
	require.Equal(t, 4188, port)
}

func TestServeLabHelp(t *testing.T) {
	cmd := newServeCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, output.String(), "--lab")
	require.Contains(t, output.String(), "current dashboard remains available")
}

func TestServeLabRejectsTCP(t *testing.T) {
	cmd := newServeCommand()
	cmd.SetArgs([]string{"--lab", "--tcp", ":9000"})
	require.ErrorContains(t, cmd.Execute(), "none of the others can be")
}
