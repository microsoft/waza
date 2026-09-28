package execution

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnumerateAvailableSkills_TopLevelSKILLmd covers the case where the
// skill directory is itself a skill folder containing SKILL.md.
func TestEnumerateAvailableSkills_TopLevelSKILLmd(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: top-skill\ndescription: Top-level skill\n---\nBody"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644))

	got := enumerateAvailableSkills([]string{dir})
	require.Len(t, got, 1)
	assert.Equal(t, "top-skill", got[0].Name)
	assert.Equal(t, filepath.Join(dir, "SKILL.md"), got[0].Path)
}

// TestEnumerateAvailableSkills_NestedSkillsCatalog covers the realistic
// case reproduced by issue #540: skill_directories points at a folder like
// `.apm/skills/` whose immediate subdirectories are individual skills.
func TestEnumerateAvailableSkills_NestedSkillsCatalog(t *testing.T) {
	root := t.TempDir()
	for _, s := range []struct{ dir, name string }{
		{"pre-mortem", "pre-mortem"},
		{"prioritize-features", "prioritize-features"},
		{"release-notes", "release-notes"},
	} {
		skillDir := filepath.Join(root, s.dir)
		require.NoError(t, os.MkdirAll(skillDir, 0755))
		content := "---\nname: " + s.name + "\ndescription: >-\n  A test skill\n---\nBody"
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644))
	}
	// Directories that should be skipped even when they contain SKILL.md.
	for _, ignored := range []string{"node_modules", "vendor", ".hidden"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, ignored), 0755))
		require.NoError(t, os.WriteFile(
			filepath.Join(root, ignored, "SKILL.md"),
			[]byte("---\nname: should-not-appear\ndescription: nope\n---\n"),
			0644,
		))
	}

	got := enumerateAvailableSkills([]string{root})
	names := make([]string, 0, len(got))
	for _, s := range got {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"pre-mortem", "prioritize-features", "release-notes"}, names)
	for _, s := range got {
		assert.True(t, filepath.IsAbs(s.Path) || filepath.IsLocal(s.Path))
		assert.Equal(t, "SKILL.md", filepath.Base(s.Path))
	}
}

// TestEnumerateAvailableSkills_DeduplicatesRepeatedDirs guards against
// double-counting when the same skill directory is listed under multiple
// skill_directories entries.
func TestEnumerateAvailableSkills_DeduplicatesRepeatedDirs(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "solo-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0755))
	content := "---\nname: solo-skill\ndescription: solo\n---\nBody"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644))

	got := enumerateAvailableSkills([]string{dir, dir, skillDir})
	require.Len(t, got, 1)
	assert.Equal(t, "solo-skill", got[0].Name)
}

// TestEnumerateAvailableSkills_EmptyForMissingDir keeps the result empty
// (rather than surfacing a partial catalog) when none of the passed
// directories exist or contain skills.
func TestEnumerateAvailableSkills_EmptyForMissingDir(t *testing.T) {
	got := enumerateAvailableSkills([]string{filepath.Join(t.TempDir(), "does-not-exist")})
	assert.Empty(t, got)
}
