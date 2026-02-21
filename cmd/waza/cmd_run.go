package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spboyer/waza/internal/cache"
	"github.com/spboyer/waza/internal/config"
	"github.com/spboyer/waza/internal/execution"
	"github.com/spboyer/waza/internal/models"
	"github.com/spboyer/waza/internal/orchestration"
	"github.com/spboyer/waza/internal/recommend"
	"github.com/spboyer/waza/internal/reporting"
	"github.com/spboyer/waza/internal/session"
	"github.com/spboyer/waza/internal/trigger"
	"github.com/spboyer/waza/internal/utils"
	"github.com/spboyer/waza/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	contextDir     string
	outputPath     string
	outputDir      string
	verbose        bool
	transcriptDir  string
	taskFilters    []string
	tagFilters     []string
	parallel       bool
	workers        int
	interpret      bool
	format         string
	enableCache    bool
	disableCache   bool
	runCacheDir    string
	modelOverrides []string
	recommendFlag  bool
	baselineFlag   bool
	sessionLog     bool
	sessionDir     string
	noSummary      bool
	judgeModel     string
	reporters      []string
)

// modelResult pairs a model identifier with its evaluation outcome.
type modelResult struct {
	modelID string
	outcome *models.EvaluationOutcome
}

func newRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run [eval.yaml | skill-name]",
		Short: "Run an evaluation benchmark",
		Long: `Run an evaluation benchmark from a spec file.

The spec file defines the benchmark configuration, test cases, and validation rules.
Resources are loaded from the context directory (defaults to ./fixtures).

With no arguments, uses workspace detection to find eval.yaml automatically:
  - Single-skill workspace → runs that skill's eval
  - Multi-skill workspace → runs ALL evals sequentially with summary

You can also specify a skill name to run its eval:
  waza run code-explainer`,
		Args: cobra.MaximumNArgs(1),
		RunE: runCommandE,
	}

	cmd.Flags().StringVar(&contextDir, "context-dir", "", "Context directory for fixtures (default: ./fixtures relative to spec)")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Output JSON file for results")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Directory for structured output (mutually exclusive with --output)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output with detailed progress")
	cmd.Flags().StringVar(&transcriptDir, "transcript-dir", "", "Directory to save per-task transcript JSON files")
	cmd.Flags().StringArrayVar(&taskFilters, "task", nil, "Filter tasks by name/ID glob pattern (can be repeated).")
	cmd.Flags().StringArrayVar(&tagFilters, "tags", nil, "Filter tasks by tags, using glob patterns (can be repeated)")
	cmd.Flags().BoolVar(&parallel, "parallel", false, "Run tasks concurrently")
	cmd.Flags().IntVar(&workers, "workers", 0, "Number of concurrent workers (default: 4, requires --parallel)")
	cmd.Flags().BoolVar(&interpret, "interpret", false, "Print a plain-language interpretation of the results")
	cmd.Flags().StringVar(&format, "format", "default", "Output format: default, github-comment")
	cmd.Flags().BoolVar(&enableCache, "cache", false, "Enable result caching (default: false)")
	cmd.Flags().BoolVar(&disableCache, "no-cache", false, "Disable result caching (default)")
	cmd.Flags().StringVar(&runCacheDir, "cache-dir", ".waza-cache", "Cache directory for storing results")
	cmd.Flags().StringArrayVar(&modelOverrides, "model", nil, "Model to use (overrides spec config, can be repeated for comparison)")
	cmd.Flags().BoolVar(&recommendFlag, "recommend", false, "Generate heuristic recommendation after multi-model run")
	cmd.Flags().BoolVar(&baselineFlag, "baseline", false, "Run A/B comparison: with skills vs without skills")
	cmd.Flags().BoolVar(&sessionLog, "session-log", false, "Enable session event logging (NDJSON)")
	cmd.Flags().StringVar(&sessionDir, "session-dir", "", "Directory for session log files (default: current directory)")
	cmd.Flags().BoolVar(&noSummary, "no-summary", false, "Skip writing combined summary.json for multi-skill runs")
	cmd.Flags().StringVar(&judgeModel, "judge-model", "", "Model for prompt graders (overrides execution model for LLM-as-judge)")
	cmd.Flags().StringArrayVar(&reporters, "reporter", nil, "Output reporters: json (default), junit:path.xml (can be repeated)")

	return cmd
}

