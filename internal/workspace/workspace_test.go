package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// skillMD returns a minimal valid SKILL.md with the given name.
func skillMD(name string) string {
	return "---\nname: " + name + "\ndescription: Test skill\n---\n\nBody content.\n"
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectContext_SingleSkillInCWD(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "SKILL.md"), skillMD("my-skill"))

	ctx, err := DetectContext(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextSingleSkill {
		t.Fatalf("expected ContextSingleSkill, got %d", ctx.Type)
	}
	if len(ctx.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(ctx.Skills))
	}
	if ctx.Skills[0].Name != "my-skill" {
		t.Errorf("expected name 'my-skill', got %q", ctx.Skills[0].Name)
	}
	if ctx.Skills[0].Dir != dir {
		t.Errorf("expected dir %q, got %q", dir, ctx.Skills[0].Dir)
	}
}

func TestDetectContext_SingleSkillWalkUp(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "SKILL.md"), skillMD("parent-skill"))

	nested := filepath.Join(root, "src", "utils")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}

	ctx, err := DetectContext(nested)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextSingleSkill {
		t.Fatalf("expected ContextSingleSkill, got %d", ctx.Type)
	}
	if ctx.Skills[0].Name != "parent-skill" {
		t.Errorf("expected name 'parent-skill', got %q", ctx.Skills[0].Name)
	}
	if ctx.Skills[0].Dir != root {
		t.Errorf("expected dir %q, got %q", root, ctx.Skills[0].Dir)
	}
}

func TestDetectContext_MultiSkillWithSkillsDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "alpha", "SKILL.md"), skillMD("alpha"))
	writeFile(t, filepath.Join(root, "skills", "beta", "SKILL.md"), skillMD("beta"))

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextMultiSkill {
		t.Fatalf("expected ContextMultiSkill, got %d", ctx.Type)
	}
	if len(ctx.Skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(ctx.Skills))
	}

	names := map[string]bool{}
	for _, s := range ctx.Skills {
		names[s.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected skills alpha and beta, got %v", names)
	}
}

func TestDetectContext_MultiSkillSiblingDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skill-a", "SKILL.md"), skillMD("skill-a"))
	writeFile(t, filepath.Join(root, "skill-b", "SKILL.md"), skillMD("skill-b"))

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextMultiSkill {
		t.Fatalf("expected ContextMultiSkill, got %d", ctx.Type)
	}
	if len(ctx.Skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(ctx.Skills))
	}

	names := map[string]bool{}
	for _, s := range ctx.Skills {
		names[s.Name] = true
	}
	if !names["skill-a"] || !names["skill-b"] {
		t.Errorf("expected skills skill-a and skill-b, got %v", names)
	}
}

func TestDetectContext_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	ctx, err := DetectContext(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextNone {
		t.Fatalf("expected ContextNone, got %d", ctx.Type)
	}
	if len(ctx.Skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(ctx.Skills))
	}
}

func TestFindEval_SeparatedConvention(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "my-skill", "SKILL.md"), skillMD("my-skill"))
	// Separated: {root}/evals/{name}/eval.yaml
	writeFile(t, filepath.Join(root, "evals", "my-skill", "eval.yaml"), "name: test\n")

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	evalPath, err := FindEval(ctx, "my-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join(root, "evals", "my-skill", "eval.yaml")
	if evalPath != expected {
		t.Errorf("expected %q, got %q", expected, evalPath)
	}
}

func TestFindEval_NestedSubdir(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "my-skill")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), skillMD("my-skill"))
	// Nested: {skill-dir}/evals/eval.yaml
	writeFile(t, filepath.Join(skillDir, "evals", "eval.yaml"), "name: test\n")

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	evalPath, err := FindEval(ctx, "my-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join(skillDir, "evals", "eval.yaml")
	if evalPath != expected {
		t.Errorf("expected %q, got %q", expected, evalPath)
	}
}

func TestFindEval_Colocated(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "my-skill")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), skillMD("my-skill"))
	// Co-located: {skill-dir}/eval.yaml
	writeFile(t, filepath.Join(skillDir, "eval.yaml"), "name: test\n")

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	evalPath, err := FindEval(ctx, "my-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join(skillDir, "eval.yaml")
	if evalPath != expected {
		t.Errorf("expected %q, got %q", expected, evalPath)
	}
}

