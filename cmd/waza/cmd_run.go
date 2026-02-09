package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spboyer/waza/internal/config"
	"github.com/spboyer/waza/internal/execution"
	"github.com/spboyer/waza/internal/models"
	"github.com/spboyer/waza/internal/orchestration"
	"github.com/spf13/cobra"
)

var (
	contextDir    string
	outputPath    string
	verbose       bool
	transcriptDir string
)

func newRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <eval.yaml>",
		Short: "Run an evaluation benchmark",
		Long: `Run an evaluation benchmark from a spec file.

The spec file defines the benchmark configuration, test cases, and validation rules.
Resources are loaded from the context directory (defaults to ./fixtures).`,
		Args: cobra.ExactArgs(1),
		RunE: runCommandE,
	}

	cmd.Flags().StringVar(&contextDir, "context-dir", "", "Context directory for fixtures (default: ./fixtures relative to spec)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output JSON file for results")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output with detailed progress")
	cmd.Flags().StringVar(&transcriptDir, "transcript-dir", "", "Directory to save per-task transcript JSON files")

	return cmd
}

func runCommandE(cmd *cobra.Command, args []string) error {
	specPath := args[0]

	// Load spec
	spec, err := models.LoadBenchmarkSpec(specPath)
	if err != nil {
		return fmt.Errorf("failed to load spec: %w", err)
	}

	// Get spec directory for resolving relative paths
	specDir := filepath.Dir(specPath)
	if !filepath.IsAbs(specDir) {
		absSpecDir, err := filepath.Abs(specDir)
		if err == nil {
			specDir = absSpecDir
		}
	}

	// Resolve fixture/context dir relative to spec file if not absolute
	fixtureDir := contextDir
	if fixtureDir == "" {
		// Default to "fixtures" subdirectory in spec directory
		fixtureDir = filepath.Join(specDir, "fixtures")
	} else if !filepath.IsAbs(fixtureDir) {
		// If relative, make it relative to current working directory, not spec dir
		absFixtureDir, err := filepath.Abs(fixtureDir)
		if err == nil {
			fixtureDir = absFixtureDir
		}
	}

	// Create config with both directories
	cfg := config.NewBenchmarkConfig(spec,
		config.WithSpecDir(specDir),       // For resolving test file patterns
		config.WithFixtureDir(fixtureDir), // For loading resource files
		config.WithVerbose(verbose),
		config.WithOutputPath(outputPath),
		config.WithTranscriptDir(transcriptDir),
	)

	// Create engine based on spec
	var engine execution.AgentEngine

	switch spec.Config.EngineType {
	case "mock":
		engine = execution.NewMockEngine(spec.Config.ModelID)
	case "copilot-sdk":
		engine = execution.NewCopilotEngineBuilder(spec.Config.ModelID).Build()
	default:
		return fmt.Errorf("unknown engine type: %s", spec.Config.EngineType)
	}

	// Create runner
	runner := orchestration.NewTestRunner(cfg, engine)

	// Add progress listener
	if verbose {
		runner.OnProgress(verboseProgressListener)
	} else {
		runner.OnProgress(simpleProgressListener)
	}

	// Run benchmark
	ctx := context.Background()

	fmt.Printf("Running benchmark: %s\n", spec.Name)
	fmt.Printf("Skill: %s\n", spec.SkillName)
	fmt.Printf("Engine: %s\n", spec.Config.EngineType)
	fmt.Printf("Model: %s\n", spec.Config.ModelID)
	fmt.Println()

	outcome, err := runner.RunBenchmark(ctx)
	if err != nil {
		return fmt.Errorf("benchmark failed: %w", err)
	}

	// Print summary
	printSummary(outcome)

	// Save output if requested
	if outputPath != "" {
		if err := saveOutcome(outcome, outputPath); err != nil {
			return fmt.Errorf("failed to save output: %w", err)
		}
		fmt.Printf("\nResults saved to: %s\n", outputPath)
	}

	// Exit with error code if tests failed
	if outcome.Digest.Failed > 0 || outcome.Digest.Errors > 0 {
		return fmt.Errorf("benchmark completed with failures")
	}

	return nil
}

