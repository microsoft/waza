package dev

import (
	"github.com/spf13/cobra"
)

const defaultCopilotModel = "claude-sonnet-4-20250514"

// NewCommand returns the `waza dev` sub-command tree.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dev <skill-name | skill-path> | <skill-name...> | --all",
		Short: "Iteratively improve skill frontmatter compliance",
		Long: `Run a frontmatter improvement loop on one or more skills.

Reads SKILL.md from each target, scores frontmatter compliance, suggests and
optionally applies improvements, iterates until the target adherence level is reached
or max iterations are exhausted.

A single target can be a workspace skill name, a directory, or a SKILL.md file.
Bare names resolve workspace skills first, then fall back to local directories.
Use ./ before a directory name to select that path explicitly.
The same targets work with --copilot and --scaffold-triggers.

Single skill:
  waza dev code-explainer
  waza dev skills/code-explainer --target high --max-iterations 3
  waza dev skills/code-explainer/SKILL.md
  waza dev SKILL.md
  waza dev .

Multiple skills:
  waza dev skill-a skill-b skill-c
  waza dev --all                         # all skills in workspace
  waza dev --all --filter low            # only skills at "low" adherence

Use --copilot to get a non-interactive report that includes recommendations from Copilot.`,
		Args:          cobra.ArbitraryArgs,
		RunE:          runDev,
		SilenceErrors: true,
	}
	cmd.Flags().String("target", "medium-high", "Target adherence level: low | medium | medium-high | high (iterative mode only)")
	cmd.Flags().Int("max-iterations", 5, "Maximum improvement iterations (iterative mode only)")
	cmd.Flags().Bool("auto", false, "Auto-apply improvements without prompting (iterative mode only)")
	cmd.Flags().Bool("copilot", false, "Generate a non-interactive markdown report with Copilot suggestions")
	cmd.Flags().String("model", defaultCopilotModel, "Model to use with --copilot")
	cmd.Flags().Bool("all", false, "Process all skills in workspace")
	cmd.Flags().String("filter", "", "Filter skills by adherence level (used with --all): low | medium | medium-high | high")
	cmd.Flags().Bool("scaffold-triggers", false, "Generate tests/trigger_tests.yaml from SKILL.md frontmatter USE FOR / DO NOT USE FOR phrases")
	return cmd
}
