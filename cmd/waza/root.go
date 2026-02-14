package main

import (
	"log/slog"
	"os"

	"github.com/spboyer/waza/cmd/waza/dev"
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

	logLevel := &slog.LevelVar{}
	logLevel.Set(slog.LevelInfo)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	debugLogging := cmd.PersistentFlags().Bool("debug", false, "Enable debug logging")
	cmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if *debugLogging {
			logLevel.Set(slog.LevelDebug)
		}
	}

	// Add subcommands
	cmd.AddCommand(newRunCommand())
	cmd.AddCommand(newInitCommand())
	cmd.AddCommand(newGenerateCommand())
	cmd.AddCommand(tokens.NewCommand())
	cmd.AddCommand(newCompareCommand())
	cmd.AddCommand(dev.NewCommand())
	cmd.AddCommand(newMetadataCommand(cmd))
	cmd.AddCommand(newCacheCommand())

	return cmd
}

func execute() error {
	rootCmd := newRootCommand()
	return rootCmd.Execute()
}
