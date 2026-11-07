package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAggregateHelpSeparatesDeclaredAndNativeCategories(t *testing.T) {
	command := newAggregateCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"--help"})
	require.NoError(t, command.Execute())
	require.Contains(t, output.String(), "Declared categories and admitted native shapes remain separate")
	require.Contains(t, output.String(), "usage totals are withheld")
	require.NotContains(t, output.String(), "Category states remain declarations")
}
