package models

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRequirementsRemainDescriptive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
id: example
name: Example
inputs:
  prompt: Inspect the fixture.
requirements:
  - id: duplicate
    category: unknown
    description: Descriptive only.
    checks:
      - scope: task
        grader: missing
  - id: duplicate
    category: recovery
    description: Also descriptive.
`), 0600))
	task, err := LoadTestCase(path)
	require.NoError(t, err, "new metadata must not change existing run/load validity")
	require.Len(t, task.Requirements, 2)
	require.NoError(t, task.Validate())
	data, err := yaml.Marshal(task)
	require.NoError(t, err)
	var roundtrip TestCase
	require.NoError(t, yaml.Unmarshal(data, &roundtrip))
	require.Equal(t, task.Requirements, roundtrip.Requirements)
}
