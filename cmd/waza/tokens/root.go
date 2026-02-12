package tokens

import (
	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "Token management for markdown files",
		Long: `Analyze token counts in markdown files. Subcommands:
  check     Check files against token limits
  compare   Compare tokens between git refs
  count     Count tokens in markdown files
  suggest   Get optimization suggestions`,
	}
	cmd.AddCommand(newCheckCmd())
	cmd.AddCommand(newCompareCmd())
	cmd.AddCommand(newCountCmd())
	cmd.AddCommand(newSuggestCmd())
	return cmd
}
