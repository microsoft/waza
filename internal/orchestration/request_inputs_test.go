package orchestration

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type observedRequestFiles struct {
	RequestFiles
	roles []string
}

func (f *observedRequestFiles) SetInputRole(role string) {
	f.roles = append(f.roles, role)
}

type failingRequestFiles struct {
	RequestFiles
	err error
}

type cancelingRequestInputs struct {
	RequestInputs
	cancel context.CancelFunc
}

func (f cancelingRequestInputs) ReadFile(string) ([]byte, error) {
	f.cancel()
	return []byte("content"), nil
}

func (f failingRequestFiles) Stat(string) (os.FileInfo, error) {
	return nil, f.err
}

func (f failingRequestFiles) WalkDir(path string, visit fs.WalkDirFunc) error {
	return visit(path, nil, f.err)
}

func TestRequestInputsRealLegacyKindsAndTraversal(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "dir")
	require.NoError(t, os.Mkdir(dir, 0700))
	file := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(file, []byte("content"), 0600))
	adapter := AdaptRequestFiles(nativeRequestFiles{})
	for _, row := range []struct {
		path string
		kind RequestInputKind
	}{{dir, RequestInputDirectory}, {file, RequestInputRegular}} {
		kind, err := adapter.Kind(row.path)
		require.NoError(t, err)
		require.Equal(t, row.kind, kind)
	}
	_, err := adapter.Kind(filepath.Join(base, "missing"))
	require.ErrorIs(t, err, fs.ErrNotExist)
	content, err := adapter.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, []byte("content"), content)
	resolved, err := adapter.Resolve(file)
	require.NoError(t, err)
	originalResolved, err := (nativeRequestFiles{}).EvalSymlinks(file)
	require.NoError(t, err)
	require.Equal(t, originalResolved, resolved)
	for _, control := range []error{nil, fs.SkipDir, fs.SkipAll, errors.New("consumer failed")} {
		var originalPaths, actualPaths []string
		originalErr := (nativeRequestFiles{}).WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			originalPaths = append(originalPaths, path)
			return control
		})
		actualErr := adapter.Walk(dir, func(path string, _ RequestInputKind) error {
			actualPaths = append(actualPaths, path)
			return control
		})
		require.Equal(t, originalPaths, actualPaths)
		require.Equal(t, originalErr, actualErr)
	}
}

func TestRequestInputsOriginalErrorPrecedenceAndRoleForwarding(t *testing.T) {
	sentinel := errors.New("original filesystem error")
	adapter := AdaptRequestFiles(failingRequestFiles{err: sentinel})
	kind, err := adapter.Kind("input")
	require.Empty(t, kind)
	require.ErrorIs(t, err, sentinel)
	called := false
	err = adapter.Walk("input", func(string, RequestInputKind) error {
		called = true
		return errors.New("unexpected consumer call")
	})
	require.ErrorIs(t, err, sentinel)
	require.False(t, called)
	files := &observedRequestFiles{RequestFiles: nativeRequestFiles{}}
	roles, ok := AdaptRequestFiles(files).(interface{ SetInputRole(string) })
	require.True(t, ok)
	roles.SetInputRole("resource")
	require.Equal(t, []string{"resource"}, files.roles)
	runner := &EvalRunner{requestFiles: files}
	runner.setRequestInputRole("instruction")
	require.Equal(t, []string{"resource", "instruction"}, files.roles)
}

func retainedRequestDeclarations() (*models.EvalSpec, *models.TestCase) {
	return &models.EvalSpec{Config: models.Config{
			EngineType: "copilot-sdk", ModelID: "offline-model", DisabledSkills: []string{"*"},
		}, Graders: []models.GraderConfig{{
			Identifier: "text", Kind: models.GraderKindText,
			Parameters: models.TextGraderParameters{Contains: []string{"hello"}},
		}}},
		&models.TestCase{TestID: "task", Stimulus: models.TaskStimulus{Message: "hello"}}
}

