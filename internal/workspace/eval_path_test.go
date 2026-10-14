package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindEvalForSkill(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config string
		files  []string
		want   string
	}{
		{name: "separated", files: []string{"evals/my-skill/eval.yaml"}, want: "evals/my-skill/eval.yaml"},
		{name: "nested", files: []string{"skills/category/my-skill/evals/eval.yaml"}, want: "skills/category/my-skill/evals/eval.yaml"},
		{name: "colocated", files: []string{"skills/category/my-skill/eval.yaml"}, want: "skills/category/my-skill/eval.yaml"},
		{name: "separated priority", files: []string{"evals/my-skill/eval.yaml", "skills/category/my-skill/evals/eval.yaml", "skills/category/my-skill/eval.yaml"}, want: "evals/my-skill/eval.yaml"},
		{name: "nested priority", files: []string{"skills/category/my-skill/evals/eval.yaml", "skills/category/my-skill/eval.yaml"}, want: "skills/category/my-skill/evals/eval.yaml"},
		{name: "configured paths", config: "paths:\n  evals: tests\nfiles:\n  evalFile: suite.yml\n", files: []string{"tests/my-skill/suite.yml", "evals/my-skill/eval.yaml"}, want: "tests/my-skill/suite.yml"},
		{name: "legacy filename", config: "paths:\n  evals: tests\nfiles:\n  evalFile: suite.yml\n", files: []string{"tests/my-skill/eval.yaml"}, want: "tests/my-skill/eval.yaml"},
		{name: "configured nested", config: "files:\n  evalFile: suite.yml\n", files: []string{"skills/category/my-skill/evals/suite.yml"}, want: "skills/category/my-skill/evals/suite.yml"},
		{name: "missing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "skills", "category", "my-skill")
			writeFile(t, filepath.Join(dir, "SKILL.md"), skillMD("my-skill"))
			if tt.config != "" {
				writeFile(t, filepath.Join(root, ".waza.yaml"), tt.config)
			}
			for _, file := range tt.files {
				writeFile(t, filepath.Join(root, filepath.FromSlash(file)), "name: test\n")
			}
			t.Chdir(t.TempDir())
			path, err := FindEvalForSkill(SkillInfo{Name: "my-skill", Dir: dir})
			require.NoError(t, err)
			if tt.want == "" {
				require.Empty(t, path)
			} else {
				require.Equal(t, filepath.Join(root, filepath.FromSlash(tt.want)), path)
			}
		})
	}
}

func TestFindEvalForSkill_ConfigBoundary(t *testing.T) {
	root := t.TempDir()
	plugin := filepath.Join(root, "plugin")
	dir := filepath.Join(plugin, "skills", "my-skill")
	writeFile(t, filepath.Join(dir, "SKILL.md"), skillMD("my-skill"))
	writeFile(t, filepath.Join(plugin, ".waza.yaml"), "paths:\n  evals: tests\n")
	writeFile(t, filepath.Join(root, "tests", "my-skill", "eval.yaml"), "name: unrelated\n")
	path, err := FindEvalForSkill(SkillInfo{Name: "my-skill", Dir: dir})
	require.NoError(t, err)
	require.Empty(t, path)
}

func TestFindEvalForSkill_AncestorBound(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, filepath.FromSlash(strings.Repeat("nested/", maxParentWalk)), "my-skill")
	writeFile(t, filepath.Join(dir, "SKILL.md"), skillMD("my-skill"))
	writeFile(t, filepath.Join(root, "evals", "my-skill", "eval.yaml"), "name: distant\n")
	path, err := FindEvalForSkill(SkillInfo{Name: "my-skill", Dir: dir})
	require.NoError(t, err)
	require.Empty(t, path)
}

func TestFindEvalForSkill_AbsoluteConfiguredPath(t *testing.T) {
	root := t.TempDir()
	evals := t.TempDir()
	dir := filepath.Join(root, "skills", "my-skill")
	writeFile(t, filepath.Join(dir, "SKILL.md"), skillMD("my-skill"))
	writeFile(t, filepath.Join(root, ".waza.yaml"), "paths:\n  evals: '"+filepath.ToSlash(evals)+"'\n")
	want := filepath.Join(evals, "my-skill", "eval.yaml")
	writeFile(t, want, "name: test\n")
	path, err := FindEvalForSkill(SkillInfo{Name: "my-skill", Dir: dir})
	require.NoError(t, err)
	require.Equal(t, want, path)
}

