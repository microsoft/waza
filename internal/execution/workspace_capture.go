package execution

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The cause remains available to errors.Is/As, never to public diagnostic text.
type workspaceCaptureDiagnostic struct {
	message string
	cause   error
}

func (e *workspaceCaptureDiagnostic) Error() string { return e.message }
func (e *workspaceCaptureDiagnostic) Unwrap() error { return e.cause }

func captureIOError(operation, path string, cause error) error {
	code := "io"
	switch {
	case errors.Is(cause, os.ErrNotExist):
		code = "not_found"
	case errors.Is(cause, os.ErrPermission):
		code = "permission"
	case errors.Is(cause, os.ErrClosed):
		code = "closed"
	}
	target := ""
	if path != "" {
		target = fmt.Sprintf(" %q", captureDiagnosticPath(path))
	}
	return &workspaceCaptureDiagnostic{
		message: fmt.Sprintf("[%s] %s%s failed; check workspace access and file stability", code, operation, target),
		cause:   cause,
	}
}

func captureDiagnosticPath(path string) string {
	if !filepath.IsLocal(path) {
		return "<workspace>"
	}
	if len(path) > 160 {
		path = path[:160] + "..."
	}
	return filepath.ToSlash(path)
}

const (
	maxWorkspaceCaptureFiles      = 10_000
	maxWorkspaceCaptureEntries    = 20_000
	maxWorkspaceCaptureFileBytes  = 16 << 20
	maxWorkspaceCaptureTotalBytes = 64 << 20
)

type workspaceCaptureLimits struct {
	maxFiles      int
	maxEntries    int
	maxFileBytes  int64
	maxTotalBytes int64
}

var defaultWorkspaceCaptureLimits = workspaceCaptureLimits{
	maxFiles:      maxWorkspaceCaptureFiles,
	maxEntries:    maxWorkspaceCaptureEntries,
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
	if limits.maxFiles <= 0 || limits.maxEntries <= 0 || limits.maxFileBytes <= 0 || limits.maxTotalBytes <= 0 {
		return nil, fmt.Errorf("workspace capture requires positive file-count, traversal-entry, per-file, and total-byte limits")
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("workspace capture incomplete: %w", captureIOError("open workspace root", "", err))
	}
	defer func() { _ = root.Close() }()

	files := make(map[string][]byte)
	var totalBytes int64
	captureFile := func(path, rel string, required bool) (captureErr error) {
		if _, ok := files[rel]; ok {
			return nil
		}
		info, err := root.Stat(path)
		if required && os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return captureIOError("inspect", rel, err)
		}
		if !info.Mode().IsRegular() {
			if required {
				return fmt.Errorf("preserved path %q is not a regular file", captureDiagnosticPath(rel))
			}
			return nil
		}

		file, err := openWorkspaceCaptureFile(root, path)
		if err != nil {
			if current, statErr := root.Stat(path); !required && statErr == nil && !current.Mode().IsRegular() {
				return nil
			}
			return captureIOError("open", rel, err)
		}
		defer func() {
			if closeErr := file.Close(); captureErr == nil && closeErr != nil {
				captureErr = captureIOError("close", rel, closeErr)
			}
		}()

		info, err = file.Stat()
		if err != nil {
			return captureIOError("inspect", rel, err)
		}
		if !info.Mode().IsRegular() {
			if required {
				return fmt.Errorf("preserved path %q is not a regular file", captureDiagnosticPath(rel))
			}
			return nil
		}
		if len(files) >= limits.maxFiles {
			return fmt.Errorf("file count exceeds limit of %d before %q", limits.maxFiles, captureDiagnosticPath(rel))
		}
		if info.Size() > limits.maxFileBytes {
			return fmt.Errorf("file %q is %d bytes and exceeds per-file limit of %d bytes", captureDiagnosticPath(rel), info.Size(), limits.maxFileBytes)
		}
		if info.Size() > limits.maxTotalBytes-totalBytes {
			return fmt.Errorf("capturing %q would exceed total limit of %d bytes", captureDiagnosticPath(rel), limits.maxTotalBytes)
		}

		readLimit := min(limits.maxFileBytes, limits.maxTotalBytes-totalBytes)
		content, exceeded, err := readWorkspaceCaptureFile(file, readLimit)
		if err != nil {
			return captureIOError("read", rel, err)
		}
		if exceeded {
			if readLimit == limits.maxTotalBytes-totalBytes {
				return fmt.Errorf("capturing %q would exceed total limit of %d bytes", captureDiagnosticPath(rel), limits.maxTotalBytes)
			}
			return fmt.Errorf("file %q exceeds per-file limit of %d bytes", captureDiagnosticPath(rel), limits.maxFileBytes)
		}

		files[rel] = content
		totalBytes += int64(len(content))
		return nil
	}

	for _, preserved := range preservePaths {
		path, rel, err := resolveWorkspaceCapturePath(dir, preserved)
		if err != nil {
			return nil, fmt.Errorf("workspace capture incomplete: %w", err)
		}
		if err := captureFile(path, rel, true); err != nil {
			return nil, fmt.Errorf("workspace capture incomplete: %w", err)
		}
	}

	err = walkWorkspaceCapture(root, limits.maxEntries, func(path, rel string) error {
		return captureFile(path, rel, false)
	})
	if err != nil {
		return nil, fmt.Errorf("workspace capture incomplete: %w", err)
	}
	return files, nil
}