func runCommandE(cmd *cobra.Command, args []string) error {
	// Validate mutual exclusion
	if outputPath != "" && outputDir != "" {
		return fmt.Errorf("--output and --output-dir are mutually exclusive")
	}

	// Resolve spec path: explicit arg or workspace detection
	specPaths, err := resolveSpecPaths(args)
	if err != nil {
		return err
	}

	if len(specPaths) == 1 {
		_, err := runCommandForSpec(cmd, specPaths[0])
		return err
	}

	// Multi-skill run — run each eval sequentially
	// Suppress per-skill output during the loop — we'll write it after
	savedOutputPath := outputPath
	outputPath = ""

	var allSkillResults []skillRunResult
	var lastErr error
	for _, sp := range specPaths {
		fmt.Printf("\n=== %s ===\n\n", sp.skillName)
		result := skillRunResult{skillName: sp.skillName}
		outcomes, err := runCommandForSpec(cmd, sp)
		result.outcomes = outcomes
		if err != nil {
			var testErr *TestFailureError
			if errors.As(err, &testErr) {
				result.err = err
				lastErr = err
			} else {
				return err
			}
		}
		allSkillResults = append(allSkillResults, result)
	}

	// Restore outputPath for per-skill output writing
	outputPath = savedOutputPath

	if len(allSkillResults) > 1 {
		printSkillRunSummary(allSkillResults)

		// Write combined summary.json if --output is specified and --no-summary is not set
		if outputPath != "" && !noSummary {
			summary := buildMultiSkillSummary(allSkillResults)
			ext := filepath.Ext(outputPath)
			base := strings.TrimSuffix(outputPath, ext)
			summaryPath := fmt.Sprintf("%s_summary%s", base, ext)

			if err := saveSummary(summary, summaryPath); err != nil {
				return fmt.Errorf("failed to save summary: %w", err)
			}
			fmt.Printf("Combined summary saved to: %s\n", summaryPath)
		}
	}

	// Write per-skill output files when --output is specified
	if outputPath != "" && len(allSkillResults) > 1 {
		ext := filepath.Ext(outputPath)
		base := strings.TrimSuffix(outputPath, ext)

		for _, skillResult := range allSkillResults {
			// For each skill, write per-model or single output
			multiModel := len(skillResult.outcomes) > 1

			for _, mr := range skillResult.outcomes {
				if mr.outcome == nil {
					continue
				}
				perSkillPath := buildOutputPath(base, ext, skillResult.skillName, mr.modelID, true, multiModel)
				if err := saveOutcome(mr.outcome, perSkillPath); err != nil {
					return fmt.Errorf("failed to save output for skill %s, model %s: %w", skillResult.skillName, mr.modelID, err)
				}
				fmt.Printf("Results saved to: %s\n", perSkillPath)
			}
		}
	}

	// Write structured directory output when --output-dir is specified
	if outputDir != "" {
		if err := writeOutputDir(outputDir, allSkillResults); err != nil {
			return fmt.Errorf("failed to write output directory: %w", err)
		}
	}

	return lastErr
}

type skillSpecPath struct {
	specPath  string
	skillName string
}

type skillRunResult struct {
	skillName string
	outcomes  []modelResult // per-model outcomes for this skill
	err       error
}

// resolveSpecPaths resolves eval.yaml paths from args or workspace detection.
func resolveSpecPaths(args []string) ([]skillSpecPath, error) {
	if len(args) > 0 {
		arg := args[0]
		// If it looks like a path, use directly
		if workspace.LooksLikePath(arg) {
			return []skillSpecPath{{specPath: arg}}, nil
		}
		// Treat as skill name
		skills, err := resolveSkillsFromArgs(args)
		if err != nil {
			return nil, err
		}
		if len(skills) == 0 {
			return nil, fmt.Errorf("skill %q not found", arg)
		}
		evalPath, err := resolveEvalPath(&skills[0])
		if err != nil {
			return nil, err
		}
		return []skillSpecPath{{specPath: evalPath, skillName: skills[0].Name}}, nil
	}

	// No args — workspace detection
	skills, err := resolveSkillsFromArgs(nil)
	if err != nil {
		return nil, fmt.Errorf("no eval.yaml specified and workspace detection failed: %w", err)
	}

	var paths []skillSpecPath
	for _, si := range skills {
		evalPath, err := resolveEvalPath(&si)
		if err != nil {
			if len(skills) == 1 {
				return nil, err
			}
			fmt.Printf("⚠️  Skipping %s: %v\n", si.Name, err)
			continue
		}
		paths = append(paths, skillSpecPath{specPath: evalPath, skillName: si.Name})
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("no eval.yaml found for any detected skills")
	}

	return paths, nil
}

