package main

import (
	"fmt"
	"os"

	"github.com/spboyer/waza/internal/generate"
	"github.com/spboyer/waza/internal/workspace"
	"github.com/spf13/cobra"
)

var generateOutputDir string

func newGenerateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "generate <skill-name | SKILL.md>",
		Short: "Generate an eval suite from a SKILL.md file (alias for 'waza new')",
		Long: `Generate evaluation files from a SKILL.md file.

Note: 'waza generate' is an alias for 'waza new'. Prefer 'waza new <name>'.

Parses the YAML frontmatter (name, description) from the given SKILL.md and
creates an eval.yaml, starter task files, and a fixtures directory using the
same idempotent scaffolding as 'waza new'.

If the argument looks like a skill name (no path separators or file extension),
it is resolved via workspace detection to find the SKILL.md path.`,
		Args: cobra.ExactArgs(1),
		RunE: generateCommandE,
	}

	cmd.Flags().StringVarP(&generateOutputDir, "output-dir", "d", "", "Output directory (default: ./eval-{skill-name}/)")

	return cmd
}

func generateCommandE(cmd *cobra.Command, args []string) error {
	skillPath := args[0]

	fmt.Fprintln(cmd.OutOrStdout(), "Note: 'waza generate' is an alias for 'waza new'. Use 'waza new <name>' instead.") //nolint:errcheck

	// If arg looks like a skill name (not a path), resolve via workspace
	if !workspace.LooksLikePath(skillPath) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		ctx, err := workspace.DetectContext(wd)
		if err != nil {
			return fmt.Errorf("detecting workspace: %w", err)
		}
		si, err := workspace.FindSkill(ctx, skillPath)
		if err != nil {
			return err
		}
		skillPath = si.SkillPath
	}

	skill, err := generate.ParseSkillMD(skillPath)
	if err != nil {
		return fmt.Errorf("failed to parse SKILL.md: %w", err)
	}

	// If --output-dir is specified, use the legacy generate path
	if generateOutputDir != "" {
		return legacyGenerate(cmd, skill, generateOutputDir)
	}

	// Delegate to waza new's idempotent code path
	return newCommandE(cmd, []string{skill.Name}, "")
}

// legacyGenerate preserves the old --output-dir behavior for backward compatibility.
func legacyGenerate(cmd *cobra.Command, skill *generate.SkillFrontmatter, outDir string) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Generating eval suite for skill: %s\n", skill.Name) //nolint:errcheck
	fmt.Fprintf(cmd.OutOrStdout(), "Output directory: %s\n", outDir)                    //nolint:errcheck

	if err := generate.GenerateEvalSuite(skill, outDir); err != nil {
		return fmt.Errorf("failed to generate eval suite: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout())                                                    //nolint:errcheck
	fmt.Fprintln(cmd.OutOrStdout(), "Generated files:")                                //nolint:errcheck
	fmt.Fprintf(cmd.OutOrStdout(), "  %s/eval.yaml\n", outDir)                         //nolint:errcheck
	fmt.Fprintf(cmd.OutOrStdout(), "  %s/tasks/%s-basic.yaml\n", outDir, skill.Name)   //nolint:errcheck
	fmt.Fprintf(cmd.OutOrStdout(), "  %s/fixtures/sample.txt\n", outDir)               //nolint:errcheck
	fmt.Fprintln(cmd.OutOrStdout())                                                    //nolint:errcheck
	fmt.Fprintln(cmd.OutOrStdout(), "Next steps:")                                     //nolint:errcheck
	fmt.Fprintf(cmd.OutOrStdout(), "  1. Edit the task files in %s/tasks/\n", outDir)  //nolint:errcheck
	fmt.Fprintf(cmd.OutOrStdout(), "  2. Add real fixtures to %s/fixtures/\n", outDir) //nolint:errcheck
	fmt.Fprintf(cmd.OutOrStdout(), "  3. Run: waza run %s/eval.yaml\n", outDir)        //nolint:errcheck

	return nil
}