func TestRetainedNativeRequestContextAndCapturedGuardParity(t *testing.T) {
	spec, task := retainedRequestDeclarations()
	req, err := BuildRetainedNativeRequest(nil, spec, task, "", "", nil) //nolint:staticcheck // Exercise the explicit nil-context boundary.
	require.Nil(t, req)
	require.ErrorContains(t, err, "requires context")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err = BuildRetainedNativeRequest(ctx, nil, nil, "", "", nil)
	require.Nil(t, req)
	require.ErrorIs(t, err, context.Canceled)
	for _, row := range []struct {
		name string
		spec *models.EvalSpec
		task *models.TestCase
	}{{"nil_spec", nil, task}, {"nil_task", spec, nil}} {
		t.Run(row.name, func(t *testing.T) {
			_, originalErr := frozenOriginalBuildCapturedNativeRequest(row.spec, row.task, "", "", nil)
			req, actualErr := BuildRetainedNativeRequest(context.Background(), row.spec, row.task, "", "", nil)
			require.Nil(t, req)
			require.Equal(t, originalErr, actualErr)
		})
	}
	req, err = BuildRetainedNativeRequest(context.Background(), spec, task, "", "", nil)
	require.Nil(t, req)
	require.ErrorContains(t, err, "requires retained inputs")
	for _, destination := range []string{"", "../outside", "/absolute", "input..txt"} {
		task.Stimulus.Resources = []models.ResourceRef{{Location: destination}}
		_, originalErr := frozenOriginalBuildCapturedNativeRequest(spec, task, "eval", "context", nativeRequestFiles{})
		req, actualErr := BuildRetainedNativeRequest(context.Background(), spec, task, "eval", "context", AdaptRequestFiles(nativeRequestFiles{}))
		require.Nil(t, req)
		require.Equal(t, originalErr, actualErr)
	}
}

func TestRetainedNativeRequestFullCapturedByteParity(t *testing.T) {
	spec, task := retainedRequestDeclarations()
	task.Stimulus.Resources = []models.ResourceRef{{Location: "inline", Body: "content"}}
	task.Stimulus.Metadata = map[string]any{"nested": []any{uint64(18446744073709551615), int64(-9223372036854775808)}}
	for _, first := range []int{0, 5} {
		task.FirstEventTimeoutSec = &first
		captured, err := frozenOriginalBuildCapturedNativeRequest(spec, task, "", "", nativeRequestFiles{})
		require.NoError(t, err)
		retained, err := BuildRetainedNativeRequest(context.Background(), spec, task, "", "", AdaptRequestFiles(nativeRequestFiles{}))
		require.NoError(t, err)
		before, err := FreezeCapturedNativeRequest(captured)
		require.NoError(t, err)
		after, err := FreezeCapturedNativeRequest(retained)
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
}

func TestRetainedNativeOriginalFixtureConstructorParity(t *testing.T) {
	for _, name := range []string{"none", "file", "directory", "empty", "task_context", "relative_context", "repeated_instruction"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			t.Chdir(base)
			contextDir := filepath.Join(base, "fixtures")
			fixtureDir := filepath.Join(base, "fixture")
			require.NoError(t, os.MkdirAll(filepath.Join(fixtureDir, "nested", "empty"), 0700))
			require.NoError(t, os.MkdirAll(contextDir, 0700))
			for _, row := range []struct{ path, content string }{
				{filepath.Join(base, "fixture.txt"), "single fixture"},
				{filepath.Join(fixtureDir, "z.txt"), "z fixture"},
				{filepath.Join(fixtureDir, "nested", "a.txt"), "nested fixture"},
				{filepath.Join(contextDir, "input"), "resource"},
				{filepath.Join(contextDir, "eval.md"), "eval instruction"},
				{filepath.Join(contextDir, "task.md"), "task instruction"},
			} {
				require.NoError(t, os.WriteFile(row.path, []byte(row.content), 0600))
			}
			spec, task := retainedRequestDeclarations()
			switch name {
			case "file":
				task.Stimulus.Metadata = map[string]any{"fixture": "fixture.txt"}
			case "directory":
				task.Stimulus.Metadata = map[string]any{"fixture": "fixture"}
			case "empty":
				task.Stimulus.Metadata = map[string]any{"fixture": "fixture/nested/empty"}
			default:
				task.Stimulus.Resources = []models.ResourceRef{{Location: "input"}, {Location: "inline", Body: "inline"}}
				spec.Config.InstructionFiles = []string{"eval.md"}
				task.InstructionFiles = []string{"task.md"}
			}
			if name == "relative_context" {
				contextDir = "fixtures"
			}
			if name == "task_context" {
				task.ContextRoot = contextDir
				contextDir = filepath.Join(base, "unused")
			}
			if name == "repeated_instruction" {
				task.InstructionFiles = []string{"eval.md", "task.md"}
			}
			original, originalErr := frozenOriginalBuildCapturedNativeRequest(spec, task, base, contextDir, nativeRequestFiles{})
			actual, actualErr := BuildRetainedNativeRequest(context.Background(), spec, task, base, contextDir,
				AdaptRequestFiles(nativeRequestFiles{}))
			require.Equal(t, originalErr, actualErr)
			require.NoError(t, actualErr)
			originalBytes, err := FreezeCapturedNativeRequest(original)
			require.NoError(t, err)
			actualBytes, err := FreezeCapturedNativeRequest(actual)
			require.NoError(t, err)
			require.Equal(t, originalBytes, actualBytes)
			if name == "empty" {
				require.Nil(t, actual.Resources)
			}

		})
	}
}

