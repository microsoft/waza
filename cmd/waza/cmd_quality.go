package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/quality"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/spf13/cobra"
)

var newQualityEngine = func(modelID string) execution.AgentEngine {
	return execution.NewCopilotEngineBuilder(modelID, nil).Build()
}

type qualityFlags struct {
	model  string
	format string
	rubric string
}

func newQualityCommand() *cobra.Command {
	_, defaultModel := scaffold.ReadProjectDefaults()
	flags := &qualityFlags{
		model:  defaultModel,
		format: "table",
	}

	cmd := &cobra.Command{
		Use:   "quality <skill-path>",
		Short: "Evaluate skill content quality with an LLM judge",
		Long: `Analyze a SKILL.md using an LLM-as-Judge to score quality across dimensions:
clarity, completeness, trigger precision, scope coverage, and anti-patterns.

Each dimension is scored 1-5 with specific feedback. Requires Copilot authentication.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runQualityCommand(cmd, args[0], flags)
		},
		SilenceErrors: true,
	}

	cmd.Flags().StringVar(&flags.model, "model", flags.model, "Model to use as judge")
	cmd.Flags().StringVar(&flags.format, "format", "table", "Output format: table|json")
	cmd.Flags().StringVar(&flags.rubric, "rubric", "", "Path to custom rubric file (reserved for future use)")

	return cmd
}

func runQualityCommand(cmd *cobra.Command, skillPath string, flags *qualityFlags) error {
	if flags.format != "table" && flags.format != "json" {
		return fmt.Errorf("invalid format %q: must be table or json", flags.format)
	}

	if flags.rubric != "" {
		return fmt.Errorf("custom rubric files are not yet supported (coming soon)")
	}

	engine := newQualityEngine(flags.model)

	if err := engine.Initialize(cmd.Context()); err != nil {
		if isAuthError(err) {
			return fmt.Errorf("not authenticated — run \"copilot login\" first")
		}
		return fmt.Errorf("failed to connect to Copilot: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer func() {
		_ = engine.Shutdown(shutdownCtx)
		_ = execution.ShutdownSharedClient(shutdownCtx)
	}()

	resp, err := quality.Judge(cmd.Context(), engine, quality.JudgeOptions{
		SkillPath: skillPath,
	})
	if err != nil {
		// If we got a partial response with validation issues, still display it
		if resp != nil && strings.Contains(err.Error(), "judge response validation") {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n\n", err) //nolint:errcheck
			return outputQualityReport(cmd, resp, flags.format)
		}
		return err
	}

	return outputQualityReport(cmd, resp, flags.format)
}

func outputQualityReport(cmd *cobra.Command, resp *quality.JudgeResponse, format string) error {
	w := cmd.OutOrStdout()

	switch format {
	case "json":
		output, err := quality.FormatJSON(resp)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, output) //nolint:errcheck
	default:
		fmt.Fprint(w, quality.FormatTable(resp)) //nolint:errcheck
	}

	return nil
}
