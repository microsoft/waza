package optimizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/schemas"
	"github.com/stretchr/testify/require"
)

func TestCapabilitiesDocumentMatchesSchema(t *testing.T) {
	doc := Capabilities()
	require.Equal(t, []string{ContractVersion}, doc.Versions)
	require.IsNonDecreasing(t, doc.Capabilities)
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	validateAgainst(t, "optimizer-capabilities-1.0.schema.json", schemas.OptimizerCapabilitiesSchemaJSON, data)
}

func TestNegotiate(t *testing.T) {
	base := func() AdapterInfo { return DefaultFakeInfo() }
	t.Run("selects target and drops the other", func(t *testing.T) {
		n, err := Negotiate(base(), TargetSkillDirectory)
		require.NoError(t, err)
		require.Contains(t, n.SelectedCapabilities, CapTargetSkillDirectory)
		require.NotContains(t, n.SelectedCapabilities, CapTargetContextOverlay)
		require.IsNonDecreasing(t, n.SelectedCapabilities)
	})
	t.Run("unknown optional capability is reported, not fatal", func(t *testing.T) {
		info := base()
		info.OptionalCapabilities = append(info.OptionalCapabilities, "trace.capture")
		n, err := Negotiate(info, TargetContextOverlay)
		require.NoError(t, err)
		require.Equal(t, []string{"trace.capture"}, n.UnavailableOptionalCapabilities)
	})
	cases := map[string]struct {
		mutate   func(*AdapterInfo)
		target   Target
		category string
	}{
		"version":          {func(i *AdapterInfo) { i.ContractVersions = []string{"0.9"} }, TargetSkillDirectory, CategoryUnsupportedVersion},
		"target missing":   {func(i *AdapterInfo) { i.OptionalCapabilities = nil }, TargetSkillDirectory, CategoryIncompatible},
		"other target":     {func(i *AdapterInfo) { i.RequiredCapabilities = append(i.RequiredCapabilities, CapTargetContextOverlay) }, TargetSkillDirectory, CategoryIncompatible},
		"unknown required": {func(i *AdapterInfo) { i.RequiredCapabilities = append(i.RequiredCapabilities, "trace.capture") }, TargetSkillDirectory, CategoryIncompatible},
		"both lists":       {func(i *AdapterInfo) { i.OptionalCapabilities = append(i.OptionalCapabilities, CapProposeFiles) }, TargetSkillDirectory, CategoryIncompatible},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			info := base()
			tc.mutate(&info)
			_, err := Negotiate(info, tc.target)
			require.Equal(t, tc.category, CategoryOf(err), err)
		})
	}
}

func TestValidateCandidatePath(t *testing.T) {
	for _, ok := range []string{"SKILL.md", "references/a-b_c.md", "x/y/z.txt"} {
		require.NoError(t, ValidateCandidatePath(ok), ok)
	}
	for _, bad := range []string{"", "/abs", "../x", "a/../b", "a//b", "./a", "a\\b", ".hidden", "a/.git/x", ".git", "CON", "con.txt", "a/nul", "trail.", "C:/x", "a b", strings.Repeat("a/", 9) + "x", strings.Repeat("a", 201)} {
		require.Equal(t, CategoryUnsafeCandidatePath, CategoryOf(ValidateCandidatePath(bad)), bad)
	}
}

func TestValidateFilesAndIdentity(t *testing.T) {
	_, err := validateFiles([]FileContent{{Path: "notes.md", Content: "x"}}, TargetSkillDirectory)
	require.Equal(t, CategoryMalformedProposal, CategoryOf(err), "skill targets need SKILL.md")
	_, err = validateFiles([]FileContent{{Path: "a.md"}, {Path: "A.md"}}, TargetContextOverlay)
	require.Error(t, err, "case-fold collisions are rejected")
	_, err = validateFiles([]FileContent{{Path: "a"}, {Path: "a/b"}}, TargetContextOverlay)
	require.Error(t, err, "file/dir conflicts are rejected")
	_, err = validateFiles([]FileContent{{Path: "a.md", Content: "\x00"}}, TargetContextOverlay)
	require.Error(t, err, "binary content is rejected")
	_, err = validateFiles(nil, TargetContextOverlay)
	require.Error(t, err)

	a := []FileContent{{Path: "b.md", Content: "2"}, {Path: "a.md", Content: "1"}}
	b := []FileContent{{Path: "a.md", Content: "1"}, {Path: "b.md", Content: "2"}}
	va, err := validateFiles(a, TargetContextOverlay)
	require.NoError(t, err)
	vb, err := validateFiles(b, TargetContextOverlay)
	require.NoError(t, err)
	idA, _ := candidateIdentity(TargetContextOverlay, va)
	idB, _ := candidateIdentity(TargetContextOverlay, vb)
	require.Equal(t, idA, idB, "identity is order independent")
	idSkill, _ := candidateIdentity(TargetSkillDirectory, va)
	require.NotEqual(t, idA, idSkill, "identity includes the target")
	require.Regexp(t, `^sha256:[0-9a-f]{64}$`, idA)
}

func TestLoadSeedRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# s\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("x"), 0o600))
	files, err := LoadSeed(dir, TargetSkillDirectory)
	require.NoError(t, err)
	require.Len(t, files, 1, "dot directories are skipped")

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	require.NoError(t, os.Symlink(filepath.Join(dir, "SKILL.md"), filepath.Join(dir, "link.md")))
	_, err = LoadSeed(dir, TargetSkillDirectory)
	require.Equal(t, CategoryUnsafeCandidatePath, CategoryOf(err))
}

func TestFakeScriptStrict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"proposals":[{"files":{"a":"b"},"bogus":1}]}`), 0o600))
	_, err := LoadFakeScript(path)
	require.ErrorContains(t, err, "bogus")
	require.NoError(t, os.WriteFile(path, []byte(`{"proposals":[]} {}`), 0o600))
	_, err = LoadFakeScript(path)
	require.Error(t, err)
}
