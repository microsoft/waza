package adversarial

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestListPacks_includesBuiltins(t *testing.T) {
	got := ListPacks()
	if len(got) == 0 {
		t.Fatal("ListPacks returned no packs; expected at least the two built-ins")
	}
	required := map[string]bool{
		"prompt-injection": false,
		"scope-bypass":     false,
	}
	for _, name := range got {
		if _, ok := required[name]; ok {
			required[name] = true
		}
		// Extra packs are allowed — this test must not break when new
		// built-in packs are added.
	}
	var missing []string
	for k, seen := range required {
		if !seen {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("missing built-in packs: %v", missing)
	}
}

func TestListPacks_sortedAndStable(t *testing.T) {
	got := ListPacks()
	sorted := append([]string(nil), got...)
	sort.Strings(sorted)
	for i := range got {
		if got[i] != sorted[i] {
			t.Fatalf("ListPacks not sorted: %v", got)
		}
	}
	// Calling twice returns the same slice (no surprises).
	again := ListPacks()
	if len(again) != len(got) {
		t.Fatalf("ListPacks not stable across calls: %d vs %d", len(again), len(got))
	}
}

func TestLoadPack_unknown(t *testing.T) {
	_, err := LoadPack("does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown pack")
	}
}

func TestLoadPack_promptInjection(t *testing.T) {
	p, err := LoadPack("prompt-injection")
	if err != nil {
		t.Fatalf("LoadPack: %v", err)
	}
	if p.Manifest.Name != "prompt-injection" {
		t.Errorf("manifest.name = %q, want prompt-injection", p.Manifest.Name)
	}
	if len(p.Manifest.Tasks) != 4 {
		t.Errorf("expected 4 tasks, got %d", len(p.Manifest.Tasks))
	}
	if p.Manifest.Title == "" {
		t.Error("manifest.title is empty")
	}
}

func TestLoadPack_scopeBypass(t *testing.T) {
	p, err := LoadPack("scope-bypass")
	if err != nil {
		t.Fatalf("LoadPack: %v", err)
	}
	if got := len(p.Manifest.Tasks); got != 4 {
		t.Errorf("expected 4 tasks, got %d", got)
	}
}

func TestPack_Extract_writesEveryFile(t *testing.T) {
	p, err := LoadPack("prompt-injection")
	if err != nil {
		t.Fatalf("LoadPack: %v", err)
	}
	dst := t.TempDir()
	root, err := p.Extract(dst)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if filepath.Base(root) != "prompt-injection" {
		t.Errorf("Extract root = %q, want a directory named prompt-injection", root)
	}
	// pack.yaml is the canonical manifest file inside every pack.
	if _, err := os.Stat(filepath.Join(root, "pack.yaml")); err != nil {
		t.Errorf("pack.yaml missing after extract: %v", err)
	}
	// Each declared task file must exist on disk after extract.
	for _, rel := range p.Manifest.Tasks {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("task file missing after extract: %s: %v", rel, err)
		}
	}
	// fixtures/ must contain at least one file.
	matches, err := filepath.Glob(filepath.Join(root, "fixtures", "*"))
	if err != nil {
		t.Fatalf("glob fixtures: %v", err)
	}
	if len(matches) == 0 {
		t.Error("fixtures/ is empty after extract")
	}
}

// TestPack_AllTasksAreGolden enforces the invariant documented in the
// adversarial package: every embedded task must be golden so that
// `waza gate` and `waza adversarial` agree on what counts as a hard
// failure.
func TestPack_AllTasksAreGolden(t *testing.T) {
	for _, name := range ListPacks() {
		p, err := LoadPack(name)
		if err != nil {
			t.Fatalf("LoadPack(%q): %v", name, err)
		}
		dst := t.TempDir()
		root, err := p.Extract(dst)
		if err != nil {
			t.Fatalf("Extract(%q): %v", name, err)
		}
		for _, rel := range p.Manifest.Tasks {
			path := filepath.Join(root, filepath.FromSlash(rel))
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			// Minimal struct — we only care that golden is true so we
			// don't import the full TestCase type here and create a
			// dependency cycle.
			var tc struct {
				ID     string `yaml:"id"`
				Golden bool   `yaml:"golden"`
			}
			if err := yaml.Unmarshal(b, &tc); err != nil {
				t.Fatalf("yaml parse %s: %v", path, err)
			}
			if tc.ID == "" {
				t.Errorf("%s: missing id", path)
			}
			if !tc.Golden {
				t.Errorf("%s: every adversarial task must be golden:true", path)
			}
		}
	}
}