func printSkillRunSummary(results []skillRunResult) {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════")
	fmt.Println(" MULTI-SKILL RUN SUMMARY")
	fmt.Println("═══════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("%-25s %-10s %-15s %-15s\n", "Skill", "Status", "Pass Rate", "Avg Score")
	fmt.Println(strings.Repeat("─", 70))

	for _, r := range results {
		status := "✅ Passed"
		passRate := "-"
		avgScore := "-"

		if r.err != nil {
			status = "❌ Failed"
		}

		// Calculate aggregate pass rate and score across all models for this skill
		if len(r.outcomes) > 0 {
			var totalPassed, totalTests int
			var sumScore float64
			validOutcomes := 0

			for _, mr := range r.outcomes {
				if mr.outcome != nil {
					totalPassed += mr.outcome.Digest.Succeeded
					totalTests += mr.outcome.Digest.TotalTests
					sumScore += mr.outcome.Digest.AggregateScore
					validOutcomes++
				}
			}

			if totalTests > 0 {
				passRate = fmt.Sprintf("%.1f%%", float64(totalPassed)/float64(totalTests)*100)
			}
			if validOutcomes > 0 {
				avgScore = fmt.Sprintf("%.2f", sumScore/float64(validOutcomes))
			}
		}

		fmt.Printf("%-25s %-10s %-15s %-15s\n", r.skillName, status, passRate, avgScore)
	}
	fmt.Println()
}

// runCommandForSpec runs the evaluation for a single spec path.
func runCommandForSpec(cmd *cobra.Command, sp skillSpecPath) ([]modelResult, error) {
	specPath := sp.specPath

	// Load spec
	spec, err := models.LoadBenchmarkSpec(specPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load spec: %w", err)
	}

	// CLI flags override spec config
	if parallel {
		spec.Config.Concurrent = true
	}
	if workers > 0 {
		spec.Config.Workers = workers
	}
	if baselineFlag {
		spec.Baseline = true
	}
	if judgeModel != "" {
		spec.Config.JudgeModel = judgeModel
	}

	// Determine the list of models to evaluate
	modelsToRun := []string{spec.Config.ModelID}
	if len(modelOverrides) > 0 {
		modelsToRun = modelOverrides
	}

	// Reject duplicate model IDs early
	if len(modelsToRun) > 1 {
		seen := make(map[string]bool, len(modelsToRun))
		for _, m := range modelsToRun {
			if seen[m] {
				return nil, fmt.Errorf("duplicate --model value: %q (each model must be unique)", m)
			}
			seen[m] = true
		}
	}

	multiModel := len(modelsToRun) > 1

	// Run evaluation for each model, collecting results
	var allResults []modelResult
	var lastErr error

	for _, modelID := range modelsToRun {
		// Override spec model for this iteration
		spec.Config.ModelID = modelID

		outcome, err := runSingleModel(cmd, spec, specPath)
		if err != nil {
			var testErr *TestFailureError
			if errors.As(err, &testErr) {
				// Test failures are recorded but don't stop a multi-model run
				allResults = append(allResults, modelResult{modelID: modelID, outcome: outcome})
				lastErr = err
				continue
			}
			return nil, err
		}
		allResults = append(allResults, modelResult{modelID: modelID, outcome: outcome})
	}

	// Print comparison table when multiple models were evaluated
	if multiModel && len(allResults) > 0 {
		printModelComparison(allResults)
	}

	// Compute and print heuristic recommendation for multi-model runs
	if multiModel && recommendFlag && len(allResults) > 0 {
		rec := computeAndPrintRecommendation(allResults)
		if rec != nil {
			for i := range allResults {
				if allResults[i].outcome != nil {
					if allResults[i].outcome.Metadata == nil {
						allResults[i].outcome.Metadata = make(map[string]any)
					}
					allResults[i].outcome.Metadata["recommendation"] = rec
				}
			}
		}
	}

	// Save per-model results when --output is specified with multiple models
	// Note: For multi-skill runs, this is skipped because outputPath is cleared
	// and per-skill output happens in the multi-skill loop instead
	if outputPath != "" && multiModel {
		ext := filepath.Ext(outputPath)
		base := strings.TrimSuffix(outputPath, ext)
		for _, mr := range allResults {
			// Use buildOutputPath for consistency (multiSkill=false for single-skill context)
			perModelPath := buildOutputPath(base, ext, "", mr.modelID, false, true)
			if err := saveOutcome(mr.outcome, perModelPath); err != nil {
				return nil, fmt.Errorf("failed to save output for model %s: %w", mr.modelID, err)
			}
			fmt.Printf("Results saved to: %s\n", perModelPath)
		}
	}

	if lastErr != nil {
		return allResults, lastErr
	}

	// Write reporter outputs for the last model result
	if len(allResults) > 0 {
		last := allResults[len(allResults)-1]
		if last.outcome != nil {
			if err := writeReporters(last.outcome); err != nil {
				return allResults, err
			}
		}
	}

	return allResults, nil
}

// runSingleModel executes a benchmark for one model and returns the outcome.
// It prints the per-model summary and saves output for single-model runs.
func runSingleModel(_ *cobra.Command, spec *models.BenchmarkSpec, specPath string) (*models.EvaluationOutcome, error) {
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
		fixtureDir = filepath.Join(specDir, "fixtures")
	} else if !filepath.IsAbs(fixtureDir) {
		absFixtureDir, err := filepath.Abs(fixtureDir)
		if err == nil {
			fixtureDir = absFixtureDir
		}
	}

	// Create config with both directories
	cfg := config.NewBenchmarkConfig(spec,
		config.WithSpecDir(specDir),
		config.WithFixtureDir(fixtureDir),
		config.WithVerbose(verbose),
		config.WithOutputPath(outputPath),
		config.WithTranscriptDir(transcriptDir),
	)

	// Setup cache if enabled
	var resultCache *cache.Cache
	useCaching := enableCache && !disableCache

	if useCaching && cache.HasNonDeterministicGraders(spec) {
		if verbose {
			fmt.Println("Note: Caching disabled due to non-deterministic graders (behavior, prompt)")
		}
		useCaching = false
	}

	if useCaching {
		absCacheDir, err := filepath.Abs(runCacheDir)
		if err != nil {
			return nil, fmt.Errorf("resolving cache directory: %w", err)
		}
		resultCache = cache.New(absCacheDir)
		if verbose {
			fmt.Printf("Cache enabled: %s\n", absCacheDir)
		}
	}

	// Create engine based on spec
	var engine execution.AgentEngine

	switch spec.Config.EngineType {
	case "mock":
		engine = execution.NewMockEngine(spec.Config.ModelID)
	case "copilot-sdk":
		engine = execution.NewCopilotEngineBuilder(spec.Config.ModelID).Build()
	default:
		return nil, fmt.Errorf("unknown engine type: %s", spec.Config.EngineType)
	}
	defer func() {
		if err := engine.Shutdown(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: engine shutdown: %v\n", err)
		}
	}()

	// Create runner with optional task filters and cache
	runnerOpts := []orchestration.RunnerOption{
		orchestration.WithTaskFilters(taskFilters...),
		orchestration.WithTagFilters(tagFilters...),
	}
	if resultCache != nil {
		runnerOpts = append(runnerOpts, orchestration.WithCache(resultCache))
	}
	runner := orchestration.NewTestRunner(cfg, engine, runnerOpts...)

	// Setup session logger if enabled
	var sessLogger session.Logger = session.NopLogger{}
	if sessionLog {
		logDir := sessionDir
		if logDir == "" {
			logDir = "."
		}
		logPath := session.DefaultLogPath(logDir)
		jl, err := session.NewJSONLogger(logPath)
		if err != nil {
			return nil, fmt.Errorf("creating session logger: %w", err)
		}
		defer jl.Close() //nolint:errcheck
		sessLogger = jl
		if verbose {
			fmt.Printf("Session log: %s\n", jl.Path())
		}
	}

	// Wire session logger as a progress listener
	runner.OnProgress(func(event orchestration.ProgressEvent) {
		var ev session.Event
		switch event.EventType {
		case orchestration.EventBenchmarkStart:
			ev = session.NewEvent(session.EventSessionStart,
				session.SessionStartData(specPath, spec.Config.ModelID, spec.Config.EngineType, event.TotalTests))
		case orchestration.EventTestStart:
			ev = session.NewEvent(session.EventTaskStart,
				session.TaskStartData(event.TestName, event.TestNum, event.TotalTests))
		case orchestration.EventTestComplete:
			score, _ := event.Details["score"].(float64)          //nolint:errcheck
			durationMs, _ := event.Details["duration_ms"].(int64) //nolint:errcheck
			ev = session.NewEvent(session.EventTaskComplete,
				session.TaskCompleteData(event.TestName, string(event.Status), score, durationMs))
		case orchestration.EventGraderResult:
			grader, _ := event.Details["grader"].(string)          //nolint:errcheck
			graderType, _ := event.Details["grader_type"].(string) //nolint:errcheck
			passed, _ := event.Details["passed"].(bool)            //nolint:errcheck
			score, _ := event.Details["score"].(float64)           //nolint:errcheck
			feedback, _ := event.Details["feedback"].(string)      //nolint:errcheck
			ev = session.NewEvent(session.EventGraderResult,
				session.GraderResultData(grader, graderType, passed, score, feedback))
		default:
			return
		}
		sessLogger.Log(ev) //nolint:errcheck
	})

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
	if spec.Config.JudgeModel != "" {
		fmt.Printf("Judge Model: %s\n", spec.Config.JudgeModel)
	}
	if spec.Config.Concurrent {
		w := spec.Config.Workers
		if w <= 0 {
			w = 4
		}
		fmt.Printf("Parallel: %d workers\n", w)
	}

	if verbose && len(spec.Config.SkillPaths) > 0 {
		fmt.Printf("Skill Directories:\n")
		resolvedPaths := utils.ResolvePaths(spec.Config.SkillPaths, specDir)
		for _, path := range resolvedPaths {
			fmt.Printf("  - %s\n", path)
		}
	}

	fmt.Println()

	outcome, err := runner.RunBenchmark(ctx)
	if err != nil {
		return nil, fmt.Errorf("benchmark failed: %w", err)
	}

	// Log task completion and session summary from outcome data
	if sessionLog {
		d := outcome.Digest
		ev := session.NewEvent(session.EventSessionEnd,
			session.SessionCompleteData(d.TotalTests, d.Succeeded, d.Failed, d.Errors, d.DurationMs))
		sessLogger.Log(ev) //nolint:errcheck
	}

	// Discover and run trigger tests if present alongside the eval spec
	if triggerSpec, err := trigger.Discover(specDir); err != nil {
		return outcome, fmt.Errorf("loading trigger tests: %w", err)
	} else if triggerSpec != nil {
		var tm *models.TriggerMetrics
		if spec.Config.EngineType == "mock" {
			// return perfect results
			var results []models.TriggerResult
			for _, p := range triggerSpec.ShouldTriggerPrompts {
				results = append(results, models.TriggerResult{
					Prompt:        p.Prompt,
					Confidence:    p.Confidence,
					ShouldTrigger: true,
					DidTrigger:    true,
				})
			}
			for _, p := range triggerSpec.ShouldNotTriggerPrompts {
				results = append(results, models.TriggerResult{
					Prompt:        p.Prompt,
					Confidence:    p.Confidence,
					ShouldTrigger: false,
					DidTrigger:    false,
				})
			}
			tm = models.ComputeTriggerMetrics(results)
		} else {
			tr := trigger.NewRunner(triggerSpec, engine, cfg, os.Stdout)
			if verbose {
				fmt.Println("Running trigger tests...")
			}
			if tm, err = tr.Run(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: trigger tests failed: %v\n", err)
			}
		}
		if tm != nil {
			outcome.TriggerMetrics = tm
			for _, m := range spec.Metrics {
				if m.Identifier == "trigger_accuracy" {
					outcome.Measures[m.Identifier] = models.MeasureResult{
						Identifier: m.Identifier,
						Value:      tm.Accuracy,
						Threshold:  m.Threshold,
						Passed:     m.Threshold <= 0 || tm.Accuracy >= m.Threshold,
						Weight:     m.Weight,
					}
					break
				}
			}
		}
	}

	// Print results based on format
	switch format {
	case "github-comment":
		fmt.Print(FormatGitHubComment(outcome))
	case "default":
		printSummary(outcome)
		if interpret {
			fmt.Println()
			fmt.Print(reporting.FormatSummaryReport(outcome))
		}
	default:
		return nil, fmt.Errorf("unknown output format: %s (supported: default, github-comment)", format)
	}

	// Save output for single-model runs (multi-model saves are handled by the caller)
	if outputPath != "" && len(modelOverrides) <= 1 {
		if err := saveOutcome(outcome, outputPath); err != nil {
			return nil, fmt.Errorf("failed to save output: %w", err)
		}
		fmt.Printf("\nResults saved to: %s\n", outputPath)
	}

	// Return test failure as error so caller can decide how to handle it
	// In baseline mode, exit code is based on skill impact (0=improvement, 1=regression/neutral)
	if outcome.IsBaseline {
		withPassRate := outcome.Digest.SuccessRate
		withoutPassRate := outcome.BaselineOutcome.Digest.SuccessRate

		if withPassRate <= withoutPassRate {
			// Skills hurt or neutral → exit 1
			return outcome, &TestFailureError{
				Message: fmt.Sprintf("baseline comparison: skills have negative/neutral impact (%.1f%% vs %.1f%%)",
					withPassRate*100, withoutPassRate*100),
			}
		}
		// Skills improved → exit 0
		return outcome, nil
	}

	// Normal mode: fail if tests failed or errors occurred
	var failures []string
	if outcome.Digest.Failed > 0 || outcome.Digest.Errors > 0 {
		failures = append(failures, fmt.Sprintf("%d failed and %d error(s)", outcome.Digest.Failed, outcome.Digest.Errors))
	}
	if m, ok := outcome.Measures["trigger_accuracy"]; ok && !m.Passed {
		failures = append(failures, fmt.Sprintf("trigger accuracy %.1f%% below threshold %.1f%%", m.Value*100, m.Threshold*100))
	}
	if len(failures) > 0 {
		return outcome, &TestFailureError{
			Message: fmt.Sprintf("benchmark completed: %s", strings.Join(failures, "; ")),
		}
	}

	return outcome, nil
}

