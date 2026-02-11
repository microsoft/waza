package main

import (
	"github.com/spboyer/waza/cmd/waza/tokens"
	"github.com/spf13/cobra"
)

var version = "dev"

func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "waza",
		Short: "Waza - CLI tool for evaluating Agent Skills",
		Long: `Waza is a command-line tool for evaluating Agent Skills.

It provides tools to run benchmarks, validate agent behavior, and measure
performance against predefined test cases.`,
		Version:      version,
		SilenceUsage: true,
	}

	// Add subcommands
	cmd.AddCommand(newRunCommand())
	cmd.AddCommand(newInitCommand())
	cmd.AddCommand(newGenerateCommand())
	cmd.AddCommand(tokens.NewCommand())
	cmd.AddCommand(newCompareCommand())
	cmd.AddCommand(newMetadataCommand(cmd))

	return cmd
}

func execute() error {
	rootCmd := newRootCommand()
	return rootCmd.Execute()
}
