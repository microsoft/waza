package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/models"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newMigrateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate <file>",
		Short: "Migrate a waza schema artifact to the current schema version",
		Long: `Migrate a waza schema artifact to the current schema version.

Legacy eval/task/result artifacts retain schema 1.x. Scenario evals explicitly
select schema 2.0; supported files need no migration and are never rewritten.
Scenario evals also support custom .yaml/.yml filenames. Legacy YAML evals
retain the eval.yaml/eval.yml filename convention; JSON results use their shape.
Future migrations will require explicit migration steps here.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrate(cmd.OutOrStdout(), args[0])
		},
	}
	return cmd
}

func runMigrate(out io.Writer, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	artifact, version, err := readArtifactSchemaVersion(path, data)
	if err != nil {
		return err
	}

	if artifact == "eval.yaml" && version == models.ScenarioSchemaVersion {
		if _, err := models.LoadEvalSpec(path); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "%s is a supported scenario eval with schemaVersion %s; no migration needed.\n", path, version)
		return err
	}

	version, err = models.ValidateSchemaVersion(artifact, path, version)
	if err != nil {
		return err
	}

	currentVersion := models.CurrentSchemaVersion
	if artifact == "results.json" {
		currentVersion = models.CurrentResultSchemaVersion
		if version == models.CurrentSchemaVersion {
			_, err = fmt.Fprintf(out, "%s is already compatible with schemaVersion %s (legacy reader default); current result writer schemaVersion %s; no migration needed.\n", artifact, version, currentVersion)
			return err
		}
	}
	if version != currentVersion {
		_, err = fmt.Fprintf(out, "%s uses schemaVersion %s, which is compatible with waza schemaVersion %s; no migration needed.\n", artifact, version, currentVersion)
		return err
	}

	_, err = fmt.Fprintf(out, "%s is already compatible with schemaVersion %s; no migration needed.\n", artifact, currentVersion)
	return err
}

func readArtifactSchemaVersion(path string, data []byte) (artifact string, version string, err error) {
	base := filepath.Base(path)
	canonicalEval := base == "eval.yaml" || base == "eval.yml"
	extension := filepath.Ext(path)
	if canonicalEval || extension == ".yaml" || extension == ".yml" {
		var legacyHeader struct {
			SchemaVersion string `yaml:"schemaVersion"`
		}
		if canonicalEval {
			// Preserve the original legacy header parsing diagnostics.
			if err := yaml.Unmarshal(data, &legacyHeader); err != nil {
				return "", "", fmt.Errorf("parsing %s: %w", path, err)
			}
		}
		var header struct {
			Scenario yaml.Node `yaml:"scenario"`
		}
		if err := yaml.Unmarshal(data, &header); err != nil {
			return "", "", fmt.Errorf("parsing %s: %w", path, err)
		}
		if header.Scenario.Kind != 0 {
			version, _, err := models.ClassifyEvalSpec(data, path)
			if err != nil {
				return "", "", err
			}
			return "eval.yaml", version, nil
		}
		if canonicalEval {
			return "eval.yaml", legacyHeader.SchemaVersion, nil
		}
	}
	if extension == ".json" {
		version, ok, err := models.ProbeEvaluationOutcomeSchemaVersion(data)
		if err != nil {
			return "", "", fmt.Errorf("parsing %s: %w", path, err)
		}
		if !ok {
			return "", "", fmt.Errorf("unsupported JSON schema artifact %s: expected a results.json object with top-level eval_id, eval_name, summary, or tasks", path)
		}
		return "results.json", version, nil
	}
	return "", "", fmt.Errorf("unsupported schema artifact %s: expected eval.yaml, eval.yml, or a JSON results artifact", path)
}
