package releasepolicy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixedArtifactSymlinksNeverPass(t *testing.T) {
	p := testPolicy(t, 8)
	original := collectTest(t, p, testCollector(p))
	if d, err := ReadSelectedDecision(original, p.Digest); err != nil || !d.Accepted {
		t.Fatalf("untampered selected collection must pass: %+v %v", d, err)
	}
	entries, err := os.ReadDir(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []string{"policy.json", "journal.json", "journal.ndjson",
		"baseline.begin.json", "candidate.begin.json", "baseline.final.json", "candidate.final.json",
		"baseline.result-binding.json", "candidate.result-binding.json",
		"baseline.results.json", "candidate.results.json"} {
		t.Run(artifact, func(t *testing.T) {
			directory := t.TempDir()
			for _, entry := range entries {
				if entry.Name() == artifact {
					if err := os.Symlink(filepath.Join(original, artifact), filepath.Join(directory, artifact)); err != nil {
						t.Skipf("symlinks unsupported: %v", err)
					}
					continue
				}
				data, err := os.ReadFile(filepath.Join(original, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, entry.Name()), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			d, err := ReadSelectedDecision(directory, p.Digest)
			if err == nil || d.Accepted || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("externally linked %s accepted: %+v %v", artifact, d, err)
			}
			if artifact == "journal.ndjson" {
				if err := os.Remove(filepath.Join(directory, "journal.json")); err != nil {
					t.Fatal(err)
				}
				d, err = ReadDecision(directory)
				if err == nil || d.Accepted || d.Compatibility.State != "invalid" {
					t.Fatalf("incomplete prefix followed symlink: %+v %v", d, err)
				}
			}
		})
	}
	other := testPolicy(t, 12)
	if d, err := ReadSelectedDecision(original, other.Digest); err == nil || d.Accepted || d.Compatibility.State != "mismatched" {
		t.Fatalf("different selected policy accepted: %+v %v", d, err)
	}
}

func TestFixedArtifactReaderRejectsNonFilesAndEscapes(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "policy.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"policy.json", "../policy.json", ".", ".."} {
		if _, err := readArtifact(directory, name); err == nil {
			t.Fatalf("invalid fixed artifact path accepted: %q", name)
		}
	}
	if _, err := readArtifact(directory, "missing.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing publication classification lost: %v", err)
	}
}
