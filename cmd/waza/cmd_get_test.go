package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

type fakeGetResolver struct {
	lock    *models.Lockfile
	entries []models.LockfileGrader
}

func (f fakeGetResolver) ResolveEvalLock(context.Context, string) (*models.Lockfile, []models.LockfileGrader, error) {
	return f.lock, f.entries, nil
}

func (f fakeGetResolver) ResolveRefs(context.Context, []string) ([]models.LockfileGrader, error) {
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
		Digest: "sha256:abc123",
		URL:    "https://github.com/waza-evals/fact.git",
	}
	lock.UpsertGrader(entry)
	var out bytes.Buffer

	err := runGet(&out, context.Background(), fakeGetResolver{lock: lock, entries: []models.LockfileGrader{entry}}, entry.Ref)
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
