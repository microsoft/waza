package graders

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// programContextFileName is the context file's name inside its private directory.
const programContextFileName = "context.json"

// programContextFile is a grading context file in a new private directory.
//
// Trust model: the grader program and other processes running as the same
// user are trusted; the program can read and copy the context anyway. The
// private directory and file protect against other local users, and cleanup
// errors fail grading so residue is never left silently.
//
// Cleanup removes only entries Waza created: the directory and the context
// file are identified at creation, and a moved, replaced or missing entry is
// left in place and reported instead of deleted. A replacement made by a
// concurrently running same-user process between verification and removal
// is out of scope.
type programContextFile struct {
	dir      string
	path     string
	dirInfo  os.FileInfo
	fileInfo os.FileInfo
	guards   []io.Closer
	hooks    programContextHooks
}

// programContextHooks wrap individual context-file steps so tests can inject
// deterministic failures per grader, without package globals.
type programContextHooks struct {
	created func(path string)
	write   func(write func([]byte) (int, error), data []byte) (int, error)
	close   func(close func() error) error
	remove  func(remove func() error) error
}

func createProgramContextFile(data []byte, hooks programContextHooks) (_ *programContextFile, err error) {
	dir, err := mkdirPrivateTemp("waza-grading-context-")
	if err != nil {
		return nil, fmt.Errorf("creating private grading context directory: %w", err)
	}
	f := &programContextFile{dir: dir, path: filepath.Join(dir, programContextFileName), hooks: hooks}
	defer func() {
		if err != nil {
			err = errors.Join(err, f.remove())
		}
	}()
	if f.dirInfo, err = entryIdentity(dir); err != nil {
		return nil, fmt.Errorf("inspecting grading context directory %s: %w", dir, err)
	}
	if err := f.hold(dir); err != nil {
		return nil, err
	}
	if hooks.created != nil {
		hooks.created(f.path)
	}
	file, err := createPrivateFile(f.path)
	if err != nil {
		return nil, fmt.Errorf("creating grading context file %s: %w", f.path, err)
	}
	f.fileInfo, err = file.Stat()
	writeErr := err
	if writeErr == nil {
		writeErr = hooks.doWrite(file.Write, data)
	}
	closeErr := hooks.doClose(file.Close)
	if writeErr != nil {
		writeErr = fmt.Errorf("writing grading context file %s: %w", f.path, writeErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("closing grading context file %s: %w", f.path, closeErr)
	}
	if err := errors.Join(writeErr, closeErr); err != nil {
		return nil, err
	}
	if err := f.hold(f.path); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *programContextFile) hold(path string) error {
	guard, err := holdEntry(path)
	if err != nil {
		return fmt.Errorf("opening grading context entry %s: %w", path, err)
	}
	if guard != nil {
		f.guards = append(f.guards, guard)
	}
	return nil
}

// remove deletes the context directory and everything in it after verifying
// that the directory and context file are still the ones Waza created.
// Anything else is left in place, and every failure reports a path so
// remaining evidence can be located.
func (f *programContextFile) remove() error {
	err := f.removeVerified()
	for _, guard := range f.guards {
		if closeErr := guard.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing grading context handle in %s: %w", f.dir, closeErr))
		}
	}
	f.guards = nil
	return err
}

func (f *programContextFile) removeVerified() error {
	if f.dirInfo != nil {
		current, err := entryIdentity(f.dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("grading context directory %s was moved; transcript data may remain elsewhere", f.dir)
		case err != nil:
			return fmt.Errorf("inspecting grading context directory %s (left in place): %w", f.dir, err)
		case !os.SameFile(f.dirInfo, current):
			return fmt.Errorf("grading context directory %s was replaced; the replacement was left in place and transcript data may remain elsewhere", f.dir)
		}
	}
	var fileErr error
	if f.fileInfo != nil {
		current, err := entryIdentity(f.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// The directory is still Waza's own, so it is removed, but the
			// context may have been moved out of it.
			fileErr = fmt.Errorf("grading context file %s was moved or deleted; transcript data may remain elsewhere", f.path)
		case err != nil:
			return fmt.Errorf("inspecting grading context file %s (directory %s left in place): %w", f.path, f.dir, err)
		case !os.SameFile(f.fileInfo, current):
			return fmt.Errorf("grading context file %s was replaced; directory %s was left in place", f.path, f.dir)
		}
	}
	if err := f.hooks.doRemove(func() error { return os.RemoveAll(f.dir) }); err != nil {
		return errors.Join(fileErr, fmt.Errorf("removing grading context directory %s (transcript data may remain): %w", f.dir, err))
	}
	return fileErr
}

// entryIdentity returns comparable identity for the entry at path without
// following a link. On Windows, os.Lstat resolves the file ID lazily from the
// path when compared, so plain files and directories are read through an open
// handle instead; anything else (a link) can never match a recorded entry.
func entryIdentity(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return info, err
	}
	h, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, statErr := h.Stat()
	return info, errors.Join(statErr, h.Close())
}

func (h programContextHooks) doWrite(write func([]byte) (int, error), data []byte) error {
	var n int
	var err error
	if h.write != nil {
		n, err = h.write(write, data)
	} else {
		n, err = write(data)
	}
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

func (h programContextHooks) doClose(close func() error) error {
	if h.close != nil {
		return h.close(close)
	}
	return close()
}

func (h programContextHooks) doRemove(remove func() error) error {
	if h.remove != nil {
		return h.remove(remove)
	}
	return remove()
}
