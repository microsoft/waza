package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/responder"
	"github.com/stretchr/testify/require"
)

// restartingMCPEngine starts a new mock server for each turn, as the native
// runtime does after resuming a disconnected session.
type restartingMCPEngine struct {
	t            *testing.T
	mu           sync.Mutex
	configs      []string
	results      []string
	afterExecute func(string)
	executeErr   error
	response     *execution.ExecutionResponse
}

func (e *restartingMCPEngine) Initialize(context.Context) error       { return nil }
func (e *restartingMCPEngine) Shutdown(context.Context) error         { return nil }
func (e *restartingMCPEngine) SessionUsage(string) *models.UsageStats { return nil }

func (e *restartingMCPEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	stdio, ok := req.MCPServers["counter"].(copilot.MCPStdioServerConfig)
	require.True(e.t, ok)
	require.Len(e.t, stdio.Args, 3)
	require.Equal(e.t, "--attempt-config-file", stdio.Args[1])
	data, err := os.ReadFile(stdio.Args[2])
	require.NoError(e.t, err)
	var cfg mcpmock.Config
	require.NoError(e.t, json.Unmarshal(data, &cfg))
	params, err := json.Marshal(map[string]any{"name": "read", "arguments": map[string]any{"id": "item"}})
	require.NoError(e.t, err)
	resp := mcpmock.NewServer(&cfg, nil).HandleRequest(ctx, &jsonrpc.Request{Method: "tools/call", Params: params, ID: json.RawMessage(`1`)})
	require.Nil(e.t, resp.Error)
	result, err := json.Marshal(resp.Result)
	require.NoError(e.t, err)
	e.mu.Lock()
	e.configs = append(e.configs, stdio.Args[2])
	e.results = append(e.results, string(result))
	e.mu.Unlock()
	if e.afterExecute != nil {
		e.afterExecute(stdio.Args[2])
	}
	e.response = &execution.ExecutionResponse{FinalOutput: string(result), SessionID: "session", WorkspaceDir: "workspace", Success: true}
	return e.response, e.executeErr
}

func newMCPLifetimeRunner(t *testing.T) (*EvalRunner, *restartingMCPEngine, string) {
	t.Helper()
	tmp := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, tmp)
	}
	spec := &models.EvalSpec{
		SchemaVersion: models.ScenarioSchemaVersion,
		SpecIdentity:  models.SpecIdentity{Name: "mcp-lifetime"},
		Config:        models.Config{TrialsPerTask: 1, TimeoutSec: 30},
		MCPMocks: []models.MCPMockConfig{{Name: "counter", Tools: map[string]models.MCPMockTool{"read": {Responses: []models.MCPMockResponse{
			{Match: map[string]any{"id": "item"}, Sequence: []models.MCPMockStep{
				{Return: map[string]any{"ordinal": 1}}, {Return: map[string]any{"ordinal": 2}},
			}},
			{Match: map[string]any{"id": "item"}, Return: map[string]any{"fallback": true}},
		}}}}},
	}
	engine := &restartingMCPEngine{t: t}
	return NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(tmp)), engine, WithSkipGraders()), engine, tmp
}

func requireMCPLifetimeResults(t *testing.T, engine *restartingMCPEngine, tmp string) {
	t.Helper()
	require.Len(t, engine.results, 6)
	for attempt := range 2 {
		results := engine.results[attempt*3 : attempt*3+3]
		require.Contains(t, results[0], `{\"ordinal\":1}`)
		require.Contains(t, results[1], `{\"ordinal\":2}`)
		require.Contains(t, results[2], "response sequence exhausted")
		require.NotContains(t, results[2], "fallback", "an exhausted first match must not fall through")
		configs := engine.configs[attempt*3 : attempt*3+3]
		require.Equal(t, configs[0], configs[1])
		require.Equal(t, configs[0], configs[2])
		require.NoFileExists(t, configs[0], "the attempt removes its retained configuration")
	}
	require.NotEqual(t, engine.configs[0], engine.configs[3], "each attempt owns separate state")
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	for _, entry := range entries {
		require.False(t, strings.HasPrefix(entry.Name(), "waza-mcp-"), "leftover MCP mock state %s", filepath.Join(tmp, entry.Name()))
	}
}

