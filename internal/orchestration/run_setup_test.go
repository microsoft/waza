package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// TestRunHookHelperProcess is not a test on its own: hook commands re-invoke
// the test binary with this test selected and an action after "--".
func TestRunHookHelperProcess(t *testing.T) {
	args := hookHelperArgs()
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "append":
		f, err := os.OpenFile(args[1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString(args[2] + "\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())
	case "copy":
		data, err := os.ReadFile(args[1])
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(args[2], data, 0o600))
	case "copy-pass":
		log, err := os.ReadFile(args[4])
		if !os.IsNotExist(err) {
			require.NoError(t, err)
		}
		source := args[1]
		if strings.Contains(string(log), "before_run\n") {
			source = args[2]
		}
		data, err := os.ReadFile(source)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(args[3], data, 0o600))
	default:
		t.Fatalf("unknown hook helper action %q", args[0])
	}
}

func hookHelperArgs() []string {
	for i, arg := range os.Args {
		if arg == "--" {
			return os.Args[i+1:]
		}
	}
	return nil
}

// hookCommand returns a hook command that runs a helper action. Hook commands
// are split on whitespace, so paths containing it cannot be passed.
func hookCommand(t *testing.T, action string, args ...string) string {
	t.Helper()
	parts := append([]string{os.Args[0], "-test.run=^TestRunHookHelperProcess$", "--", action}, args...)
	for _, part := range parts {
		if strings.ContainsAny(part, " \t") {
			t.Skipf("hook command argument %q contains whitespace", part)
		}
	}
	return strings.Join(parts, " ")
}

// writeHookSuite writes an eval whose before_run hook generates the only task
// file from a staged copy outside the task glob. The hooks log to log.txt.
func writeHookSuite(t *testing.T, mcpMocks, task string) (*EvalRunner, *admissionEngine, string) {
	t.Helper()
	dir := t.TempDir()
	staged := filepath.Join(dir, "staged-task.yaml")
	require.NoError(t, os.WriteFile(staged, []byte(task), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tasks"), 0o755))
	log := filepath.Join(dir, "log.txt")
	eval := `schemaVersion: "1.1"
name: hook-generated
skill: example
config:
  executor: copilot-sdk
  model: test-model
  trials_per_task: 1
  timeout_seconds: 60
tasks:
  - tasks/*.yaml
hooks:
  before_run:
    - command: '` + hookCommand(t, "copy", staged, filepath.Join(dir, "tasks", "generated.yaml")) + `'
      error_on_fail: true
    - command: '` + hookCommand(t, "append", log, "before_run") + `'
      error_on_fail: true
  after_run:
    - command: '` + hookCommand(t, "append", log, "after_run") + `'
      error_on_fail: true
` + mcpMocks
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(eval), 0o600))
	spec, err := models.LoadEvalSpec(evalPath)
	require.NoError(t, err)
	engine := &admissionEngine{t: t, answers: map[string]string{}, servers: map[string][]string{}}
	return NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir)), engine, WithSkipGraders()), engine, log
}

func requireHookLog(t *testing.T, log string) {
	t.Helper()
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "before_run\nafter_run\n", strings.ReplaceAll(string(data), "\r\n", "\n"), "setup and teardown each run exactly once")
}

const hookHostMock = `mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_schema: {}
            return: {answer: Not now}
`

type setupMutationEngine struct {
	*admissionEngine
	task        string
	original    string
	replacement string
	executes    int
}

func (e *setupMutationEngine) Initialize(ctx context.Context) error {
	data, err := os.ReadFile(e.task)
	if err != nil {
		return err
	}
	require.Equal(e.t, e.original, string(data), "before_run must generate the admitted task before initialization")
	if err := os.WriteFile(e.task, []byte(e.replacement), 0o600); err != nil {
		return err
	}
	return e.admissionEngine.Initialize(ctx)
}

func (e *setupMutationEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.executes++
	return e.admissionEngine.Execute(ctx, req)
}

