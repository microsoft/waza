package scaffold

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{"valid kebab-case", "my-skill", false, ""},
		{"valid simple", "skill", false, ""},
		{"empty", "", true, "must not be empty"},
		{"path traversal dots", "../evil", true, "invalid path characters"},
		{"forward slash", "a/b", true, "invalid path characters"},
		{"backslash", "a\\b", true, "invalid path characters"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateName(tc.input)
			if tc.wantErr {
				assert.Error(t, err)
				if tc.errMsg != "" {
					assert.Contains(t, err.Error(), tc.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestTitleCase(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-skill", "My Skill"},
		{"code-analyzer", "Code Analyzer"},
		{"skill", "Skill"},
		{"a-b-c", "A B C"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.want, TitleCase(tc.input))
		})
	}
}

func TestEvalYAML(t *testing.T) {
	content := EvalYAML("code-analyzer", "copilot-sdk", "claude-sonnet-4.6")

	assert.Contains(t, content, "name: code-analyzer-eval")
	assert.Contains(t, content, "skill: code-analyzer")
	assert.Contains(t, content, "executor: copilot-sdk")
	assert.Contains(t, content, "model: claude-sonnet-4.6")
	assert.Contains(t, content, "type: code")
	assert.Contains(t, content, "type: regex")
	assert.Contains(t, content, `"tasks/*.yaml"`)
}

func TestEvalYAML_CustomEngine(t *testing.T) {
	content := EvalYAML("my-skill", "mock", "gpt-4o")

	assert.Contains(t, content, "executor: mock")
	assert.Contains(t, content, "model: gpt-4o")
}

func TestTaskFiles(t *testing.T) {
	tasks := TaskFiles("my-skill")

	assert.Contains(t, tasks, "basic-usage.yaml")
	assert.Contains(t, tasks, "edge-case.yaml")
	assert.Contains(t, tasks, "should-not-trigger.yaml")
	assert.Len(t, tasks, 3)

	assert.Contains(t, tasks["basic-usage.yaml"], "id: basic-usage-001")
	assert.Contains(t, tasks["edge-case.yaml"], "id: edge-case-001")
	assert.Contains(t, tasks["should-not-trigger.yaml"], "id: should-not-trigger-001")
}

func TestFixture(t *testing.T) {
	content := Fixture()
	assert.Contains(t, content, "def hello(name):")
}

func TestReadProjectDefaults(t *testing.T) {
	dir := t.TempDir()
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	// No .waza.yaml → defaults
	engine, model := ReadProjectDefaults()
	assert.Equal(t, "copilot-sdk", engine)
	assert.Equal(t, "claude-sonnet-4.6", model)
}

func TestReadProjectDefaults_WithConfig(t *testing.T) {
	dir := t.TempDir()
	origDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { os.Chdir(origDir) }) //nolint:errcheck // best-effort cleanup

	wazaConfig := "defaults:\n  engine: mock\n  model: gpt-4o\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte(wazaConfig), 0o644))

	engine, model := ReadProjectDefaults()
	assert.Equal(t, "mock", engine)
	assert.Equal(t, "gpt-4o", model)
}