func TestExecuteRunMCPMockSequenceSpansFollowUpsAndResetsPerAttempt(t *testing.T) {
	runner, engine, tmp := newMCPLifetimeRunner(t)
	tc := &models.TestCase{TestID: "follow-up", Stimulus: models.TaskStimulus{
		Message: "read", FollowUps: []string{"read again", "read a third time"},
	}}

	for attempt := 1; attempt <= 2; attempt++ {
		run := runner.executeRun(context.Background(), tc, attempt)
		require.Equal(t, models.StatusSkipped, run.Status, run.ErrorMsg)
	}
	requireMCPLifetimeResults(t, engine, tmp)
}

func TestExecuteRunMCPMockSequenceSpansResponderTurnsAndResetsPerAttempt(t *testing.T) {
	runner, engine, tmp := newMCPLifetimeRunner(t)
	runner.newClassifier = func(models.ResponderConfig, string) responderClassifier {
		return &scriptedClassifier{decisions: []responder.Decision{
			{Kind: responder.DecisionReply, Answer: "read again"},
			{Kind: responder.DecisionReply, Answer: "read a third time"},
			{Kind: responder.DecisionStop},
		}}
	}
	tc := &models.TestCase{TestID: "responder", Stimulus: models.TaskStimulus{
		Message: "read", Responder: &models.ResponderConfig{Instructions: "ask twice", MaxFollowups: 3},
	}}

	for attempt := 1; attempt <= 2; attempt++ {
		run := runner.executeRun(context.Background(), tc, attempt)
		require.Equal(t, models.StatusSkipped, run.Status, run.ErrorMsg)
		require.Equal(t, models.ResponderOutcomeStopped, run.Responder.Outcome)
	}
	requireMCPLifetimeResults(t, engine, tmp)
}

func TestExecuteRunMCPMockSetupFailureRemovesAttemptState(t *testing.T) {
	runner, engine, tmp := newMCPLifetimeRunner(t)
	runner.cfg.Spec().MCPMocks = append(runner.cfg.Spec().MCPMocks, models.MCPMockConfig{Name: "broken",
		Tools: map[string]models.MCPMockTool{"read": {Responses: []models.MCPMockResponse{{Return: make(chan int)}}}}})

	run := runner.executeRun(context.Background(), &models.TestCase{TestID: "setup", Stimulus: models.TaskStimulus{Message: "read"}}, 1)
	require.Equal(t, models.StatusError, run.Status)
	require.Contains(t, run.ErrorMsg, `configure MCP mock "broken"`)
	require.Empty(t, engine.results)
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestExecuteRunMCPMockCleanupPreservesReplacementAndFailsRun(t *testing.T) {
	for _, executionFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful-execution", true: "execution-error"}[executionFails], func(t *testing.T) {
			runner, engine, _ := newMCPLifetimeRunner(t)
			runner.skipGraders = false
			if executionFails {
				engine.executeErr = errors.New("original execution failure")
			}
			var sentinel, retainedConfig, attemptDir, originalConfig string
			renameBlocked := false
			engine.afterExecute = func(configFile string) {
				originalConfig = configFile
				attemptDir = filepath.Dir(filepath.Dir(configFile))
				moved := attemptDir + "-moved"
				relative, err := filepath.Rel(attemptDir, configFile)
				require.NoError(t, err)
				renameErr := os.Rename(attemptDir, moved)
				if runtime.GOOS == "windows" && renameErr != nil {
					require.ErrorIs(t, renameErr, syscall.Errno(32), "only a Windows sharing violation is an expected retained-root pin")
					renameBlocked = true
					return
				}
				require.NoError(t, renameErr)
				require.NoError(t, os.Mkdir(attemptDir, 0700))
				sentinel = filepath.Join(attemptDir, "unrelated")
				require.NoError(t, os.WriteFile(sentinel, []byte("preserve replacement"), 0600))
				retainedConfig = filepath.Join(moved, relative)
			}
			run := runner.executeRun(context.Background(), &models.TestCase{TestID: "cleanup", Stimulus: models.TaskStimulus{Message: "read"}}, 1)

			require.Len(t, engine.results, 1, "the regression must reach actual attempt execution")
			require.Contains(t, engine.results[0], `{\"ordinal\":1}`)
			if renameBlocked {
				require.NoFileExists(t, originalConfig, "successful cleanup must remove the retained configuration")
				require.NoError(t, os.Mkdir(attemptDir, 0700))
				require.NoError(t, os.Rename(attemptDir, attemptDir+"-moved"), "production cleanup must close the owning root and release the Windows pin")
				if executionFails {
					require.Equal(t, models.StatusError, run.Status)
					require.Contains(t, run.ErrorMsg, "original execution failure")
				} else {
					require.NotEqual(t, models.StatusError, run.Status, run.ErrorMsg)
					require.True(t, engine.response.Success)
				}
				return
			}
			body, err := os.ReadFile(sentinel)
			require.NoError(t, err, "attempt cleanup must not recursively remove the replacement directory")
			require.Equal(t, "preserve replacement", string(body))
			require.FileExists(t, retainedConfig, "refused cleanup must preserve the renamed original configuration")
			require.Equal(t, models.StatusError, run.Status)
			require.False(t, engine.response.Success, "cleanup failure must invalidate otherwise successful execution")
			require.Contains(t, run.ErrorMsg, "clean up MCP mock attempt state")
			require.Contains(t, run.ErrorMsg, "identity changed")
			require.Contains(t, run.ErrorMsg, attemptDir)
			require.Contains(t, run.ErrorMsg, "if renamed, locate it before manual cleanup")
			if executionFails {
				require.Contains(t, run.ErrorMsg, "original execution failure")
			}
			require.Equal(t, models.StatusFailed, overallStatus([]models.RunResult{run}))
			data, err := json.Marshal(run)
			require.NoError(t, err)
			var saved models.RunResult
			require.NoError(t, json.Unmarshal(data, &saved))
			require.Equal(t, models.StatusError, saved.Status)
			require.Equal(t, run.ErrorMsg, saved.ErrorMsg)
		})
	}
}