func TestRunBenchmark_ReadmitsHookGeneratedTaskAfterInitialization(t *testing.T) {
	for _, tt := range []struct {
		name        string
		replacement string
		wantErr     string
		wantAnswer  string
	}{
		{
			name:        "empty task mocks",
			replacement: admissionInheritTask + "mcp_mocks: []\n",
			wantErr:     "mcp_mocks must not be empty",
		},
		{
			name:        "missing task fixtures",
			replacement: admissionInheritTask + "mcp_mocks:\n  - name: host\n    fixtures: missing-startup-fixtures\n",
			wantErr:     `MCP mock admission: task "inherit": resolve MCP mock "host"`,
		},
		{name: "unchanged valid", replacement: admissionInheritTask, wantAnswer: "Not now"},
		{name: "valid replacement", replacement: admissionReplaceTask("inherit"), wantAnswer: "Publish this version"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner, engine, log := writeHookSuite(t, hookHostMock, admissionInheritTask)
			task := filepath.Join(filepath.Dir(log), "tasks", "generated.yaml")
			mutator := &setupMutationEngine{
				admissionEngine: engine,
				task:            task,
				original:        admissionInheritTask,
				replacement:     tt.replacement,
			}
			runner.engine = mutator
			outcome, err := runner.RunBenchmark(t.Context())
			require.Equal(t, 1, engine.initialized)
			data, readErr := os.ReadFile(task)
			require.NoError(t, readErr)
			require.Equal(t, tt.replacement, string(data))
			if tt.name != "unchanged valid" {
				require.NotEqual(t, admissionInheritTask, string(data))
			}
			requireHookLog(t, log)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				if tt.name == "missing task fixtures" {
					require.ErrorContains(t, err, "missing-startup-fixtures")
				}
				require.Nil(t, outcome)
				require.Zero(t, mutator.executes)
				require.Empty(t, engine.answers, "invalid initialization changes must not reach Execute")
				return
			}
			require.NoError(t, err)
			require.Len(t, outcome.TestOutcomes, 1)
			require.Equal(t, 1, mutator.executes)
			require.Equal(t, models.StatusSkipped, outcome.TestOutcomes[0].Status)
			require.Len(t, outcome.TestOutcomes[0].Runs, 1)
			require.Equal(t, models.StatusSkipped, outcome.TestOutcomes[0].Runs[0].Status)
			require.Equal(t, map[string]string{"inherit": tt.wantAnswer}, engine.answers)
		})
	}
}

func TestRunBenchmark_BeforeRunGeneratesTaskBeforeLoading(t *testing.T) {
	runner, engine, log := writeHookSuite(t, hookHostMock, admissionInheritTask)
	outcome, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err, "the task file only exists after before_run")
	require.Len(t, outcome.TestOutcomes, 1)
	require.Equal(t, "Not now", engine.answers["inherit"])
	require.Equal(t, 1, engine.initialized)
	requireHookLog(t, log)
}

func TestRunBenchmark_AdmissionSeesHookGeneratedTaskOverride(t *testing.T) {
	runner, engine, log := writeHookSuite(t, `mcp_mocks:
  - name: host
    fixtures: missing-host-fixtures
`, admissionReplaceTask("generated"))
	outcome, err := runner.RunBenchmark(t.Context())
	require.NoError(t, err, "the generated task replaces host, so its missing fixtures are unused")
	require.Len(t, outcome.TestOutcomes, 1)
	require.Equal(t, "Publish this version", engine.answers["generated"])
	requireHookLog(t, log)
}

func TestRunBenchmark_AdmissionFailureAfterSetupStillTearsDown(t *testing.T) {
	runner, engine, log := writeHookSuite(t, `mcp_mocks:
  - name: host
    fixtures: missing-host-fixtures
`, admissionInheritTask)
	outcome, err := runner.RunBenchmark(t.Context())
	require.ErrorContains(t, err, "MCP mock admission")
	require.ErrorContains(t, err, `resolve MCP mock "host"`)
	require.Nil(t, outcome)
	require.Zero(t, engine.initialized, "admission still fails before engine initialization")
	requireHookLog(t, log)
}