func resolveWorkspaceCapturePath(root, requested string) (string, string, error) {
	if filepath.IsAbs(requested) {
		return "", "", fmt.Errorf("preserved path must be relative to the workspace")
	}
	rel := filepath.Clean(requested)
	check, err := filepath.Rel(root, filepath.Join(root, rel))
	if err != nil || check == ".." || strings.HasPrefix(check, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("preserved path escapes workspace; use a contained relative target")
	}
	return check, filepath.ToSlash(check), nil
}

func excludedWorkspaceCaptureDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "node_modules":
		return true
	default:
		return false
	}
}

func walkWorkspaceCapture(root *os.Root, maxEntries int, captureFile func(path, rel string) error) error {
	directories := []string{"."}
	entriesSeen := 0
	for len(directories) > 0 {
		index := len(directories) - 1
		dirPath := directories[index]
		directories = directories[:index]

		dir, err := openWorkspaceCaptureFile(root, dirPath)
		if err != nil {
			return captureIOError("open directory", dirPath, err)
		}
		info, err := dir.Stat()
		if err != nil {
			_ = dir.Close()
			return captureIOError("inspect directory", dirPath, err)
		}
		if !info.IsDir() {
			_ = dir.Close()
			return fmt.Errorf("workspace directory %q changed during capture", captureDiagnosticPath(dirPath))
		}

		var walkErr error
		for {
			entries, readErr := dir.ReadDir(128)
			for _, entry := range entries {
				entriesSeen++
				if entriesSeen > maxEntries {
					walkErr = fmt.Errorf("traversal entries exceed limit of %d", maxEntries)
					break
				}
				path := entry.Name()
				if dirPath != "." {
					path = filepath.Join(dirPath, entry.Name())
				}
				if entry.IsDir() {
					if !excludedWorkspaceCaptureDir(entry.Name()) {
						directories = append(directories, path)
					}
					continue
				}
				if entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					walkErr = captureIOError("inspect", path, err)
					break
				}
				if !info.Mode().IsRegular() {
					continue
				}
				if err := captureFile(path, filepath.ToSlash(path)); err != nil {
					walkErr = err
					break
				}
			}
			if walkErr != nil || readErr == io.EOF {
				break
			}
			if readErr != nil {
				walkErr = captureIOError("read directory", dirPath, readErr)
				break
			}
		}
		if closeErr := dir.Close(); walkErr == nil && closeErr != nil {
			walkErr = captureIOError("close directory", dirPath, closeErr)
		}
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}

func readWorkspaceCaptureFile(file *os.File, limit int64) (content []byte, exceeded bool, err error) {
	content, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(content)) > limit {
		return nil, true, nil
	}
	return content, false, nil
}