// printModelComparison renders a comparison table for multi-model runs.
func printModelComparison(results []modelResult) {
	fmt.Println()
	fmt.Println("═" + strings.Repeat("═", 54))
	fmt.Println(" MODEL COMPARISON")
	fmt.Println("═" + strings.Repeat("═", 54))
	fmt.Println()
	fmt.Printf("%-20s %-8s %-10s %s\n", "Model", "Score", "Pass Rate", "Duration")
	fmt.Println("─" + strings.Repeat("─", 54))

	for _, mr := range results {
		score := 0.0
		passRate := 0.0
		durationMs := int64(0)
		if mr.outcome != nil {
			score = mr.outcome.Digest.AggregateScore
			passRate = mr.outcome.Digest.SuccessRate * 100
			durationMs = mr.outcome.Digest.DurationMs
		}
		duration := time.Duration(durationMs) * time.Millisecond
		passStr := fmt.Sprintf("%.1f%%", passRate)
		fmt.Printf("%-20s %-8.2f %-10s %v\n", mr.modelID, score, passStr, duration)
	}
	fmt.Println()
}

// sanitizePathSegment replaces characters that are invalid in filenames.
func sanitizePathSegment(name string) string {
	r := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-")
	return r.Replace(name)
}

// buildOutputPath constructs the output file path based on multi-skill and multi-model context.
func buildOutputPath(base, ext, skillName, modelID string, multiSkill, multiModel bool) string {
	switch {
	case multiSkill && multiModel:
		return fmt.Sprintf("%s_%s_%s%s", base, sanitizePathSegment(skillName), sanitizePathSegment(modelID), ext)
	case multiSkill:
		return fmt.Sprintf("%s_%s%s", base, sanitizePathSegment(skillName), ext)
	case multiModel:
		return fmt.Sprintf("%s_%s%s", base, sanitizePathSegment(modelID), ext)
	default:
		return base + ext
	}
}