func TestFindEvalForSkill_CompiledSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "skills", "my-skill")
	compiled := filepath.Join(source, ".apm", "skills", "my-skill")
	writeFile(t, filepath.Join(compiled, "SKILL.md"), skillMD("my-skill"))
	want := filepath.Join(source, "eval.yaml")
	writeFile(t, want, "name: source\n")
	path, err := FindEvalForSkill(SkillInfo{Name: "my-skill", Dir: compiled, SourceDir: source})
	require.NoError(t, err)
	require.Equal(t, want, path)
	t.Chdir(root)
	path, err = FindEvalForSkill(SkillInfo{
		Name:      "my-skill",
		Dir:       compiled,
		SourceDir: filepath.Join("skills", "my-skill"),
	})
	require.NoError(t, err)
	require.Equal(t, want, path)
}

func TestFindEvalForSkill_CompiledFallback(t *testing.T) {
	root := t.TempDir()
	compiled := filepath.Join(root, ".apm", "skills", "namespace", "my-skill")
	writeFile(t, filepath.Join(compiled, "SKILL.md"), skillMD("my-skill"))
	want := filepath.Join(compiled, "eval.yaml")
	writeFile(t, want, "name: compiled\n")
	path, err := FindEvalForSkill(SkillInfo{Name: "my-skill", Dir: compiled})
	require.NoError(t, err)
	require.Equal(t, want, path)
}

func TestFindEvalForSkill_RelativeSkillDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "my-skill")
	writeFile(t, filepath.Join(dir, "SKILL.md"), skillMD("my-skill"))
	want := filepath.Join(dir, "eval.yaml")
	writeFile(t, want, "name: colocated\n")
	t.Chdir(root)
	path, err := FindEvalForSkill(SkillInfo{Name: "my-skill", Dir: filepath.Join("skills", "my-skill")})
	require.NoError(t, err)
	require.Equal(t, want, path)
}

func TestFindEvalForSkill_InvalidConfig(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".waza.yaml"), "unknown_field: true\n")
	_, err := FindEvalForSkill(SkillInfo{Name: "my-skill", Dir: root})
	require.ErrorContains(t, err, "loading skill workspace configuration")
}

func TestFindSeparatedEval_DirectoryIsNotEval(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "evals", "my-skill", "eval.yaml"), 0o755))
	path, err := findSeparatedEval(root, "evals", "my-skill", []string{"eval.yaml"})
	require.NoError(t, err)
	require.Empty(t, path)
}

func TestFindEvalForSkill_UnreadableEvalDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("requires Unix directory permissions and a non-root user")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "my-skill")
	writeFile(t, filepath.Join(dir, "SKILL.md"), skillMD("my-skill"))
	evals := filepath.Join(root, "evals")
	writeFile(t, filepath.Join(evals, "my-skill", "eval.yaml"), "name: test\n")
	require.NoError(t, os.Chmod(evals, 0))
	t.Cleanup(func() { require.NoError(t, os.Chmod(evals, 0o755)) })
	_, err := FindEvalForSkill(SkillInfo{Name: "my-skill", Dir: dir})
	require.ErrorContains(t, err, "checking eval file")
	require.ErrorIs(t, err, os.ErrPermission)
}

func TestFindEval_UnreadableFallbacks(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("requires Unix directory permissions and a non-root user")
	}
	for _, layout := range []string{"nested", "colocated", "compiled nested", "compiled colocated"} {
		t.Run(layout, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			compiled := filepath.Join(root, "compiled")
			si := SkillInfo{Name: "my-skill", Dir: source}
			dir := source
			switch layout {
			case "nested":
				dir = filepath.Join(source, "evals")
			case "compiled nested":
				si.Dir, si.SourceDir = compiled, source
				dir = filepath.Join(compiled, "evals")
			case "compiled colocated":
				si.Dir, si.SourceDir = compiled, source
				dir = compiled
			}
			writeFile(t, filepath.Join(dir, "eval.yaml"), "name: test\n")
			require.NoError(t, os.Chmod(dir, 0))
			t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o755)) })
			path, err := FindEval(&WorkspaceContext{Root: root, Skills: []SkillInfo{si}}, si.Name)
			require.Empty(t, path)
			require.ErrorContains(t, err, "checking eval file")
			require.ErrorIs(t, err, os.ErrPermission)
		})
	}
}
