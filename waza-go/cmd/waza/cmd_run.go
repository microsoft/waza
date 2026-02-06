package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spboyer/waza/waza-go/internal/config"
	"github.com/spboyer/waza/waza-go/internal/execution"
	"github.com/spboyer/waza/waza-go/internal/models"
	"github.com/spboyer/waza/waza-go/internal/orchestration"
	"github.com/spf13/cobra"
)

var (
	contextDir string
	outputPath string
	verbose    bool
)

func newRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <spec.yaml>",
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
	)

	// Create engine based on spec
	var engine execution.AgentEngine

	switch spec.RuntimeOptions.EngineType {
	case "mock":
		engine = execution.NewMockEngine(spec.RuntimeOptions.ModelID)
	case "copilot-sdk":
		engine = execution.NewCopilotEngineBuilder(spec.RuntimeOptions.ModelID).Build()
	default:
		return fmt.Errorf("unknown engine type: %s", spec.RuntimeOptions.EngineType)
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
	fmt.Printf("Engine: %s\n", spec.RuntimeOptions.EngineType)
	fmt.Printf("Model: %s\n", spec.RuntimeOptions.ModelID)
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
	case "benchmark_start":
		fmt.Printf("Starting benchmark with %d test(s)...\n\n", event.TotalTests)
	case "test_start":
		fmt.Printf("[%d/%d] Running test: %s\n", event.TestNum, event.TotalTests, event.TestName)
	case "run_start":
		fmt.Printf("  Run %d/%d...", event.RunNum, event.TotalRuns)
	case "run_complete":
		duration := time.Duration(event.DurationMs) * time.Millisecond
		fmt.Printf(" %s (%v)\n", event.Status, duration)
	case "test_complete":
		fmt.Printf("  Test %s: %s\n\n", event.TestName, event.Status)
	case "benchmark_complete":
		duration := time.Duration(event.DurationMs) * time.Millisecond
		fmt.Printf("Benchmark completed in %v\n\n", duration)
	}
}

func simpleProgressListener(event orchestration.ProgressEvent) {
	switch event.EventType {
	case "test_complete":
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
								fmt.Printf("    • %s: %s\n", val.Identifier, val.Feedback)
							}
						}
					}
				}
			}
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
