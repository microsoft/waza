package tokens

import (
	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Token management for markdown files",
		Long: `Analyze token counts in markdown files.

Limits are loaded from .waza.yaml (tokens.limits), falling back to
.token-limits.json, then built-in defaults.

Subcommands:
  check     Check files against token limits
  diff      Compare skill token budgets vs a base ref
  compare   Compare tokens between git refs
  count     Count tokens in markdown files
  suggest   Get optimization suggestions`,
	}
	cmd.AddCommand(newCheckCmd())
	cmd.AddCommand(newDiffCmd())
	cmd.AddCommand(newCompareCmd())
	cmd.AddCommand(newCountCmd())
	cmd.AddCommand(newProfileCmd())
	cmd.AddCommand(newSuggestCmd())
	return cmd
}
