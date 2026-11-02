package execution

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxWorkspaceCaptureFiles      = 10_000
	maxWorkspaceCaptureFileBytes  = 16 << 20
	maxWorkspaceCaptureTotalBytes = 64 << 20
)

type workspaceCaptureLimits struct {
	maxFiles      int
	maxFileBytes  int64
	maxTotalBytes int64
}

var defaultWorkspaceCaptureLimits = workspaceCaptureLimits{
	maxFiles:      maxWorkspaceCaptureFiles,
	maxFileBytes:  maxWorkspaceCaptureFileBytes,
	maxTotalBytes: maxWorkspaceCaptureTotalBytes,
}

// captureWorkspaceFiles preserves bounded post-execution workspace state before
// session disconnect can restore files. It returns no files when capture is
// incomplete so graders never consume a success-shaped partial snapshot.
func captureWorkspaceFiles(dir string, preservePaths []string) (map[string][]byte, error) {
	return captureWorkspaceFilesWithLimits(dir, preservePaths, defaultWorkspaceCaptureLimits)
}

func captureWorkspaceFilesWithLimits(dir string, preservePaths []string, limits workspaceCaptureLimits) (map[string][]byte, error) {
	if dir == "" {
		return nil, nil
	}
	if limits.maxFiles <= 0 || limits.maxFileBytes <= 0 || limits.maxTotalBytes <= 0 {
		return nil, fmt.Errorf("workspace capture requires positive file-count, per-file, and total-byte limits")
	}

	files := make(map[string][]byte)
	var totalBytes int64
	captureFile := func(path, rel string, entry os.DirEntry) error {
		if _, ok := files[rel]; ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect %q: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if len(files) >= limits.maxFiles {
			return fmt.Errorf("file count exceeds limit of %d before %q", limits.maxFiles, rel)
		}
		if info.Size() > limits.maxFileBytes {
			return fmt.Errorf("file %q is %d bytes and exceeds per-file limit of %d bytes", rel, info.Size(), limits.maxFileBytes)
		}
		if info.Size() > limits.maxTotalBytes-totalBytes {
			return fmt.Errorf("capturing %q would exceed total limit of %d bytes", rel, limits.maxTotalBytes)
		}

		readLimit := min(limits.maxFileBytes, limits.maxTotalBytes-totalBytes)
		content, exceeded, err := readWorkspaceCaptureFile(path, readLimit)
		if err != nil {
			return fmt.Errorf("read %q: %w", rel, err)
		}
		if exceeded {
			if readLimit == limits.maxTotalBytes-totalBytes {
				return fmt.Errorf("capturing %q would exceed total limit of %d bytes", rel, limits.maxTotalBytes)
			}
			return fmt.Errorf("file %q exceeds per-file limit of %d bytes", rel, limits.maxFileBytes)
		}

		files[rel] = content
		totalBytes += int64(len(content))
		return nil
	}

	for _, preserved := range preservePaths {
		rel, path, err := resolveWorkspaceCapturePath(dir, preserved)
		if err != nil {
			return nil, fmt.Errorf("workspace capture incomplete: %w", err)
		}
		entry, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("workspace capture incomplete: inspect %q: %w", rel, err)
		}
		if err := captureFile(path, rel, fileInfoDirEntry{entry}); err != nil {
			return nil, fmt.Errorf("workspace capture incomplete: %w", err)
		}
	}

	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %q: %w", workspaceRelativePath(dir, path), walkErr)
		}
		if entry.IsDir() {
			if path != dir && excludedWorkspaceCaptureDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		rel := workspaceRelativePath(dir, path)
		return captureFile(path, rel, entry)
	})
	if err != nil {
		return nil, fmt.Errorf("workspace capture incomplete: %w", err)
	}
	return files, nil
}

type fileInfoDirEntry struct {
	os.FileInfo
}

func (entry fileInfoDirEntry) Type() os.FileMode          { return entry.Mode().Type() }
func (entry fileInfoDirEntry) Info() (os.FileInfo, error) { return entry.FileInfo, nil }

func resolveWorkspaceCapturePath(root, requested string) (string, string, error) {
	if filepath.IsAbs(requested) {
		return "", "", fmt.Errorf("preserved path %q must be relative", requested)
	}
	rel := filepath.Clean(requested)
	path := filepath.Join(root, rel)
	check, err := filepath.Rel(root, path)
	if err != nil || check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("preserved path %q escapes workspace", requested)
	}
	return filepath.ToSlash(check), path, nil
}

func excludedWorkspaceCaptureDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "node_modules":
		return true
	default:
		return false
	}
}

func workspaceRelativePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func readWorkspaceCaptureFile(path string, limit int64) (content []byte, exceeded bool, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	content, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(content)) > limit {
		return nil, true, nil
	}
	return content, false, nil
}
