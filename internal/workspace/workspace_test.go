package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestDetectContext_MultiSkillNestedCategories(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "development", "skill-creator", "SKILL.md"), skillMD("skill-creator"))
	writeFile(t, filepath.Join(root, "skills", "testing", "eval-harness", "SKILL.md"), skillMD("eval-harness"))
	writeFile(t, filepath.Join(root, "skills", "vendor", "ignored", "SKILL.md"), skillMD("ignored"))

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextMultiSkill {
		t.Fatalf("expected ContextMultiSkill, got %d", ctx.Type)
	}

	names := map[string]bool{}
	for _, s := range ctx.Skills {
		names[s.Name] = true
	}
	if !names["skill-creator"] || !names["eval-harness"] {
		t.Errorf("expected nested skills skill-creator and eval-harness, got %v", names)
	}
	if names["ignored"] {
		t.Errorf("did not expect skills under vendor to be discovered: %v", names)
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

func TestDetectContext_WithCustomSkillsDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "my-skills", "alpha", "SKILL.md"), skillMD("alpha"))

	// Default detection should NOT find skills under my-skills/
	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextNone {
		t.Fatalf("expected ContextNone, got %v", ctx.Type)
	}

	// With custom skills dir, should find skills
	ctx, err = DetectContext(root, WithSkillsDir("my-skills"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextMultiSkill {
		t.Fatalf("expected ContextMultiSkill, got %v", ctx.Type)
	}
	if len(ctx.Skills) != 1 || ctx.Skills[0].Name != "alpha" {
		t.Errorf("expected 1 skill named 'alpha', got %v", ctx.Skills)
	}
}

func TestFindEval_WithCustomEvalsDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "my-skills", "my-skill", "SKILL.md"), skillMD("my-skill"))
	writeFile(t, filepath.Join(root, "my-evals", "my-skill", "eval.yaml"), "name: test\n")

	ctx, err := DetectContext(root, WithSkillsDir("my-skills"), WithEvalsDir("my-evals"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.EvalsDir != "my-evals" {
		t.Fatalf("expected EvalsDir 'my-evals', got %q", ctx.EvalsDir)
	}

	evalPath, err := FindEval(ctx, "my-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(root, "my-evals", "my-skill", "eval.yaml")
	if evalPath != expected {
		t.Errorf("expected %q, got %q", expected, evalPath)
	}
}

func TestFindEval_WithCustomEvalFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "my-skill", "SKILL.md"), skillMD("my-skill"))
	writeFile(t, filepath.Join(root, "evals", "my-skill", "waza-eval.yaml"), "name: test\n")

	ctx, err := DetectContext(root, WithEvalFile("waza-eval.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.EvalFile != "waza-eval.yaml" {
		t.Fatalf("expected EvalFile 'waza-eval.yaml', got %q", ctx.EvalFile)
	}

	evalPath, err := FindEval(ctx, "my-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(root, "evals", "my-skill", "waza-eval.yaml")
	if evalPath != expected {
		t.Errorf("expected %q, got %q", expected, evalPath)
	}
}

func TestFindEval_CustomEvalFileFallsBackToLegacy(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "my-skill", "SKILL.md"), skillMD("my-skill"))
	writeFile(t, filepath.Join(root, "evals", "my-skill", "eval.yaml"), "name: test\n")

	ctx, err := DetectContext(root, WithEvalFile("waza-eval.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	evalPath, err := FindEval(ctx, "my-skill")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(root, "evals", "my-skill", "eval.yaml")
	if evalPath != expected {
		t.Errorf("expected legacy fallback %q, got %q", expected, evalPath)
	}
}

func TestDetectContext_GitHubSkillsDir(t *testing.T) {
	// Skills in .github/skills/ are auto-discovered
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".github", "skills", "github-skill", "SKILL.md"), skillMD("github-skill"))

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextMultiSkill {
		t.Fatalf("expected ContextMultiSkill, got %d", ctx.Type)
	}
	if len(ctx.Skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(ctx.Skills))
	}
	if ctx.Skills[0].Name != "github-skill" {
		t.Errorf("expected 'github-skill', got %q", ctx.Skills[0].Name)
	}
}

