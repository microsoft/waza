package models

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestEvalSourceSnapshot(t *testing.T) {
	document := []byte("schemaVersion: '2.0'\nscenario: recover\nname: immutable\nconfig:\n  executor: mock\n  trials_per_task: 1\n  timeout_seconds: 5\ntasks: []\n")
	for _, parse := range []func([]byte, string) (*EvalSpec, error){ParseEvalSpec, ParseEvalSpecOffline} {
		input := bytes.Clone(document)
		spec, err := parse(input, "eval.yaml")
		require.NoError(t, err)
		require.Equal(t, document, spec.SourceBytes())
		input[0] = '!'
		copy := spec.SourceBytes()
		copy[0] = '!'
		require.Equal(t, document, spec.SourceBytes())
		assertSourceNotSerialized(t, spec)
	}
}

func TestTaskSourceSnapshotAndPromptResolution(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("loaded prompt"), 0o600))
	document := []byte("id: capture\nname: immutable\ninputs:\n  prompt_file: prompt.txt\ncommand_mocks: []\n")
	for _, parse := range []func([]byte, string) (*TestCase, error){ParseTestCase, ParseTestCaseOffline} {
		input := bytes.Clone(document)
		tc, err := parse(input, filepath.Join(dir, "task.yaml"))
		require.NoError(t, err)
		require.Equal(t, "loaded prompt", tc.Stimulus.Message)
		require.NotNil(t, tc.CommandMocks)
		require.Empty(t, *tc.CommandMocks)
		input[0] = '!'
		copy := tc.SourceBytes()
		copy[0] = '!'
		require.Equal(t, document, tc.SourceBytes())
		assertSourceNotSerialized(t, tc)
	}
}

func TestByteTaskParserKeepsLoadErrors(t *testing.T) {
	for _, parse := range []func([]byte, string) (*TestCase, error){ParseTestCase, ParseTestCaseOffline} {
		tc, err := parse([]byte("id: missing\ninputs:\n  prompt_file: absent.txt\n"), filepath.Join(t.TempDir(), "task.yaml"))
		require.ErrorContains(t, err, "prompt_file")
		require.Nil(t, tc)
		tc, err = parse([]byte("id: bad\ntimeout_seconds: 0\n"), "task.yaml")
		require.ErrorContains(t, err, "timeout_seconds")
		require.Nil(t, tc)
	}
}

func assertSourceNotSerialized(t *testing.T, model any) {
	t.Helper()
	j, err := json.Marshal(model)
	require.NoError(t, err)
	y, err := yaml.Marshal(model)
	require.NoError(t, err)
	require.NotContains(t, string(j), "sourceDocument")
	require.NotContains(t, string(y), "sourcedocument")
}