func verboseProgressListener(event orchestration.ProgressEvent) {
	switch event.EventType {
	case orchestration.EventBenchmarkStart:
		fmt.Printf("Starting benchmark with %d test(s)...\n\n", event.TotalTests)
	case orchestration.EventTestStart:
		fmt.Printf("[%d/%d] Running test: %s\n", event.TestNum, event.TotalTests, event.TestName)
	case orchestration.EventRunStart:
		fmt.Printf("  Run %d/%d...", event.RunNum, event.TotalRuns)
	case orchestration.EventRunComplete:
		duration := time.Duration(event.DurationMs) * time.Millisecond
		fmt.Printf(" %s (%v)\n", event.Status, duration)
	case orchestration.EventTestComplete:
		fmt.Printf("  Test %s: %s\n\n", event.TestName, event.Status)
	case orchestration.EventBenchmarkComplete:
		duration := time.Duration(event.DurationMs) * time.Millisecond
		fmt.Printf("Benchmark completed in %v\n\n", duration)
	case orchestration.EventAgentPrompt:
		if msg, ok := event.Details["message"].(string); ok {
			fmt.Printf("  [PROMPT] %s\n", msg)
		}
	case orchestration.EventAgentResponse:
		if output, ok := event.Details["output"].(string); ok {
			fmt.Printf("  [RESPONSE] %s\n", truncate(output, 200))
		}
		if tc, ok := event.Details["tool_calls"].(int); ok && tc > 0 {
			fmt.Printf("  [TOOLS] %d tool call(s)\n", tc)
		}
	case orchestration.EventGraderResult:
		name := fmt.Sprintf("%v", event.Details["grader"])
		passed, ok := event.Details["passed"].(bool)
		if !ok {
			passed = false
		}
		score, ok := event.Details["score"].(float64)
		if !ok {
			score = 0
		}
		feedback := fmt.Sprintf("%v", event.Details["feedback"])
		icon := "✗"
		if passed {
			icon = "✓"
		}
		duration := time.Duration(event.DurationMs) * time.Millisecond
		fmt.Printf("  [GRADER] %s %s score=%.2f (%v)", icon, name, score, duration)
		if feedback != "" {
			fmt.Printf(" — %s", feedback)
		}
		fmt.Println()
	}
}

// truncate shortens s to maxLen characters, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func simpleProgressListener(event orchestration.ProgressEvent) {
	switch event.EventType {
	case orchestration.EventTestComplete:
		status := "✓"
		if event.Status != "passed" {
			status = "✗"
		}
		fmt.Printf("%s [%d/%d] %s\n", status, event.TestNum, event.TotalTests, event.TestName)
	}
}

func printSummary(outcome *models.EvaluationOutcome) {
	fmt.Println("=" + strings.Repeat("=", 50))
	fmt.Println(" BENCHMARK RESULTS")
	fmt.Println("=" + strings.Repeat("=", 50))
	fmt.Println()

	digest := outcome.Digest

	fmt.Printf("Total Tests:    %d\n", digest.TotalTests)
	fmt.Printf("Succeeded:      %d\n", digest.Succeeded)
	fmt.Printf("Failed:         %d\n", digest.Failed)
	fmt.Printf("Errors:         %d\n", digest.Errors)
	fmt.Printf("Success Rate:   %.1f%%\n", digest.SuccessRate*100)
	fmt.Printf("Aggregate Score: %.2f\n", digest.AggregateScore)

	duration := time.Duration(digest.DurationMs) * time.Millisecond
	fmt.Printf("Duration:       %v\n", duration)
	fmt.Println()

	// Show failed tests
	if digest.Failed > 0 || digest.Errors > 0 {
		fmt.Println("Failed Tests:")
		for _, to := range outcome.TestOutcomes {
			if to.Status != "passed" {
				fmt.Printf("  - %s (%s)\n", to.DisplayName, to.Status)

				// Show validation failures
				if len(to.Runs) > 0 {
					for _, run := range to.Runs {
						for _, val := range run.Validations {
							if !val.Passed {
								fmt.Printf("    • %s: %s\n", val.Name, val.Feedback)
							}
						}
					}
				}
			}
		}
		fmt.Println()
	}

	// Show flaky tasks
	var flakyTasks []models.TestOutcome
	for _, to := range outcome.TestOutcomes {
		if to.Stats != nil && to.Stats.Flaky {
			flakyTasks = append(flakyTasks, to)
		}
	}
	if len(flakyTasks) > 0 {
		fmt.Println("\u26a0 Flaky Tasks (inconsistent pass/fail across trials):")
		for _, to := range flakyTasks {
			fmt.Printf("  - %s  pass_rate=%.0f%%  score=%.2f\u00b1%.2f  CI95=[%.2f, %.2f]\n",
				to.DisplayName,
				to.Stats.PassRate*100,
				to.Stats.AvgScore,
				to.Stats.StdDevScore,
				to.Stats.CI95Lo,
				to.Stats.CI95Hi,
			)
		}
		fmt.Println()
	}
}

func saveOutcome(outcome *models.EvaluationOutcome, path string) error {
	data, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
