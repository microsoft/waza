//go:build linux || darwin

package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type workspaceRoot struct {
	file *os.File
}

func openWorkspaceRoot(root string) (*workspaceRoot, error) {
	// Cleaning removes trailing slashes that would bypass final-symlink refusal.
	fd, err := unix.Open(filepath.Clean(root), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("workspace: root is unavailable or not a non-symlink directory")
	}
	return &workspaceRoot{file: os.NewFile(uintptr(fd), "workspace root")}, nil
}

func (root *workspaceRoot) Close() error {
	return root.file.Close()
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
					_ = unix.Close(fd) // Preserve the primary close failure.
				}
				return nil, errors.New("closing approved directory failed")
			}
		}
		if openErr != nil {
			return nil, errors.New("approved file is unavailable or has an unsupported symlink or ancestor")
		}
		current = os.NewFile(uintptr(fd), "approved workspace entry")
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
	after, statErr := current.Stat()
	if statErr != nil {
		return content, errors.New("inspecting captured file failed")
	}
	if after.Size() != info.Size() {
		return content, errors.New("approved file size changed during capture")
	}
	return content, nil
}