func TestDetectContext_BothSkillsDirs(t *testing.T) {
	// Skills in both skills/ and .github/skills/ are merged
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "regular-skill", "SKILL.md"), skillMD("regular-skill"))
	writeFile(t, filepath.Join(root, ".github", "skills", "github-skill", "SKILL.md"), skillMD("github-skill"))

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
	if !names["regular-skill"] || !names["github-skill"] {
		t.Errorf("expected skills regular-skill and github-skill, got %v", names)
	}
}

func TestDetectContext_GitHubSkillsDirDedup(t *testing.T) {
	// Same skill name in both dirs: configured skills/ wins
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "shared-name", "SKILL.md"), skillMD("shared-name"))
	writeFile(t, filepath.Join(root, ".github", "skills", "shared-name", "SKILL.md"), skillMD("shared-name"))

	ctx, err := DetectContext(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextMultiSkill {
		t.Fatalf("expected ContextMultiSkill, got %d", ctx.Type)
	}
	if len(ctx.Skills) != 1 {
		t.Fatalf("expected 1 skill (deduped), got %d", len(ctx.Skills))
	}
	if ctx.Skills[0].Name != "shared-name" {
		t.Errorf("expected 'shared-name', got %q", ctx.Skills[0].Name)
	}
	// Verify that skills/ directory won (not .github/skills/)
	expectedDir := filepath.Join(root, "skills", "shared-name")
	if ctx.Skills[0].Dir != expectedDir {
		t.Errorf("expected dir %q (configured should win), got %q", expectedDir, ctx.Skills[0].Dir)
	}
}

func TestDetectContext_GitHubSkillsDirWithCustomOverride(t *testing.T) {
	// Custom paths.skills + .github/skills/ both work
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "my-skills", "custom-skill", "SKILL.md"), skillMD("custom-skill"))
	writeFile(t, filepath.Join(root, ".github", "skills", "github-skill", "SKILL.md"), skillMD("github-skill"))

	ctx, err := DetectContext(root, WithSkillsDir("my-skills"))
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
	if !names["custom-skill"] || !names["github-skill"] {
		t.Errorf("expected skills custom-skill and github-skill, got %v", names)
	}
}

func TestDetectContext_GitHubSkillsDirCustomPathNoDuplicateScan(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".github", "skills", "github-skill", "SKILL.md"), skillMD("github-skill"))

	ctx, err := DetectContext(root, WithSkillsDir(filepath.Join(".github", "skills")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ctx.Skills) != 1 {
		t.Fatalf("expected 1 skill without duplicate scan, got %d", len(ctx.Skills))
	}
	if ctx.Skills[0].Name != "github-skill" {
		t.Errorf("expected 'github-skill', got %q", ctx.Skills[0].Name)
	}
}

func TestSamePath(t *testing.T) {
	root := t.TempDir()

	if !samePath(filepath.Join(root, "a", "..", "b"), filepath.Join(root, "b")) {
		t.Fatal("expected cleaned equivalent paths to match")
	}

	targetDir := filepath.Join(root, "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("creating target dir: %v", err)
	}
	linkDir := filepath.Join(root, "link")
	if err := os.Symlink(targetDir, linkDir); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}
	if !samePath(linkDir, targetDir) {
		t.Fatal("expected symlink path and target path to match")
	}

	if runtime.GOOS == "windows" {
		if !samePath(strings.ToUpper(targetDir), strings.ToLower(targetDir)) {
			t.Fatal("expected path comparison to be case-insensitive on windows")
		}
	}

	if samePath(filepath.Join(root, "b"), filepath.Join(root, "c")) {
		t.Fatal("expected different paths to not match")
	}
}

func TestMergeSkillsByName(t *testing.T) {
	base := []SkillInfo{
		{Name: "base"},
		{Name: "shared", Dir: "skills-shared"},
	}
	additional := []SkillInfo{
		{Name: "shared", Dir: "github-shared"},
		{Name: "github-only"},
	}

	merged := mergeSkillsByName(base, additional)
	if len(merged) != 3 {
		t.Fatalf("expected 3 merged skills, got %d", len(merged))
	}
	if merged[1].Dir != "skills-shared" {
		t.Fatalf("expected base skill to win for duplicates, got %q", merged[1].Dir)
	}
	if merged[2].Name != "github-only" {
		t.Fatalf("expected github-only to be appended, got %q", merged[2].Name)
	}
}

