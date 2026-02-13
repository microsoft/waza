package dev

import (
	"github.com/spf13/cobra"
)

// NewCommand returns the `waza dev` sub-command tree.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dev [skill-path]",
		Short: "Iteratively improve skill frontmatter compliance",
		Long: `Run a frontmatter improvement loop on a skill directory.

Reads SKILL.md from the target directory, scores frontmatter compliance, suggests and
optionally applies improvements, iterates until the target adherence level is reached
or max iterations are exhausted.

If skill-path is omitted, the current working directory is used.

Example:
  waza dev skills/code-explainer --target high --max-iterations 3`,
		Args:          cobra.MaximumNArgs(1),
		RunE:          runDev,
		SilenceErrors: true,
	}
	cmd.Flags().String("target", "medium-high", "Target adherence level: low | medium | medium-high | high")
	cmd.Flags().Int("max-iterations", 5, "Maximum improvement iterations")
	cmd.Flags().Bool("auto", false, "Auto-apply improvements without prompting")
	return cmd
}