// A failed pass tears down its setup; retrying RunBenchmark on the same runner
// must run before_run again, so inputs removed by after_run are regenerated.
func TestRunBenchmark_RetryAfterFailedStartRunsSetupAgain(t *testing.T) {
	t.Run("admission failure", func(t *testing.T) {
		runner, engine, log := writeHookSuite(t, `mcp_mocks:
  - name: host
    fixtures: mcp-host
`, admissionInheritTask)
		_, err := runner.RunBenchmark(t.Context())
		require.ErrorContains(t, err, "MCP mock admission")
		require.Zero(t, engine.initialized)

		fixtures := filepath.Join(filepath.Dir(log), "mcp-host")
		require.NoError(t, os.MkdirAll(fixtures, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(fixtures, "ask_user.json"), []byte(`{"responses":[{"return":{"answer":"Not now"}}]}`), 0o600))
		outcome, err := runner.RunBenchmark(t.Context())
		require.NoError(t, err)
		require.Len(t, outcome.TestOutcomes, 1)
		requireHookLogLines(t, log, "before_run", "after_run", "before_run", "after_run")
	})
	t.Run("engine initialization failure", func(t *testing.T) {
		runner, engine, log := writeHookSuite(t, hookHostMock, admissionInheritTask)
		engine.failInits = 1
		_, err := runner.RunBenchmark(t.Context())
		require.ErrorContains(t, err, "engine unavailable")
		outcome, err := runner.RunBenchmark(t.Context())
		require.NoError(t, err)
		require.Len(t, outcome.TestOutcomes, 1)
		require.Equal(t, 2, engine.initialized)
		requireHookLogLines(t, log, "before_run", "after_run", "before_run", "after_run")
	})
}

func requireHookLogLines(t *testing.T, log string, want ...string) {
	t.Helper()
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, strings.Join(want, "\n")+"\n", strings.ReplaceAll(string(data), "\r\n", "\n"))
}

type baselineSetupEngine struct {
	*admissionEngine
	log      string
	prompts  []string
	answers  []string
	baseline []bool
}

func (e *baselineSetupEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.prompts = append(e.prompts, req.Message)
	e.baseline = append(e.baseline, len(req.SkillPaths) == 0)
	if len(e.prompts) == 1 {
		requireHookLogLines(e.t, e.log, "before_run")
	} else {
		requireHookLogLines(e.t, e.log, "before_run", "after_run", "before_run")
	}
	result, err := e.admissionEngine.Execute(ctx, req)
	e.answers = append(e.answers, e.admissionEngine.answers[req.TaskName])
	return result, err
}

