//go:build linux || darwin

package snapshot

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

const exclusionInventoryEntries = 256
const exclusionInventoryDepth = 32

type workspaceRoot struct {
	file          *os.File
	excluded      []os.FileInfo
	held          []*os.File
	inspected     int
	inventoryOpen func(int, string, int, uint32) (int, error)
	inventoryStat func(int, string, *unix.Stat_t, int) error
	afterRead     func(*os.File)
}

func openWorkspaceRoot(root string) (*workspaceRoot, error) {
	for _, part := range strings.Split(filepath.ToSlash(root), "/") {
		if part == ".." {
			return nil, errors.New("workspace: root traversal is unsupported")
		}
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, errors.New("workspace: resolve root failed")
	}
	if runtime.GOOS == "darwin" {
		absolute, err = workspaceSystemRootAlias(absolute, os.Readlink)
		if err != nil {
			return nil, err
		}
	}
	fd, err := unix.Open("/", directoryOpenFlags(), 0)
	if err != nil {
		return nil, errors.New("workspace: open root anchor failed")
	}
	current := os.NewFile(uintptr(fd), "workspace anchor")
	for _, part := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		if part == "" {
			continue
		}
		child, openErr := unix.Openat(int(current.Fd()), part, directoryOpenFlags(), 0)
		closeErr := current.Close()
		if openErr != nil || closeErr != nil {
			if openErr == nil {
				if err := unix.Close(child); err != nil {
					return nil, errors.New("workspace: root admission and close failed")
				}
			}
			return nil, errors.New("workspace: root ancestor is unavailable or not a non-symlink directory")
		}
		current = os.NewFile(uintptr(child), "workspace root")
	}
	return &workspaceRoot{file: current, inventoryOpen: unix.Openat, inventoryStat: unix.Fstatat}, nil
}

func workspaceSystemRootAlias(absolute string, readlink func(string) (string, error)) (string, error) {
	for _, alias := range []string{"/var", "/tmp"} {
		if absolute == alias || strings.HasPrefix(absolute, alias+"/") {
			target, err := readlink(alias)
			if err != nil || target != "/private"+alias && target != "private"+alias {
				return "", errors.New("workspace: system root alias is unavailable or changed")
			}
			return "/private" + absolute, nil
		}
	}
	return absolute, nil
}

func directoryOpenFlags() int {
	return unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
}

func (root *workspaceRoot) Close() error {
	var problems []error
	for _, file := range root.held {
		if err := file.Close(); err != nil {
			problems = append(problems, errors.New("workspace: close exclusion descriptor failed"))
		}
	}
	root.held = nil
	if err := root.file.Close(); err != nil {
		problems = append(problems, errors.New("workspace: close root descriptor failed"))
	}
	return errors.Join(problems...)
}

// Inventory observes metadata only. It does not freeze mutable directory membership.
func (root *workspaceRoot) admitExclusions(paths []string) error {
	for _, name := range paths {
		parts := strings.Split(name, "/")
		if len(parts) > exclusionInventoryDepth {
			return errors.New("workspace: exclusion inventory exceeds depth 32; no selected content captured")
		}
		parent := root.file
		var ancestors []*os.File
		for i, part := range parts[:len(parts)-1] {
			if err := root.countExclusionInspection(i + 1); err != nil {
				return errors.Join(err, closeExclusionAncestors(ancestors))
			}
			fd, err := root.inventoryOpen(int(parent.Fd()), part, directoryOpenFlags(), 0)
			if errors.Is(err, unix.ENOENT) {
				break
			}
			if err != nil {
				return errors.Join(errors.New("workspace: exclusion ancestor inspection failed; no selected content captured"), closeExclusionAncestors(ancestors))
			}
			parent = os.NewFile(uintptr(fd), "exclusion ancestor")
			ancestors = append(ancestors, parent)
		}
		var problem error
		if len(ancestors) == len(parts)-1 {
			problem = root.inventoryEntry(parent, parts[len(parts)-1], len(parts), true)
		}
		if err := errors.Join(problem, closeExclusionAncestors(ancestors)); err != nil {
			return err
		}
	}
	return nil
}