func TestRunTestMCPMockSequenceResetsPerTaskTrialAndRetry(t *testing.T) {
	runner, engine, _ := newMCPLifetimeRunner(t)
	runner.skipGraders = false
	runner.cfg.Spec().Config.TrialsPerTask = 2
	runner.cfg.Spec().Config.MaxAttempts = 2
	for _, taskID := range []string{"first-task", "second-task"} {
		outcome, cached := runner.runTest(context.Background(), &models.TestCase{
			TestID: taskID,
			Stimulus: models.TaskStimulus{
				Message: "read", FollowUps: []string{"read again", "read a third time"},
			},
			Validators: []models.ValidatorInline{{Identifier: "force-retry", Kind: models.GraderKindText,
				Parameters: models.TextGraderParameters{Contains: []string{"deliberately missing output"}}}},
		}, 1, 1)
		require.False(t, cached)
		require.Equal(t, models.StatusFailed, outcome.Status)
		require.Len(t, outcome.Runs, 2)
		for _, run := range outcome.Runs {
			require.Equal(t, models.StatusFailed, run.Status)
			require.Equal(t, 2, run.Attempts, "the failed grader must exercise both retry attempts")
			require.Contains(t, run.FinalOutput, "response sequence exhausted")
		}
	}
	require.Len(t, engine.results, 24, "two tasks x two trials x two attempts x three turns")
	configs := make(map[string]bool)
	for attempt := range 8 {
		offset := attempt * 3
		require.Contains(t, engine.results[offset], `{\"ordinal\":1}`)
		require.Contains(t, engine.results[offset+1], `{\"ordinal\":2}`)
		require.Contains(t, engine.results[offset+2], "response sequence exhausted")
		require.Equal(t, engine.configs[offset], engine.configs[offset+1])
		require.Equal(t, engine.configs[offset], engine.configs[offset+2])
		require.False(t, configs[engine.configs[offset]], "every actual retry/trial/task must own fresh state")
		configs[engine.configs[offset]] = true
		require.NoFileExists(t, engine.configs[offset])
	}
}

