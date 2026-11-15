package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/runnerassurance"
	"github.com/spf13/cobra"
)

const defaultAssurancePackagePath = "docs/PRODUCTION_INTEGRATION_ASSURANCE-1.0.json"

func newRunnerAssuranceCommand() *cobra.Command {
	var inputPath, outputFormat, candidateEvidencePath, evidenceRoot string
	cmd := &cobra.Command{
		Use:          "assurance",
		Short:        "Build and validate the versioned production-integration assurance package",
		SilenceUsage: true,
	}
	build := &cobra.Command{
		Use:   "build",
		Short: "Validate package evidence and emit the JSON package or generated documentation page",
		Long: `Validate the v0.42.0 production-integration assurance package against its
published schema, referenced contract schemas, repository evidence files, and
existing Lab guides, then write the deterministic package or documentation to
stdout. This maintainer command runs offline from a Waza source checkout.
Use --candidate-evidence with --evidence-root to bind the generated package to
one exact qualification workflow artifact and its locally verified file hashes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := runnerassurance.FindRepositoryRoot(".")
			if err != nil {
				return err
			}
			relativeInput := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(inputPath, "\\", "/")))
			relative, relErr := filepath.Rel(".", relativeInput)
			if relErr != nil || relativeInput == "." || filepath.IsAbs(relativeInput) ||
				filepath.VolumeName(relativeInput) != "" || relative == ".." ||
				strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return fmt.Errorf("--input must be a repository-relative path without parent traversal")
			}
			input, err := os.ReadFile(filepath.Join(root, relativeInput))
			if err != nil {
				return fmt.Errorf("reading assurance package %q: %w", inputPath, err)
			}
			var result runnerassurance.Result
			if candidateEvidencePath != "" {
				if evidenceRoot == "" {
					return fmt.Errorf("--evidence-root is required with --candidate-evidence")
				}
				evidence, err := os.ReadFile(candidateEvidencePath)
				if err != nil {
					return fmt.Errorf("reading workflow evidence %q: %w", candidateEvidencePath, err)
				}
				result, err = runnerassurance.BuildWithWorkflowEvidence(input, root, evidence, evidenceRoot)
			} else {
				if evidenceRoot != "" {
					return fmt.Errorf("--evidence-root requires --candidate-evidence")
				}
				result, err = runnerassurance.Build(input, root)
			}
			if err != nil {
				return err
			}
			var output []byte
			switch outputFormat {
			case "json":
				output = result.JSON
			case "markdown":
				output = result.Markdown
			default:
				return fmt.Errorf("--format must be json or markdown")
			}
			_, err = cmd.OutOrStdout().Write(output)
			return err
		},
	}
	build.Flags().StringVar(&inputPath, "input", defaultAssurancePackagePath, "Versioned assurance package JSON (repository-relative)")
	build.Flags().StringVar(&outputFormat, "format", "json", "Output format: json or markdown")
	build.Flags().StringVar(&candidateEvidencePath, "candidate-evidence", "", "Workflow run and uploaded qualification artifact identity JSON")
	build.Flags().StringVar(&evidenceRoot, "evidence-root", "", "Downloaded qualification artifact directory to hash and validate")
	cmd.AddCommand(build)
	return cmd
}
