//go:build unix

package workspace

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// makeFIFO creates a FIFO and, on cleanup, unblocks any reader that a
// regression left waiting on it.
func makeFIFO(t *testing.T, path string) {
	t.Helper()
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo %s: %v", path, err)
	}
	t.Cleanup(func() {
		if f, err := os.OpenFile(path, os.O_WRONLY|unix.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	})
}

func TestDetectContextRejectsNonRegularSkillDefinitions(t *testing.T) {
	writeSkill := func(t *testing.T, dir, name string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: test\n---\n# " + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{
			name: "linked directory with FIFO SKILL.md",
			setup: func(t *testing.T, root string) {
				resources := filepath.Join(root, "resources")
				if err := os.MkdirAll(resources, 0o755); err != nil {
					t.Fatal(err)
				}
				makeFIFO(t, filepath.Join(resources, "SKILL.md"))
				if err := os.Symlink(filepath.Join("..", "resources"), filepath.Join(root, "skills", "docs")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "linked directory with FIFO agent definition",
			setup: func(t *testing.T, root string) {
				resources := filepath.Join(root, "resources")
				if err := os.MkdirAll(resources, 0o755); err != nil {
					t.Fatal(err)
				}
				makeFIFO(t, filepath.Join(resources, "docs.agent.md"))
				if err := os.Symlink(filepath.Join("..", "resources"), filepath.Join(root, "skills", "docs")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "skill directory with FIFO SKILL.md",
			setup: func(t *testing.T, root string) {
				dir := filepath.Join(root, "skills", "docs")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				makeFIFO(t, filepath.Join(dir, "SKILL.md"))
			},
		},
		{
			name: "skill directory with FIFO agent definition",
			setup: func(t *testing.T, root string) {
				dir := filepath.Join(root, "skills", "docs")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				makeFIFO(t, filepath.Join(dir, "docs.agent.md"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeSkill(t, filepath.Join(root, "skills", "alpha"), "alpha")
			tt.setup(t, root)

			type result struct {
				ctx *WorkspaceContext
				err error
			}
			done := make(chan result, 1)
			go func() {
				ctx, err := DetectContext(root, WithDiscoverySymlinkValidation())
				done <- result{ctx, err}
			}()

			var got result
			select {
			case got = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("DetectContext blocked reading a non-regular skill definition")
			}
			if got.err != nil {
				t.Fatalf("DetectContext: %v", got.err)
			}
			if got.ctx == nil || len(got.ctx.Skills) != 1 || got.ctx.Skills[0].Name != "alpha" {
				t.Fatalf("expected only the regular alpha skill, got %+v", got.ctx)
			}
		})
	}
}
