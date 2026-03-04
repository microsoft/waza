package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileEntry represents a file to be written with its content and label.
type FileEntry struct {
	Path    string
	Content string
	Label   string
}

// FileOutcome describes what happened when writing a file.
type FileOutcome string

const (
	FileCreated FileOutcome = "created"
	FileSkipped FileOutcome = "skipped"
)

// FileInventory represents the result of writing a file.
type FileInventory struct {
	Path    string
	Label   string
	Outcome FileOutcome
}

// FileWriter handles safe file creation with existence checking.
type FileWriter struct {
	entries []FileEntry
}

// NewFileWriter creates a new FileWriter with the given file entries.
func NewFileWriter(entries []FileEntry) *FileWriter {
	return &FileWriter{entries: entries}
}

// Write writes all files, creating missing ones and skipping existing ones.
// It creates parent directories as needed and returns an inventory of outcomes.
func (w *FileWriter) Write() ([]FileInventory, error) {
	inventory := make([]FileInventory, 0, len(w.entries))

	for _, entry := range w.entries {
		outcome, err := w.writeFile(entry)
		if err != nil {
			return inventory, err
		}

		inventory = append(inventory, FileInventory{
			Path:    entry.Path,
			Label:   entry.Label,
			Outcome: outcome,
		})
	}

	return inventory, nil
}

// writeFile writes a single file, returning its outcome.
func (w *FileWriter) writeFile(entry FileEntry) (FileOutcome, error) {
	// Check if file exists
	if _, err := os.Stat(entry.Path); err == nil {
		return FileSkipped, nil
	}

	// Create parent directory if needed
	dir := filepath.Dir(entry.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create directory for %s: %w", entry.Path, err)
	}

	// Write file
	if err := os.WriteFile(entry.Path, []byte(entry.Content), 0o644); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", entry.Path, err)
	}

	return FileCreated, nil
}
