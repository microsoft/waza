// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package safeio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrFileTooLarge indicates that a regular file exceeds the requested read limit.
var ErrFileTooLarge = errors.New("file exceeds size limit")

// Root provides canonical, rooted file access that rejects symlink components.
type Root struct {
	root *os.Root
}

// OpenRoot canonicalizes path and opens it as a rooted directory.
func OpenRoot(path string) (*Root, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving root path: %w", err)
	}
	canonicalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return nil, fmt.Errorf("canonicalizing root path: %w", err)
	}
	info, err := os.Stat(canonicalPath)
	if err != nil {
		return nil, fmt.Errorf("checking root path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root path %q is not a directory", path)
	}
	root, err := os.OpenRoot(canonicalPath)
	if err != nil {
		return nil, fmt.Errorf("opening root path: %w", err)
	}
	openedInfo, err := root.Lstat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("checking opened root path: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		_ = root.Close()
		return nil, fmt.Errorf("root path %q changed while opening", path)
	}
	return &Root{root: root}, nil
}

// Close closes the rooted directory.
func (r *Root) Close() error {
	return r.root.Close()
}

// FS returns the rooted directory as an fs.FS.
func (r *Root) FS() fs.FS {
	return r.root.FS()
}

// ReadRegularFile reads a root-relative regular file without following symlinks.
// A non-positive maxSize disables the size limit.
func (r *Root) ReadRegularFile(path string, maxSize int64) ([]byte, fs.FileInfo, error) {
	return r.readRegularFile(path, maxSize, nil)
}

func (r *Root) readRegularFile(path string, maxSize int64, beforeRead func()) ([]byte, fs.FileInfo, error) {
	cleanPath, parts, err := validateRelativePath(path)
	if err != nil {
		return nil, nil, err
	}

	current := ""
	var checkedInfo fs.FileInfo
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := r.root.Lstat(current)
		if err != nil {
			return nil, nil, fmt.Errorf("checking path %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("path %q contains symlink %q", path, current)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, nil, fmt.Errorf("path %q has non-directory component %q", path, current)
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("path %q is not a regular file", path)
		}
		checkedInfo = info
	}

	file, err := r.root.Open(cleanPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening path %q: %w", path, err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("checking opened path %q: %w", path, err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(checkedInfo, openedInfo) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("path %q changed while opening", path)
	}
	if maxSize > 0 && openedInfo.Size() > maxSize {
		_ = file.Close()
		return nil, openedInfo, fmt.Errorf("%w: path %q is %d bytes (limit %d)", ErrFileTooLarge, path, openedInfo.Size(), maxSize)
	}
	if beforeRead != nil {
		beforeRead()
	}

	reader := io.Reader(file)
	if maxSize > 0 {
		reader = io.LimitReader(file, maxSize)
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("reading path %q: %w", path, err)
	}
	if maxSize > 0 && int64(len(content)) == maxSize {
		var overflow [1]byte
		n, overflowErr := file.Read(overflow[:])
		if overflowErr != nil && !errors.Is(overflowErr, io.EOF) {
			_ = file.Close()
			return nil, nil, fmt.Errorf("checking path %q size limit: %w", path, overflowErr)
		}
		if n > 0 {
			_ = file.Close()
			return nil, openedInfo, fmt.Errorf("%w: path %q grew beyond limit %d while reading", ErrFileTooLarge, path, maxSize)
		}
	}
	if err := file.Close(); err != nil {
		return nil, nil, fmt.Errorf("closing path %q: %w", path, err)
	}
	return content, openedInfo, nil
}

func validateRelativePath(path string) (string, []string, error) {
	if path == "" {
		return "", nil, fmt.Errorf("path must not be empty")
	}
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\") {
		return "", nil, fmt.Errorf("path %q must be relative", path)
	}

	parts := strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("path must not be empty")
	}
	for _, part := range parts {
		if part == ".." {
			return "", nil, fmt.Errorf("path %q must not contain path traversal", path)
		}
	}

	cleanPath := filepath.Join(parts...)
	if cleanPath == "." {
		return "", nil, fmt.Errorf("path must not be empty")
	}
	return cleanPath, parts, nil
}
