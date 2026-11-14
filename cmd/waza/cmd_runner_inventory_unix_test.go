//go:build unix

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRunnerInventoryRejectsWriterlessFIFOs(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	embedded := filepath.Join(dir, "embedded")
	if err := os.Mkdir(embedded, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(embedded, "zcopilot_1.0.0_linux-x64.tgz"), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	inventoryPath := filepath.Join(dir, "plugin.cdx.json")
	if _, code := runRunnerCLI(t, "inventory", "generate", "--subject", "plugin", "--name", "p", "--digest", digest,
		"--file", "../../go.mod", "--out", inventoryPath); code != 0 {
		t.Fatalf("generate fixture inventory: exit %d", code)
	}
	for name, args := range map[string][]string{
		"subject file": {"inventory", "generate", "--subject", "plugin", "--name", "p", "--subject-file", fifo, "--file", "../../go.mod"},
		"go binary":    {"inventory", "generate", "--name", "r", "--digest", digest, "--go-binary", fifo},
		"file":         {"inventory", "generate", "--name", "r", "--digest", digest, "--file", fifo},
		"embedded":     {"inventory", "generate", "--name", "r", "--digest", digest, "--go-binary", self, "--embedded-dir", embedded},
		"plugin":       {"inventory", "verify", "--inventory", inventoryPath, "--plugin", fifo},
	} {
		t.Run(name, func(t *testing.T) {
			if args[1] == "generate" {
				args = append(args, "--out", filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".cdx.json"))
			}
			done := make(chan error, 1)
			go func() {
				cmd := newRunnerCommand()
				cmd.SetOut(&bytes.Buffer{})
				cmd.SetErr(&bytes.Buffer{})
				cmd.SetArgs(args)
				done <- cmd.Execute()
			}()
			select {
			case err := <-done:
				var exit *ExitCodeError
				if !errors.As(err, &exit) || exit.Code != 2 {
					t.Fatalf("err = %v; want exit 2", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("opening a writerless FIFO blocked")
			}
		})
	}
}
