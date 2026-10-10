package controlledcomparison

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func encoded(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

func fullClientBytes(t *testing.T, request *execution.ExecutionRequest) []byte {
	t.Helper()
	type plain execution.ExecutionRequest
	return encoded(t, struct {
		*plain
		PermissionHandler any
		Tools             any
	}{plain: (*plain)(request)})
}

// The reference calls the actual pre-extraction planner, independently copied
// before editing source.go. No projector supplies any expected value.
func TestOriginalMockSourceProjectionParity(t *testing.T) {
	for _, mode := range []string{"default", "omitted_attempts", "first_event_default", "ordered_inventory", "zero_overrides",
		"task_context", "task_context_relative", "cli_context_relative", "prompt_file", "lock_present", "empty_lock", "no_golden"} {
		t.Run(mode, func(t *testing.T) {
			source := fixture(t, "mock-label")
			dir := filepath.Dir(source.EvalPath)
			eval, err := os.ReadFile(source.EvalPath)
			require.NoError(t, err)
			eval = bytes.ReplaceAll(eval, []byte("  max_attempts: 2"), []byte("  max_attempts: 2\n  first_event_timeout_seconds: 5\n  judge_model: judge-label"))
			require.NoError(t, os.WriteFile(source.EvalPath, eval, 0600))
			task, err := os.ReadFile(filepath.Join(dir, "task.yaml"))
			require.NoError(t, err)
			task = bytes.ReplaceAll(task, []byte("    fixture: fixtures"), []byte("    fixture: fixtures\n    large: 9007199254740993\n    radix: 0xFFFFFFFFFFFFFFFF\n    nested: {values: [-1, 0, 9007199254740993], absent: null}\n    date: 2026-10-10\n    quoted: '9007199254740993'\n    legacy_float: 0.5"))
			switch mode {
			case "omitted_attempts":
				eval = bytes.ReplaceAll(eval, []byte("  max_attempts: 2\n"), nil)
				require.NoError(t, os.WriteFile(source.EvalPath, eval, 0600))
			case "first_event_default":
				eval = bytes.ReplaceAll(eval, []byte("  first_event_timeout_seconds: 5\n"), nil)
				require.NoError(t, os.WriteFile(source.EvalPath, eval, 0600))
			case "ordered_inventory":
				eval = bytes.ReplaceAll(eval, []byte("tasks: [task.yaml]"), []byte("tasks: [task.yaml, a-task.yaml, disabled.yaml]"))
				eval = bytes.ReplaceAll(eval, []byte("  max_attempts: 2"), []byte("  instruction_files: [z.md, a.md, z.md]\n  max_attempts: 2"))
				require.NoError(t, os.WriteFile(source.EvalPath, eval, 0600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "a-task.yaml"),
					bytes.ReplaceAll(task, []byte("task-0"), []byte("a-task")), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "disabled.yaml"), []byte("id: disabled\nname: disabled\nenabled: false\ngolden: true\ninputs:\n  prompt: ignored\n"), 0600))
				for _, name := range []string{"a.md", "z.md", "fixture.txt"} {
					require.NoError(t, os.WriteFile(filepath.Join(dir, "fixtures", name), []byte(name), 0600))
				}
				task = append(task, []byte("instruction_files: [a.md]\n")...)
			case "zero_overrides":
				task = append(task, []byte("timeout_seconds: 12\nfirst_event_timeout_seconds: 0\n")...)
			case "task_context":
				task = append(task, []byte("context_dir: "+filepath.Join(dir, "fixtures")+"\n")...)
			case "task_context_relative":
				t.Chdir(dir)
				task = append(task, []byte("context_dir: fixtures\n")...)
			case "cli_context_relative":
				t.Chdir(dir)
				source.ContextDir = "fixtures"
			case "prompt_file":
				task = bytes.ReplaceAll(task, []byte("  prompt: hello"), []byte("  prompt_file: prompt.txt"))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("hello"), 0600))
			case "lock_present":
				require.NoError(t, os.WriteFile(filepath.Join(dir, models.LockfileName), []byte("{\"version\":1}"), 0600))
			case "empty_lock":
				require.NoError(t, os.WriteFile(filepath.Join(dir, models.LockfileName), nil, 0600))
			case "no_golden":
				task = bytes.ReplaceAll(task, []byte("golden: true"), []byte("golden: false"))
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), task, 0600))
			before, err := originalPrepare(originalSource(source))
			require.NoError(t, err)
			after, err := prepare(source)
			require.NoError(t, err)
			require.Equal(t, encoded(t, before.plan), encoded(t, after.plan), "full ordered arm, domains, settings, runtime and digest")
			require.Equal(t, encoded(t, before.golden), encoded(t, after.golden))
			require.Equal(t, encoded(t, before.tasks), encoded(t, after.tasks), "actual inspector context and native task representation")
			require.Equal(t, encoded(t, before.cfg.Spec()), encoded(t, after.cfg.Spec()))
			require.Len(t, after.inputs, len(before.inputs))
			for id, input := range before.inputs {
				left, err := input.View()
				require.NoError(t, err)
				right, err := after.inputs[id].View()
				require.NoError(t, err)
				require.Equal(t, left, right, "every exported actual frozen CLIENT field")
				require.Equal(t, fullClientBytes(t, left), fullClientBytes(t, right), "complete CLIENT JSON bytes, not a narrower intent")
			}
		})
	}
}

