package main

import (
	"encoding/json"
	"fmt"

	"github.com/azure/azure-dev/cli/azd/pkg/azdext"
	"github.com/spf13/cobra"
)

const metadataSchemaVersion = "1.0"
const extensionID = "microsoft.azd.waza"

func newMetadataCommand(rootCmd *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:    "metadata",
		Short:  "Output extension metadata as JSON",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			metadata := azdext.GenerateExtensionMetadata(metadataSchemaVersion, extensionID, rootCmd)

			data, err := json.MarshalIndent(metadata, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal metadata: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
}
