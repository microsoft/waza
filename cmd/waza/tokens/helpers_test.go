package tokens

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindMarkdownFiles(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hi"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "doc.mdx"), []byte("# Doc"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "script.ts"), []byte("//"), 0644))

	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "nested.md"), []byte("# Nested"), 0644))

	nm := filepath.Join(dir, "node_modules")
	require.NoError(t, os.MkdirAll(nm, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(nm, "excluded.md"), []byte("# No"), 0644))

	files, err := findMarkdownFiles(nil, dir)
	require.NoError(t, err)
	sort.Strings(files)

	require.Len(t, files, 3)

	for _, f := range files {
		require.NotEqual(t, "excluded.md", filepath.Base(f), "node_modules/excluded.md should be excluded")
	}
}

func TestFindMarkdownFilesEmpty(t *testing.T) {
	dir := t.TempDir()
	files, err := findMarkdownFiles(nil, dir)
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestFindMarkdownFilesNonexistent(t *testing.T) {
	_, err := findMarkdownFiles(nil, "/nonexistent/path")
	require.Error(t, err)
}

// --- resolveLimitsConfig priority tests ---

func TestResolveLimitsConfig_WazaYamlOnly(t *testing.T) {
	dir := t.TempDir()

	yaml := "tokens:\n  limits:\n    defaults:\n      \"*.md\": 800\n    overrides:\n      \"special.md\": 5000\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte(yaml), 0644))

	cfg, usedLegacy := resolveLimitsConfig(dir)
	require.False(t, usedLegacy, "should not use legacy path when .waza.yaml has limits")
	require.NotNil(t, cfg.Defaults, "config defaults should be populated from .waza.yaml")
	require.Equal(t, 800, cfg.Defaults["*.md"])
	require.Equal(t, 5000, cfg.Overrides["special.md"])
}

func TestResolveLimitsConfig_LegacyJSONOnly(t *testing.T) {
	dir := t.TempDir()

	limitsJSON, err := json.Marshal(map[string]any{
		"defaults":  map[string]int{"*.md": 100},
		"overrides": map[string]int{},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".token-limits.json"), limitsJSON, 0644))

	cfg, usedLegacy := resolveLimitsConfig(dir)
	require.True(t, usedLegacy, "should flag legacy usage when only .token-limits.json exists")
	require.Nil(t, cfg.Defaults, "should return empty config so Check() loads .token-limits.json")
}

func TestResolveLimitsConfig_BothPresent_WazaYamlWins(t *testing.T) {
	dir := t.TempDir()

	yaml := "tokens:\n  limits:\n    defaults:\n      \"*.md\": 900\n    overrides:\n      \"special.md\": 6000\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte(yaml), 0644))

	limitsJSON, err := json.Marshal(map[string]any{
		"defaults":  map[string]int{"*.md": 10},
		"overrides": map[string]int{"special.md": 20},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".token-limits.json"), limitsJSON, 0644))

	cfg, usedLegacy := resolveLimitsConfig(dir)
	require.False(t, usedLegacy, ".waza.yaml should win when both are present")
	require.NotNil(t, cfg.Defaults, "config should come from .waza.yaml")
	require.Equal(t, 900, cfg.Defaults["*.md"], ".waza.yaml limits should take priority")
	require.Equal(t, 6000, cfg.Overrides["special.md"], ".waza.yaml overrides should take priority")
}

func TestResolveLimitsConfig_NeitherPresent(t *testing.T) {
	dir := t.TempDir()

	cfg, usedLegacy := resolveLimitsConfig(dir)
	require.False(t, usedLegacy, "no legacy usage when neither config exists")
	require.Nil(t, cfg.Defaults, "should return empty config so Check() uses built-in defaults")
}

func TestResolveLimitsConfig_OverridesOnly(t *testing.T) {
	dir := t.TempDir()

	yaml := "tokens:\n  limits:\n    overrides:\n      \"special.md\": 4000\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte(yaml), 0644))

	cfg, usedLegacy := resolveLimitsConfig(dir)
	require.False(t, usedLegacy, "should not flag legacy when .waza.yaml has overrides")
	require.NotNil(t, cfg.Defaults, "defaults map should be initialized even when only overrides are set")
	require.Equal(t, 4000, cfg.Overrides["special.md"])
}
