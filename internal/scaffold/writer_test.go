package scaffold

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileWriter_CreateIfMissing(t *testing.T) {
	tmpDir := t.TempDir()

	entries := []FileEntry{
		{
			Path:    filepath.Join(tmpDir, "test.txt"),
			Content: "test content",
			Label:   "Test file",
		},
	}

	writer := NewFileWriter(entries)
	inventory, err := writer.Write()

	if err != nil {
		t.Fatalf("Write() failed: %v", err)
	}

	if len(inventory) != 1 {
		t.Fatalf("expected 1 inventory item, got %d", len(inventory))
	}

	if inventory[0].Outcome != FileCreated {
		t.Errorf("expected outcome %s, got %s", FileCreated, inventory[0].Outcome)
	}

	// Verify file was actually created with correct content
	data, err := os.ReadFile(entries[0].Path)
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}

	if string(data) != "test content" {
		t.Errorf("expected content %q, got %q", "test content", string(data))
	}
}

func TestFileWriter_SkipIfExists(t *testing.T) {
	tmpDir := t.TempDir()

	testFile := filepath.Join(tmpDir, "existing.txt")
	originalContent := "original content"

	// Pre-create the file
	if err := os.WriteFile(testFile, []byte(originalContent), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	entries := []FileEntry{
		{
			Path:    testFile,
			Content: "new content",
			Label:   "Existing file",
		},
	}

	writer := NewFileWriter(entries)
	inventory, err := writer.Write()

	if err != nil {
		t.Fatalf("Write() failed: %v", err)
	}

	if len(inventory) != 1 {
		t.Fatalf("expected 1 inventory item, got %d", len(inventory))
	}

	if inventory[0].Outcome != FileSkipped {
		t.Errorf("expected outcome %s, got %s", FileSkipped, inventory[0].Outcome)
	}

	// Verify file was NOT overwritten
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if string(data) != originalContent {
		t.Errorf("file was overwritten! expected %q, got %q", originalContent, string(data))
	}
}

func TestFileWriter_Inventory(t *testing.T) {
	tmpDir := t.TempDir()

	existingFile := filepath.Join(tmpDir, "existing.txt")
	if err := os.WriteFile(existingFile, []byte("exists"), 0o644); err != nil {
		t.Fatalf("failed to create existing file: %v", err)
	}

	entries := []FileEntry{
		{
			Path:    existingFile,
			Content: "content",
			Label:   "Existing",
		},
		{
			Path:    filepath.Join(tmpDir, "new.txt"),
			Content: "new content",
			Label:   "New file",
		},
	}

	writer := NewFileWriter(entries)
	inventory, err := writer.Write()

	if err != nil {
		t.Fatalf("Write() failed: %v", err)
	}

	if len(inventory) != 2 {
		t.Fatalf("expected 2 inventory items, got %d", len(inventory))
	}

	// First file should be skipped
	if inventory[0].Outcome != FileSkipped {
		t.Errorf("first file: expected outcome %s, got %s", FileSkipped, inventory[0].Outcome)
	}

	// Second file should be created
	if inventory[1].Outcome != FileCreated {
		t.Errorf("second file: expected outcome %s, got %s", FileCreated, inventory[1].Outcome)
	}

	// Verify labels are preserved
	if inventory[0].Label != "Existing" {
		t.Errorf("first file: expected label %q, got %q", "Existing", inventory[0].Label)
	}

	if inventory[1].Label != "New file" {
		t.Errorf("second file: expected label %q, got %q", "New file", inventory[1].Label)
	}
}

func TestFileWriter_CreatesDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	deepPath := filepath.Join(tmpDir, "a", "b", "c", "test.txt")

	entries := []FileEntry{
		{
			Path:    deepPath,
			Content: "deep content",
			Label:   "Deep file",
		},
	}

	writer := NewFileWriter(entries)
	inventory, err := writer.Write()

	if err != nil {
		t.Fatalf("Write() failed: %v", err)
	}

	if len(inventory) != 1 {
		t.Fatalf("expected 1 inventory item, got %d", len(inventory))
	}

	if inventory[0].Outcome != FileCreated {
		t.Errorf("expected outcome %s, got %s", FileCreated, inventory[0].Outcome)
	}

	// Verify file and all parent directories were created
	data, err := os.ReadFile(deepPath)
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}

	if string(data) != "deep content" {
		t.Errorf("expected content %q, got %q", "deep content", string(data))
	}

	// Verify intermediate directories exist
	for _, dir := range []string{
		filepath.Join(tmpDir, "a"),
		filepath.Join(tmpDir, "a", "b"),
		filepath.Join(tmpDir, "a", "b", "c"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("directory %s was not created: %v", dir, err)
		} else if !info.IsDir() {
			t.Errorf("%s is not a directory", dir)
		}
	}
}
