package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/microsoft/waza/cmd/waza/dev"
	"github.com/microsoft/waza/cmd/waza/tokens"
	"github.com/microsoft/waza/internal/usagetelemetry"
	versionpkg "github.com/microsoft/waza/internal/version"
	"github.com/spf13/cobra"
)

var version = "dev"

func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "waza",
		Short: "Waza - CLI tool for evaluating agent workflows and skills",
		Long: `Waza is a command-line tool for evaluating agent workflows, skills and custom agents.

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
	noUpdateCheck := cmd.PersistentFlags().Bool("no-update-check", false, "Disable automatic update check")
	// Honored by the usage telemetry policy, which scans argv before Cobra runs.
	cmd.PersistentFlags().Bool(usagetelemetry.NoFlagName, false, "Disable product usage telemetry for this invocation and its child processes")

	var checker *versionpkg.Checker
	cmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if *debugLogging {
			logLevel.Set(slog.LevelDebug)
		}
		if shouldRunUpdateCheck(cmd, *noUpdateCheck) {
			checker = versionpkg.NewChecker(version)
			checker.Run(context.Background())
		}
	}
	cmd.PersistentPostRun = func(cmd *cobra.Command, args []string) {
		if checker != nil {
			versionpkg.PrintNotice(checker.Result(), "")
		}
	}

	// Add subcommands
	cmd.AddCommand(newRunCommand())
	cmd.AddCommand(newPreflightCommand())
	cmd.AddCommand(newInitCommand())
	cmd.AddCommand(newGetCommand())
	cmd.AddCommand(tokens.NewCommand())
	cmd.AddCommand(newCompareCommand())
	cmd.AddCommand(newComparePlanCommand())
	cmd.AddCommand(newCompareAssurancePlanCommand())
	cmd.AddCommand(newCompareCollectCommand())
	cmd.AddCommand(newGateCommand())
	cmd.AddCommand(newCoverageCommand())
	cmd.AddCommand(dev.NewCommand())
	cmd.AddCommand(newGradeCommand())
	cmd.AddCommand(newAssureCommand())
	cmd.AddCommand(newMetadataCommand(cmd))
	cmd.AddCommand(newCheckCommand())
	cmd.AddCommand(newSuggestCommand())
	cmd.AddCommand(newCacheCommand())
	cmd.AddCommand(newNewCommand())
	cmd.AddCommand(newSessionCommand())
	cmd.AddCommand(newServeCommand())
	cmd.AddCommand(newResultsCommand())
	cmd.AddCommand(newModelsCommand())
	cmd.AddCommand(newQualityCommand())
	cmd.AddCommand(newUpdateCommand())
	cmd.AddCommand(newSpecCommand())
	cmd.AddCommand(newMigrateCommand())
	cmd.AddCommand(newMCPMockCommand())
	cmd.AddCommand(newCommandMockCommand())
	cmd.AddCommand(newReplayCommand())
	cmd.AddCommand(newAdversarialCommand())
	cmd.AddCommand(newRegistryCommand())
	cmd.AddCommand(newRunnerCommand())
	cmd.AddCommand(newIsolationCommand())
	cmd.AddCommand(newOptimizeCommand())
	cmd.AddCommand(newArtifactCommand())
	cmd.AddCommand(newAggregateCommand())
	cmd.AddCommand(newInteropCommand())
	cmd.AddCommand(newTelemetryCommand())

	return cmd
}

// execute runs the CLI and reports the resolved command and whether its run
// function was entered, so argument and setup failures are distinguishable.
func execute(diagnostics io.Writer, args []string) (*cobra.Command, bool, error) {
	rootCmd := newRootCommand()
	rootCmd.SetErr(diagnostics)
	rootCmd.SetArgs(args)
	entered := false
	markCommandEntry(rootCmd, &entered)
	cmd, err := rootCmd.ExecuteC()
	return cmd, entered, err
}

func markCommandEntry(cmd *cobra.Command, entered *bool) {
	if runE := cmd.RunE; runE != nil {
		cmd.RunE = func(c *cobra.Command, args []string) error {
			*entered = true
			return runE(c, args)
		}
	} else if run := cmd.Run; run != nil {
		cmd.Run = func(c *cobra.Command, args []string) {
			*entered = true
			run(c, args)
		}
	}
	for _, child := range cmd.Commands() {
		markCommandEntry(child, entered)
	}
}

// collectableCommand excludes telemetry management, help, shell completion,
// and hidden internal helpers from usage collection.
func collectableCommand(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Hidden || strings.HasPrefix(c.Name(), "__") {
			return false
		}
		if c.HasParent() && !c.Parent().HasParent() {
			switch c.Name() {
			case "telemetry", "help", "completion":
				return false
			}
		}
	}
	return true
}

// commandName returns the canonical command path without the root name. It
// never includes arguments or flag values.
func commandName(cmd *cobra.Command) string {
	if cmd == nil || !cmd.HasParent() {
		return ""
	}
	return strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
}

func commandOutcome(err error, code int, entered bool) string {
	switch {
	case err == nil:
		return usagetelemetry.CommandSuccess
	case errors.Is(err, context.Canceled):
		return usagetelemetry.CommandCancelled
	case !entered:
		return usagetelemetry.CommandSetupError
	case code == ExitTestFailed:
		return usagetelemetry.CommandTestFailed
	}
	return usagetelemetry.CommandRuntimeError
}

func shouldRunUpdateCheck(cmd *cobra.Command, noUpdateCheck bool) bool {
	if noUpdateCheck || os.Getenv("WAZA_NO_UPDATE_CHECK") != "" {
		return false
	}
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "update" || c.Name() == "telemetry" || c.Name() == "preflight" || c.Name() == "compare-plan" || c.Name() == "compare-assurance-plan" || c.Name() == "compare-collect" || c.Name() == "assure" || c.Name() == "runner" || c.Name() == "interop" || c.Name() == "aggregate" {
			return false
		}
	}
	for _, name := range []string{"release-policy", "collection-dir", "assurance-contract",
		"runner-compat",
		"baseline-eval", "candidate-eval", "baseline-context-dir", "candidate-context-dir",
		"baseline-references", "candidate-references", "baseline-review", "candidate-review",
		"baseline-accept-review-source", "candidate-accept-review-source"} {
		if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
			return false
		}
	}
	return true
}
