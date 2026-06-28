package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/models"
	"github.com/spf13/cobra"
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
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	artifact := "artifact"
	switch filepath.Base(path) {
	case "eval.yaml", "eval.yml":
		artifact = "eval.yaml"
	default:
		if filepath.Ext(path) == ".json" {
			artifact = "results.json"
		}
	}

	_, err := fmt.Fprintf(out, "%s is already compatible with schemaVersion %s; no migration needed.\n", artifact, models.CurrentSchemaVersion)
	return err
}
