package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

func (s *session) checkObserved() error {
	for _, item := range s.observed {
		if err := item.check(); err != nil {
			return err
		}
	}
	return nil
}

func (item observation) check() error {
	current, err := item.parent.Lstat(item.name)
	if err != nil || !os.SameFile(item.info, current) || item.info.Mode().Type() != current.Mode().Type() {
		return fmt.Errorf("observed entry %q was replaced: %w", item.name, errors.Join(err, errors.New("identity mismatch")))
	}
	return nil
}

func (s *session) checkDirectory(root *os.Root) error {
	for {
		item, ok := s.rootEntries[root]
		if !ok {
			return nil
		}
		if err := item.check(); err != nil {
			return err
		}
		root = item.parent
	}
}

func intendedDirectories(snap snapshot) map[string]bool {
	dirs := make(map[string]bool)
	for _, file := range snap.files {
		for parent := path.Dir(file.Path); parent != "."; parent = path.Dir(parent) {
			dirs[parent] = true
		}
	}
	return dirs
}

func admitInstalled(expected, actual snapshot, complete bool) error {
	entries := make(map[string]entry, len(expected.files))
	for _, file := range expected.files {
		entries[file.Path] = file
	}
	for _, file := range actual.files {
		want, ok := entries[file.Path]
		if !ok || file != want {
			return fmt.Errorf("existing output %q conflicts with verified inventory; no files were replaced", file.Path)
		}
		delete(entries, file.Path)
	}
	for _, file := range expected.files {
		if _, missing := entries[file.Path]; missing && (complete || !strings.HasPrefix(file.Path, "assets/")) {
			return fmt.Errorf("required existing output %q is missing; only absent intended assets may be installed", file.Path)
		}
	}
	return nil
}

func (s *session) installFile(dist *os.Root, name string, data []byte) error {
	dir := dist
	parts := strings.Split(name, "/")
	prefix := ""
	for _, part := range parts[:len(parts)-1] {
		prefix = path.Join(prefix, part)
		if held := s.directories[prefix]; held != nil {
			dir = held
			continue
		}
		if err := dir.Mkdir(part, 0o700); err != nil {
			return fmt.Errorf("exclusively create asset directory %q: %w", prefix, err)
		}
		var err error
		dir, err = s.child(dir, part)
		if err != nil {
			return err
		}
		s.directories[prefix] = dir
	}
	leaf := parts[len(parts)-1]
	if err := s.checkDirectory(dir); err != nil {
		return err
	}
	file, err := dir.OpenFile(leaf, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("exclusively create asset %q: %w", name, err)
	}
	s.files = append(s.files, file)
	info, err := file.Stat()
	if err != nil {
		return err
	}
	observed := observation{dir, leaf, info}
	s.observed = append(s.observed, observed)
	if err := observed.check(); err != nil {
		return err
	}
	if s.hooks.write != nil {
		err = s.hooks.write(file, data)
	} else {
		var n int
		n, err = file.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		return fmt.Errorf("write asset %q: %w", name, err)
	}
	return nil
}

func executeInstall(opts options, fault hooks) (digest string, err error) {
	s := session{hooks: fault, directories: make(map[string]*os.Root), rootEntries: make(map[*os.Root]observation)}
	destination := filepath.Join(opts.workspace, "web", "dist")
	started := false
	defer func() {
		err = errors.Join(err, s.close())
		if err != nil {
			digest = ""
			if started {
				err = fmt.Errorf("%w; partial output may remain (requested destination %q; current pathname is not guaranteed after directory replacement); no paths were deleted", err, destination)
			}
		}
	}()
	artifact, err := s.openRoot(opts.input)
	if err != nil {
		return "", err
	}
	expected, err := s.verify(artifact, opts.source, opts.digest)
	if err != nil {
		return "", err
	}
	workspace, err := s.openRoot(opts.workspace)
	if err != nil {
		return "", err
	}
	web, err := s.child(workspace, "web")
	if err != nil {
		return "", err
	}
	dist, err := s.child(web, "dist")
	if err != nil {
		return "", err
	}
	dirs := intendedDirectories(expected)
	actual, err := s.inventoryWithDirectories(dist, dirs)
	if err != nil {
		return "", err
	}
	complete := opts.command == "verify-installed"
	if err := admitInstalled(expected, actual, complete); err != nil {
		return "", err
	}
	if s.hooks.beforeCopy != nil {
		if err := s.hooks.beforeCopy(); err != nil {
			return "", err
		}
	}
	if err := s.checkObserved(); err != nil {
		return "", err
	}
	sourceNow, err := s.verify(artifact, opts.source, opts.digest)
	if err != nil || !equalSnapshot(expected, sourceNow) {
		return "", fmt.Errorf("artifact changed before installation: %w", errors.Join(err, errors.New("inventory mismatch")))
	}
	beforeWrite, err := s.inventoryWithDirectories(dist, dirs)
	if err != nil || !equalSnapshot(actual, beforeWrite) || !slices.Equal(actual.dirs, beforeWrite.dirs) {
		return "", fmt.Errorf("destination changed before installation: %w", errors.Join(err, errors.New("inventory mismatch")))
	}
	if err := s.checkObserved(); err != nil {
		return "", err
	}
	present := make(map[string]bool, len(actual.files))
	for _, file := range actual.files {
		present[file.Path] = true
	}
	for _, file := range expected.files {
		if present[file.Path] {
			continue
		}
		started = true
		if err := s.installFile(dist, file.Path, expected.data[file.Path]); err != nil {
			return "", err
		}
	}
	installed, err := s.inventoryWithDirectories(dist, dirs)
	if err != nil || !equalSnapshot(expected, installed) {
		return "", fmt.Errorf("reverify installed output: %w", errors.Join(err, errors.New("inventory mismatch")))
	}
	sourceNow, err = s.verify(artifact, opts.source, opts.digest)
	if err != nil || !equalSnapshot(expected, sourceNow) {
		return "", fmt.Errorf("artifact changed before installation completed: %w", errors.Join(err, errors.New("inventory mismatch")))
	}
	if err := s.checkObserved(); err != nil {
		return "", err
	}
	return opts.digest, nil
}
