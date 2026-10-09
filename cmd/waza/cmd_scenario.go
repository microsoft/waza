package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/microsoft/waza/internal/scaffold"
	"github.com/spf13/cobra"
)

func scenarioNewCommandE(cmd *cobra.Command, name, outputPath, template string) error {
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
	for _, path := range paths {
		full := filepath.Join(base, path)
		if _, err := os.Stat(full); err == nil {
			return fmt.Errorf("refusing to overwrite existing file: %s", full)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("checking %s: %w", full, err)
		}
	}
	for _, path := range paths {
		full := filepath.Join(base, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", full, err)
		}
		if err := os.WriteFile(full, []byte(files[path]), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", full, err)
		}
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Scenario scaffold created: %s\nReal execution: waza run %s\nFor harness-only checks, set config.executor: mock in a copy (not agent-quality evidence).\n", outputPath, outputPath)
	return nil
}
