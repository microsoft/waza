package nativesnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

const snapshotEval = `schemaVersion: "1.0"
name: offline-native
config:
  executor: copilot-sdk
  model: offline-model
  disabled_skills: ["*"]
  trials_per_task: 1
  timeout_seconds: 30
tasks: ["tasks/*.yaml"]
graders:
  - name: text
    type: text
    config:
      contains: ["hello"]
`

const snapshotTask = `id: task
name: task-name
inputs:
  prompt_file: prompt.txt
  files:
    - path: a.txt
    - path: inline.txt
      content: inline
instruction_files: ["instruction.md"]
`

func write(t *testing.T, root, path, data string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, path), []byte(data), 0600))
}

func fixture(t *testing.T) (string, map[releasepolicy.Arm]ArmLocation) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native snapshot directory discovery is unsupported on Windows; covered by the platform negative test")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for _, prefix := range []string{"baseline", "candidate"} {
		write(t, base, prefix+"/eval.yaml", snapshotEval)
		write(t, base, prefix+"/tasks/task.yaml", snapshotTask)
		write(t, base, prefix+"/tasks/prompt.txt", "hello")
		write(t, base, prefix+"/context/a.txt", prefix)
		write(t, base, prefix+"/context/instruction.md", "exact instruction\n")
	}
	write(t, base, "evaluator", "offline executable identity, never launched")
	root, err := os.OpenRoot(base)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	locations := map[releasepolicy.Arm]ArmLocation{}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		locations[arm] = ArmLocation{Root: root, EvalPath: string(arm) + "/eval.yaml", CWD: base,
			ContextDir: string(arm) + "/context", Executable: "evaluator"}
	}
	return base, locations
}

func TestPrepareRecheckActualTwoArms(t *testing.T) {
	base, locations := fixture(t)
	prepared, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	require.Len(t, prepared.sources, 2)
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		request, err := prepared.Request(arm, "task")
		require.NoError(t, err)
		require.Equal(t, "hello", request.Message)
		require.Equal(t, []byte(arm), request.Resources[0].Content)
		require.Equal(t, "inline.txt", request.Resources[1].Path)
		require.Equal(t, "instruction.md", request.Resources[2].Path)
		require.Equal(t, "exact instruction\n", string(request.Instructions[0].Content))
		require.True(t, request.NoSkills)
		require.True(t, request.SkipWorkspaceCapture)
		require.False(t, request.EphemeralSession)
		require.Equal(t, "deny_all", string(request.ToolPolicy.Mode))
		require.Nil(t, request.Tools)
		require.Nil(t, request.PermissionHandler)
		last := ""
		for _, source := range prepared.sources[arm] {
			require.Greater(t, source.Path, last)
			require.Equal(t, source.Digest, releasepolicy.SourceDigest(source.Bytes))
			last = source.Path
		}
		request.Resources[0].Content[0] = '!'
		request.Message = "caller mutation"
		detached, err := prepared.Request(arm, "task")
		require.NoError(t, err)
		require.Equal(t, "hello", detached.Message)
		require.Equal(t, []byte(arm), detached.Resources[0].Content)
	}
	// Modifying the caller map cannot retarget the opaque captured association.
	locations[releasepolicy.Baseline] = ArmLocation{}
	_, err = Recheck(context.Background(), prepared)
	require.NoError(t, err)
	write(t, base, "baseline/context/unused.txt", "not a native input")
	_, err = Recheck(context.Background(), prepared)
	require.NoError(t, err)
}

func TestRecheckDetectsOriginalMutations(t *testing.T) {
	cases := []struct{ name, path, data string }{
		{"eval_raw", "baseline/eval.yaml", snapshotEval + "\n# raw change\n"},
		{"task_raw", "baseline/tasks/task.yaml", snapshotTask + "\n# raw change\n"},
		{"prompt_file", "baseline/tasks/prompt.txt", "different"},
		{"fixture", "candidate/context/a.txt", "different"},
		{"instruction", "candidate/context/instruction.md", "different"},
		{"new_matching_task", "baseline/tasks/new.yaml", "id: new\ninputs: {prompt: hello}\n"},
		{"present_lock", "baseline/waza.lock", "new present lock bytes"},
		{"executable", "evaluator", "changed executable bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, locations := fixture(t)
			prepared, err := Prepare(context.Background(), locations)
			require.NoError(t, err)
			write(t, base, tc.path, tc.data)
			_, err = Recheck(context.Background(), prepared)
			require.Error(t, err)
		})
	}
}

