package orchestration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type inertRequestFixture struct {
	data map[string][]byte
	info map[string]os.FileInfo
}

func (f inertRequestFixture) ReadFile(path string) ([]byte, error) {
	data, ok := f.data[path]
	if !ok {
		return nil, fmt.Errorf("uncaptured test input %q", path)
	}
	return bytes.Clone(data), nil
}

func (f inertRequestFixture) Stat(path string) (os.FileInfo, error) {
	info, ok := f.info[path]
	if !ok {
		return nil, fmt.Errorf("uncaptured test stat %q", path)
	}
	return info, nil
}

func (f inertRequestFixture) EvalSymlinks(path string) (string, error) {
	_, err := f.Stat(path)
	return path, err
}

func (f inertRequestFixture) WalkDir(string, fs.WalkDirFunc) error {
	return fmt.Errorf("test fixture has no directory input")
}

// Direct control: no engine, mock or SDK object is constructed or executed.
func TestCapturedNativeRequestLegacyEquivalence(t *testing.T) {
	for _, mode := range []struct {
		name        string
		taskContext bool
		relative    bool
	}{{"cli_context", false, false}, {"task_context", true, false},
		{"native_relative_cli_context", false, true}, {"native_relative_task_context", true, true}} {
		t.Run(mode.name, func(t *testing.T) {
			base := t.TempDir()
			t.Chdir(base)
			contextDir := filepath.Join(base, "context")
			require.NoError(t, os.Mkdir(contextDir, 0700))
			for name, body := range map[string]string{"file.txt": "original", "eval.md": "eval instruction", "task.md": "task instruction"} {
				require.NoError(t, os.WriteFile(filepath.Join(contextDir, name), []byte(body), 0600))
			}

			require.NoError(t, os.WriteFile(filepath.Join(base, "fixture.txt"), []byte("context fixture"), 0600))
			spec := &models.EvalSpec{Config: models.Config{EngineType: "copilot-sdk", ModelID: "offline-model",
				DisabledSkills: []string{"*"}, InstructionFiles: []string{"eval.md"}, FirstEventTimeoutSec: 5},
				Graders: []models.GraderConfig{{Identifier: "text", Kind: models.GraderKindText, Parameters: models.TextGraderParameters{Contains: []string{"hello"}}}}}
			tc := &models.TestCase{TestID: "test", DisplayName: "name", Summary: "description",
				InstructionFiles: []string{"task.md"}, Stimulus: models.TaskStimulus{Message: "hello", WorkDir: "nested",
					Metadata:  map[string]any{"fixture": "fixture.txt", "large": json.Number("9007199254740993")},
					Resources: []models.ResourceRef{{Location: "file.txt"}, {Location: "inline.txt", Body: "inline"}}}}
			if mode.relative {
				contextDir = "context"
			}
			if mode.taskContext {
				tc.ContextRoot = contextDir
				contextDir = filepath.Join(base, "not-used")
				zero := 0
				tc.FirstEventTimeoutSec = &zero
			}
			cfg := config.NewEvalConfig(spec, config.WithSpecDir(base), config.WithFixtureDir(contextDir))
			control := &EvalRunner{cfg: cfg}
			legacy, err := control.buildExecutionRequest(tc)
			require.NoError(t, err)
			inert := inertRequestFixture{data: map[string][]byte{}, info: map[string]os.FileInfo{}}
			for _, path := range []string{base, filepath.Join(base, "fixture.txt")} {
				info, err := os.Stat(path)
				require.NoError(t, err)
				inert.info[path] = info
			}
			actualContext := contextDir
			if tc.ContextRoot != "" {
				actualContext = tc.ContextRoot
			}
			if !filepath.IsAbs(actualContext) {
				actualContext = filepath.Join(base, actualContext)
			}
			for _, name := range []string{"file.txt", "eval.md", "task.md"} {
				path := filepath.Join(actualContext, name)
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				inert.data[path] = data
			}
			data, err := os.ReadFile(filepath.Join(base, "fixture.txt"))
			require.NoError(t, err)
			inert.data[filepath.Join(base, "fixture.txt")] = data
			// Prove selected construction cannot reopen any original file.
			for path := range inert.data {
				require.NoError(t, os.Remove(path))
			}
			capturedTask := *tc
			if capturedTask.ContextRoot != "" && !filepath.IsAbs(capturedTask.ContextRoot) {
				capturedTask.ContextRoot = filepath.Join(base, capturedTask.ContextRoot)
			}
			capturedContext := contextDir
			if !filepath.IsAbs(capturedContext) {
				capturedContext = filepath.Join(base, capturedContext)
			}
			actual, err := BuildCapturedNativeRequest(spec, &capturedTask, base, capturedContext, inert)
			require.NoError(t, err)
			// The only selected-profile differences are explicit owned defaults.
			legacy.ToolPolicy = &execution.ToolPolicy{Mode: execution.ToolPolicyDenyAll}
			legacy.SkipWorkspaceCapture = true
			legacy.CommandMocksBaseDir = ""
			require.Equal(t, legacy, actual)
		})
	}
}

