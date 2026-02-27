package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spboyer/waza/internal/projectconfig"
	"github.com/spboyer/waza/internal/storage"
	"github.com/spf13/cobra"
)

func newResultsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "results",
		Short: "Manage stored evaluation results",
		Long: `Query and compare evaluation results stored locally or in Azure Blob Storage.

Requires a storage section in .waza.yaml:

  storage:
    provider: azure-blob
    accountName: myaccount
    containerName: waza-results
    enabled: true`,
	}

	cmd.AddCommand(newResultsListCommand())
	cmd.AddCommand(newResultsCompareCommand())

	return cmd
}

func newResultsListCommand() *cobra.Command {
	var (
		skillFilter string
		modelFilter string
		sinceStr    string
		limit       int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List stored evaluation results",
		Long: `List evaluation results from configured storage.

Examples:
  waza results list
  waza results list --skill my-skill --model gpt-4o
  waza results list --since 2026-01-01 --limit 10`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := projectconfig.Load(".")
			if err != nil || cfg == nil {
				cfg = projectconfig.New()
			}

			if cfg.Storage.Provider == "" || !cfg.Storage.Enabled {
				return fmt.Errorf("storage not configured. Add a storage section to .waza.yaml")
			}

			store, err := storage.NewStore(&cfg.Storage, cfg.Paths.Results)
			if err != nil {
				return fmt.Errorf("creating result store: %w", err)
			}

			opts := storage.ListOptions{
				Skill: skillFilter,
				Model: modelFilter,
				Limit: limit,
			}

			if sinceStr != "" {
				t, err := time.Parse("2006-01-02", sinceStr)
				if err != nil {
					return fmt.Errorf("invalid --since date (use YYYY-MM-DD): %w", err)
				}
				opts.Since = t
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			results, err := store.List(ctx, opts)
			if err != nil {
				return fmt.Errorf("listing results: %w", err)
			}

			if len(results) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No results found.")
				return nil
			}

			// Print table header
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-36s  %-20s  %-24s  %9s  %s\n",
				"Run ID", "Skill", "Model", "Pass Rate", "Timestamp")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n",
				strings.Repeat("-", 36)+"  "+strings.Repeat("-", 20)+"  "+strings.Repeat("-", 24)+"  "+strings.Repeat("-", 9)+"  "+strings.Repeat("-", 19))

			for _, r := range results {
				runID := r.RunID
				if len(runID) > 36 {
					runID = runID[:33] + "..."
				}
				skill := r.Skill
				if len(skill) > 20 {
					skill = skill[:17] + "..."
				}
				model := r.Model
				if len(model) > 24 {
					model = model[:21] + "..."
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%-36s  %-20s  %-24s  %8.1f%%  %s\n",
					runID, skill, model, r.PassRate, r.Timestamp.Format("2006-01-02 15:04:05"))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&skillFilter, "skill", "", "Filter by skill name")
	cmd.Flags().StringVar(&modelFilter, "model", "", "Filter by model ID")
	cmd.Flags().StringVar(&sinceStr, "since", "", "Filter by date (YYYY-MM-DD)")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of results to show")

	return cmd
}

func newResultsCompareCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compare <run-id-1> <run-id-2>",
		Short: "Compare two evaluation runs",
		Long: `Compare two stored evaluation runs side by side.

Shows pass rate delta, score delta, and per-metric differences.
Green indicates improvements, red indicates regressions.

Examples:
  waza results compare abc123 def456`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := projectconfig.Load(".")
			if err != nil || cfg == nil {
				cfg = projectconfig.New()
			}

			if cfg.Storage.Provider == "" || !cfg.Storage.Enabled {
				return fmt.Errorf("storage not configured. Add a storage section to .waza.yaml")
			}

			store, err := storage.NewStore(&cfg.Storage, cfg.Paths.Results)
			if err != nil {
				return fmt.Errorf("creating result store: %w", err)
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			report, err := store.Compare(ctx, args[0], args[1])
			if err != nil {
				return fmt.Errorf("comparing runs: %w", err)
			}

			out := cmd.OutOrStdout()

			// Header
			_, _ = fmt.Fprintf(out, "\n📊 Comparison: %s vs %s\n\n", report.Run1.RunID, report.Run2.RunID)

			// Run info
			_, _ = fmt.Fprintf(out, "  Run 1: %s (skill: %s, model: %s)\n", report.Run1.RunID, report.Run1.Skill, report.Run1.Model)
			_, _ = fmt.Fprintf(out, "  Run 2: %s (skill: %s, model: %s)\n", report.Run2.RunID, report.Run2.Skill, report.Run2.Model)
			_, _ = fmt.Fprintln(out)

			// Pass rate delta
			passIndicator := deltaIndicator(report.PassDelta)
			_, _ = fmt.Fprintf(out, "  Pass Rate: %.1f%% → %.1f%% (%s%.1f%%)\n",
				report.Run1.PassRate, report.Run2.PassRate, passIndicator, report.PassDelta)

			// Score delta
			scoreIndicator := deltaIndicator(report.ScoreDelta)
			_, _ = fmt.Fprintf(out, "  Score:     %.3f → %.3f (%s%.3f)\n\n",
				report.Run1.PassRate/100, report.Run2.PassRate/100, scoreIndicator, report.ScoreDelta)

			// Per-metric deltas
			if len(report.Metrics) > 0 {
				_, _ = fmt.Fprintf(out, "  %-24s  %10s  %10s  %10s\n", "Metric", "Run 1", "Run 2", "Delta")
				_, _ = fmt.Fprintf(out, "  %s\n", strings.Repeat("-", 24)+"  "+strings.Repeat("-", 10)+"  "+strings.Repeat("-", 10)+"  "+strings.Repeat("-", 10))

				for _, m := range report.Metrics {
					indicator := deltaIndicator(m.Delta)
					_, _ = fmt.Fprintf(out, "  %-24s  %10.3f  %10.3f  %s%10.3f\n",
						m.Name, m.Value1, m.Value2, indicator, m.Delta)
				}
				_, _ = fmt.Fprintln(out)
			}

			return nil
		},
	}

	return cmd
}

// deltaIndicator returns a colored prefix for positive/negative deltas.
func deltaIndicator(delta float64) string {
	if delta > 0 {
		return "\033[32m+" // green
	}
	if delta < 0 {
		return "\033[31m" // red (negative sign already present)
	}
	return ""
}
