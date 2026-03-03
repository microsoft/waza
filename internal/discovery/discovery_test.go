package discovery

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// setupSkillDir creates a SKILL.md in the given directory.
func setupSkillDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Test Skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupEvalFile creates an eval.yaml at the given path.
func setupEvalFile(t *testing.T, path string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("name: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverMultipleSkills(t *testing.T) {
	root := t.TempDir()

	// skill-a: has tests/eval.yaml
	setupSkillDir(t, filepath.Join(root, "skill-a"))
	setupEvalFile(t, filepath.Join(root, "skill-a", "tests", "eval.yaml"))

	// skill-b: has eval.yaml in root
	setupSkillDir(t, filepath.Join(root, "skill-b"))
	setupEvalFile(t, filepath.Join(root, "skill-b", "eval.yaml"))

	skills, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	// Sort for deterministic ordering
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })

	if skills[0].Name != "skill-a" {
		t.Errorf("expected skill-a, got %s", skills[0].Name)
	}
	if !skills[0].HasEval() {
		t.Error("skill-a should have eval")
	}
	if filepath.Base(filepath.Dir(skills[0].EvalPath)) != "tests" {
		t.Error("skill-a eval should be in tests/ subdir")
	}

	if skills[1].Name != "skill-b" {
		t.Errorf("expected skill-b, got %s", skills[1].Name)
	}
	if !skills[1].HasEval() {
		t.Error("skill-b should have eval")
	}
}

func TestDiscoverNestedDirectories(t *testing.T) {
	root := t.TempDir()

	// Nested: root/category/deep-skill/SKILL.md
	setupSkillDir(t, filepath.Join(root, "category", "deep-skill"))
	setupEvalFile(t, filepath.Join(root, "category", "deep-skill", "eval.yaml"))

	skills, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "deep-skill" {
		t.Errorf("expected deep-skill, got %s", skills[0].Name)
	}
	if !skills[0].HasEval() {
		t.Error("deep-skill should have eval")
	}
}

func TestDiscoverSkillMissingEval(t *testing.T) {
	root := t.TempDir()

	// Skill with SKILL.md but no eval.yaml
	setupSkillDir(t, filepath.Join(root, "no-eval-skill"))

	skills, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].HasEval() {
		t.Error("no-eval-skill should NOT have eval")
	}
	if skills[0].EvalPath != "" {
		t.Error("EvalPath should be empty")
	}
}

func TestDiscoverSkipsHiddenDirs(t *testing.T) {
	root := t.TempDir()

	// Hidden directory with a skill — should be skipped
	setupSkillDir(t, filepath.Join(root, ".hidden", "secret-skill"))
	setupEvalFile(t, filepath.Join(root, ".hidden", "secret-skill", "eval.yaml"))

	// Visible skill
	setupSkillDir(t, filepath.Join(root, "visible-skill"))
	setupEvalFile(t, filepath.Join(root, "visible-skill", "eval.yaml"))

	skills, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill (hidden skipped), got %d", len(skills))
	}
	if skills[0].Name != "visible-skill" {
		t.Errorf("expected visible-skill, got %s", skills[0].Name)
	}
}

func TestDiscoverEmptyDirectory(t *testing.T) {
	root := t.TempDir()

	skills, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(skills) != 0 {
		t.Fatalf("expected 0 skills, got %d", len(skills))
	}
}

func TestDiscoverTestsEvalTakesPriority(t *testing.T) {
	root := t.TempDir()

	// Skill with both tests/eval.yaml AND eval.yaml — tests/ should win
	setupSkillDir(t, filepath.Join(root, "both-skill"))
	setupEvalFile(t, filepath.Join(root, "both-skill", "tests", "eval.yaml"))
	setupEvalFile(t, filepath.Join(root, "both-skill", "eval.yaml"))

	skills, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if filepath.Base(filepath.Dir(skills[0].EvalPath)) != "tests" {
		t.Error("tests/eval.yaml should take priority over eval.yaml")
	}
}

func TestDiscoverEvalsSubdir(t *testing.T) {
	root := t.TempDir()

	setupSkillDir(t, filepath.Join(root, "evals-skill"))
	setupEvalFile(t, filepath.Join(root, "evals-skill", "evals", "eval.yaml"))

	skills, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if filepath.Base(filepath.Dir(skills[0].EvalPath)) != "evals" {
		t.Error("evals/eval.yaml should be discovered")
	}
}

func TestDiscoverProjectLayout(t *testing.T) {
	// Simulates the layout produced by `waza new` in project mode:
	//   project-root/skills/{name}/SKILL.md
	//   project-root/evals/{name}/eval.yaml
	root := t.TempDir()

	setupSkillDir(t, filepath.Join(root, "skills", "my-skill"))
	setupEvalFile(t, filepath.Join(root, "evals", "my-skill", "eval.yaml"))

	skills, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	if skills[0].Name != "my-skill" {
		t.Errorf("expected my-skill, got %s", skills[0].Name)
	}
	if !skills[0].HasEval() {
		t.Error("my-skill should have eval (project layout evals/{name}/eval.yaml)")
	}
	wantEvalPath := filepath.Join(root, "evals", "my-skill", "eval.yaml")
	if skills[0].EvalPath != wantEvalPath {
		t.Errorf("expected EvalPath %s, got %s", wantEvalPath, skills[0].EvalPath)
	}
}

func TestDiscoverNonexistentRoot(t *testing.T) {
	_, err := Discover("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Error("expected error for nonexistent root")
	}
}

func TestFilterWithEval(t *testing.T) {
	root := t.TempDir()

	setupSkillDir(t, filepath.Join(root, "has-eval"))
	setupEvalFile(t, filepath.Join(root, "has-eval", "eval.yaml"))

	setupSkillDir(t, filepath.Join(root, "no-eval"))

	skills, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}

	withEval := FilterWithEval(skills)
	if len(withEval) != 1 {
		t.Fatalf("expected 1 skill with eval, got %d", len(withEval))
	}
	if withEval[0].Name != "has-eval" {
		t.Errorf("expected has-eval, got %s", withEval[0].Name)
	}

	withoutEval := FilterWithoutEval(skills)
	if len(withoutEval) != 1 {
		t.Fatalf("expected 1 skill without eval, got %d", len(withoutEval))
	}
	if withoutEval[0].Name != "no-eval" {
		t.Errorf("expected no-eval, got %s", withoutEval[0].Name)
	}
}
