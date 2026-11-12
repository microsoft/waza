package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/optimizer"
	"github.com/stretchr/testify/require"
)

func copyOptimizerExample(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "suite")
	require.NoError(t, os.CopyFS(dst, os.DirFS("../../examples/optimizer")))
	return dst
}

func executeOptimize(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newOptimizeCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestOptimizeCapabilities(t *testing.T) {
	out, err := executeOptimize(t, "capabilities")
	require.NoError(t, err)
	var doc optimizer.CapabilityDocument
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	require.Equal(t, optimizer.Capabilities(), doc)
}

func TestOptimizeRunExample(t *testing.T) {
	suite := copyOptimizerExample(t)
	outDir := filepath.Join(t.TempDir(), "out")
	base := []string{"run", filepath.Join(suite, "eval.yaml"), "--seed", filepath.Join(suite, "seed"),
		"--target", "context-overlay", "--optimization-tag", "optimization", "--qualification-tag", "qualification", "--output-dir", outDir}

	out, err := executeOptimize(t, append(base, "--adapter", "fake", "--fake-script", filepath.Join(suite, "fake-script.json"))...)
	require.NoError(t, err)
	require.Contains(t, out, "accepted")
	data, err := os.ReadFile(filepath.Join(outDir, "report.json"))
	require.NoError(t, err)
	var report optimizer.Report
	require.NoError(t, json.Unmarshal(data, &report))
	require.Equal(t, optimizer.OutcomeAccepted, report.Outcome)

	_, err = executeOptimize(t, append(base[:len(base)-1], filepath.Join(t.TempDir(), "consent"),
		"--adapter", "command", "--adapter-command", os.Args[0])...)
	exitErr, ok := err.(*ExitCodeError)
	require.True(t, ok, "consent failures exit nonzero: %v", err)
	require.Equal(t, 1, exitErr.Code)
	require.ErrorContains(t, err, optimizer.CategoryConsentRequired)
}

func TestOptimizeRunValidation(t *testing.T) {
	suite := copyOptimizerExample(t)
	spec := filepath.Join(suite, "eval.yaml")
	cases := map[string][]string{
		"missing seed":    {"run", spec, "--optimization-tag", "optimization", "--qualification-tag", "qualification"},
		"missing tags":    {"run", spec, "--seed", filepath.Join(suite, "seed")},
		"unknown adapter": {"run", spec, "--seed", filepath.Join(suite, "seed"), "--optimization-tag", "optimization", "--qualification-tag", "qualification", "--adapter", "nope", "--output-dir", filepath.Join(t.TempDir(), "o")},
		"unknown target":  {"run", spec, "--target", "prompt"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := executeOptimize(t, args...)
			require.Error(t, err)
		})
	}
}
