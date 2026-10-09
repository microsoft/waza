//go:build linux || darwin

package snapshot

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

type materializeHooks struct {
	afterAdmission    func(string)
	beforeFile        func(string, string)
	beforePublication func(string)
}

type materializedEntry struct {
	parent *os.File
	name   string
	info   os.FileInfo
	dir    bool
}

func materializeVerifiedWorkspace(snap *Snapshot, required []string) (*MaterializedWorkspace, error) {
	return materializeWithHooks(snap, required, materializeHooks{})
}

func materializeWithHooks(snap *Snapshot, required []string, hooks materializeHooks) (_ *MaterializedWorkspace, resultErr error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, errors.New("snapshot: select private materialization directory failed")
	}
	parentFD, err := unix.Open(cwd, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("snapshot: open materialization parent directory failed")
	}
	container := os.NewFile(uintptr(parentFD), "materialization parent")
	name := ".waza-evidence-" + rand.Text()
	root := filepath.Join(cwd, name)
	if err := unix.Mkdirat(parentFD, name, 0o700); err != nil {
		problem := errors.New("snapshot: create private materialization directory failed")
		if closeErr := container.Close(); closeErr != nil {
			problem = errors.Join(problem, errors.New("snapshot: close materialization parent failed"))
		}
		return nil, problem
	}
	dir, err := openMaterializedEntry(container, name, true)
	if err != nil {
		if removeErr := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); removeErr != nil {
			err = errors.Join(err, errors.New("snapshot: remove unadmitted materialization directory failed"))
		}
		if closeErr := container.Close(); closeErr != nil {
			err = errors.Join(err, errors.New("snapshot: close materialization parent failed"))
		}
		return nil, err
	}
	handles := []*os.File{container, dir}
	directories := map[string]*os.File{"": dir}
	var entries []materializedEntry
	workspace := &MaterializedWorkspace{dir: root}
	workspace.cleanup = func() error {
		var problems []error
		for _, entry := range slices.Backward(entries) {
			flags := 0
			if entry.dir {
				flags = unix.AT_REMOVEDIR
			}
			if err := unix.Unlinkat(int(entry.parent.Fd()), entry.name, flags); err != nil && !errors.Is(err, unix.ENOENT) {
				problems = append(problems, errors.New("snapshot: remove materialized entry failed"))
			}
		}
		if err := checkPrivateDirectoryIdentity(root, dir); err != nil {
			problems = append(problems, errors.New("snapshot: materialized directory identity changed during cleanup"))
		} else if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil {
			problems = append(problems, errors.New("snapshot: remove private materialization directory failed"))
		}
		for _, handle := range slices.Backward(handles) {
			if err := handle.Close(); err != nil {
				problems = append(problems, errors.New("snapshot: close materialization directory failed"))
			}
		}
		return errors.Join(problems...)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, workspace.Close())
		}
	}()
	if err := checkPrivateDirectoryIdentity(root, dir); err != nil {
		return nil, errors.New("snapshot: materialization root identity changed during admission")
	}
	if hooks.afterAdmission != nil {
		hooks.afterAdmission(root)
	}
	for _, file := range snap.WorkspaceFiles {
		if !slices.Contains(required, file.Path) {
			continue
		}
		if workspacePathProblem(file.Path, nil, DefaultPolicy()) != nil {
			return nil, errors.New("snapshot: required materialization path is unsafe")
		}
		if hooks.beforeFile != nil {
			hooks.beforeFile(root, file.Path)
		}
		parts := strings.Split(file.Path, "/")
		parent := dir
		prefix := ""
		for _, part := range parts[:len(parts)-1] {
			if prefix != "" {
				prefix += "/"
			}
			prefix += part
			if cached := directories[prefix]; cached != nil {
				parent = cached
				continue
			}
			mkdirErr := unix.Mkdirat(int(parent.Fd()), part, 0o700)
			if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				return nil, errors.New("snapshot: create private preserved directory failed")
			}
			child, err := openMaterializedEntry(parent, part, true)
			if err != nil {
				if mkdirErr == nil {
					entries = append(entries, materializedEntry{parent: parent, name: part, dir: true})
				}
				return nil, err
			}
			info, err := child.Stat()
			if err != nil {
				closeErr := child.Close()
				problem := errors.New("snapshot: inspect preserved directory failed")
				if closeErr != nil {
					problem = errors.Join(problem, errors.New("snapshot: close preserved directory failed"))
				}
				return nil, problem
			}
			handles = append(handles, child)
			directories[prefix] = child
			entries = append(entries, materializedEntry{parent: parent, name: part, info: info, dir: true})
			parent = child
		}
		name := parts[len(parts)-1]
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
		if err != nil {
			return nil, errors.New("snapshot: create exclusive preserved file failed")
		}
		output := os.NewFile(uintptr(fd), "preserved materialized file")
		info, statErr := output.Stat()
		entries = append(entries, materializedEntry{parent: parent, name: name, info: info})
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			problem := errors.New("snapshot: preserved file is not a private regular file")
			if closeErr := output.Close(); closeErr != nil {
				problem = errors.Join(problem, errors.New("snapshot: close preserved file failed"))
			}
			return nil, problem
		}
		_, writeErr := output.WriteString(file.Content)
		closeErr := output.Close()
		if writeErr != nil || closeErr != nil {
			return nil, errors.New("snapshot: write preserved file failed")
		}
	}
	if hooks.beforePublication != nil {
		hooks.beforePublication(root)
	}
	if err := checkPrivateDirectoryIdentity(root, dir); err != nil {
		return nil, errors.New("snapshot: materialized directory identity changed before publication")
	}
	for _, entry := range entries {
		current, err := openMaterializedEntry(entry.parent, entry.name, entry.dir)
		if err != nil {
			return nil, errors.New("snapshot: materialized entry identity changed before publication")
		}
		info, statErr := current.Stat()
		closeErr := current.Close()
		if statErr != nil || closeErr != nil || !os.SameFile(entry.info, info) {
			return nil, errors.New("snapshot: materialized entry identity changed before publication")
		}
	}
	if err := checkPrivateDirectoryIdentity(root, dir); err != nil {
		return nil, errors.New("snapshot: materialized directory identity changed before publication")
	}
	return workspace, nil
}

func openMaterializedEntry(parent *os.File, name string, directory bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	mode := os.FileMode(0o600)
	if directory {
		flags |= unix.O_DIRECTORY
		mode = 0o700
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, errors.New("snapshot: open private non-symlink preserved entry failed")
	}
	entry := os.NewFile(uintptr(fd), "private preserved entry")
	info, err := entry.Stat()
	if err != nil || info.Mode().Perm() != mode || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		if closeErr := entry.Close(); closeErr != nil {
			return nil, errors.New("snapshot: inspect and close private preserved entry failed")
		}
		return nil, errors.New("snapshot: preserved entry has unsupported type or permissions")
	}
	return entry, nil
}
