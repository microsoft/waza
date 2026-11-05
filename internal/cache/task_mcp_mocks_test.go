package cache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func answerMock(name, answer string) models.MCPMockConfig {
	return models.MCPMockConfig{Name: name, Tools: map[string]models.MCPMockTool{
		"ask_user": {Responses: []models.MCPMockResponse{{Return: map[string]any{"answer": answer}}}},
	}}
}

// frozenNoMCPKey is CacheKey for frozenNoMCP* computed on main c8aad785, before
// MCP mocks entered the key. Tasks with no effective MCP mocks must keep it.
const frozenNoMCPKey = "f2eec41f13098ea3f110279e0d5c62f57d456f888a8d3875347d1dbe78fb433b"

func TestCacheKey_NoMCPMocksKeepFrozenKey(t *testing.T) {
	spec := &models.EvalSpec{SchemaVersion: "1.1", SpecIdentity: models.SpecIdentity{Name: "frozen"},
		Config: models.Config{ModelID: "m", EngineType: "copilot-sdk", TimeoutSec: 30, TrialsPerTask: 1}}
	task := &models.TestCase{TestID: "t", DisplayName: "t", Stimulus: models.TaskStimulus{Message: "go"}}
	key, err := CacheKeyWithSpecDir(spec, task, "unused-fixtures", "unused-spec")
	require.NoError(t, err)
	require.Equal(t, frozenNoMCPKey, key)
	key, err = CacheKey(spec, task, "unused-fixtures")
	require.NoError(t, err)
	require.Equal(t, frozenNoMCPKey, key)
}

func TestCacheKey_TaskMCPMocksChangeKey(t *testing.T) {
	spec := &models.EvalSpec{SchemaVersion: "1.1", SpecIdentity: models.SpecIdentity{Name: "eval"}, Config: models.Config{ModelID: "m"}}
	task := func(answer string) *models.TestCase {
		tc := &models.TestCase{TestID: "task"}
		if answer != "" {
			tc.MCPMocks = []models.MCPMockConfig{answerMock("host", answer)}
		}
		return tc
	}
	key := func(tc *models.TestCase) string {
		k, err := CacheKey(spec, tc, t.TempDir())
		require.NoError(t, err)
		return k
	}
	plain, publish, decline := key(task("")), key(task("Publish this version")), key(task("Not now"))
	require.NotEqual(t, plain, publish)
	require.NotEqual(t, publish, decline, "a changed task mock answer must not reuse a cached outcome")
	require.Equal(t, publish, key(task("Publish this version")))
}

func TestCacheKey_InheritedEvalMCPMocksChangeKey(t *testing.T) {
	task := &models.TestCase{TestID: "task"}
	key := func(spec *models.EvalSpec) string {
		k, err := CacheKeyWithSpecDir(spec, task, t.TempDir(), t.TempDir())
		require.NoError(t, err)
		return k
	}
	base := models.EvalSpec{SchemaVersion: "1.1", SpecIdentity: models.SpecIdentity{Name: "eval"}, Config: models.Config{ModelID: "m"}}
	withDecline, withApprove := base, base
	withDecline.MCPMocks = []models.MCPMockConfig{answerMock("host", "Not now")}
	withApprove.MCPMocks = []models.MCPMockConfig{answerMock("host", "Accept")}
	require.NotEqual(t, key(&withDecline), key(&withApprove), "a task inheriting an edited eval mock must not reuse a cached outcome")
	require.NotEqual(t, key(&base), key(&withDecline))
}

func TestCacheKey_MCPMockFixtureContentChangesKey(t *testing.T) {
	specDir := t.TempDir()
	fixtures := filepath.Join(specDir, "mcp")
	require.NoError(t, os.MkdirAll(fixtures, 0o755))
	write := func(answer string) {
		require.NoError(t, os.WriteFile(filepath.Join(fixtures, "ask_user.json"),
			[]byte(`{"responses":[{"return":{"answer":"`+answer+`"}}]}`), 0o600))
	}
	spec := &models.EvalSpec{SchemaVersion: "1.1", SpecIdentity: models.SpecIdentity{Name: "eval"}, Config: models.Config{ModelID: "m"}}
	task := &models.TestCase{TestID: "task", MCPMocks: []models.MCPMockConfig{{Name: "host", Fixtures: "mcp"}}}
	key := func() string {
		k, err := CacheKeyWithSpecDir(spec, task, t.TempDir(), specDir)
		require.NoError(t, err)
		return k
	}
	write("Not now")
	first := key()
	write("Publish this version")
	require.NotEqual(t, first, key(), "editing a fixture under the same path must change the key")
}

func TestRecordedMCPMocksMatch(t *testing.T) {
	outcome := func(modes ...models.DependencyMode) *models.TestOutcome {
		return &models.TestOutcome{Runs: []models.RunResult{{DependencyModes: modes}}}
	}
	host := models.DependencyMode{Kind: "mcp", Name: "host", Mode: "mocked"}
	require.True(t, RecordedMCPMocksMatch(outcome(), nil))
	require.True(t, RecordedMCPMocksMatch(outcome(models.DependencyMode{Kind: "mcp", Name: "host", Mode: "live"}), nil))
	require.True(t, RecordedMCPMocksMatch(outcome(models.DependencyMode{Kind: "cli", Name: "az", Mode: "mocked"}), nil))
	require.False(t, RecordedMCPMocksMatch(outcome(host), nil), "a mocked outcome is stale once the mock is removed")
	require.True(t, RecordedMCPMocksMatch(outcome(host), []models.MCPMockConfig{{Name: " host "}}))
}