func TestRunBenchmark_ReadmitsBaselineSetup(t *testing.T) {
	const fixtureTask = admissionInheritTask + "mcp_mocks:\n  - name: host\n    fixtures: generated-mocks\n"
	const originalFixture = `{"responses":[{"return":{"answer":"Not now"}}]}`
	const invalidFixture = `{"responses":[{"sequence":[]}]}`
	validReplacement := strings.Replace(admissionReplaceTask("inherit"), "Publish the skill.", "Inspect the changed baseline.", 1)
	for _, tt := range []struct {
		name        string
		firstTask   string
		secondTask  string
		fixture     string
		wantErr     string
		wantPrompts []string
		wantAnswers []string
	}{
		{
			name: "missing fixture", firstTask: admissionInheritTask,
			secondTask: admissionInheritTask + "mcp_mocks:\n  - name: host\n    fixtures: missing-baseline-fixtures\n",
			wantErr:    `MCP mock admission: task "inherit": resolve MCP mock "host"`,
		},
		{
			name: "empty fault steps", firstTask: fixtureTask, secondTask: fixtureTask,
			fixture: invalidFixture, wantErr: "fault sequence must contain at least one step",
		},
		{
			name: "unchanged valid", firstTask: admissionInheritTask, secondTask: admissionInheritTask,
			wantPrompts: []string{"Publish the skill.", "Publish the skill."},
			wantAnswers: []string{"Not now", "Not now"},
		},
		{
			name: "changed valid", firstTask: admissionInheritTask, secondTask: validReplacement,
			wantPrompts: []string{"Publish the skill.", "Inspect the changed baseline."},
			wantAnswers: []string{"Not now", "Publish this version"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner, engine, log := writeHookSuite(t, hookHostMock, tt.firstTask)
			dir := filepath.Dir(log)
			task := filepath.Join(dir, "tasks", "generated.yaml")
			require.NoFileExists(t, task, "before_run must generate the first task")
			second := filepath.Join(dir, "second-task.yaml")
			require.NoError(t, os.WriteFile(second, []byte(tt.secondTask), 0o600))
			spec := runner.cfg.Spec()
			spec.Baseline = true
			spec.Config.SkillPaths = []string{dir}
			spec.Hooks.BeforeRun[0].Command = hookCommand(t, "copy-pass", filepath.Join(dir, "staged-task.yaml"), second, task, log)
			var fixture string
			if tt.fixture != "" {
				spec.SchemaVersion = models.ScenarioSchemaVersion
				spec.Scenario = "recovery"
				mocks := filepath.Join(dir, "generated-mocks")
				require.NoError(t, os.Mkdir(mocks, 0o755))
				fixture = filepath.Join(mocks, "ask_user.json")
				first := filepath.Join(dir, "first-fixture.json")
				second := filepath.Join(dir, "second-fixture.json")
				require.NoError(t, os.WriteFile(first, []byte(originalFixture), 0o600))
				require.NoError(t, os.WriteFile(second, []byte(tt.fixture), 0o600))
				copyFixture := spec.Hooks.BeforeRun[0]
				copyFixture.Command = hookCommand(t, "copy-pass", first, second, fixture, log)
				spec.Hooks.BeforeRun = slices.Insert(spec.Hooks.BeforeRun, 1, copyFixture)
				require.NoFileExists(t, fixture, "each before_run generates the fixture")
			}
			recorder := &baselineSetupEngine{admissionEngine: engine, log: log}
			runner.engine = recorder
			outcome, err := runner.RunBenchmark(t.Context())
			require.Equal(t, 1, engine.initialized)
			requireHookLogLines(t, log, "before_run", "after_run", "before_run", "after_run")
			data, readErr := os.ReadFile(task)
			require.NoError(t, readErr)
			require.Equal(t, tt.secondTask, string(data), "second before_run must replace the generated task")
			if fixture != "" {
				data, readErr = os.ReadFile(fixture)
				require.NoError(t, readErr)
				require.Equal(t, tt.fixture, string(data))
				require.NotEqual(t, originalFixture, string(data))
			}
			require.Equal(t, []string{dir}, spec.Config.SkillPaths, "baseline must restore skills even on admission failure")
			require.Nil(t, runner.setup, "no completed setup may remain pending")
			if tt.wantErr != "" {
				require.Equal(t, []string{"Publish the skill."}, recorder.prompts, "only the first pass may execute")
				require.Equal(t, []string{"Not now"}, recorder.answers)
				require.Equal(t, []bool{false}, recorder.baseline, "baseline must never reach Execute")
				require.ErrorContains(t, err, "baseline run (skills disabled) failed:")
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, outcome)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, outcome)
			require.Len(t, outcome.TestOutcomes, 1)
			require.Equal(t, tt.wantPrompts, recorder.prompts)
			require.Equal(t, tt.wantAnswers, recorder.answers)
			require.Equal(t, []bool{false, true}, recorder.baseline)
		})
	}
}
