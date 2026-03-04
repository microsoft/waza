package scaffold

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

// FileOutcome describes what happened when processing a file entry.
type FileOutcome int

const (
	// OutcomeCreated means the file or directory was newly created.
	OutcomeCreated FileOutcome = iota
	// OutcomeSkipped means the file or directory already existed.
	OutcomeSkipped
)

// FileEntry describes a file or directory to be written.
type FileEntry struct {
	Path    string // absolute path on disk
	Label   string // human-readable description
	IsDir   bool   // true for directories, false for files
	Content string // file content (ignored for directories)
}

// InventoryItem records the outcome for a single file entry.
type InventoryItem struct {
	Entry   FileEntry
	Outcome FileOutcome
	RelPath string // path relative to the base directory, used for display
}

// Inventory holds the results of a FileWriter.Write call.
type Inventory struct {
	Items   []InventoryItem
	BaseDir string
}

// CreatedCount returns the number of items that were created.
func (inv *Inventory) CreatedCount() int {
	n := 0
	for _, item := range inv.Items {
		if item.Outcome == OutcomeCreated {
			n++
		}
	}
	return n
}

// Fprint writes the inventory table to w using emoji indicators.
func (inv *Inventory) Fprint(w io.Writer) {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	for _, item := range inv.Items {
		indicator := "{new}"
		suffix := ""
		if item.Outcome == OutcomeSkipped {
			indicator = "{exist}"
			suffix = " (already exists)"
		}
		relPath := item.RelPath
		if item.Entry.IsDir {
			relPath += string(filepath.Separator)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s%s\n", indicator, relPath, item.Entry.Label, suffix) //nolint:errcheck
	}
	tw.Flush() //nolint:errcheck
	result := buf.String()
	result = strings.ReplaceAll(result, "{exist}", "✅")
	result = strings.ReplaceAll(result, "{new}", "➕")
	fmt.Fprint(w, result) //nolint:errcheck
}

// FileWriter creates files and directories that don't exist, skipping
// those that do. It records an inventory of outcomes for each entry.
type FileWriter struct {
	baseDir string
}

// NewFileWriter creates a FileWriter rooted at baseDir.
// Relative display paths in the inventory are computed against baseDir.
func NewFileWriter(baseDir string) *FileWriter {
	return &FileWriter{baseDir: baseDir}
}

// Write processes each entry: creates missing files/directories and
// skips existing ones. Returns a structured Inventory of outcomes.
func (fw *FileWriter) Write(entries []FileEntry) (*Inventory, error) {
	inv := &Inventory{BaseDir: fw.baseDir}

	for _, entry := range entries {
		item := InventoryItem{Entry: entry}

		// Compute relative display path
		item.RelPath = entry.Path
		if rel, err := filepath.Rel(fw.baseDir, entry.Path); err == nil {
			item.RelPath = rel
		}

		if entry.IsDir {
			info, err := os.Stat(entry.Path)
			if err == nil && info.IsDir() {
				item.Outcome = OutcomeSkipped
			} else if err == nil {
				return nil, fmt.Errorf("path %s exists but is not a directory", entry.Path)
			} else if os.IsNotExist(err) {
				if err := os.MkdirAll(entry.Path, 0o755); err != nil {
					return nil, fmt.Errorf("failed to create %s: %w", entry.Path, err)
				}
				item.Outcome = OutcomeCreated
			} else {
				return nil, fmt.Errorf("failed to stat %s: %w", entry.Path, err)
			}
		} else {
			info, err := os.Stat(entry.Path)
			if err == nil {
				if info.IsDir() {
					return nil, fmt.Errorf("path %s exists but is a directory", entry.Path)
				}
				item.Outcome = OutcomeSkipped
			} else if os.IsNotExist(err) {
				if entry.Content != "" {
					if err := os.MkdirAll(filepath.Dir(entry.Path), 0o755); err != nil {
						return nil, fmt.Errorf("failed to create directory for %s: %w", entry.Path, err)
					}
					if err := os.WriteFile(entry.Path, []byte(entry.Content), 0o644); err != nil {
						return nil, fmt.Errorf("failed to write %s: %w", entry.Path, err)
					}
					item.Outcome = OutcomeCreated
				} else {
					// No content and file doesn't exist — nothing to create.
					item.Outcome = OutcomeSkipped
				}
			} else {
				return nil, fmt.Errorf("failed to stat %s: %w", entry.Path, err)
			}
		}

		inv.Items = append(inv.Items, item)
	}

	return inv, nil
}
