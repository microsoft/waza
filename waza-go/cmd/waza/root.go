package main

import (
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

	return cmd
}

func execute() error {
	rootCmd := newRootCommand()
	return rootCmd.Execute()
}