func TestRetainedNativeRequestConstructorFailureAndCancellation(t *testing.T) {
	spec, task := retainedRequestDeclarations()
	task.Stimulus.Metadata = map[string]any{"fixture": 1}
	req, err := BuildRetainedNativeRequest(context.Background(), spec, task, "", "", requestFilesInputs{nativeRequestFiles{}})
	require.Nil(t, req)
	require.ErrorContains(t, err, "must be a string")
	task.Stimulus.Metadata = nil
	task.Stimulus.Resources = []models.ResourceRef{{Location: "input"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err = BuildRetainedNativeRequest(ctx, spec, task, t.TempDir(), t.TempDir(),
		cancelingRequestInputs{cancel: cancel})
	require.Nil(t, req)
	require.ErrorIs(t, err, context.Canceled)
}

func TestAdaptRequestFilesNilAndProviderOwnership(t *testing.T) {
	require.Nil(t, AdaptRequestFiles(nil))
	var typedNil *observedRequestFiles
	adapted := AdaptRequestFiles(typedNil)
	require.NotNil(t, adapted)
	private, ok := adapted.(requestFilesInputs)
	require.True(t, ok)
	require.Same(t, typedNil, private.files)
	spec, task := retainedRequestDeclarations()
	request, err := BuildRetainedNativeRequest(context.Background(), spec, task, "", "", AdaptRequestFiles(nil))
	require.Nil(t, request)
	require.ErrorContains(t, err, "requires retained inputs")
	files := &observedRequestFiles{RequestFiles: nativeRequestFiles{}}
	first, second := AdaptRequestFiles(files), AdaptRequestFiles(files)
	firstRoles, ok := first.(interface{ SetInputRole(string) })
	require.True(t, ok)
	secondRoles, ok := second.(interface{ SetInputRole(string) })
	require.True(t, ok)
	firstRoles.SetInputRole("resource")
	secondRoles.SetInputRole("instruction")
	require.Equal(t, []string{"resource", "instruction"}, files.roles)
}

func TestRequestInputsRunnerSelection(t *testing.T) {
	legacy := &observedRequestFiles{RequestFiles: nativeRequestFiles{}}
	selected := &observedRequestFiles{RequestFiles: nativeRequestFiles{}}
	runner := &EvalRunner{requestFiles: legacy, requestInputs: requestFilesInputs{selected}}
	runner.setRequestInputRole("resource")
	require.Nil(t, legacy.roles)
	require.Equal(t, []string{"resource"}, selected.roles)
	require.IsType(t, requestFilesInputs{}, (&EvalRunner{}).inputFiles())
}