func TestFindEval_PriorityOrder(t *testing.T) {
	// When all three locations exist, separated (priority 1) should win
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "my-skill")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), skillMD("my-skill"))
	writeFile(t, filepath.Join(root, "evals", "my-skill", "eval.yaml"), "separated\n")
	writeFile(t, filepath.Join(skillDir, "evals", "eval.yaml"), "nested\n")
	writeFile(t, filepath.Join(skillDir, "eval.yaml"), "colocated\n")

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	evalPath, err := FindEval(ctx, "my-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join(root, "evals", "my-skill", "eval.yaml")
	if evalPath != expected {
		t.Errorf("expected separated path %q, got %q", expected, evalPath)
	}
}

func TestFindEval_NotFound(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "my-skill", "SKILL.md"), skillMD("my-skill"))

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	evalPath, err := FindEval(ctx, "my-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if evalPath != "" {
		t.Errorf("expected empty string, got %q", evalPath)
	}
}

func TestFindSkill_Found(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "alpha", "SKILL.md"), skillMD("alpha"))
	writeFile(t, filepath.Join(root, "skills", "beta", "SKILL.md"), skillMD("beta"))

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	si, err := FindSkill(ctx, "alpha")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if si.Name != "alpha" {
		t.Errorf("expected 'alpha', got %q", si.Name)
	}
}

func TestFindSkill_NotFound(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "alpha", "SKILL.md"), skillMD("alpha"))

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = FindSkill(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFindEval_MixedLayouts(t *testing.T) {
	// Some skills use separated, some use co-located
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "separated-skill", "SKILL.md"), skillMD("separated-skill"))
	writeFile(t, filepath.Join(root, "evals", "separated-skill", "eval.yaml"), "separated\n")

	writeFile(t, filepath.Join(root, "skills", "colocated-skill", "SKILL.md"), skillMD("colocated-skill"))
	writeFile(t, filepath.Join(root, "skills", "colocated-skill", "eval.yaml"), "colocated\n")

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Separated skill should find separated eval
	sep, err := FindEval(ctx, "separated-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(root, "evals", "separated-skill", "eval.yaml")
	if sep != expected {
		t.Errorf("separated: expected %q, got %q", expected, sep)
	}

	// Co-located skill should find co-located eval
	col, err := FindEval(ctx, "colocated-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected = filepath.Join(root, "skills", "colocated-skill", "eval.yaml")
	if col != expected {
		t.Errorf("colocated: expected %q, got %q", expected, col)
	}
}

func TestDetectContext_SkillsDirHidden(t *testing.T) {
	// Hidden directories (starting with .) should be skipped
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".hidden", "SKILL.md"), skillMD("hidden-skill"))
	writeFile(t, filepath.Join(root, "visible", "SKILL.md"), skillMD("visible-skill"))

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextMultiSkill {
		t.Fatalf("expected ContextMultiSkill, got %d", ctx.Type)
	}
	if len(ctx.Skills) != 1 {
		t.Fatalf("expected 1 skill (hidden skipped), got %d", len(ctx.Skills))
	}
	if ctx.Skills[0].Name != "visible-skill" {
		t.Errorf("expected 'visible-skill', got %q", ctx.Skills[0].Name)
	}
}

func TestFindEval_SkillNotInContext(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &WorkspaceContext{
		Type: ContextNone,
		Root: tmpDir,
	}

	_, err := FindEval(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for skill not in context")
	}
}

func TestTryParseSkill_NoFrontmatter_FallsBackToDirName(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "my-cool-skill")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "# My Cool Skill\n\nNo frontmatter here.\n")

	info, ok := tryParseSkill(skillDir)
	if !ok {
		t.Fatal("tryParseSkill should return true when SKILL.md exists without frontmatter")
	}
	if info.Name != "my-cool-skill" {
		t.Errorf("expected name %q, got %q", "my-cool-skill", info.Name)
	}
	if info.Dir != skillDir {
		t.Errorf("expected dir %q, got %q", skillDir, info.Dir)
	}
}

func TestTryParseSkill_EmptyFrontmatterName_FallsBackToDirName(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "another-skill")
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), "---\nname: \"\"\n---\n# Another Skill\n")

	info, ok := tryParseSkill(skillDir)
	if !ok {
		t.Fatal("tryParseSkill should return true when SKILL.md has empty name in frontmatter")
	}
	if info.Name != "another-skill" {
		t.Errorf("expected name %q, got %q", "another-skill", info.Name)
	}
}
