package scaffold

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileWriter_CreateIfMissing(t *testing.T) {
	dir := t.TempDir()
	fw := NewFileWriter(dir)

	entries := []FileEntry{
		{Path: filepath.Join(dir, "skills"), Label: "Skill definitions", IsDir: true},
		{Path: filepath.Join(dir, "evals"), Label: "Evaluation suites", IsDir: true},
		{Path: filepath.Join(dir, ".waza.yaml"), Label: "Config", Content: "engine: copilot\n"},
		{Path: filepath.Join(dir, "README.md"), Label: "Readme", Content: "# Hello\n"},
	}

	inv, err := fw.Write(entries)
	require.NoError(t, err)

	// All items should be created
	assert.Equal(t, 4, inv.CreatedCount())
	for _, item := range inv.Items {
		assert.Equal(t, OutcomeCreated, item.Outcome, "expected %s to be created", item.RelPath)
	}

	// Verify files/dirs actually exist
	assert.DirExists(t, filepath.Join(dir, "skills"))
	assert.DirExists(t, filepath.Join(dir, "evals"))
	assert.FileExists(t, filepath.Join(dir, ".waza.yaml"))
	assert.FileExists(t, filepath.Join(dir, "README.md"))

	// Verify content
	data, err := os.ReadFile(filepath.Join(dir, ".waza.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "engine: copilot\n", string(data))
}

func TestFileWriter_SkipIfExists(t *testing.T) {
	dir := t.TempDir()

	// Pre-create files and directories
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "skills"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Custom\n"), 0o644))

	fw := NewFileWriter(dir)
	entries := []FileEntry{
		{Path: filepath.Join(dir, "skills"), Label: "Skill definitions", IsDir: true},
		{Path: filepath.Join(dir, "README.md"), Label: "Readme", Content: "# Generated\n"},
	}

	inv, err := fw.Write(entries)
	require.NoError(t, err)

	// Both should be skipped
	assert.Equal(t, 0, inv.CreatedCount())
	for _, item := range inv.Items {
		assert.Equal(t, OutcomeSkipped, item.Outcome, "expected %s to be skipped", item.RelPath)
	}

	// Verify existing content was NOT overwritten
	data, err := os.ReadFile(filepath.Join(dir, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "# Custom\n", string(data))
}

func TestFileWriter_MixedCreateAndSkip(t *testing.T) {
	dir := t.TempDir()

	// Pre-create only the README
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Existing\n"), 0o644))

	fw := NewFileWriter(dir)
	entries := []FileEntry{
		{Path: filepath.Join(dir, "skills"), Label: "Skill definitions", IsDir: true},
		{Path: filepath.Join(dir, "README.md"), Label: "Readme", Content: "# New\n"},
		{Path: filepath.Join(dir, ".gitignore"), Label: "Gitignore", Content: "results/\n"},
	}

	inv, err := fw.Write(entries)
	require.NoError(t, err)

	assert.Equal(t, 2, inv.CreatedCount())
	assert.Equal(t, OutcomeCreated, inv.Items[0].Outcome) // skills dir
	assert.Equal(t, OutcomeSkipped, inv.Items[1].Outcome) // README.md
	assert.Equal(t, OutcomeCreated, inv.Items[2].Outcome) // .gitignore
}

func TestFileWriter_CreatesParentDirectories(t *testing.T) {
	dir := t.TempDir()
	fw := NewFileWriter(dir)

	entries := []FileEntry{
		{Path: filepath.Join(dir, ".github", "workflows", "eval.yml"), Label: "CI", Content: "name: eval\n"},
	}

	inv, err := fw.Write(entries)
	require.NoError(t, err)

	assert.Equal(t, 1, inv.CreatedCount())
	assert.FileExists(t, filepath.Join(dir, ".github", "workflows", "eval.yml"))
}

func TestFileWriter_InventoryOutput(t *testing.T) {
	dir := t.TempDir()

	// Pre-create README
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hi\n"), 0o644))

	fw := NewFileWriter(dir)
	entries := []FileEntry{
		{Path: filepath.Join(dir, "skills"), Label: "Skill definitions", IsDir: true},
		{Path: filepath.Join(dir, "README.md"), Label: "Readme", Content: "# New\n"},
	}

	inv, err := fw.Write(entries)
	require.NoError(t, err)

	var buf bytes.Buffer
	inv.Fprint(&buf)
	output := buf.String()

	// Created items get ➕
	assert.True(t, strings.Contains(output, "➕"), "expected ➕ for created item")
	assert.True(t, strings.Contains(output, "Skill definitions"), "expected label in output")

	// Skipped items get ✅ and "(already exists)"
	assert.True(t, strings.Contains(output, "✅"), "expected ✅ for skipped item")
	assert.True(t, strings.Contains(output, "already exists"), "expected 'already exists' suffix")
}

func TestFileWriter_RelativePaths(t *testing.T) {
	dir := t.TempDir()
	fw := NewFileWriter(dir)

	entries := []FileEntry{
		{Path: filepath.Join(dir, "skills"), Label: "Skills", IsDir: true},
		{Path: filepath.Join(dir, ".waza.yaml"), Label: "Config", Content: "engine: copilot\n"},
	}

	inv, err := fw.Write(entries)
	require.NoError(t, err)

	// Paths should be relative to baseDir
	assert.Equal(t, "skills", inv.Items[0].RelPath)
	assert.Equal(t, ".waza.yaml", inv.Items[1].RelPath)
}

func TestFileWriter_EmptyContentSkipped(t *testing.T) {
	dir := t.TempDir()
	fw := NewFileWriter(dir)

	// File entry with empty content and file doesn't exist — should be skipped
	entries := []FileEntry{
		{Path: filepath.Join(dir, ".waza.yaml"), Label: "Config", Content: ""},
	}

	inv, err := fw.Write(entries)
	require.NoError(t, err)

	assert.Equal(t, 0, inv.CreatedCount())
	assert.Equal(t, OutcomeSkipped, inv.Items[0].Outcome)
	// File should NOT be created
	_, err = os.Stat(filepath.Join(dir, ".waza.yaml"))
	assert.True(t, os.IsNotExist(err))
}

func TestInventory_CreatedCount(t *testing.T) {
	inv := &Inventory{
		Items: []InventoryItem{
			{Outcome: OutcomeCreated},
			{Outcome: OutcomeSkipped},
			{Outcome: OutcomeCreated},
			{Outcome: OutcomeSkipped},
		},
	}
	assert.Equal(t, 2, inv.CreatedCount())
}

func TestFileWriter_DirEntry_FileExistsAtPath(t *testing.T) {
	dir := t.TempDir()

	// Create a regular file where we expect a directory
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills"), []byte("not a dir"), 0o644))

	fw := NewFileWriter(dir)
	entries := []FileEntry{
		{Path: filepath.Join(dir, "skills"), Label: "Skill definitions", IsDir: true},
	}

	_, err := fw.Write(entries)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exists but is not a directory")
}

func TestFileWriter_FileEntry_DirExistsAtPath(t *testing.T) {
	dir := t.TempDir()

	// Create a directory where we expect a file
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "README.md"), 0o755))

	fw := NewFileWriter(dir)
	entries := []FileEntry{
		{Path: filepath.Join(dir, "README.md"), Label: "Readme", Content: "# Hello\n"},
	}

	_, err := fw.Write(entries)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exists but is a directory")
}

