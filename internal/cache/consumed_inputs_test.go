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
	require.Equal(t, plain, empty, "no inputs and empty inputs share a key")
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

	write("my-skill/.git/index", "ignored")
	require.Equal(t, added, key(), "VCS metadata is not fingerprinted")

	write("my-skill/vendor/.config/settings.json", "{}")
	vendored := key()
	require.NotEqual(t, added, vendored, "hidden and vendored skill files are fingerprinted")
	added = vendored

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

func TestCacheKeyWithInputs_FollowsSymlinkedSkillContent(t *testing.T) {
	spec := &models.EvalSpec{Config: models.Config{ModelID: "m"}}
	task := &models.TestCase{TestID: "t"}
	shared := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(shared, "guide.md"), []byte("v1"), 0o644))
	root := t.TempDir()
	skill := filepath.Join(root, "skill")
	require.NoError(t, os.MkdirAll(skill, 0o755))
	if err := os.Symlink(shared, filepath.Join(skill, "references")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A cycle back to the root must terminate.
	require.NoError(t, os.Symlink(skill, filepath.Join(skill, "loop")))
	linkedRoot := filepath.Join(root, "linked")
	require.NoError(t, os.Symlink(skill, linkedRoot))

	key := func(dir string) string {
		k, err := CacheKeyWithInputs(spec, task, "", "", &ConsumedInputs{SkillDirs: []string{dir}})
		require.NoError(t, err)
		return k
	}
	direct, linked := key(skill), key(linkedRoot)
	require.NoError(t, os.WriteFile(filepath.Join(shared, "guide.md"), []byte("v2"), 0o644))
	require.NotEqual(t, direct, key(skill), "editing content behind a directory symlink must miss")
	require.NotEqual(t, linked, key(linkedRoot), "editing content beneath a symlinked skill root must miss")
}

func TestCacheKeyWithInputs_ExcludesRunOutputs(t *testing.T) {
	spec := &models.EvalSpec{Config: models.Config{ModelID: "m"}}
	task := &models.TestCase{TestID: "t"}
	skill := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("skill"), 0o644))
	outputs := filepath.Join(skill, "transcripts")
	inputs := &ConsumedInputs{SkillDirs: []string{skill}, ExcludePaths: []string{outputs, filepath.Join(skill, "results.json")}}
	key := func() string {
		k, err := CacheKeyWithInputs(spec, task, "", "", inputs)
		require.NoError(t, err)
		return k
	}
	base := key()
	require.NoError(t, os.MkdirAll(outputs, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outputs, "t.json"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skill, "results.json"), []byte("{}"), 0o644))
	require.Equal(t, base, key(), "run outputs beneath a skill directory must not change the key")
}
