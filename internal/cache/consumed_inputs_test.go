package cache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCacheKeyWithInputs_ConsumedBytes(t *testing.T) {
	spec := &models.EvalSpec{Config: models.Config{ModelID: "m", SkillPaths: []string{"skills"}}}
	task := &models.TestCase{TestID: "t", Stimulus: models.TaskStimulus{Message: "go"}}
	skillDir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(skillDir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	write("my-skill/SKILL.md", "v1")
	write("my-skill/references/guide.md", "guide v1")
	files := []ConsumedFile{{Role: "instruction", Path: "AGENTS.md", Content: []byte("be brief")}}
	key := func() string {
		k, err := CacheKeyWithInputs(spec, task, "", "", &ConsumedInputs{Files: files, SkillDirs: []string{skillDir}})
		require.NoError(t, err)
		return k
	}

	base := key()
	require.Equal(t, base, key(), "unchanged inputs must hit")

	plain, err := CacheKeyWithSpecDir(spec, task, "", "")
	require.NoError(t, err)
	empty, err := CacheKeyWithInputs(spec, task, "", "", &ConsumedInputs{})
	require.NoError(t, err)
	require.Equal(t, plain, empty, "tasks without consumed inputs keep their key")
	require.NotEqual(t, plain, base)

	write("my-skill/SKILL.md", "v2")
	edited := key()
	require.NotEqual(t, base, edited, "editing SKILL.md in place must miss")

	write("my-skill/references/guide.md", "guide v2")
	nested := key()
	require.NotEqual(t, edited, nested, "editing a skill reference file must miss")

	write("my-skill/scripts/run.sh", "echo")
	added := key()
	require.NotEqual(t, nested, added, "adding a skill file must miss")

	write("my-skill/node_modules/pkg/index.js", "ignored")
	require.Equal(t, added, key(), "vendored packages are not fingerprinted")

	files[0].Content = []byte("be verbose")
	require.NotEqual(t, added, key(), "editing consumed file bytes must miss")
}

func TestCacheKeyWithInputs_MissingSkillDirChangesKey(t *testing.T) {
	spec := &models.EvalSpec{Config: models.Config{ModelID: "m"}}
	task := &models.TestCase{TestID: "t"}
	dir := t.TempDir()
	missing, err := CacheKeyWithInputs(spec, task, "", "", &ConsumedInputs{SkillDirs: []string{filepath.Join(dir, "absent")}})
	require.NoError(t, err)
	present, err := CacheKeyWithInputs(spec, task, "", "", &ConsumedInputs{SkillDirs: []string{dir}})
	require.NoError(t, err)
	require.NotEqual(t, missing, present)
}