func TestCapturedNativeDefaultFixtureEquivalence(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	specDir := filepath.Join(base, "eval")
	// This is the actual cmd_run default when the CLI context flag is empty;
	// NewEvalConfig itself intentionally retains its prior empty default.
	defaultFixtureDir := filepath.Join(specDir, "fixtures")
	require.NoError(t, os.MkdirAll(defaultFixtureDir, 0700))
	inputPath := filepath.Join(defaultFixtureDir, "input.txt")
	require.NoError(t, os.WriteFile(inputPath, []byte("native default"), 0600))
	spec := &models.EvalSpec{Config: models.Config{EngineType: "copilot-sdk", ModelID: "offline-model",
		DisabledSkills: []string{"*"}},
		Graders: []models.GraderConfig{{Identifier: "text", Kind: models.GraderKindText, Parameters: models.TextGraderParameters{Contains: []string{"hello"}}}}}
	task := &models.TestCase{TestID: "task", Stimulus: models.TaskStimulus{Message: "hello",
		Resources: []models.ResourceRef{{Location: "input.txt"}}}}
	control := &EvalRunner{cfg: config.NewEvalConfig(spec, config.WithSpecDir(specDir), config.WithFixtureDir(defaultFixtureDir))}
	legacy, err := control.buildExecutionRequest(task)
	require.NoError(t, err)
	require.Equal(t, "native default", string(legacy.Resources[0].Content))
	inert := inertRequestFixture{data: map[string][]byte{inputPath: []byte("native default")}}
	require.NoError(t, os.Remove(inputPath))
	actual, err := BuildCapturedNativeRequest(spec, task, specDir, defaultFixtureDir, inert)
	require.NoError(t, err)
	legacy.ToolPolicy = &execution.ToolPolicy{Mode: execution.ToolPolicyDenyAll}
	legacy.SkipWorkspaceCapture = true
	legacy.CommandMocksBaseDir = ""
	require.Equal(t, legacy, actual)
	require.Empty(t, config.NewEvalConfig(spec, config.WithSpecDir(specDir)).FixtureDir())
}

func TestCapturedNativeRejectsLoaderSkippedResource(t *testing.T) {
	spec := &models.EvalSpec{Config: models.Config{EngineType: "copilot-sdk", ModelID: "offline-model",
		DisabledSkills: []string{"*"}},
		Graders: []models.GraderConfig{{Identifier: "text", Kind: models.GraderKindText, Parameters: models.TextGraderParameters{Contains: []string{"hello"}}}}}
	for _, path := range []string{"input..txt", "../input.txt", "/input.txt"} {
		t.Run(path, func(t *testing.T) {
			task := &models.TestCase{TestID: "task", Stimulus: models.TaskStimulus{Message: "hello",
				Resources: []models.ResourceRef{{Location: path}}}}
			_, err := BuildCapturedNativeRequest(spec, task, t.TempDir(), t.TempDir(), inertRequestFixture{})
			require.ErrorContains(t, err, "native loader")
		})
	}
}
