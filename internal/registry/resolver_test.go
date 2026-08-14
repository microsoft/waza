package registry

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func TestResolverResolveAndExpandLockedGrader(t *testing.T) {
	repo := createModuleRepo(t)
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	resolver, err := NewResolver(
		WithCacheRoot(cacheRoot),
		WithGitURLFunc(func(Ref) string { return repo }),
	)
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}

	evalDir := t.TempDir()
	evalPath := filepath.Join(evalDir, "eval.yaml")
	specYAML := `name: remote-eval
skill: test
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
graders:
  - ref: example.com/acme/graders#factuality@v1.0.0
    name: strict_fact
    weight: 2
    config:
      not_contains: ["forbidden"]
metrics: []
tasks: []
`
	if err := os.WriteFile(evalPath, []byte(specYAML), 0o644); err != nil {
		t.Fatalf("write eval: %v", err)
	}

	lock, entries, err := resolver.ResolveEvalLock(context.Background(), evalPath)
	if err != nil {
		t.Fatalf("ResolveEvalLock() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	lockPath := filepath.Join(evalDir, models.LockfileName)
	if err := models.WriteLockfile(lockPath, lock); err != nil {
		t.Fatalf("WriteLockfile() error = %v", err)
	}

	spec, err := models.LoadEvalSpec(evalPath)
	if err != nil {
		t.Fatalf("LoadEvalSpec() error = %v", err)
	}
	if err := resolver.ExpandLockedGraders(context.Background(), spec, evalPath); err != nil {
		t.Fatalf("ExpandLockedGraders() error = %v", err)
	}
	if spec.Graders[0].Kind != models.GraderKindText {
		t.Fatalf("Kind = %q", spec.Graders[0].Kind)
	}
	if spec.Graders[0].Identifier != "strict_fact" {
		t.Fatalf("Identifier = %q", spec.Graders[0].Identifier)
	}
	if spec.Graders[0].Weight != 2 {
		t.Fatalf("Weight = %v", spec.Graders[0].Weight)
	}
	params, ok := spec.Graders[0].Parameters.(models.TextGraderParameters)
	if !ok {
		t.Fatalf("Parameters = %T", spec.Graders[0].Parameters)
	}
	if len(params.Contains) != 1 || params.Contains[0] != "supported" {
		t.Fatalf("remote contains not preserved: %#v", params)
	}
	if len(params.NotContains) != 1 || params.NotContains[0] != "forbidden" {
		t.Fatalf("local not_contains not merged: %#v", params)
	}
}

func TestExpandLockedGradersFailsWithoutLock(t *testing.T) {
	resolver, err := NewResolver(WithCacheRoot(t.TempDir()))
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}
	evalDir := t.TempDir()
	evalPath := filepath.Join(evalDir, "eval.yaml")
	specYAML := `name: remote-eval
skill: test
config:
  trials_per_task: 1
  timeout_seconds: 60
  executor: mock
graders:
  - ref: example.com/acme/graders#factuality@v1.0.0
metrics: []
tasks: []
`
	if err := os.WriteFile(evalPath, []byte(specYAML), 0o644); err != nil {
		t.Fatalf("write eval: %v", err)
	}
	spec, err := models.LoadEvalSpec(evalPath)
	if err != nil {
		t.Fatalf("LoadEvalSpec() error = %v", err)
	}
	err = resolver.ExpandLockedGraders(context.Background(), spec, evalPath)
	if err == nil || !strings.Contains(err.Error(), "waza.lock is missing") {
		t.Fatalf("expected missing lock error, got %v", err)
	}
}

func TestSafeJoinModulePathRejectsTraversal(t *testing.T) {
	for _, unsafe := range []string{"../outside.yaml", `..\outside.yaml`, "graders/../../outside.yaml"} {
		if _, err := safeJoinModulePath(t.TempDir(), unsafe); err == nil {
			t.Fatalf("expected traversal path %q to be rejected", unsafe)
		}
	}
}

func TestCacheDirRejectsUnsafeCommit(t *testing.T) {
	resolver, err := NewResolver(WithCacheRoot(t.TempDir()))
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}
	ref, err := ParseRef("example.com/acme/graders#factuality@v1.0.0")
	if err != nil {
		t.Fatalf("ParseRef() error = %v", err)
	}
	if _, err := resolver.cacheDir(ref, "../outside"); err == nil {
		t.Fatalf("expected invalid commit to be rejected")
	}
}

func TestExtractTarRejectsBackslashTraversal(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:     `..\outside.yaml`,
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len("bad")),
	}); err != nil {
		t.Fatalf("WriteHeader() error = %v", err)
	}
	if _, err := tw.Write([]byte("bad")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err := extractTar(bytes.NewReader(buf.Bytes()), t.TempDir()); err == nil {
		t.Fatalf("expected unsafe tar path to be rejected")
	}
}

func createModuleRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run(t, dir, "git", "init", "--quiet")
	run(t, dir, "git", "config", "user.email", "waza@example.com")
	run(t, dir, "git", "config", "user.name", "Waza Test")
	if err := os.MkdirAll(filepath.Join(dir, "graders"), 0o755); err != nil {
		t.Fatalf("mkdir graders: %v", err)
	}
	manifest := `schema_version: 1
module: example.com/acme/graders
exports:
  graders:
    factuality:
      path: graders/factuality.yaml
`
	grader := `type: text
name: factuality
config:
  contains: ["supported"]
`
	if err := os.WriteFile(filepath.Join(dir, "waza.registry.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "graders", "factuality.yaml"), []byte(grader), 0o644); err != nil {
		t.Fatalf("write grader: %v", err)
	}
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "--quiet", "-m", "initial")
	run(t, dir, "git", "tag", "v1.0.0")
	return dir
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v: %s", name, strings.Join(args, " "), err, out)
	}
}
