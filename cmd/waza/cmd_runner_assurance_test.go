package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/runnerassurance"
	"github.com/stretchr/testify/require"
)

func TestRunnerAssuranceBuildEmitsVersionedJSONPackage(t *testing.T) {
	cmd := newRunnerCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"assurance", "build"})

	require.NoError(t, cmd.Execute())
	var pkg runnerassurance.Package
	require.NoError(t, json.Unmarshal(output.Bytes(), &pkg))
	require.Equal(t, runnerassurance.Kind, pkg.Kind)
	require.Equal(t, runnerassurance.Version, pkg.PackageVersion)
	require.Equal(t, "v0.42.0", pkg.TargetRelease)
}

func TestRunnerAssuranceBuildEmitsGeneratedMarkdown(t *testing.T) {
	cmd := newRunnerCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"assurance", "build", "--format", "markdown"})

	require.NoError(t, cmd.Execute())
	require.Contains(t, output.String(), "## Operator conformance matrix")
	require.Contains(t, output.String(), "## Existing Lab evidence")
}

func TestRunnerAssuranceBuildRejectsUnsupportedOutputFormat(t *testing.T) {
	cmd := newRunnerCommand()
	cmd.SetArgs([]string{"assurance", "build", "--format", "html"})
	require.ErrorContains(t, cmd.Execute(), "--format must be json or markdown")
}
