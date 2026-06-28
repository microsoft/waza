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

The current schema version is 1.0, so v1 artifacts are already current and the
command performs no file changes. Future major schema versions will add explicit
migration steps here.`,
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

	version, err = models.ValidateSchemaVersion(artifact, path, version)
	if err != nil {
		return err
	}

	if version != models.CurrentSchemaVersion {
		_, err = fmt.Fprintf(out, "%s uses schemaVersion %s, which is compatible with waza schemaVersion %s; no migration needed.\n", artifact, version, models.CurrentSchemaVersion)
		return err
	}

	_, err = fmt.Fprintf(out, "%s is already compatible with schemaVersion %s; no migration needed.\n", artifact, models.CurrentSchemaVersion)
	return err
}

func readArtifactSchemaVersion(path string, data []byte) (artifact string, version string, err error) {
	switch filepath.Base(path) {
	case "eval.yaml", "eval.yml":
		artifact = "eval.yaml"
		var header struct {
			SchemaVersion string `yaml:"schemaVersion"`
		}
		if err := yaml.Unmarshal(data, &header); err != nil {
			return "", "", fmt.Errorf("parsing %s: %w", path, err)
		}
		return artifact, header.SchemaVersion, nil
	default:
		if filepath.Ext(path) == ".json" {
			version, ok, err := models.ProbeEvaluationOutcomeSchemaVersion(data)
			if err != nil {
				return "", "", fmt.Errorf("parsing %s: %w", path, err)
			}
			if !ok {
				return "", "", fmt.Errorf("unsupported JSON schema artifact %s: expected a results.json object with top-level eval_id, eval_name, summary, or tasks", path)
			}
			return "results.json", version, nil
		}
	}
	return "", "", fmt.Errorf("unsupported schema artifact %s: expected eval.yaml, eval.yml, or a JSON results artifact", path)
}
