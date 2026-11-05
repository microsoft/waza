package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/microsoft/waza/internal/scaffold"
	"github.com/spf13/cobra"
)

func scenarioNewCommandE(cmd *cobra.Command, name, outputPath, template string) (err error) {
	fileConfig := scaffold.ReadProjectFiles()
	_, model := scaffold.ReadProjectDefaults()
	if outputPath == "" {
		outputPath = filepath.Join("evals", name, fileConfig.EvalFile)
	}
	files, err := scaffold.ScenarioFiles(name, model, filepath.Base(outputPath), fileConfig.TaskGlob, fileConfig.TaskFileSuffix, template)
	if err != nil {
		return err
	}
	base := filepath.Dir(outputPath)
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	directory, err := openScenarioDirectory(base, false)
	if err != nil {
		return err
	}
	if directory != nil {
		for _, path := range paths {
			parent, parentErr := walkScenarioDirectory(directory, filepath.Dir(path), false)
			if parentErr != nil {
				return errors.Join(parentErr, directory.Close())
			}
			if parent == nil {
				continue
			}
			checkErr := checkScenarioOutput(parent, filepath.Base(path), filepath.Join(base, path))
			if checkErr = errors.Join(checkErr, parent.Close()); checkErr != nil {
				return errors.Join(checkErr, directory.Close())
			}
		}
		if err := directory.Close(); err != nil {
			return fmt.Errorf("closing scenario preflight directory: %w", err)
		}
	}
	directory, err = openScenarioDirectory(base, true)
	if err != nil {
		return err
	}
	parents := make(map[string]*os.Root, len(paths))
	defer func() {
		for _, path := range paths {
			if parent := parents[path]; parent != nil {
				if closeErr := parent.Close(); closeErr != nil {
					err = errors.Join(err, fmt.Errorf("closing scenario output parent %s: %w", path, closeErr))
				}
			}
		}
		if directory != nil {
			err = errors.Join(err, directory.Close())
		}
	}()
	for _, path := range paths {
		parent, err := walkScenarioDirectory(directory, filepath.Dir(path), true)
		if err != nil {
			return err
		}
		parents[path] = parent
		if err := checkScenarioOutput(parent, filepath.Base(path), filepath.Join(base, path)); err != nil {
			return err
		}
	}
	if err := writeScenarioFiles(paths, parents, base, files, writeScenarioFileAt); err != nil {
		return err
	}
	for _, path := range paths {
		closeErr := parents[path].Close()
		parents[path] = nil
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing scenario output parent %s: %w", path, closeErr))
		}
	}
	err = errors.Join(err, directory.Close())
	directory = nil
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Scenario scaffold created: %s\nReal execution: waza run %s\nFor harness-only checks, set config.executor: mock in a copy (not agent-quality evidence).\n", outputPath, outputPath)
	return nil
}

func checkScenarioOutput(parent *os.Root, name, path string) error {
	if _, err := parent.Lstat(name); err == nil {
		return fmt.Errorf("refusing to overwrite existing file: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking %s: %w", path, err)
	}
	return nil
}

func writeScenarioFiles(paths []string, parents map[string]*os.Root, base string, files map[string]string,
	create func(*os.Root, string, string, string) (*os.File, error),
) (err error) {
	created := make(map[string]*os.File, len(paths))
	defer func() {
		if err != nil {
			for _, path := range slices.Backward(paths) {
				file := created[path]
				if file == nil {
					continue
				}
				original, originalErr := file.Stat()
				if originalErr != nil {
					err = errors.Join(err, fmt.Errorf("inspecting scenario rollback ownership %s: %w", path, originalErr))
					continue
				}
				parent, name := parents[path], filepath.Base(path)
				current, statErr := parent.Lstat(name)
				switch {
				case errors.Is(statErr, os.ErrNotExist):
					continue
				case statErr != nil:
					err = errors.Join(err, fmt.Errorf("checking scenario rollback %s: %w", path, statErr))
				case !os.SameFile(original, current):
					err = errors.Join(err, fmt.Errorf("refusing scenario rollback of replaced file %s; inspect retained output", path))
				default:
					if removeErr := parent.Remove(name); removeErr != nil {
						err = errors.Join(err, fmt.Errorf("rolling back scenario output %s: %w", path, removeErr))
					}
				}
			}
		}
		for _, path := range paths {
			if file := created[path]; file != nil {
				if closeErr := file.Close(); closeErr != nil {
					err = errors.Join(err, fmt.Errorf("closing scenario output %s: %w", path, closeErr))
				}
			}
		}
	}()
	for _, path := range paths {
		file, createErr := create(parents[path], filepath.Base(path), filepath.Join(base, path), files[path])
		if file != nil {
			created[path] = file
		}
		if createErr != nil {
			return createErr
		}
	}
	return nil
}

func writeScenarioFileAt(parent *os.Root, name, path, contents string) (*os.File, error) {
	file, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("refusing to overwrite existing file: %s", path)
	}
	if err != nil {
		return nil, fmt.Errorf("creating %s: %w", path, err)
	}
	_, statErr := file.Stat()
	if statErr != nil {
		return nil, errors.Join(fmt.Errorf("inspecting new scenario file %s; retained because ownership is unavailable: %w", path, statErr), file.Close())
	}
	_, writeErr := file.WriteString(contents)
	if writeErr != nil {
		return file, fmt.Errorf("writing %s: %w", path, writeErr)
	}
	return file, nil
}
