package nativesnapshot

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPinnedOriginalFixturesPreserveCheckoutBytes(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("checkout regression requires Git test infrastructure; the independent retry oracle does not: %v", err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	attributes, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{
		"internal/releasepolicy/testdata/journal_711867044.go.original",
		"internal/nativesnapshot/testdata/g2-original/nativesnapshot-files.go.original",
		"internal/nativesnapshot/testdata/g2-original/nativesnapshot-join.go.original",
		"internal/nativesnapshot/testdata/g2-original/nativesnapshot-snapshot.go.original",
		"internal/nativesnapshot/testdata/g2-original/orchestration-request_files.go.original",
		"internal/nativesnapshot/testdata/g2-original/orchestration-runner.go.original",
	}
	for _, mode := range []string{"true", "false"} {
		t.Run("autocrlf_"+mode, func(t *testing.T) {
			dir := t.TempDir()
			run := func(args ...string) []byte {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, git, args...)
				cmd.Dir = dir
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, output)
				}
				return output
			}
			run("init", "--quiet")
			run("config", "core.autocrlf", mode)
			if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), attributes, 0600); err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				path := filepath.FromSlash(path)
				original, err := os.ReadFile(filepath.Join(root, path))
				if err != nil {
					t.Fatal(err)
				}
				dest := filepath.Join(dir, path)
				if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dest, original, 0600); err != nil {
					t.Fatal(err)
				}
				run("add", "--", path)
				if err := os.Remove(dest); err != nil {
					t.Fatal(err)
				}
				run("checkout-index", "-f", "--", path)
				got, err := os.ReadFile(dest)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, original) {
					t.Fatalf("checkout changed pinned source bytes: %s", path)
				}
				fields := bytes.Split(run("check-attr", "-z", "text", "--", path), []byte{0})
				if len(fields) != 4 || string(fields[2]) != "unset" {
					t.Fatalf("pinned source text attribute is not unset: %s: %q", path, fields)
				}
			}
		})
	}
}