func TestPrepareNativeOrderingAndDisabledCollision(t *testing.T) {
	t.Run("pattern_order", func(t *testing.T) {
		base, locations := fixture(t)
		write(t, base, "baseline/eval.yaml", strings.Replace(snapshotEval,
			`tasks: ["tasks/*.yaml"]`, `tasks: ["tasks/z.yaml", "tasks/task.yaml"]`, 1))
		write(t, base, "baseline/tasks/z.yaml", "id: z\ninputs: {prompt: hello}\n")
		prepared, err := Prepare(context.Background(), locations)
		require.NoError(t, err)
		var views map[releasepolicy.Arm]armSnapshot
		require.NoError(t, json.Unmarshal(prepared.canonical, &views))
		require.Equal(t, "z", views[releasepolicy.Baseline].Tasks[0].ID)
		require.Equal(t, "task", views[releasepolicy.Baseline].Tasks[1].ID)
	})
	for _, disabled := range []bool{false, true} {
		t.Run("collision", func(t *testing.T) {
			base, locations := fixture(t)
			other := "id: task\ninputs: {prompt: hello}\n"
			if disabled {
				other += "enabled: false\n"
			}
			write(t, base, "baseline/tasks/other.yaml", other)
			_, err := Prepare(context.Background(), locations)
			require.ErrorContains(t, err, "duplicate")
		})
	}
	t.Run("multiplicity", func(t *testing.T) {
		base, locations := fixture(t)
		write(t, base, "baseline/eval.yaml", strings.Replace(snapshotEval,
			`tasks: ["tasks/*.yaml"]`, `tasks: ["tasks/*.yaml", "tasks/task.yaml"]`, 1))
		_, err := Prepare(context.Background(), locations)
		require.ErrorContains(t, err, "duplicate")
	})
	t.Run("disabled_mutation", func(t *testing.T) {
		base, locations := fixture(t)
		write(t, base, "baseline/tasks/disabled.yaml", "id: disabled\nenabled: false\ninputs: {prompt: hello}\n")
		prepared, err := Prepare(context.Background(), locations)
		require.NoError(t, err)
		_, err = prepared.Request(releasepolicy.Baseline, "disabled")
		require.Error(t, err)
		write(t, base, "baseline/tasks/disabled.yaml", "id: disabled\nenabled: true\ninputs: {prompt: hello}\n")
		_, err = Recheck(context.Background(), prepared)
		require.Error(t, err)
	})
}

func TestPreparePathSemanticsAndNumericIdentity(t *testing.T) {
	base, locations := fixture(t)
	write(t, base, "baseline/tasks/task.yaml", `id: task
context_dir: task-context
inputs:
  prompt_file: prompt.txt
  context:
    fixture: eval-fixture
    large: 9007199254740993
  files: [{path: own.txt}]
instruction_files: [own.md]
`)
	write(t, base, "baseline/eval-fixture/z.txt", "eval-directory-relative")
	write(t, base, "baseline/eval-fixture/a.txt", "ordered-first")
	write(t, base, "task-context/own.txt", "task cwd-relative")
	write(t, base, "task-context/own.md", "task instruction")
	prepared, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	request, err := prepared.Request(releasepolicy.Baseline, "task")
	require.NoError(t, err)
	require.Equal(t, "hello", request.Message)
	require.Equal(t, "a.txt", request.Resources[0].Path)
	require.Equal(t, "z.txt", request.Resources[1].Path)
	require.Equal(t, "own.txt", request.Resources[2].Path)
	require.Equal(t, "task cwd-relative", string(request.Resources[2].Content))
	require.Equal(t, json.Number("9007199254740993"), request.Context["large"])
	require.True(t, bytes.Contains(prepared.canonical, []byte("9007199254740993")))
	write(t, base, "baseline/eval-fixture/new.txt", "actual newly consumed native fixture")
	_, err = Recheck(context.Background(), prepared)
	require.Error(t, err)
}

