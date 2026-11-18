package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRunTest_CacheMissesWhenConsumedBytesChangeInPlace(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	write("skills/demo/SKILL.md", "---\nname: demo\ndescription: demo\n---\nv1")
	write("skills/demo/references/notes.md", "notes v1")
	write("fixtures/input.txt", "input v1")
	write("AGENTS.md", "instructions v1")

	spec := &models.EvalSpec{
		SkillName: "demo",
		Config: models.Config{
			EngineType: "mock", ModelID: "mock-model", TimeoutSec: 30, TrialsPerTask: 1,
			SkillPaths: []string{"skills"}, InstructionFiles: []string{"AGENTS.md"},
		},
	}
	tc := &models.TestCase{TestID: "task", DisplayName: "Task", Stimulus: models.TaskStimulus{
		Message:  "go",
		Metadata: map[string]any{"fixture": "fixtures"},
	}}
	engine := execution.NewMockEngine("mock-model")
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(t.Context())) })
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(dir), config.WithFixtureDir(dir)),
		engine, WithCache(cache.New(t.TempDir())))

	run := func() bool {
		outcome, cached := runner.runTest(t.Context(), tc, 1, 1)
		require.Equal(t, models.StatusPassed, outcome.Status)
		return cached
	}
	require.False(t, run(), "first run executes")
	require.True(t, run(), "unchanged consumed bytes hit")

	for _, edit := range []struct{ path, content string }{
		{"skills/demo/SKILL.md", "---\nname: demo\ndescription: demo\n---\nv2"},
		{"skills/demo/references/notes.md", "notes v2"},
		{"fixtures/input.txt", "input v2"},
		{"AGENTS.md", "instructions v2"},
	} {
		write(edit.path, edit.content)
		require.False(t, run(), "editing %s in place must miss", edit.path)
		require.True(t, run(), "re-run after editing %s hits", edit.path)
	}

	require.NoError(t, os.Remove(filepath.Join(dir, "AGENTS.md")))
	_, err := runner.taskCacheKey(spec, tc)
	require.Error(t, err, "an unreadable consumed input disables caching")
}

func TestRunTest_CacheHitsWithCacheDirBeneathSkillRoot(t *testing.T) {
	skillDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo\ndescription: demo\n---\nbody"), 0o644))
	spec := &models.EvalSpec{SkillName: "demo", Config: models.Config{
		EngineType: "mock", ModelID: "mock-model", TimeoutSec: 30, TrialsPerTask: 1, SkillPaths: []string{skillDir},
	}}
	tc := &models.TestCase{TestID: "task", DisplayName: "Task", Stimulus: models.TaskStimulus{Message: "go"}}
	engine := execution.NewMockEngine("mock-model")
	require.NoError(t, engine.Initialize(t.Context()))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown(t.Context())) })
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(skillDir)), engine,
		WithCache(cache.New(filepath.Join(skillDir, "result-cache"))))

	_, cached := runner.runTest(t.Context(), tc, 1, 1)
	require.False(t, cached)
	_, cached = runner.runTest(t.Context(), tc, 1, 1)
	require.True(t, cached, "cache entries beneath the skill root must not change the key")
}

func TestTaskCacheKey_FingerprintsSkillDiscoveredFromWorkingDirectory(t *testing.T) {
	writeSkill := func(root string) {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "demo", "references"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, "demo", "SKILL.md"), []byte("---\nname: demo\ndescription: demo\n---\nbody"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(root, "demo", "references", "guide.md"), []byte("v1"), 0o644))
	}
	first, second := t.TempDir(), t.TempDir()
	writeSkill(first)
	writeSkill(second)
	spec := &models.EvalSpec{SkillName: "demo", Config: models.Config{EngineType: "mock", ModelID: "mock-model", TimeoutSec: 30, TrialsPerTask: 1}}
	tc := &models.TestCase{TestID: "task", Stimulus: models.TaskStimulus{Message: "go"}}
	runner := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(t.TempDir())), execution.NewMockEngine("mock-model"))
	key := func() string {
		k, err := runner.taskCacheKey(spec, tc)
		require.NoError(t, err)
		return k
	}

	t.Chdir(first)
	base := key()
	require.Equal(t, base, key())
	require.NoError(t, os.WriteFile(filepath.Join(first, "demo", "references", "guide.md"), []byte("v2"), 0o644))
	edited := key()
	require.NotEqual(t, base, edited, "editing the selected skill's references must miss")

	require.NoError(t, os.WriteFile(filepath.Join(second, "demo", "references", "guide.md"), []byte("v2"), 0o644))
	t.Chdir(second)
	require.NotEqual(t, edited, key(), "an identical skill at a different injected directory must miss")
}