func TestOriginalMockPolicyGoldenUnionParity(t *testing.T) {
	for _, golden := range []bool{true, false} {
		t.Run(map[bool]string{true: "golden", false: "empty"}[golden], func(t *testing.T) {
			baseline, candidate := fixture(t, "before"), fixture(t, "after")
			for _, source := range []Source{baseline, candidate} {
				path := filepath.Join(filepath.Dir(source.EvalPath), "task.yaml")
				raw, err := os.ReadFile(path)
				require.NoError(t, err)
				if !golden {
					raw = bytes.ReplaceAll(raw, []byte("golden: true"), []byte("golden: false"))
					require.NoError(t, os.WriteFile(path, raw, 0600))
				}
			}
			before, err := originalPlan(originalSource(baseline), originalSource(candidate), design(), requirements(), []string{"model"})
			require.NoError(t, err)
			after, err := Plan(baseline, candidate, design(), requirements(), []string{"model"})
			require.NoError(t, err)
			left, err := releasepolicy.DecodePolicy(before)
			require.NoError(t, err)
			right, err := releasepolicy.DecodePolicy(after)
			require.NoError(t, err)
			// Only the independently random allocation and its enclosing policy
			// digest differ. All other complete policy bytes must remain equal.
			left.Design.Allocation = right.Design.Allocation
			left.Digest = right.Digest
			require.Equal(t, encoded(t, left), encoded(t, right))
			require.Equal(t, encoded(t, left.GoldenIDs), encoded(t, right.GoldenIDs))
			require.NotNil(t, right.GoldenIDs)
		})
	}
}

func TestOriginalMockGuardAndLockErrorParity(t *testing.T) {
	for _, mode := range []string{"engine", "generated", "missing_resource", "lock_directory", "duplicate_task", "disabled_only", "zero_task_timeout"} {
		t.Run(mode, func(t *testing.T) {
			source := fixture(t, "label")
			dir := filepath.Dir(source.EvalPath)
			eval, err := os.ReadFile(source.EvalPath)
			require.NoError(t, err)
			task, err := os.ReadFile(filepath.Join(dir, "task.yaml"))
			require.NoError(t, err)
			switch mode {
			case "engine":
				eval = bytes.ReplaceAll(eval, []byte("executor: mock"), []byte("executor: copilot-sdk"))
			case "generated":
				eval = append(eval, []byte("tasks_from: missing.json\n")...)
			case "missing_resource":
				task = bytes.ReplaceAll(task, []byte("  prompt: hello"), []byte("  prompt: hello\n  files: [missing.txt]"))
			case "lock_directory":
				require.NoError(t, os.Mkdir(filepath.Join(dir, models.LockfileName), 0700))
			case "duplicate_task":
				eval = bytes.ReplaceAll(eval, []byte("tasks: [task.yaml]"), []byte("tasks: [task.yaml, duplicate.yaml]"))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "duplicate.yaml"), task, 0600))
			case "disabled_only":
				task = append(task, []byte("enabled: false\n")...)
			case "zero_task_timeout":
				task = append(task, []byte("timeout_seconds: 0\n")...)
			}
			require.NoError(t, os.WriteFile(source.EvalPath, eval, 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), task, 0600))
			_, before := originalPrepare(originalSource(source))
			_, after := prepare(source)
			require.Error(t, before)
			require.EqualError(t, after, before.Error())
			require.False(t, strings.Contains(after.Error(), "native source limit"), "legacy mock must not acquire native mode limits")
		})
	}
}