func verboseProgressListener(event orchestration.ProgressEvent) {
	switch event.EventType {
	case orchestration.EventBenchmarkStart:
		fmt.Printf("Starting benchmark with %d test(s)...\n\n", event.TotalTests)
	case orchestration.EventTestStart:
		fmt.Printf("[%d/%d] Running test: %s\n", event.TestNum, event.TotalTests, event.TestName)
	case orchestration.EventTestCached:
		fmt.Printf("[%d/%d] Test: %s [cached]\n\n", event.TestNum, event.TotalTests, event.TestName)
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
		if output, ok := event.Details["output"].(string); ok && output != "" {
			fmt.Printf("  [RESPONSE] %s\n", truncate(output, 200))
		}
		if tc, ok := event.Details["tool_calls"].(int); ok && tc > 0 {
			fmt.Printf("  [TOOLS] %d tool call(s)\n", tc)
		}
		if e, ok := event.Details["error"].(string); ok && e != "" {
			fmt.Printf("  [ERROR] %s\n", e)
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
	case orchestration.EventTestCached:
		fmt.Printf("✓ [%d/%d] %s [cached]\n", event.TestNum, event.TotalTests, event.TestName)
	case orchestration.EventTestComplete:
		status := "✓"
		if event.Status != models.StatusPassed {
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
	fmt.Printf("Min Score:      %.2f\n", digest.MinScore)
	fmt.Printf("Max Score:      %.2f\n", digest.MaxScore)
	fmt.Printf("Std Dev:        %.4f\n", digest.StdDev)

	duration := time.Duration(digest.DurationMs) * time.Millisecond
	fmt.Printf("Duration:       %v\n", duration)
	fmt.Println()

	// Grouped results summary
	if len(digest.Groups) > 0 {
		fmt.Println("-" + strings.Repeat("-", 50))
		fmt.Println(" RESULTS BY GROUP")
		fmt.Println("-" + strings.Repeat("-", 50))
		for _, g := range digest.Groups {
			pct := 0.0
			if g.Total > 0 {
				pct = float64(g.Passed) / float64(g.Total) * 100
			}
			fmt.Printf("  %-20s %d/%d passed (%.0f%%)  avg: %.2f\n",
				g.Name+":", g.Passed, g.Total, pct, g.AvgScore)
		}
		fmt.Println()
	}

	// Per-task breakdown
	fmt.Println("-" + strings.Repeat("-", 50))
	fmt.Println(" PER-TASK BREAKDOWN")
	fmt.Println("-" + strings.Repeat("-", 50))
	for _, to := range outcome.TestOutcomes {
		icon := "✓"
		if to.Status != models.StatusPassed {
			icon = "✗"
		}
		fmt.Printf("  %s %s [%s]\n", icon, to.DisplayName, to.Status)
		if to.Stats != nil {
			fmt.Printf("      pass_rate=%.1f%%  avg=%.2f  min=%.2f  max=%.2f  stddev=%.4f  avg_dur=%dms\n",
				to.Stats.PassRate*100, to.Stats.AvgScore,
				to.Stats.MinScore, to.Stats.MaxScore,
				to.Stats.StdDevScore, to.Stats.AvgDurationMs)
		}
	}
	fmt.Println()

	// Show failed tests
	if digest.Failed > 0 || digest.Errors > 0 {
		fmt.Println("Failed Tests:")
		for _, to := range outcome.TestOutcomes {
			if to.Status != models.StatusPassed {
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

	// Show trigger accuracy if trigger tests were run
	if outcome.TriggerMetrics != nil {
		m := outcome.TriggerMetrics
		fmt.Println("-" + strings.Repeat("-", 50))
		fmt.Println(" TRIGGER ACCURACY")
		fmt.Println("-" + strings.Repeat("-", 50))
		fmt.Printf("  Accuracy:  %.1f%%\n", m.Accuracy*100)
		if m.Errors > 0 {
			fmt.Printf("  Errors:    %d prompt(s) returned errors\n", m.Errors)
		}
		fmt.Printf("  Precision: %.1f%%  Recall: %.1f%%  F1: %.1f%%\n", m.Precision*100, m.Recall*100, m.F1*100)
		fmt.Printf("  TP: %d  FP: %d  FN: %d  TN: %d\n", m.TP, m.FP, m.FN, m.TN)
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

// writeReporters processes --reporter flags and writes the requested outputs.
func writeReporters(outcome *models.EvaluationOutcome) error {
	for _, r := range reporters {
		switch {
		case r == "json":
			// JSON is already handled by --output; this is a no-op explicit selection
			continue
		case strings.HasPrefix(r, "junit:"):
			path := strings.TrimPrefix(r, "junit:")
			if path == "" {
				return fmt.Errorf("--reporter junit requires a file path (e.g. junit:results.xml)")
			}
			if err := reporting.WriteJUnitXML(outcome, path); err != nil {
				return fmt.Errorf("failed to write JUnit XML: %w", err)
			}
			fmt.Printf("JUnit XML saved to: %s\n", path)
		default:
			return fmt.Errorf("unknown reporter: %s (supported: json, junit:<path>)", r)
		}
	}
	return nil
}

// computeAndPrintRecommendation runs the heuristic engine and prints results.
func computeAndPrintRecommendation(results []modelResult) *models.Recommendation {
	inputs := make([]recommend.ModelInput, len(results))
	for i, mr := range results {
		inputs[i] = recommend.ModelInput{
			ModelID: mr.modelID,
			Outcome: mr.outcome,
		}
	}

	engine := recommend.NewEngine()
	rec := engine.Recommend(inputs)
	if rec == nil {
		return nil
	}

	printRecommendationSummary(rec, results)
	return rec
}

func printRecommendationSummary(rec *models.Recommendation, results []modelResult) {
	fmt.Println()
	fmt.Println("═" + strings.Repeat("═", 54))
	fmt.Println(" RECOMMENDATION (HEURISTIC)")
	fmt.Println("═" + strings.Repeat("═", 54))
	fmt.Println()

	fmt.Printf("Recommended Model: %s (weighted score: %.1f/10)\n",
		rec.RecommendedModel, rec.HeuristicScore)
	fmt.Println()

	fmt.Println("Reasoning:")
	fmt.Printf("  • %s\n", rec.Reason)
	if rec.WinnerMarginPct != 0 {
		fmt.Printf("  • Margin of victory: %.1f%% ahead of runner-up\n", rec.WinnerMarginPct)
	}
	fmt.Println()

	fmt.Println("Component Scores (normalized 0–10):")
	fmt.Printf("%-20s %-12s %-12s %-12s %-12s\n", "Model", "Aggregate", "PassRate", "Consistency", "Speed")
	fmt.Println("─" + strings.Repeat("─", 54))
	for _, ms := range rec.ModelScores {
		fmt.Printf("%-20s %-12.1f %-12.1f %-12.1f %-12.1f\n",
			ms.ModelID,
			ms.Scores["aggregate_score_normalized"],
			ms.Scores["pass_rate_normalized"],
			ms.Scores["consistency_normalized"],
			ms.Scores["speed_normalized"],
		)
	}
	fmt.Println()

	fmt.Println("Methodology: Weighted average of normalized scores (0–10):")
	fmt.Printf("  • Aggregate score: %.0f%% weight\n", rec.Weights.AggregateScore*100)
	fmt.Printf("  • Pass rate: %.0f%% weight\n", rec.Weights.PassRate*100)
	fmt.Printf("  • Consistency (inverse stddev): %.0f%% weight\n", rec.Weights.Consistency*100)
	fmt.Printf("  • Speed (inverse duration): %.0f%% weight\n", rec.Weights.Speed*100)
	fmt.Println()
}

// buildMultiSkillSummary aggregates results from multiple skill runs into a summary.
func buildMultiSkillSummary(results []skillRunResult) *models.MultiSkillSummary {
	summary := &models.MultiSkillSummary{
		Timestamp: time.Now(),
		Skills:    make([]models.SkillSummary, 0, len(results)),
	}

	var totalPassRate, totalAggregateScore float64
	var validSkills int
	modelsMap := make(map[string]bool)

	for _, r := range results {
		skill := models.SkillSummary{
			SkillName:   r.skillName,
			Models:      make([]string, 0, len(r.outcomes)),
			OutputFiles: make([]string, 0, len(r.outcomes)),
		}

		var totalPassed, totalTests int
		var sumScore float64
		var validOutcomes int

		// Aggregate across all models for this skill
		for _, mr := range r.outcomes {
			skill.Models = append(skill.Models, mr.modelID)
			modelsMap[mr.modelID] = true

			if mr.outcome != nil {
				totalPassed += mr.outcome.Digest.Succeeded
				totalTests += mr.outcome.Digest.TotalTests
				sumScore += mr.outcome.Digest.AggregateScore
				validOutcomes++
			}

			// Build output file path (matches multi-model output naming)
			if outputPath != "" {
				ext := filepath.Ext(outputPath)
				base := strings.TrimSuffix(outputPath, ext)
				perModelPath := fmt.Sprintf("%s_%s%s", base, sanitizePathSegment(mr.modelID), ext)
				skill.OutputFiles = append(skill.OutputFiles, perModelPath)
			}
		}

		// Calculate skill-level metrics
		if totalTests > 0 {
			skill.PassRate = float64(totalPassed) / float64(totalTests)
		}
		if validOutcomes > 0 {
			skill.AggregateScore = sumScore / float64(validOutcomes)
		}

		// Only count skills with valid outcomes for overall averages
		if validOutcomes > 0 {
			totalPassRate += skill.PassRate
			totalAggregateScore += skill.AggregateScore
			validSkills++
		}

		summary.Skills = append(summary.Skills, skill)
	}

	// Calculate overall metrics
	summary.Overall.TotalSkills = len(results)
	summary.Overall.TotalModels = len(modelsMap)
	if validSkills > 0 {
		summary.Overall.AvgPassRate = totalPassRate / float64(validSkills)
		summary.Overall.AvgAggregateScore = totalAggregateScore / float64(validSkills)
	}

	return summary
}

// saveSummary writes a MultiSkillSummary to a JSON file.
func saveSummary(summary *models.MultiSkillSummary, path string) error {
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// writeOutputDir writes results to a structured directory hierarchy.
// For multi-skill runs: {outputDir}/{skillName}/{modelName}.json
// For single-skill runs: {outputDir}/{modelName}.json
func writeOutputDir(dir string, results []skillRunResult) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	multiSkill := len(results) > 1

	for _, skillResult := range results {
		for _, mr := range skillResult.outcomes {
			if mr.outcome == nil {
				continue
			}

			var outPath string
			if multiSkill {
				// Multi-skill: create skill subdirectory
				skillDir := filepath.Join(dir, sanitizePathSegment(skillResult.skillName))
				if err := os.MkdirAll(skillDir, 0755); err != nil {
					return fmt.Errorf("create skill directory %s: %w", skillDir, err)
				}
				modelFile := sanitizePathSegment(mr.modelID) + ".json"
				outPath = filepath.Join(skillDir, modelFile)
			} else {
				// Single-skill: write directly to output dir
				modelFile := sanitizePathSegment(mr.modelID) + ".json"
				outPath = filepath.Join(dir, modelFile)
			}

			if err := saveOutcome(mr.outcome, outPath); err != nil {
				return fmt.Errorf("save outcome to %s: %w", outPath, err)
			}
			fmt.Printf("Results saved to: %s\n", outPath)
		}
	}

	return nil
}