func TestRunTestMCPMockParentReplacementFailsOutcome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating the parent alias requires Windows symlink privileges")
	}
	for _, executionFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful-execution", true: "execution-error"}[executionFails], func(t *testing.T) {
			aliasBase := t.TempDir()
			runner, engine, parent := newMCPLifetimeRunner(t)
			runner.skipGraders = false
			canonical, err := filepath.EvalSymlinks(parent)
			require.NoError(t, err)
			alias := filepath.Join(aliasBase, "alias")
			require.NoError(t, os.Symlink(filepath.Dir(canonical), alias))
			for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(name, filepath.Join(alias, filepath.Base(canonical)))
			}
			if executionFails {
				engine.executeErr = errors.New("original execution failure")
			}
			var configBody []byte
			var retainedConfig, sentinel, originalConfig string
			var configIdentity os.FileInfo
			engine.afterExecute = func(configFile string) {
				originalConfig = configFile
				configBody, err = os.ReadFile(configFile)
				require.NoError(t, err)
				configIdentity, err = os.Stat(configFile)
				require.NoError(t, err)
				relative, err := filepath.Rel(canonical, configFile)
				require.NoError(t, err)
				require.True(t, filepath.IsLocal(relative), "setup must move the parent containing the original config")
				moved := canonical + "-moved"
				require.NoError(t, os.Rename(canonical, moved))
				retainedConfig = filepath.Join(moved, relative)
				movedIdentity, err := os.Stat(retainedConfig)
				require.NoError(t, err)
				require.True(t, os.SameFile(configIdentity, movedIdentity))
				require.NoFileExists(t, configFile)
				require.NoError(t, os.Mkdir(canonical, 0700))
				sentinel = filepath.Join(canonical, "sentinel")
				require.NoError(t, os.WriteFile(sentinel, []byte("preserve parent replacement"), 0600))
			}
			outcome, cached := runner.runTest(context.Background(), &models.TestCase{
				TestID: "parent-cleanup", Stimulus: models.TaskStimulus{Message: "read"},
			}, 1, 1)
			require.False(t, cached)
			require.Len(t, engine.results, 1)
			require.Contains(t, engine.results[0], `{\"ordinal\":1}`)
			retained, err := os.ReadFile(retainedConfig)
			require.NoError(t, err)
			require.Equal(t, configBody, retained)
			retainedIdentity, err := os.Stat(retainedConfig)
			require.NoError(t, err)
			require.True(t, os.SameFile(configIdentity, retainedIdentity))
			replacement, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			require.Equal(t, "preserve parent replacement", string(replacement))
			require.NoFileExists(t, originalConfig)
			require.False(t, engine.response.Success)
			require.Equal(t, models.StatusFailed, outcome.Status)
			require.Len(t, outcome.Runs, 1)
			run := outcome.Runs[0]
			require.Equal(t, models.StatusError, run.Status)
			require.Contains(t, run.ErrorMsg, "clean up MCP mock attempt state")
			require.Contains(t, run.ErrorMsg, "identity changed")
			require.Contains(t, run.ErrorMsg, "if renamed, locate it before manual cleanup")
			if executionFails {
				require.Contains(t, run.ErrorMsg, "original execution failure")
			}
			body, err := json.Marshal(outcome)
			require.NoError(t, err)
			var saved models.TestOutcome
			require.NoError(t, json.Unmarshal(body, &saved))
			require.Equal(t, models.StatusFailed, saved.Status)
			require.Equal(t, models.StatusError, saved.Runs[0].Status)
			require.Equal(t, run.ErrorMsg, saved.Runs[0].ErrorMsg)
		})
	}
}