// --- APM-compiled layout (.apm/skills/<name>/SKILL.md) ---
// See https://github.com/microsoft/waza/issues/400

func TestDetectContext_APMSingleSkillFromSkillDir(t *testing.T) {
	// APM layout: skills/my-skill/.apm/skills/my-skill/SKILL.md, with no
	// top-level SKILL.md in the user-facing skill folder. Running detection
	// from the skill folder itself must find the compiled skill.
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "my-skill")
	writeFile(t, filepath.Join(skillDir, ".apm", "skills", "my-skill", "SKILL.md"), skillMD("my-skill"))

	ctx, err := DetectContext(skillDir)
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
	// Dir should stay the user-facing skill folder, not the .apm output,
	// so evals/fixtures continue to resolve correctly.
	if ctx.Skills[0].Dir != skillDir {
		t.Errorf("expected dir %q, got %q", skillDir, ctx.Skills[0].Dir)
	}
	expectedSkillPath := filepath.Join(skillDir, ".apm", "skills", "my-skill", "SKILL.md")
	if ctx.Skills[0].SkillPath != expectedSkillPath {
		t.Errorf("expected SkillPath %q, got %q", expectedSkillPath, ctx.Skills[0].SkillPath)
	}
}

func TestDetectContext_APMMultiSkillFromWorkspaceRoot(t *testing.T) {
	// Detection from the workspace root should discover APM-compiled skills
	// underneath skills/*/ even though .apm is a dotfolder we normally skip.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "alpha", ".apm", "skills", "alpha", "SKILL.md"), skillMD("alpha"))
	writeFile(t, filepath.Join(root, "skills", "beta", ".apm", "skills", "beta", "SKILL.md"), skillMD("beta"))

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
		t.Errorf("expected alpha and beta, got %v", names)
	}
}

func TestDetectContext_APMTopLevelSkillTakesPrecedence(t *testing.T) {
	// When both a top-level SKILL.md and a .apm/skills/<name>/SKILL.md exist,
	// the top-level one wins (it's the source of truth being edited).
	skillDir := t.TempDir()
	writeFile(t, filepath.Join(skillDir, "SKILL.md"), skillMD("top-level"))
	writeFile(t, filepath.Join(skillDir, ".apm", "skills", filepath.Base(skillDir), "SKILL.md"), skillMD("apm-compiled"))

	ctx, err := DetectContext(skillDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextSingleSkill {
		t.Fatalf("expected ContextSingleSkill, got %d", ctx.Type)
	}
	if ctx.Skills[0].Name != "top-level" {
		t.Errorf("expected top-level SKILL.md to win, got name %q", ctx.Skills[0].Name)
	}
	expected := filepath.Join(skillDir, "SKILL.md")
	if ctx.Skills[0].SkillPath != expected {
		t.Errorf("expected SkillPath %q, got %q", expected, ctx.Skills[0].SkillPath)
	}
}

func TestDetectContext_APMPrefersMatchingBasename(t *testing.T) {
	// If a skill folder's .apm/skills/ contains multiple compiled entries
	// (e.g., transitive deps compiled alongside), prefer the one whose name
	// matches the parent folder's basename.
	root := t.TempDir()
	skillDir := filepath.Join(root, "primary")
	writeFile(t, filepath.Join(skillDir, ".apm", "skills", "primary", "SKILL.md"), skillMD("primary"))
	writeFile(t, filepath.Join(skillDir, ".apm", "skills", "helper", "SKILL.md"), skillMD("helper"))

	ctx, err := DetectContext(skillDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != ContextSingleSkill {
		t.Fatalf("expected ContextSingleSkill, got %d", ctx.Type)
	}
	if ctx.Skills[0].Name != "primary" {
		t.Errorf("expected 'primary' to be chosen, got %q", ctx.Skills[0].Name)
	}
}

func TestDetectContext_APMMixedWithClassicLayout(t *testing.T) {
	// A workspace where one skill is classic and another is APM-installed
	// should surface both.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "skills", "classic", "SKILL.md"), skillMD("classic"))
	writeFile(t, filepath.Join(root, "skills", "apm-installed", ".apm", "skills", "apm-installed", "SKILL.md"), skillMD("apm-installed"))

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
	if !names["classic"] || !names["apm-installed"] {
		t.Errorf("expected both classic and apm-installed, got %v", names)
	}
}