func TestFileWriter_RootedCreationDoesNotOverwriteCompetingFile(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	path := filepath.Join(dir, "task.yaml")
	original := []byte("competing task\n")
	fw := NewFileWriter(dir)
	fw.beforeRootedCreate = func(path string) {
		require.NoError(t, os.WriteFile(path, original, 0o600))
	}

	_, err = fw.WriteRooted(root, []FileEntry{{Path: path, Content: "generated task\n"}})
	require.ErrorIs(t, err, os.ErrExist)
	preserved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, preserved)
}

func TestFileWriter_RootedCreationRejectsSwappedParent(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	probe := filepath.Join(dir, "link-probe")
	if err := os.Symlink(outside, probe); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	require.NoError(t, os.Remove(probe))
	parent := filepath.Join(dir, "evals")
	require.NoError(t, os.MkdirAll(parent, 0o755))
	sentinel := filepath.Join(outside, "sentinel.txt")
	require.NoError(t, os.WriteFile(sentinel, []byte("outside sentinel"), 0o600))
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	fw := NewFileWriter(dir)
	fw.beforeRootedCreate = func(string) {
		require.NoError(t, os.Rename(parent, filepath.Join(dir, "original-evals")))
		require.NoError(t, os.Symlink(outside, parent))
	}

	path := filepath.Join(parent, "new", "task.yaml")
	_, err = fw.WriteRooted(root, []FileEntry{{Path: path, Content: "generated task\n"}})
	require.Error(t, err)
	require.NoDirExists(t, filepath.Join(outside, "new"))
	preserved, readErr := os.ReadFile(sentinel)
	require.NoError(t, readErr)
	require.Equal(t, "outside sentinel", string(preserved))
}

type failingRootedFile struct {
	*os.File
	writeErr error
	closeErr error
	short    bool
}

func (f *failingRootedFile) Write(p []byte) (int, error) {
	if f.writeErr != nil || f.short {
		n, err := f.File.Write(p[:len(p)/2])
		if err != nil {
			return n, err
		}
		return n, f.writeErr
	}
	return f.File.Write(p)
}

func (f *failingRootedFile) Close() error {
	return errors.Join(f.File.Close(), f.closeErr)
}

func TestFileWriter_RootedCreationReportsWriteAndCloseFailures(t *testing.T) {
	for _, mode := range []string{"write", "short write", "close"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			path := filepath.Join(dir, "task.yaml")
			injected := errors.New("injected " + mode + " failure")
			fw := NewFileWriter(dir)
			fw.openRootedFile = func(root *os.Root, name string) (io.WriteCloser, error) {
				file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					return nil, err
				}
				wrapped := &failingRootedFile{File: file}
				switch mode {
				case "write":
					wrapped.writeErr = injected
				case "short write":
					wrapped.short = true
				case "close":
					wrapped.closeErr = injected
				}
				return wrapped, nil
			}

			_, err = fw.WriteRooted(root, []FileEntry{{Path: path, Content: "generated task\n"}})
			if mode == "short write" {
				require.ErrorIs(t, err, io.ErrShortWrite)
			} else {
				require.ErrorIs(t, err, injected)
			}
			require.ErrorContains(t, err, path)
			require.ErrorContains(t, err, "partial output may remain")
			retained, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			if mode == "close" {
				require.Equal(t, "generated task\n", string(retained))
			} else {
				require.Equal(t, "generat", string(retained))
			}
		})
	}
}