func closeExclusionAncestors(files []*os.File) error {
	var problems []error
	for _, file := range files {
		if err := file.Close(); err != nil {
			problems = append(problems, errors.New("workspace: close exclusion ancestor failed"))
		}
	}
	return errors.Join(problems...)
}

func (root *workspaceRoot) inventoryEntry(parent *os.File, name string, depth int, missingAllowed bool) error {
	if err := root.countExclusionInspection(depth); err != nil {
		return err
	}
	var metadata unix.Stat_t
	if err := root.inventoryStat(int(parent.Fd()), name, &metadata, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if missingAllowed && errors.Is(err, unix.ENOENT) {
			return nil
		}
		return errors.New("workspace: exclusion inspection failed; no selected content captured")
	}
	return root.admitInventoryEntry(parent, name, depth, metadata)
}

func (root *workspaceRoot) countExclusionInspection(depth int) error {
	if depth > exclusionInventoryDepth || root.inspected >= exclusionInventoryEntries {
		return errors.New("workspace: exclusion inventory exceeds 256 entries or depth 32; no selected content captured")
	}
	root.inspected++
	return nil
}

func (root *workspaceRoot) admitInventoryEntry(parent *os.File, name string, depth int, metadata unix.Stat_t) error {
	kind := metadata.Mode & unix.S_IFMT
	if kind != unix.S_IFREG && kind != unix.S_IFDIR {
		return nil // Symlinks and special entries are inspected, never opened or traversed.
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if kind == unix.S_IFDIR {
		flags |= unix.O_DIRECTORY
	}
	fd, err := root.inventoryOpen(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return errors.New("workspace: exclusion descriptor admission failed; no selected content captured")
	}
	file := os.NewFile(uintptr(fd), "excluded workspace entry")
	root.held = append(root.held, file)
	info, err := file.Stat()
	if err != nil || !sameInventoryIdentity(&metadata, info) ||
		kind == unix.S_IFDIR && !info.IsDir() || kind == unix.S_IFREG && !info.Mode().IsRegular() {
		return errors.New("workspace: exclusion identity changed during admission; no selected content captured")
	}
	root.excluded = append(root.excluded, info)
	if !info.IsDir() {
		return nil
	}
	for {
		names, readErr := file.Readdirnames(1)
		for _, name := range names {
			if err := root.inventoryEntry(file, name, depth+1, false); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return errors.New("workspace: exclusion directory inspection failed; no selected content captured")
		}
	}
}

func (root *workspaceRoot) readFile(name string, limit int64) (content []byte, err error) {
	current := root.file
	parts := strings.Split(name, "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, openErr := unix.Openat(int(current.Fd()), part, flags, 0)
		if current != root.file {
			if closeErr := current.Close(); closeErr != nil {
				if openErr == nil {
					if err := unix.Close(fd); err != nil {
						return nil, errors.New("closing approved entry failed")
					}
				}
				return nil, errors.New("closing approved directory failed")
			}
		}
		if openErr != nil {
			return nil, errors.New("approved file is unavailable or has an unsupported symlink or ancestor")
		}
		current = os.NewFile(uintptr(fd), "approved workspace entry")
		info, statErr := current.Stat()
		excluded := false
		for _, identity := range root.excluded {
			excluded = excluded || statErr == nil && os.SameFile(info, identity)
		}
		if statErr != nil || excluded {
			problem := errors.New("evaluator-only identity or alias is excluded")
			if statErr != nil {
				problem = errors.New("inspecting approved candidate failed")
			}
			if closeErr := current.Close(); closeErr != nil {
				problem = errors.Join(problem, errors.New("closing excluded candidate failed"))
			}
			return nil, problem
		}
	}
	defer func() {
		if closeErr := current.Close(); closeErr != nil {
			err = errors.Join(err, errors.New("closing approved file failed"))
		}
	}()
	info, statErr := current.Stat()
	if statErr != nil {
		return nil, errors.New("inspecting approved file failed")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("unsupported file type; only regular files can be captured")
	}
	content, err = workspaceRead(current, info.Size(), limit)
	if err != nil {
		return content, err
	}
	if root.afterRead != nil {
		root.afterRead(current)
	}
	after, statErr := current.Stat()
	if statErr != nil || !sameObservedFile(info, after) {
		return content, errors.New("approved file metadata changed during capture")
	}
	return content, nil
}
