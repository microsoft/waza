package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/pathadmission"
)

func openScenarioDirectory(path string, create bool) (directory *os.Root, err error) {
	cwd, err := os.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("opening scenario working directory: %w", err)
	}
	defer func() {
		// This runs after the filesystem-anchor closer, including its errors.
		directory, err = finishScenarioDirectoryAdmission(directory, errors.Join(err, cwd.Close()))
	}()
	if !filepath.IsAbs(path) && filepath.IsLocal(filepath.Clean(path)) {
		if err := validateScenarioWorkingDirectory(cwd); err != nil {
			return nil, err
		}
		return walkScenarioDirectory(cwd, filepath.Clean(path), create)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving scenario directory %s: %w", path, err)
	}
	absolute, err = pathadmission.SystemRootAlias(absolute)
	if err != nil {
		return nil, err
	}
	anchorPath := filepath.VolumeName(absolute) + string(filepath.Separator)
	anchor, err := os.OpenRoot(anchorPath)
	if err != nil {
		return nil, fmt.Errorf("opening scenario filesystem anchor %s: %w", anchorPath, err)
	}
	defer func() { err = errors.Join(err, anchor.Close()) }()
	relative, err := filepath.Rel(anchorPath, absolute)
	if err != nil {
		return nil, fmt.Errorf("resolving scenario directory from filesystem anchor: %w", err)
	}
	return walkScenarioDirectory(anchor, relative, create)
}

func finishScenarioDirectoryAdmission(directory *os.Root, err error) (*os.Root, error) {
	if err != nil && directory != nil {
		err = errors.Join(err, directory.Close())
		directory = nil
	}
	return directory, err
}

func validateScenarioWorkingDirectory(root *os.Root) (err error) {
	current, err := os.Open(".")
	if err != nil {
		return fmt.Errorf("checking scenario working directory: %w", err)
	}
	defer func() { err = errors.Join(err, current.Close()) }()
	observed, err := current.Stat()
	if err != nil {
		return fmt.Errorf("checking scenario working directory identity: %w", err)
	}
	retained, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("checking retained scenario working directory: %w", err)
	}
	if !os.SameFile(observed, retained) {
		return fmt.Errorf("scenario working directory changed during admission")
	}
	return nil
}

func walkScenarioDirectory(anchor *os.Root, relative string, create bool) (directory *os.Root, err error) {
	current, err := anchor.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("retaining scenario directory anchor: %w", err)
	}
	defer func() {
		if current != nil {
			err = errors.Join(err, current.Close())
		}
	}()
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		if !filepath.IsLocal(part) || part == ".." {
			return nil, fmt.Errorf("scenario directory component %q must be local", part)
		}
		info, statErr := current.Lstat(part)
		if errors.Is(statErr, os.ErrNotExist) {
			if !create {
				return nil, nil
			}
			// MkdirAll accepts directory links; admit each component separately.
			if mkdirErr := current.Mkdir(part, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return nil, fmt.Errorf("creating scenario directory %s: %w", part, mkdirErr)
			}
			info, statErr = current.Lstat(part)
		}
		if statErr != nil {
			return nil, fmt.Errorf("checking scenario directory %s: %w", part, statErr)
		}
		child, openErr := admitScenarioDirectory(current, part, info)
		if openErr != nil {
			return nil, openErr
		}
		if closeErr := current.Close(); closeErr != nil {
			current = nil
			return nil, errors.Join(fmt.Errorf("closing scenario parent directory: %w", closeErr), child.Close())
		}
		current = child
	}
	directory, current = current, nil
	return directory, nil
}

func admitScenarioDirectory(parent *os.Root, name string, observed os.FileInfo) (directory *os.Root, err error) {
	if observed.Mode()&os.ModeSymlink != 0 || !observed.IsDir() {
		return nil, fmt.Errorf("scenario parent %s must be a directory, not a symlink; use a physical output path", name)
	}
	directory, err = parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("opening scenario parent %s: %w", name, err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, directory.Close())
			directory = nil
		}
	}()
	err = validateScenarioDirectory(parent, name, observed, directory)
	return directory, err
}

func validateScenarioDirectory(parent *os.Root, name string, observed os.FileInfo, directory *os.Root) error {
	current, err := parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("rechecking scenario parent %s: %w", name, err)
	}
	retained, err := directory.Stat(".")
	if err != nil {
		return fmt.Errorf("checking retained scenario parent %s: %w", name, err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
		!os.SameFile(observed, current) || !os.SameFile(observed, retained) {
		return fmt.Errorf("scenario parent %s changed during admission; use a stable physical output path", name)
	}
	return nil
}