func TestRunTestMCPMockCacheInvalidatesPriorAttemptBehavior(t *testing.T) {
	// Frozen using CacheKey from main 6e8c54d64ea11b037f90ffcaa5a9f6bac0e03187.
	// That implementation ignores both MCP mocks and the enclosing schema version.
	const oldKey = "e6c90a8a682cea3ae33bb25c26c89de19e37873221f6de7cd58dd4105bfb36ce"
	for _, source := range []string{"inline", "fixtures", "no-mocks", "empty-mocks", "invalid-version"} {
		t.Run(source, func(t *testing.T) {
			runner, engine, dir := newMCPLifetimeRunner(t)
			spec := runner.cfg.Spec()
			task := &models.TestCase{TestID: "cached-sequence", Stimulus: models.TaskStimulus{
				Message: "read", FollowUps: []string{"read again", "read a third time"},
			}}
			runner.cfg = config.NewEvalConfig(spec, config.WithSpecDir(dir), config.WithFixtureDir(t.TempDir()))
			mock := spec.MCPMocks[0]
			writeMock := func() {
				if source != "fixtures" {
					spec.MCPMocks[0] = mock
					return
				}
				fixtures := filepath.Join(dir, "mcp")
				require.NoError(t, os.MkdirAll(fixtures, 0700))
				body, err := json.Marshal(mock.Tools["read"])
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(fixtures, "read.json"), body, 0600))
				spec.MCPMocks[0] = models.MCPMockConfig{Name: mock.Name, Fixtures: "mcp"}
			}
			writeMock()
			withoutMocks := source == "no-mocks" || source == "empty-mocks"
			switch source {
			case "no-mocks":
				spec.MCPMocks = nil
			case "empty-mocks":
				spec.MCPMocks = []models.MCPMockConfig{}
			case "invalid-version":
				spec.SchemaVersion = "1.4"
			}
			runner.cache = cache.New(t.TempDir())
			legacy := &models.TestOutcome{TestID: task.TestID, Status: models.StatusPassed,
				Runs: []models.RunResult{{RunNumber: 1, Status: models.StatusPassed, FinalOutput: `{"ordinal":1}`}}}
			require.NoError(t, runner.cache.Put(oldKey, legacy))
			_, found := runner.cache.Get(oldKey)
			require.True(t, found, "the historical entry must exist before exercising the production caller")

			outcome, cached := runner.runTest(context.Background(), task, 1, 1)
			if withoutMocks {
				require.True(t, cached, "unaffected legacy cache entries must retain their original keys")
				require.Empty(t, engine.results)
				require.Equal(t, legacy.Runs[0].FinalOutput, outcome.Runs[0].FinalOutput)
				return
			}
			require.False(t, cached, "pre-attempt-lifetime results must not bypass execution after upgrade")
			require.Len(t, outcome.Runs, 1)
			if source == "invalid-version" {
				require.Empty(t, engine.results)
				require.Equal(t, models.StatusFailed, outcome.Status)
				require.Equal(t, models.StatusError, outcome.Runs[0].Status)
				require.Contains(t, outcome.Runs[0].ErrorMsg, "requires explicit enclosing scenario schemaVersion")
				return
			}
			require.Len(t, engine.results, 3)
			require.Contains(t, engine.results[0], `{\"ordinal\":1}`)
			require.Contains(t, engine.results[1], `{\"ordinal\":2}`)
			require.Contains(t, outcome.Runs[0].FinalOutput, "response sequence exhausted")
			for _, path := range engine.configs {
				require.NoFileExists(t, path)
			}
			same, cached := runner.runTest(context.Background(), task, 1, 1)
			require.True(t, cached, "unchanged effective mocks should still be cacheable")
			require.Equal(t, outcome.Runs[0].FinalOutput, same.Runs[0].FinalOutput)
			require.Len(t, engine.results, 3, "a current cache hit must not execute the engine")

			mock.Tools["read"].Responses[0].Sequence[0].Return = map[string]any{"ordinal": 11}
			writeMock()
			_, cached = runner.runTest(context.Background(), task, 1, 1)
			require.False(t, cached, "changed inline or spec-relative fixture content must invalidate the result")
			require.Len(t, engine.results, 6)
			require.Contains(t, engine.results[3], `{\"ordinal\":11}`)
			require.Contains(t, engine.results[4], `{\"ordinal\":2}`)
			require.Contains(t, engine.results[5], "response sequence exhausted")

			mock.Tools["read"].Responses[0].Sequence = nil
			mock.Tools["read"].Responses[0].Return = map[string]any{"legacy": true}
			writeMock()
			_, cached = runner.runTest(context.Background(), task, 1, 1)
			require.False(t, cached, "removing a sequence must not fall back to the pre-upgrade key")
			require.Len(t, engine.results, 9)
			for _, result := range engine.results[6:] {
				require.Contains(t, result, `{\"legacy\":true}`, "static responses must remain repeatable")
			}
		})
	}
}