func TestPrepareUnsupportedInputs(t *testing.T) {
	cases := []struct{ name, eval, task string }{
		{"skills", strings.Replace(snapshotEval, `disabled_skills: ["*"]`, `skill_directories: ["skills"]`, 1), snapshotTask},
		{"parallel", snapshotEval + "config_extra: unknown\n", snapshotTask},
		{"unknown_task", snapshotEval, snapshotTask + "unknown: ignored-by-legacy\n"},
		{"unknown_text_config", strings.Replace(snapshotEval, `contains: ["hello"]`, "contains: [hello]\n      unknown: true", 1), snapshotTask},
		{"prompt_grader", strings.Replace(snapshotEval, "type: text", "type: prompt", 1), snapshotTask},
		{"expectation", snapshotEval, snapshotTask + "expected: {output_contains: [hello]}\n"},
		{"resource_traversal", snapshotEval, strings.Replace(snapshotTask, "path: a.txt", "path: ../a.txt", 1)},
		{"instruction_alias", snapshotEval, strings.Replace(snapshotTask, "instruction.md", "./instruction.md", 1)},
		{"missing_resource", snapshotEval, strings.Replace(snapshotTask, "a.txt", "missing.txt", 1)},
		{"yaml_alias", snapshotEval, "id: task\ninputs: &alias {prompt: hello}\n"},
		{"multiple_documents", snapshotEval + "---\nname: hidden\n", snapshotTask},
		{"float_weight", strings.Replace(snapshotEval, "type: text", "type: text\n    weight: 0.1", 1), snapshotTask},
		{"float_context", snapshotEval, strings.Replace(snapshotTask, "  prompt_file:", "  context: {decimal: 0.1234567890123456789}\n  prompt_file:", 1)},
		{"resource_alias_without_instructions", snapshotEval, "id: task\ninputs: {prompt: hello, files: [{path: ./missing.txt}]}\n"},
		{"fixture_alias_without_instructions", snapshotEval, "id: task\ninputs: {prompt: hello, context: {fixture: ./context/missing.txt}}\n"},
		{"native_skipped_resource", snapshotEval, "id: task\ninputs: {prompt: hello, files: [{path: input..txt}]}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, locations := fixture(t)
			write(t, base, "baseline/eval.yaml", tc.eval)
			write(t, base, "baseline/tasks/task.yaml", tc.task)
			_, err := Prepare(context.Background(), locations)
			require.Error(t, err)
			if strings.HasSuffix(tc.name, "_without_instructions") {
				require.ErrorContains(t, err, "noncanonical rooted path")
			}
		})
	}
	t.Run("symlink_file", func(t *testing.T) {
		base, locations := fixture(t)
		require.NoError(t, os.Remove(filepath.Join(base, "baseline/context/a.txt")))
		require.NoError(t, os.Symlink("instruction.md", filepath.Join(base, "baseline/context/a.txt")))
		_, err := Prepare(context.Background(), locations)
		require.Error(t, err)
	})
	t.Run("symlink_parent", func(t *testing.T) {
		base, locations := fixture(t)
		require.NoError(t, os.Rename(filepath.Join(base, "baseline/context"), filepath.Join(base, "baseline/real-context")))
		require.NoError(t, os.Symlink("real-context", filepath.Join(base, "baseline/context")))
		_, err := Prepare(context.Background(), locations)
		require.Error(t, err)
	})
	t.Run("hardlink_alias", func(t *testing.T) {
		base, locations := fixture(t)
		require.NoError(t, os.Remove(filepath.Join(base, "baseline/context/instruction.md")))
		require.NoError(t, os.Link(filepath.Join(base, "baseline/context/a.txt"), filepath.Join(base, "baseline/context/instruction.md")))
		_, err := Prepare(context.Background(), locations)
		require.ErrorContains(t, err, "physical source path alias")
	})
	t.Run("one_arm", func(t *testing.T) {
		_, locations := fixture(t)
		delete(locations, releasepolicy.Candidate)
		_, err := Prepare(context.Background(), locations)
		require.Error(t, err)
	})
	t.Run("cancel", func(t *testing.T) {
		_, locations := fixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Prepare(ctx, locations)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestPrepareUniqueSharedSourcesAndPresentLock(t *testing.T) {
	base, locations := fixture(t)
	write(t, base, "baseline/waza.lock", "inert present lock bytes")
	write(t, base, "baseline/tasks/other.yaml", strings.Replace(snapshotTask, "id: task", "id: other", 1))
	prepared, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	count := 0
	for _, source := range prepared.sources[releasepolicy.Baseline] {
		if source.Path == "baseline/context/instruction.md" {
			count++
			require.Contains(t, source.Roles, "instruction")
		}
	}
	require.Equal(t, 1, count, "two requests share one original source identity")
	_, err = Recheck(context.Background(), prepared)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(base, "baseline/waza.lock")))
	_, err = Recheck(context.Background(), prepared)
	require.Error(t, err)
}

