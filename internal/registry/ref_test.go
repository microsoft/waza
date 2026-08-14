package registry

import "testing"

func TestParseRef(t *testing.T) {
	ref, err := ParseRef("github.com/waza-evals/fact/graders/factuality#strict@v1.2.3")
	if err != nil {
		t.Fatalf("ParseRef() error = %v", err)
	}
	if ref.Host != "github.com" || ref.Owner != "waza-evals" || ref.Repo != "fact" {
		t.Fatalf("unexpected module parts: %#v", ref)
	}
	if ref.Path != "graders/factuality" {
		t.Fatalf("Path = %q", ref.Path)
	}
	if ref.Export != "strict" {
		t.Fatalf("Export = %q", ref.Export)
	}
	if ref.Version != "v1.2.3" {
		t.Fatalf("Version = %q", ref.Version)
	}
}

func TestParseRefRequiresVersion(t *testing.T) {
	if _, err := ParseRef("github.com/waza-evals/fact#factuality"); err == nil {
		t.Fatalf("expected missing version error")
	}
}

func TestParseRefRejectsBackslashAndTraversal(t *testing.T) {
	for _, raw := range []string{
		`github.com/waza-evals/fact\..\evil#factuality@v1.0.0`,
		"github.com/waza-evals/fact/../evil#factuality@v1.0.0",
		"github.com/../fact#factuality@v1.0.0",
	} {
		if _, err := ParseRef(raw); err == nil {
			t.Fatalf("ParseRef(%q) expected error", raw)
		}
	}
}
