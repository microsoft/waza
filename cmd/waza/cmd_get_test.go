package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

type fakeGetResolver struct {
	lock    *models.Lockfile
	entries []models.LockfileGrader
	evals   []string
	refs    []string
}

func (f *fakeGetResolver) ResolveEvalLock(_ context.Context, evalPath string) (*models.Lockfile, []models.LockfileGrader, error) {
	f.evals = append(f.evals, evalPath)
	return f.lock, f.entries, nil
}

func (f *fakeGetResolver) ResolveRefs(_ context.Context, refs []string) ([]models.LockfileGrader, error) {
	f.refs = append(f.refs, refs...)
	return f.entries, nil
}

func TestRootCommandHasGetSubcommand(t *testing.T) {
	root := newRootCommand()
	for _, cmd := range root.Commands() {
		if cmd.Name() == "get" {
			return
		}
	}
	t.Fatalf("root command should have get subcommand")
}

func TestRunGetWritesLockForSingleRef(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	lock := models.NewLockfile()
	entry := models.LockfileGrader{
		Ref:    "github.com/waza-evals/fact#factuality@v1.0.0",
		Commit: "0123456789abcdef0123456789abcdef01234567",
		Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		URL:    "https://github.com/waza-evals/fact.git",
	}
	lock.UpsertGrader(entry)
	var out bytes.Buffer

	err := runGet(&out, context.Background(), &fakeGetResolver{lock: lock, entries: []models.LockfileGrader{entry}}, entry.Ref)
	if err != nil {
		t.Fatalf("runGet() error = %v", err)
	}
	if out.String() != "Resolved 1 remote grader ref(s); wrote waza.lock\n" {
		t.Fatalf("output = %q", out.String())
	}
	loaded, err := models.LoadLockfile(models.LockfileName)
	if err != nil {
		t.Fatalf("LoadLockfile() error = %v", err)
	}
	if _, ok := loaded.Grader(entry.Ref); !ok {
		t.Fatalf("expected lock entry for %s", entry.Ref)
	}
}

func TestRunGetTreatsExistingNonYAMLFileAsRef(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("README.md", []byte("not an eval"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	resolver := &fakeGetResolver{
		entries: []models.LockfileGrader{{
			Ref:    "README.md",
			Commit: "0123456789abcdef0123456789abcdef01234567",
			Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			URL:    "https://github.com/waza-evals/fact.git",
		}},
	}

	err := runGet(ioDiscard{}, context.Background(), resolver, "README.md")
	if err != nil {
		t.Fatalf("runGet() error = %v", err)
	}
	if len(resolver.evals) != 0 {
		t.Fatalf("ResolveEvalLock called for non-YAML file: %v", resolver.evals)
	}
	if len(resolver.refs) != 1 || resolver.refs[0] != "README.md" {
		t.Fatalf("ResolveRefs = %v", resolver.refs)
	}
	if _, err := os.Stat(filepath.Join(dir, models.LockfileName)); err != nil {
		t.Fatalf("expected lockfile: %v", err)
	}
}

func TestIsEvalYAMLTargetPrefersParseableRefs(t *testing.T) {
	target := "github.com/acme/graders/rubrics/factuality.yaml@v1.0.0"
	if isEvalYAMLTarget(target) {
		t.Fatalf("parseable ref ending in .yaml must not be treated as eval file")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}