func TestRecheckRootAssociationCannotRetarget(t *testing.T) {
	base, locations := fixture(t)
	prepared, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	require.NoError(t, os.Rename(base, base+"-moved"))
	t.Cleanup(func() { require.NoError(t, os.Rename(base+"-moved", base)) })
	require.NoError(t, os.Mkdir(base, 0700))
	t.Cleanup(func() { require.NoError(t, os.Remove(base)) })
	_, err = Recheck(context.Background(), prepared)
	require.Error(t, err)
}

func TestRecheckCWDPhysicalAssociationCannotRetarget(t *testing.T) {
	base, locations := fixture(t)
	require.NoError(t, os.Mkdir(filepath.Join(base, "cwd"), 0700))
	location := locations[releasepolicy.Baseline]
	location.CWD = filepath.Join(base, "cwd")
	location.ContextDir = filepath.Join(base, "baseline/context")
	locations[releasepolicy.Baseline] = location
	prepared, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	require.NoError(t, os.Rename(filepath.Join(base, "cwd"), filepath.Join(base, "old-cwd")))
	require.NoError(t, os.Mkdir(filepath.Join(base, "cwd"), 0700))
	_, err = Recheck(context.Background(), prepared)
	require.ErrorContains(t, err, "association changed")
}

func TestPrepareNativeScenarioNoSkillsDefaults(t *testing.T) {
	base, locations := fixture(t)
	eval := strings.Replace(snapshotEval, `schemaVersion: "1.0"`, `schemaVersion: "2.0"`, 1)
	eval = strings.Replace(eval, "name: offline-native", "name: offline-native\nscenario: independent", 1)
	eval = strings.Replace(eval, "  disabled_skills: [\"*\"]\n", "", 1)
	write(t, base, "baseline/eval.yaml", eval)
	prepared, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	request, err := prepared.Request(releasepolicy.Baseline, "task")
	require.NoError(t, err)
	require.True(t, request.NoSkills, "actual scenario declaration disables ambient skill discovery")
}

func TestPrepareNativeDefaultFixtureDirectory(t *testing.T) {
	base, locations := fixture(t)
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		location := locations[arm]
		location.ContextDir = ""
		locations[arm] = location
		write(t, base, string(arm)+"/fixtures/a.txt", string(arm)+" default fixture")
		write(t, base, string(arm)+"/fixtures/instruction.md", "default instruction")
	}
	write(t, base, "fixtures/a.txt", "wrong cwd-relative default")
	write(t, base, "fixtures/instruction.md", "wrong cwd-relative instruction")
	prepared, err := Prepare(context.Background(), locations)
	require.NoError(t, err)
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		request, err := prepared.Request(arm, "task")
		require.NoError(t, err)
		require.Equal(t, string(arm)+" default fixture", string(request.Resources[0].Content))
		require.Equal(t, "default instruction", string(request.Instructions[0].Content))
		for _, source := range prepared.sources[arm] {
			require.NotContains(t, source.Path, "/context/")
		}
	}
	_, err = Recheck(context.Background(), prepared)
	require.NoError(t, err)
	write(t, base, "baseline/fixtures/a.txt", "actual default fixture mutation")
	_, err = Recheck(context.Background(), prepared)
	require.Error(t, err)
}
