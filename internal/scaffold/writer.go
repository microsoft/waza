package scaffold

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	baseDir            string
	beforeRootedCreate func(string)
	openRootedFile     func(*os.Root, string) (io.WriteCloser, error)
}

// NewFileWriter creates a FileWriter rooted at baseDir.
// Relative display paths in the inventory are computed against baseDir.
func NewFileWriter(baseDir string) *FileWriter {
	return &FileWriter{baseDir: baseDir}
}

// Write processes each entry: creates missing files/directories and
// skips existing ones. Returns a structured Inventory of outcomes.
func (fw *FileWriter) Write(entries []FileEntry) (*Inventory, error) {
	return fw.write(entries, nil)
}

// WriteRooted confines newly created entries to root, which must be opened at
// baseDir and remain open for the operation. Existing entries are not changed.
func (fw *FileWriter) WriteRooted(root *os.Root, entries []FileEntry) (*Inventory, error) {
	if root == nil {
		return nil, errors.New("rooted scaffold writer requires an open project root")
	}
	return fw.write(entries, root)
}

func (fw *FileWriter) write(entries []FileEntry, root *os.Root) (*Inventory, error) {
	inv := &Inventory{BaseDir: fw.baseDir}

	for _, entry := range entries {
		item := InventoryItem{Entry: entry}

		// Compute relative display path
		item.RelPath = entry.Path
		if rel, err := filepath.Rel(fw.baseDir, entry.Path); err == nil {
			item.RelPath = rel
		}

		if root != nil {
			outcome, err := fw.writeRootedEntry(root, entry)
			if err != nil {
				return nil, err
			}
			item.Outcome = outcome
			inv.Items = append(inv.Items, item)
			continue
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

func (fw *FileWriter) writeRootedEntry(root *os.Root, entry FileEntry) (FileOutcome, error) {
	rel, err := filepath.Rel(fw.baseDir, entry.Path)
	if err != nil || !filepath.IsLocal(rel) {
		return OutcomeSkipped, fmt.Errorf("scaffold path %s is outside project directory %s", entry.Path, fw.baseDir)
	}
	info, err := os.Stat(entry.Path)
	switch {
	case err == nil:
		if entry.IsDir != info.IsDir() {
			if entry.IsDir {
				return OutcomeSkipped, fmt.Errorf("path %s exists but is not a directory", entry.Path)
			}
			return OutcomeSkipped, fmt.Errorf("path %s exists but is a directory", entry.Path)
		}
		return OutcomeSkipped, nil
	case !errors.Is(err, fs.ErrNotExist):
		return OutcomeSkipped, fmt.Errorf("failed to stat %s: %w", entry.Path, err)
	case !entry.IsDir && entry.Content == "":
		return OutcomeSkipped, nil
	}

	name, err := fw.rootedCreationName(entry.Path)
	if err != nil {
		return OutcomeSkipped, err
	}
	if fw.beforeRootedCreate != nil {
		fw.beforeRootedCreate(entry.Path)
	}
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return OutcomeSkipped, fmt.Errorf("failed to create directory for %s: %w", entry.Path, err)
	}
	if entry.IsDir {
		if err := root.Mkdir(name, 0o755); err != nil {
			if errors.Is(err, fs.ErrExist) {
				info, statErr := root.Stat(name)
				if statErr == nil && info.IsDir() {
					return OutcomeSkipped, nil
				}
			}
			return OutcomeSkipped, fmt.Errorf("failed to create %s: %w", entry.Path, err)
		}
		return OutcomeCreated, nil
	}

	open := fw.openRootedFile
	if open == nil {
		open = func(root *os.Root, name string) (io.WriteCloser, error) {
			return root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		}
	}
	file, err := open(root, name)
	if err != nil {
		return OutcomeSkipped, fmt.Errorf("failed to create %s without overwriting: %w", entry.Path, err)
	}
	n, writeErr := file.Write([]byte(entry.Content))
	if writeErr == nil && n != len(entry.Content) {
		writeErr = io.ErrShortWrite
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return OutcomeSkipped, fmt.Errorf("failed to write or close %s; partial output may remain: %w", entry.Path, err)
	}
	return OutcomeCreated, nil
}

// rootedCreationName resolves existing ancestors to support absolute in-root
// symlinks. Root still enforces containment when the path is actually created.
func (fw *FileWriter) rootedCreationName(path string) (string, error) {
	base, err := filepath.EvalSymlinks(fw.baseDir)
	if err != nil {
		return "", fmt.Errorf("resolving project directory %s: %w", fw.baseDir, err)
	}
	parent, suffix := filepath.Dir(path), filepath.Base(path)
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			rel, relErr := filepath.Rel(base, resolved)
			if relErr != nil || !filepath.IsLocal(rel) {
				return "", fmt.Errorf("scaffold path %s resolves outside project directory %s", path, fw.baseDir)
			}
			return filepath.Join(rel, suffix), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolving scaffold parent for %s: %w", path, err)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("resolving scaffold parent for %s: %w", path, err)
		}
		suffix = filepath.Join(filepath.Base(parent), suffix)
		parent = next
	}
}
