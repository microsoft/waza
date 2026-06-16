package orchestration

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/copilotconfig"
	"github.com/microsoft/waza/internal/dataset"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/failures"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/hooks"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/template"
	"github.com/microsoft/waza/internal/transcript"
	"github.com/microsoft/waza/internal/utils"

	copilot "github.com/github/copilot-sdk/go"
)

// EvalRunner orchestrates the execution of tests.
//
// Deprecated alias: TestRunner is provided for backward compatibility.
type EvalRunner struct {
	cfg     *config.EvalConfig
	engine  execution.AgentEngine
	verbose bool

	// Task filtering
	taskFilters []string

	// Tag filtering for tasks
	tagFilters []string

	// Result caching
	cache *cache.Cache

	// Snapshot updates for diff graders.
	updateSnapshots bool

	// Skip grading (execution only)
	skipGraders bool

	// Lifecycle hooks
	hookRunner *hooks.Runner

	// Progress tracking
	progressMu sync.Mutex
	listeners  []ProgressListener

	failureHandler *failures.Handler
}

// ProgressListener receives progress updates
type ProgressListener func(event ProgressEvent)

// EventType represents the type of progress event
type EventType string

// EventType constants
const (
	EventBenchmarkStart    EventType = "benchmark_start"
	EventBenchmarkComplete EventType = "benchmark_complete"
	EventBenchmarkStopped  EventType = "benchmark_stopped"
	EventTestStart         EventType = "test_start"
	EventTestComplete      EventType = "test_complete"
	EventTestCached        EventType = "test_cached"
	EventRunStart          EventType = "run_start"
	EventRunComplete       EventType = "run_complete"
	EventAgentPrompt       EventType = "agent_prompt"
	EventAgentResponse     EventType = "agent_response"
	EventGraderResult      EventType = "grader_result"
)

// ProgressEvent represents a progress update
type ProgressEvent struct {
	EventType  EventType
	TestName   string
	TestNum    int
	TotalTests int
	RunNum     int
	TotalRuns  int
	Status     models.Status
	DurationMs int64
	Details    map[string]any
}

// RunnerOption configures a EvalRunner.
type RunnerOption func(*EvalRunner)

// WithTaskFilters sets glob patterns used to filter test cases by DisplayName or TestID.
func WithTaskFilters(patterns ...string) RunnerOption {
	return func(r *EvalRunner) {
		r.taskFilters = patterns
	}
}

func WithTagFilters(patterns ...string) RunnerOption {
	return func(r *EvalRunner) {
		r.tagFilters = patterns
	}
}

// WithCache enables result caching
func WithCache(c *cache.Cache) RunnerOption {
	return func(r *EvalRunner) {
		r.cache = c
	}
}

// WithUpdateSnapshots enables snapshot file updates in diff graders.
func WithUpdateSnapshots(enabled bool) RunnerOption {
	return func(r *EvalRunner) {
		r.updateSnapshots = enabled
	}
}

// WithSkipGraders disables grading so only execution occurs.
func WithSkipGraders() RunnerOption {
	return func(r *EvalRunner) {
		r.skipGraders = true
	}
}

// NewEvalRunner creates a new test runner. The caller owns the engine and is responsible for initializing and shutting it down as needed.
func NewEvalRunner(cfg *config.EvalConfig, engine execution.AgentEngine, opts ...RunnerOption) *EvalRunner {
	r := &EvalRunner{
		cfg:            cfg,
		engine:         engine,
		verbose:        cfg.Verbose(),
		listeners:      []ProgressListener{},
		failureHandler: failures.NewHandler(),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// OnProgress registers a progress listener
func (r *EvalRunner) OnProgress(listener ProgressListener) {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	r.listeners = append(r.listeners, listener)
}

// testOutcomeDetails extracts score and duration from a TestOutcome for inclusion
// in EventTestComplete Details.
func testOutcomeDetails(o *models.TestOutcome) map[string]any {
	score := 0.0
	durationMs := int64(0)
	if o.Stats != nil {
		score = o.Stats.AvgScore
		durationMs = o.Stats.AvgDurationMs
	}
	return map[string]any{
		"score":       score,
		"duration_ms": durationMs,
	}
}

func (r *EvalRunner) notifyProgress(event ProgressEvent) {
	r.progressMu.Lock()
	listeners := make([]ProgressListener, len(r.listeners))
	copy(listeners, r.listeners)
	r.progressMu.Unlock()

	for _, listener := range listeners {
		listener(event)
	}
}

// RunBenchmark executes the entire benchmark
// If Baseline is enabled, runs twice: skills-enabled and skills-disabled
func (r *EvalRunner) RunBenchmark(ctx context.Context) (*models.EvaluationOutcome, error) {

	if err := r.engine.Initialize(ctx); err != nil {
		return nil, err
	}

	spec := r.cfg.Spec()

	if spec.Baseline {
		return r.runBaselineComparison(ctx)
	}

	return r.runNormalBenchmark(ctx)
}

// runNormalBenchmark executes a normal single-pass evaluation
func (r *EvalRunner) runNormalBenchmark(ctx context.Context) (*models.EvaluationOutcome, error) {
	startTime := time.Now()

	// Set up hooks runner
	spec := r.cfg.Spec()
	r.hookRunner = &hooks.Runner{Verbose: r.verbose}

	// Run after_run hooks on exit (even on error)
	defer func() {
		if len(spec.Hooks.AfterRun) > 0 {
			if err := r.hookRunner.Execute(ctx, "after_run", spec.Hooks.AfterRun); err != nil {
				fmt.Printf("[WARN] after_run hook error: %v\n", err)
			}
		}
	}()

	// Run before_run hooks
	if len(spec.Hooks.BeforeRun) > 0 {
		if err := r.hookRunner.Execute(ctx, "before_run", spec.Hooks.BeforeRun); err != nil {
			return nil, fmt.Errorf("before_run hook failed: %w", err)
		}
	}

	// Preflight check: validate required skills
	if err := r.validateRequiredSkills(); err != nil {
		return nil, err
	}

	// Auto-inject tool_constraint grader from .agent.md tools if applicable
	resolvedPaths := utils.ResolvePaths(spec.Config.SkillPaths, r.cfg.SpecDir())
	if agentPath := resolveAgentPath(resolvedPaths); agentPath != "" {
		spec.Graders = augmentGradersFromAgent(spec.Graders, agentPath)
	}

	// Load test cases
	testCases, err := r.loadTestCases()
	if err != nil {
		return nil, fmt.Errorf("failed to load test cases: %w", err)
	}

	// Apply task/tag filters
	if len(r.taskFilters) > 0 || len(r.tagFilters) > 0 {
		testCases, err = FilterTestCases(testCases, r.taskFilters, r.tagFilters)
		if err != nil {
			return nil, fmt.Errorf("task/tag filter error: %w", err)
		}
		fmt.Printf("Task and tag filters matched %d test(s):\n", len(testCases))
		for _, tc := range testCases {
			fmt.Printf("  • %s (%s)\n", tc.DisplayName, tc.TestID)
		}
		fmt.Println()
	}

	if len(testCases) == 0 {
		return nil, fmt.Errorf("no test cases found")
	}

	r.notifyProgress(ProgressEvent{
		EventType:  EventBenchmarkStart,
		TotalTests: len(testCases),
	})

	// Execute tests
	var testOutcomes []models.TestOutcome

	// Now that CopilotEngine is concurrency-safe (protected by mutex),
	// we can safely use concurrent execution when configured
	if spec.Config.Concurrent {
		testOutcomes = r.runConcurrent(ctx, testCases)
	} else {
		testOutcomes = r.runSequential(ctx, testCases)
	}

	// Compute statistics
	digest := BuildDigest(testOutcomes, time.Since(startTime).Milliseconds(), spec.Config.TrialsPerTask)
	outcome := &models.EvaluationOutcome{
		RunID:       fmt.Sprintf("run-%d", time.Now().Unix()),
		SkillTested: spec.SkillName,
		BenchName:   spec.Name,
		Timestamp:   startTime,
		Setup: models.OutcomeSetup{
			RunsPerTest: spec.Config.TrialsPerTask,
			ModelID:     spec.Config.ModelID,
			EngineType:  spec.Config.EngineType,
			TimeoutSec:  spec.Config.TimeoutSec,
			JudgeModel:  spec.Config.JudgeModel,
		},
		Digest:       digest,
		Measures:     make(map[string]models.MeasureResult),
		TestOutcomes: testOutcomes,
		Metadata:     make(map[string]any),
	}

	r.notifyProgress(ProgressEvent{
		EventType:  EventBenchmarkComplete,
		DurationMs: time.Since(startTime).Milliseconds(),
	})

	return outcome, nil
}

// runBaselineComparison orchestrates A/B testing: skills-enabled vs skills-disabled
func (r *EvalRunner) runBaselineComparison(ctx context.Context) (*models.EvaluationOutcome, error) {
	spec := r.cfg.Spec()

	// Validation: eval must have skills configured
	if len(spec.Config.SkillPaths) == 0 && len(spec.Config.RequiredSkills) == 0 {
		fmt.Println("[WARN] --baseline specified but eval has no skills configured (skill_directories, required_skills empty). Skipping baseline comparison.")
		return r.runNormalBenchmark(ctx)
	}

	// PASS 1: Skills-Enabled
	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Println("PASS 1: Skills-Enabled Run")
	fmt.Println("════════════════════════════════════════════════════════════════")
	outcomesWithSkills, err := r.runNormalBenchmark(ctx)
	if err != nil {
		return nil, fmt.Errorf("skills-enabled run failed: %w", err)
	}

	// PASS 2: Skills Disabled (baseline)
	savedSkillPaths := spec.Config.SkillPaths
	savedRequiredSkills := spec.Config.RequiredSkills
	spec.Config.SkillPaths = []string{}
	spec.Config.RequiredSkills = []string{}
	defer func() {
		spec.Config.SkillPaths = savedSkillPaths
		spec.Config.RequiredSkills = savedRequiredSkills
	}()

	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Println("PASS 2: Skills Baseline (skills stripped)")
	fmt.Println("════════════════════════════════════════════════════════════════")
	outcomesWithoutSkills, err := r.runNormalBenchmark(ctx)
	if err != nil {
		return nil, fmt.Errorf("baseline run (skills disabled) failed: %w", err)
	}

	// Restore skills before merging
	spec.Config.SkillPaths = savedSkillPaths
	spec.Config.RequiredSkills = savedRequiredSkills

	// PASS 3: Compare and merge results
	return r.mergeBaselineOutcomes(outcomesWithSkills, outcomesWithoutSkills)
}

// mergeBaselineOutcomes pairs task results and computes skill impact
func (r *EvalRunner) mergeBaselineOutcomes(
	withSkills, withoutSkills *models.EvaluationOutcome,
) (*models.EvaluationOutcome, error) {

	// Build maps: TestID → TestOutcome for quick lookup
	withMap := make(map[string]*models.TestOutcome)
	withoutMap := make(map[string]*models.TestOutcome)

	for i := range withSkills.TestOutcomes {
		withMap[withSkills.TestOutcomes[i].TestID] = &withSkills.TestOutcomes[i]
	}
	for i := range withoutSkills.TestOutcomes {
		withoutMap[withoutSkills.TestOutcomes[i].TestID] = &withoutSkills.TestOutcomes[i]
	}

	// Merge: for each task, compute skill_impact
	for testID, withTo := range withMap {
		withoutTo, ok := withoutMap[testID]
		if !ok {
			return nil, fmt.Errorf("baseline mismatch: task %q present in skills-enabled but not baseline", testID)
		}

		withTo.SkillImpact = computeSkillImpact(withTo, withoutTo)
	}

	// Check for extra tasks in baseline
	for testID := range withoutMap {
		if _, ok := withMap[testID]; !ok {
			return nil, fmt.Errorf("baseline mismatch: task %q present in baseline but not skills-enabled", testID)
		}
	}

	// Print comparison report
	r.printSkillImpactReport(withSkills, withoutSkills)

	// Return merged outcome (use withSkills as the primary result)
	withSkills.IsBaseline = true
	withSkills.BaselineOutcome = withoutSkills
	return withSkills, nil
}

// computeSkillImpact calculates per-task impact metric
func computeSkillImpact(withSkills, without *models.TestOutcome) *models.SkillImpactMetric {
	passRateWith := computePassRate(withSkills)
	passRateWithout := computePassRate(without)

	delta := passRateWith - passRateWithout

	// Compute % improvement (with div-by-zero guard)
	denom := math.Max(passRateWithout, 0.01)
	percentImprovement := (delta / denom) * 100.0

	return &models.SkillImpactMetric{
		PassRateWithSkills: passRateWith,
		PassRateBaseline:   passRateWithout,
		Delta:              delta,
		PercentChange:      percentImprovement,
	}
}

func computePassRate(outcome *models.TestOutcome) float64 {
	if outcome.Stats != nil {
		return outcome.Stats.PassRate
	}
	// Fallback: compute from runs when stats haven't been populated yet
	if len(outcome.Runs) == 0 {
		return 0.0
	}
	passed := 0
	for _, r := range outcome.Runs {
		if r.Status == models.StatusPassed {
			passed++
		}
	}
	return float64(passed) / float64(len(outcome.Runs))
}

// printSkillImpactReport prints the A/B comparison summary
func (r *EvalRunner) printSkillImpactReport(withSkills, withoutSkills *models.EvaluationOutcome) {
	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Println("SKILL IMPACT ANALYSIS")
	fmt.Println("════════════════════════════════════════════════════════════════")

	withPassRate := withSkills.Digest.SuccessRate
	withoutPassRate := withoutSkills.Digest.SuccessRate
	delta := withPassRate - withoutPassRate

	fmt.Printf("Overall Performance Delta:\n")
	fmt.Printf("  With Skills:    %.1f%% (%d/%d tasks passed)\n",
		withPassRate*100, withSkills.Digest.Succeeded, withSkills.Digest.TotalTests)
	fmt.Printf("  Without Skills: %.1f%% (%d/%d tasks passed)\n",
		withoutPassRate*100, withoutSkills.Digest.Succeeded, withoutSkills.Digest.TotalTests)

	if delta > 0 {
		fmt.Printf("  Impact:         +%.1f percentage points\n\n", delta*100)
	} else if delta < 0 {
		fmt.Printf("  Impact:         %.1f percentage points\n\n", delta*100)
	} else {
		fmt.Printf("  Impact:         no change\n\n")
	}

	fmt.Println("Per-Task Breakdown:")
	improved := 0
	regressed := 0
	neutral := 0

	for i := range withSkills.TestOutcomes {
		to := &withSkills.TestOutcomes[i]
		if to.SkillImpact == nil {
			continue
		}

		impact := to.SkillImpact
		status := "[NEUTRAL]"
		if impact.Delta > 0 {
			status = "[IMPROVED]"
			improved++
		} else if impact.Delta < 0 {
			status = "[REGRESSED]"
			regressed++
		} else {
			neutral++
		}

		fmt.Printf("  • %-30s %s  %.0f%% → %.0f%% (%+.0fpp)\n",
			to.DisplayName,
			status,
			impact.PassRateBaseline*100,
			impact.PassRateWithSkills*100,
			impact.Delta*100,
		)
	}

	fmt.Println()
	if delta > 0 {
		fmt.Printf("Verdict: Skills have POSITIVE IMPACT (improved %d/%d tasks)\n", improved, len(withSkills.TestOutcomes))
	} else if delta < 0 {
		fmt.Printf("Verdict: Skills have NEGATIVE IMPACT (regressed %d/%d tasks)\n", regressed, len(withSkills.TestOutcomes))
	} else {
		fmt.Printf("Verdict: Skills have NEUTRAL IMPACT (no net change)\n")
	}
	fmt.Println("════════════════════════════════════════════════════════════════")
}

func (r *EvalRunner) loadTestCases() ([]*models.TestCase, error) {
	spec := r.cfg.Spec()

	// CSV dataset path: generate tasks from CSV rows
	if spec.TasksFrom != "" {
		return r.loadTestCasesFromCSV()
	}

	// Fall through to existing Tasks []string behavior
	return r.loadTestCasesFromFiles()
}

// loadTestCasesFromCSV generates in-memory TestCases from CSV rows.
func (r *EvalRunner) loadTestCasesFromCSV() ([]*models.TestCase, error) {
	spec := r.cfg.Spec()

	// Resolve CSV path relative to spec directory
	csvPath := spec.TasksFrom
	baseDir := r.cfg.SpecDir()
	if baseDir == "" {
		baseDir = "."
	}
	if !filepath.IsAbs(csvPath) {
		csvPath = filepath.Join(baseDir, csvPath)
	}

	// Path containment: CSV must resolve within spec directory
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving spec directory: %w", err)
	}
	absCSVPath, err := filepath.Abs(csvPath)
	if err != nil {
		return nil, fmt.Errorf("resolving CSV path: %w", err)
	}
	if !strings.HasPrefix(absCSVPath, absBaseDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("tasks_from path %q escapes spec directory", spec.TasksFrom)
	}

	// Validate and load CSV with optional range filtering
	var rows []dataset.Row
	if spec.Range != [2]int{} {
		if spec.Range[0] <= 0 || spec.Range[1] <= 0 {
			return nil, fmt.Errorf("invalid range: both values must be > 0, got [%d, %d]", spec.Range[0], spec.Range[1])
		}
		if spec.Range[0] > spec.Range[1] {
			return nil, fmt.Errorf("invalid range: start (%d) must be <= end (%d)", spec.Range[0], spec.Range[1])
		}
		rows, err = dataset.LoadCSVRange(csvPath, spec.Range[0], spec.Range[1])
	} else {
		rows, err = dataset.LoadCSV(csvPath)
	}
	if err != nil {
		return nil, fmt.Errorf("loading CSV dataset: %w", err)
	}

	// Build template context for resolving templates
	now := time.Now()
	baseCtx := &template.Context{
		JobID:     fmt.Sprintf("run-%d", now.Unix()),
		Timestamp: now.Format(time.RFC3339),
		Vars:      make(map[string]string),
	}

	// Merge spec.Inputs as base variables
	for k, v := range spec.Inputs {
		baseCtx.Vars[k] = v
	}

	testCases := make([]*models.TestCase, 0, len(rows))
	for i, row := range rows {
		rowNum := i + 1

		// Determine TestID: prefer "id" column, then "name", then "row-N"
		testID := fmt.Sprintf("row-%d", rowNum)
		if v, ok := row["id"]; ok && v != "" {
			testID = v
		} else if v, ok := row["name"]; ok && v != "" {
			testID = v
		}

		// Determine DisplayName: prefer "name" column, then "row-N"
		displayName := fmt.Sprintf("row-%d", rowNum)
		if v, ok := row["name"]; ok && v != "" {
			displayName = v
		}

		// Build per-row template context: inputs + CSV row (CSV overrides inputs on conflict)
		rowCtx := &template.Context{
			JobID:     baseCtx.JobID,
			TaskName:  displayName,
			Iteration: 0,
			Attempt:   0,
			Timestamp: baseCtx.Timestamp,
			Vars:      make(map[string]string),
		}
		for k, v := range spec.Inputs {
			rowCtx.Vars[k] = v
		}
		for k, v := range row {
			rowCtx.Vars[k] = v
		}

		// Resolve prompt: use "prompt" column if present, otherwise empty
		prompt := row["prompt"]
		if strings.Contains(prompt, "{{") {
			prompt, err = template.Render(prompt, rowCtx)
			if err != nil {
				return nil, fmt.Errorf("resolving prompt template for row %d: %w", rowNum, err)
			}
		}

		tc := &models.TestCase{
			TestID:      testID,
			DisplayName: displayName,
			Stimulus: models.TaskStimulus{
				Message: prompt,
			},
		}
		testCases = append(testCases, tc)
	}

	return testCases, nil
}

// loadTestCasesFromFiles loads test cases from YAML files via glob patterns.
func (r *EvalRunner) loadTestCasesFromFiles() ([]*models.TestCase, error) {
	spec := r.cfg.Spec()

	// Get base directory for test file resolution (spec directory)
	baseDir := r.cfg.SpecDir()
	if baseDir == "" {
		baseDir = "."
	}

	// Resolve test file patterns relative to the spec directory
	testFiles := []string{}
	for _, pattern := range spec.Tasks {
		fullPattern := filepath.Join(baseDir, pattern)
		matches, err := filepath.Glob(fullPattern)
		if err != nil {
			return nil, err
		}
		testFiles = append(testFiles, matches...)
	}

	if len(testFiles) == 0 {
		return nil, fmt.Errorf("no test files matched patterns: %v in directory: %s", spec.Tasks, baseDir)
	}

	var testCases []*models.TestCase
	for _, path := range testFiles {
		tc, err := models.LoadTestCase(path)
		if err != nil {
			return nil, fmt.Errorf("failed to load test case %s: %w", path, err)
		}
		// Only include active test cases
		// LoadTestCase defaults Active to true (nil case), so include nil or explicitly true
		if tc.Active == nil || *tc.Active {
			testCases = append(testCases, tc)
		}
	}

	return testCases, nil
}

// validateRequiredSkills performs preflight validation that all required skills are present.
func (r *EvalRunner) validateRequiredSkills() error {
	spec := r.cfg.Spec()

	// If all skills are disabled, skip validation
	if spec.Config.AllSkillsDisabled() {
		return nil
	}

	// If no required skills specified, skip validation
	if len(spec.Config.RequiredSkills) == 0 {
		return nil
	}

	// Get base directory for path resolution
	baseDir := r.cfg.SpecDir()
	if baseDir == "" {
		baseDir = "."
	}

	// Resolve skill paths
	resolvedPaths := utils.ResolvePaths(spec.Config.SkillPaths, baseDir)

	// If required skills specified but no skill directories, that's an error
	if len(resolvedPaths) == 0 {
		return fmt.Errorf("required_skills specified but no skill_directories configured")
	}

	// Discover skills in the specified directories
	discoveredSkills, err := discoverSkills(resolvedPaths)
	if err != nil {
		return fmt.Errorf("discovering skills: %w", err)
	}

	// Validate that all required skills were found
	if err := validateRequiredSkills(spec.Config.RequiredSkills, discoveredSkills, resolvedPaths); err != nil {
		return fmt.Errorf("skill validation failed:\n%w", err)
	}

	if r.verbose {
		fmt.Printf("✓ Required skills validation passed (%d/%d skills found)\n\n",
			len(spec.Config.RequiredSkills), len(spec.Config.RequiredSkills))
	}

	return nil
}

func (r *EvalRunner) runSequential(ctx context.Context, testCases []*models.TestCase) []models.TestOutcome {
	outcomes := make([]models.TestOutcome, 0, len(testCases))
	spec := r.cfg.Spec()

	for i, tc := range testCases {
		// Check if we should stop on error
		if spec.Config.StopOnError && i > 0 {
			// Check if any previous test failed or had an error
			for _, prevResult := range outcomes {
				if prevResult.Status != models.StatusPassed {
					r.notifyProgress(ProgressEvent{
						EventType: EventBenchmarkStopped,
						Details:   map[string]any{"reason": "fail_fast enabled and previous test failed"},
					})
					// Skip remaining tests
					return outcomes
				}
			}
		}

		// Run before_task hooks
		if r.hookRunner != nil && len(spec.Hooks.BeforeTask) > 0 {
			if err := r.hookRunner.Execute(ctx, "before_task", spec.Hooks.BeforeTask); err != nil {
				// before_task failure with error_on_fail: mark task as failed and skip
				outcomes = append(outcomes, models.TestOutcome{
					TestID:      tc.TestID,
					DisplayName: tc.DisplayName,
					Status:      models.StatusFailed,
					Runs:        []models.RunResult{},
				})
				r.notifyProgress(ProgressEvent{
					EventType:  EventTestComplete,
					TestName:   tc.DisplayName,
					TestNum:    i + 1,
					TotalTests: len(testCases),
					Status:     models.StatusFailed,
					Details:    map[string]any{"score": 0.0, "duration_ms": int64(0)},
				})
				continue
			}
		}

		r.notifyProgress(ProgressEvent{
			EventType:  EventTestStart,
			TestName:   tc.DisplayName,
			TestNum:    i + 1,
			TotalTests: len(testCases),
		})

		taskStart := time.Now()
		outcome, wasCached := r.runTest(ctx, tc, i+1, len(testCases))
		r.writeTaskTranscript(tc, outcome, taskStart)
		outcomes = append(outcomes, outcome)

		// Run after_task hooks
		if r.hookRunner != nil && len(spec.Hooks.AfterTask) > 0 {
			if err := r.hookRunner.Execute(ctx, "after_task", spec.Hooks.AfterTask); err != nil {
				fmt.Printf("[WARN] after_task hook error for %s: %v\n", tc.DisplayName, err)
			}
		}

		if wasCached {
			// Emit cached event instead of complete
			r.notifyProgress(ProgressEvent{
				EventType:  EventTestCached,
				TestName:   tc.DisplayName,
				TestNum:    i + 1,
				TotalTests: len(testCases),
				Status:     outcome.Status,
			})
		} else {
			r.notifyProgress(ProgressEvent{
				EventType:  EventTestComplete,
				TestName:   tc.DisplayName,
				TestNum:    i + 1,
				TotalTests: len(testCases),
				Status:     outcome.Status,
				Details:    testOutcomeDetails(&outcome),
			})
		}
	}

	return outcomes
}

func (r *EvalRunner) runConcurrent(ctx context.Context, testCases []*models.TestCase) []models.TestOutcome {
	// Simple concurrent implementation
	spec := r.cfg.Spec()
	workers := ResolveWorkersStderr(spec.Config.Workers, len(testCases), "tasks")

	type result struct {
		index   int
		outcome models.TestOutcome
	}

	resultChan := make(chan result, len(testCases))
	semaphore := make(chan struct{}, workers)

	var wg sync.WaitGroup

	for i, tc := range testCases {
		wg.Add(1)
		go func(idx int, test *models.TestCase) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Run before_task hooks
			if r.hookRunner != nil && len(spec.Hooks.BeforeTask) > 0 {
				if err := r.hookRunner.Execute(ctx, "before_task", spec.Hooks.BeforeTask); err != nil {
					resultChan <- result{index: idx, outcome: models.TestOutcome{
						TestID:      test.TestID,
						DisplayName: test.DisplayName,
						Status:      models.StatusFailed,
						Runs:        []models.RunResult{},
					}}
					r.notifyProgress(ProgressEvent{
						EventType:  EventTestComplete,
						TestName:   test.DisplayName,
						TestNum:    idx + 1,
						TotalTests: len(testCases),
						Status:     models.StatusFailed,
						Details:    map[string]any{"score": 0.0, "duration_ms": int64(0)},
					})
					return
				}
			}

			r.notifyProgress(ProgressEvent{
				EventType:  EventTestStart,
				TestName:   test.DisplayName,
				TestNum:    idx + 1,
				TotalTests: len(testCases),
			})

			taskStart := time.Now()
			outcome, wasCached := r.runTest(ctx, test, idx+1, len(testCases))
			r.writeTaskTranscript(test, outcome, taskStart)
			resultChan <- result{index: idx, outcome: outcome}

			// Run after_task hooks
			if r.hookRunner != nil && len(spec.Hooks.AfterTask) > 0 {
				if err := r.hookRunner.Execute(ctx, "after_task", spec.Hooks.AfterTask); err != nil {
					fmt.Printf("[WARN] after_task hook error for %s: %v\n", test.DisplayName, err)
				}
			}

			if wasCached {
				r.notifyProgress(ProgressEvent{
					EventType:  EventTestCached,
					TestName:   test.DisplayName,
					TestNum:    idx + 1,
					TotalTests: len(testCases),
					Status:     outcome.Status,
				})
			} else {
				r.notifyProgress(ProgressEvent{
					EventType:  EventTestComplete,
					TestName:   test.DisplayName,
					TestNum:    idx + 1,
					TotalTests: len(testCases),
					Status:     outcome.Status,
					Details:    testOutcomeDetails(&outcome),
				})
			}
		}(i, tc)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	results := make([]models.TestOutcome, len(testCases))
	for res := range resultChan {
		results[res.index] = res.outcome
	}

	return results
}

func (r *EvalRunner) runTest(ctx context.Context, tc *models.TestCase, testNum, totalTests int) (models.TestOutcome, bool) {
	spec := r.cfg.Spec()

	// Check cache if enabled
	if r.cache != nil {
		cacheKey, err := cache.CacheKey(spec, tc, r.cfg.FixtureDir())
		if err == nil {
			if cachedOutcome, found := r.cache.Get(cacheKey); found {
				// Return cached outcome with cached flag
				return *cachedOutcome, true
			}
			// Run the test and cache the result
			outcome := r.runTestUncached(ctx, tc, testNum, totalTests)
			// Store in cache and log any failures
			if err := r.cache.Put(cacheKey, &outcome); err != nil {
				fmt.Fprintf(os.Stderr, "[WARN] Failed to write cache for test %q: %v\n", tc.DisplayName, err)
			}
			return outcome, false
		}
	}

	// No cache or cache key generation failed
	return r.runTestUncached(ctx, tc, testNum, totalTests), false
}

func (r *EvalRunner) writeTaskTranscript(tc *models.TestCase, outcome models.TestOutcome, startTime time.Time) {
	transcriptDir := r.cfg.TranscriptDir()
	if transcriptDir == "" {
		return
	}

	taskTranscript := transcript.BuildTaskTranscript(tc, outcome, startTime)
	if _, err := transcript.Write(transcriptDir, taskTranscript); err != nil {
		fmt.Fprintf(os.Stderr, "[WARN] Failed to write transcript for %q: %v\n", tc.DisplayName, err)
	}
}

func (r *EvalRunner) runTestUncached(ctx context.Context, tc *models.TestCase, testNum, totalTests int) models.TestOutcome {
	spec := r.cfg.Spec()
	runsPerTest := spec.Config.TrialsPerTask
	maxAttempts := spec.Config.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	runs := make([]models.RunResult, 0, runsPerTest)

	for runNum := 1; runNum <= runsPerTest; runNum++ {
		r.notifyProgress(ProgressEvent{
			EventType:  EventRunStart,
			TestName:   tc.DisplayName,
			TestNum:    testNum,
			TotalTests: totalTests,
			RunNum:     runNum,
			TotalRuns:  runsPerTest,
		})

		var run models.RunResult
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			run = r.executeRun(ctx, tc, runNum)
			run.Attempts = attempt

			// If all graders passed or this is an infrastructure error, stop retrying
			if run.Status == models.StatusPassed || run.Status == models.StatusError || run.Status == models.StatusSkipped {
				break
			}

			// If more attempts remain, log the retry
			if attempt < maxAttempts && r.verbose {
				fmt.Printf("[RETRY] %s run %d: attempt %d/%d failed, retrying\n",
					tc.DisplayName, runNum, attempt, maxAttempts)
			}
		}

		// Surface errors even in non-verbose mode because they're critical for understanding test failures
		if run.ErrorMsg != "" && !r.verbose {
			fmt.Printf("[ERROR] %s\n\n", run.ErrorMsg)
		}

		runs = append(runs, run)

		r.notifyProgress(ProgressEvent{
			EventType:  EventRunComplete,
			TestName:   tc.DisplayName,
			TestNum:    testNum,
			TotalTests: totalTests,
			RunNum:     runNum,
			TotalRuns:  runsPerTest,
			Status:     run.Status,
			DurationMs: run.DurationMs,
			Details:    map[string]any{"workspace_dir": run.WorkspaceDir},
		})
	}

	// Compute test statistics
	stats := ComputeTestStats(runs)

	// Determine overall status
	status := overallStatus(runs)

	return models.TestOutcome{
		TestID:      tc.TestID,
		DisplayName: tc.DisplayName,
		Group:       r.resolveGroup(),
		Status:      status,
		Runs:        runs,
		Stats:       stats,
	}
}

func overallStatus(runs []models.RunResult) models.Status {
	if len(runs) == 0 {
		return models.StatusSkipped
	}
	status := models.StatusPassed
	allSkipped := true
	for _, run := range runs {
		switch run.Status {
		case models.StatusSkipped:
			continue
		case models.StatusPassed:
			allSkipped = false
		default:
			allSkipped = false
			status = models.StatusFailed
		}
	}
	if allSkipped {
		status = models.StatusSkipped
	}
	return status
}

func (r *EvalRunner) executeRun(ctx context.Context, tc *models.TestCase, runNum int) models.RunResult {
	startTime := time.Now()
	returnWithArtifacts := func(run models.RunResult) models.RunResult {
		r.captureFailureArtifacts(&run)
		return run
	}

	// Prepare execution request
	req, err := r.buildExecutionRequest(tc)
	if err != nil {
		return returnWithArtifacts(models.RunResult{
			RunNumber:  runNum,
			Status:     models.StatusError,
			DurationMs: time.Since(startTime).Milliseconds(),
			ErrorMsg:   err.Error(),
		})
	}

	// Emit agent prompt event before execution
	if r.verbose {
		r.notifyProgress(ProgressEvent{
			EventType: EventAgentPrompt,
			TestName:  tc.DisplayName,
			Details:   map[string]any{"message": req.Message},
		})
	}

	// Execute with the eval/task timeout represented as a context deadline.
	timeout, err := r.executionTimeout(tc)
	if err != nil {
		return returnWithArtifacts(models.RunResult{
			RunNumber:  runNum,
			Status:     models.StatusError,
			DurationMs: time.Since(startTime).Milliseconds(),
			ErrorMsg:   err.Error(),
		})
	}
	execCtx, cancelExec := context.WithTimeout(ctx, timeout)
	resp, err := r.engine.Execute(execCtx, req)
	cancelExec()
	if err != nil {
		return returnWithArtifacts(models.RunResult{
			RunNumber:  runNum,
			Status:     models.StatusError,
			DurationMs: time.Since(startTime).Milliseconds(),
			ErrorMsg:   err.Error(),
		})
	}

	// Emit agent response event after execution
	if r.verbose {
		r.notifyProgress(ProgressEvent{
			EventType: EventAgentResponse,
			TestName:  tc.DisplayName,
			Details: map[string]any{
				"error":      resp.ErrorMsg,
				"output":     resp.FinalOutput,
				"transcript": r.buildTranscript(resp),
				"tool_calls": len(resp.ToolCalls),
			},
		})
	}

	// Execute follow-up prompts if defined
	if len(tc.Stimulus.FollowUps) > 0 {
		r.executeFollowUps(ctx, tc, resp)
	}

	// Build validation context
	vCtx := r.buildGraderContext(tc, resp)

	var gradersResults map[string]models.GraderResults
	if r.skipGraders {
		gradersResults = make(map[string]models.GraderResults)
	} else {
		var err error
		gradersResults, err = r.runGraders(ctx, tc, vCtx)

		if err != nil {
			return returnWithArtifacts(models.RunResult{
				RunNumber:  runNum,
				Status:     models.StatusError,
				DurationMs: time.Since(startTime).Milliseconds(),
				ErrorMsg:   "running graders: " + err.Error(),
			})
		}
	}

	// Emit grader result events (sorted for stable output)
	graderNames := make([]string, 0, len(gradersResults))
	for name := range gradersResults {
		graderNames = append(graderNames, name)
	}
	sort.Strings(graderNames)
	for _, name := range graderNames {
		gr := gradersResults[name]
		r.notifyProgress(ProgressEvent{
			EventType:  EventGraderResult,
			TestName:   tc.DisplayName,
			DurationMs: gr.DurationMs,
			Details: map[string]any{
				"grader":      name,
				"grader_type": gr.Type,
				"passed":      gr.Passed,
				"score":       gr.Score,
				"feedback":    gr.Feedback,
			},
		})
	}

	// Determine status
	status := models.StatusPassed
	if resp.ErrorMsg != "" {
		status = models.StatusError
	} else if r.skipGraders {
		status = models.StatusSkipped
	} else {
		for _, v := range gradersResults {
			if !v.Passed {
				status = models.StatusFailed
				break
			}
		}
	}

	// Build transcript
	transcript := r.buildTranscript(resp)

	skillInvocations := make([]models.SkillInvocation, len(resp.SkillInvocations))
	for i, si := range resp.SkillInvocations {
		skillInvocations[i] = models.SkillInvocation{Name: si.Name, Path: si.Path}
	}

	return returnWithArtifacts(models.RunResult{
		RunNumber:        runNum,
		Status:           status,
		DurationMs:       resp.DurationMs,
		Validations:      gradersResults,
		SessionDigest:    r.buildSessionDigest(resp),
		Transcript:       transcript,
		FinalOutput:      resp.FinalOutput,
		ErrorMsg:         resp.ErrorMsg,
		SkillInvocations: skillInvocations,
		WorkspaceDir:     resp.WorkspaceDir,
	})
}

func (r *EvalRunner) captureFailureArtifacts(run *models.RunResult) {
	if run == nil || r.failureHandler == nil {
		return
	}
	if run.Status != models.StatusFailed && run.Status != models.StatusError {
		return
	}

	exitCode := 1
	if run.Status == models.StatusError {
		exitCode = 2
	}
	r.failureHandler.CaptureFailure(run, exitCode, run.ErrorMsg, run.FinalOutput)
}

func (r *EvalRunner) buildExecutionRequest(tc *models.TestCase) (*execution.ExecutionRequest, error) {
	// Load resource files
	resources, err := r.loadContextFixtureResources(tc)
	if err != nil {
		return nil, err
	}
	resources = append(resources, r.loadResources(tc)...)
	instructions, instructionResources, err := r.loadInstructionFiles(tc)
	if err != nil {
		return nil, err
	}
	resources = append(resources, instructionResources...)
	if err := rejectRelativePathPromptWithEmptySandbox(tc, resources); err != nil {
		return nil, err
	}

	spec := r.cfg.Spec()
	// Use task-level skill paths if specified, otherwise fall back to eval-level
	skillPaths := spec.Config.FilteredSkillPaths()
	if len(tc.SkillPaths) > 0 {
		skillPaths = tc.SkillPaths
	}
	resolvedSkillPaths := utils.ResolvePaths(skillPaths, r.cfg.SpecDir())
	noSkills := spec.Config.AllSkillsDisabled()

	return &execution.ExecutionRequest{
		Message:           tc.Stimulus.Message,
		Context:           tc.Stimulus.Metadata,
		Resources:         resources,
		GitResources:      tc.Stimulus.Repos,
		WorkDir:           tc.Stimulus.WorkDir,
		Instructions:      instructions,
		SkillName:         spec.SkillName,
		TaskName:          tc.DisplayName,
		TaskDescription:   tc.Summary,
		SkillPaths:        resolvedSkillPaths,
		NoSkills:          noSkills,
		SuppressSkillBody: !spec.Config.ShouldInjectSkillBody(),
		MCPServers:        convertMCPServers(spec.Config.ServerConfigs),
		FirstEventTimeout: r.firstEventTimeout(tc),
	}, nil
}

func (r *EvalRunner) executionTimeout(tc *models.TestCase) (time.Duration, error) {
	timeout := r.cfg.Spec().Config.TimeoutSec
	if tc.TimeoutSec != nil {
		if err := tc.Validate(); err != nil {
			return 0, err
		}
		timeout = *tc.TimeoutSec
	}
	return time.Duration(timeout) * time.Second, nil
}

// firstEventTimeout resolves the per-run "time to first session event" budget:
// the task-level override when set, otherwise the eval-level default. A value of
// 0 (the default) disables the first-event watchdog so existing specs that never
// configure it keep their prior behavior.
func (r *EvalRunner) firstEventTimeout(tc *models.TestCase) time.Duration {
	sec := r.cfg.Spec().Config.FirstEventTimeoutSec
	if tc.FirstEventTimeoutSec != nil {
		sec = *tc.FirstEventTimeoutSec
	}
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

func rejectRelativePathPromptWithEmptySandbox(tc *models.TestCase, resources []execution.ResourceFile) error {
	if len(resources) > 0 {
		return nil
	}

	message := tc.Stimulus.Message
	if strings.Contains(message, "./") || strings.Contains(message, "../") {
		return fmt.Errorf("prompt references relative paths but no workspace files were loaded; use inputs.files to copy fixtures into the sandbox")
	}

	return nil
}

// executeFollowUps sends follow-up prompts using the same workspace and session,
// aggregating results into the original response.
func (r *EvalRunner) executeFollowUps(ctx context.Context, tc *models.TestCase, resp *execution.ExecutionResponse) {
	for i, prompt := range tc.Stimulus.FollowUps {
		followReq, err := r.buildExecutionRequest(tc)
		if err != nil {
			resp.ErrorMsg = fmt.Sprintf("follow-up %d/%d setup failed: %v", i+1, len(tc.Stimulus.FollowUps), err)
			break
		}
		followReq.Message = prompt
		followReq.SessionID = resp.SessionID
		followReq.WorkspaceDir = resp.WorkspaceDir

		if r.verbose {
			r.notifyProgress(ProgressEvent{
				EventType: EventAgentPrompt,
				TestName:  tc.DisplayName,
				Details:   map[string]any{"message": prompt, "follow_up": i + 1, "total": len(tc.Stimulus.FollowUps)},
			})
		}

		timeout, err := r.executionTimeout(tc)
		if err != nil {
			resp.ErrorMsg = fmt.Sprintf("follow-up %d/%d setup failed: %v", i+1, len(tc.Stimulus.FollowUps), err)
			break
		}
		followCtx, cancelFollow := context.WithTimeout(ctx, timeout)
		followResp, err := r.engine.Execute(followCtx, followReq)
		cancelFollow()
		if err != nil {
			resp.ErrorMsg = fmt.Sprintf("follow-up %d/%d failed: %v", i+1, len(tc.Stimulus.FollowUps), err)
			break
		}

		if followResp.ErrorMsg != "" {
			resp.ErrorMsg = fmt.Sprintf("follow-up %d/%d: %s", i+1, len(tc.Stimulus.FollowUps), followResp.ErrorMsg)
			break
		}

		// Aggregate results
		resp.Events = append(resp.Events, followResp.Events...)
		resp.ToolCalls = append(resp.ToolCalls, followResp.ToolCalls...)
		resp.SkillInvocations = append(resp.SkillInvocations, followResp.SkillInvocations...)
		resp.DurationMs += followResp.DurationMs
		resp.FinalOutput = followResp.FinalOutput
		resp.WorkspaceFiles = followResp.WorkspaceFiles
		if followResp.Usage != nil {
			if resp.Usage == nil {
				resp.Usage = followResp.Usage
			} else {
				resp.Usage = models.AggregateUsageStats([]*models.UsageStats{resp.Usage, followResp.Usage})
			}
		}
	}
}

func (r *EvalRunner) loadResources(tc *models.TestCase) []execution.ResourceFile {
	var resources []execution.ResourceFile

	// Determine fixture directory (for loading resource files)
	fixtureDir := r.cfg.FixtureDir()
	if tc.ContextRoot != "" {
		fixtureDir = tc.ContextRoot
	}

	for _, ref := range tc.Stimulus.Resources {
		if ref.Body != "" {
			// Inline content
			resources = append(resources, execution.ResourceFile{
				Path:    ref.Location,
				Content: []byte(ref.Body),
			})
		} else if ref.Location != "" && fixtureDir != "" {
			// Load from file - validate path to prevent directory traversal
			if filepath.IsAbs(ref.Location) {
				fmt.Fprintf(os.Stderr, "Warning: absolute resource path %q rejected\n", ref.Location)
				continue
			}

			cleanPath := filepath.Clean(ref.Location)
			if strings.Contains(cleanPath, "..") {
				fmt.Fprintf(os.Stderr, "Warning: resource path %q contains '..' and is rejected\n", ref.Location)
				continue
			}

			fullPath := filepath.Join(fixtureDir, cleanPath)

			// Ensure the resolved path is still within fixtureDir
			absFixtureDir, err := filepath.Abs(fixtureDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to get absolute path for fixture dir: %v\n", err)
				continue
			}

			absFullPath, err := filepath.Abs(fullPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to get absolute path for resource: %v\n", err)
				continue
			}

			if !strings.HasPrefix(absFullPath, absFixtureDir+string(filepath.Separator)) {
				fmt.Fprintf(os.Stderr, "Warning: resource path %q escapes fixture directory\n", ref.Location)
				continue
			}

			content, err := os.ReadFile(fullPath)
			if err != nil {
				// Log error but continue - let the test fail if resource is critical
				fmt.Fprintf(os.Stderr, "Warning: failed to load resource file %s: %v\n", fullPath, err)
				continue
			}
			resources = append(resources, execution.ResourceFile{
				Path:    ref.Location,
				Content: content,
			})
		}
	}

	return resources
}

func (r *EvalRunner) loadContextFixtureResources(tc *models.TestCase) ([]execution.ResourceFile, error) {
	fixtureValue, ok := tc.Stimulus.Metadata["fixture"]
	if !ok {
		return nil, nil
	}
	fixturePath, ok := fixtureValue.(string)
	if !ok {
		return nil, fmt.Errorf("inputs.context.fixture must be a string")
	}
	if fixturePath == "" {
		return nil, fmt.Errorf("inputs.context.fixture must not be empty")
	}
	if filepath.IsAbs(fixturePath) || strings.HasPrefix(fixturePath, "/") || strings.HasPrefix(fixturePath, "\\") || filepath.VolumeName(fixturePath) != "" {
		return nil, fmt.Errorf("inputs.context.fixture path %q must be relative", fixturePath)
	}
	if containsPathTraversal(fixturePath) {
		return nil, fmt.Errorf("inputs.context.fixture path %q must not contain path traversal", fixturePath)
	}

	baseDir := r.cfg.SpecDir()
	if baseDir == "" {
		baseDir = "."
	}

	cleanFixturePath := filepath.Clean(fixturePath)
	if cleanFixturePath == "." {
		return nil, fmt.Errorf("inputs.context.fixture path %q must not refer to the spec directory itself", fixturePath)
	}
	fullPath := filepath.Join(baseDir, cleanFixturePath)
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving spec directory: %w", err)
	}
	absFullPath, err := filepath.Abs(fullPath)
	if err != nil {
		return nil, fmt.Errorf("resolving inputs.context.fixture path %q: %w", fixturePath, err)
	}
	if absFullPath != absBaseDir && !strings.HasPrefix(absFullPath, absBaseDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("inputs.context.fixture path %q escapes spec directory", fixturePath)
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, fmt.Errorf("reading inputs.context.fixture %q: %w", fixturePath, err)
	}

	// Re-check containment using symlink-resolved paths.
	realFullPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return nil, fmt.Errorf("resolving symlinks for inputs.context.fixture %q: %w", fixturePath, err)
	}
	realBaseDir, err := filepath.EvalSymlinks(absBaseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving symlinks for spec directory: %w", err)
	}
	if realFullPath != realBaseDir && !strings.HasPrefix(realFullPath, realBaseDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("inputs.context.fixture path %q escapes spec directory", fixturePath)
	}

	if !info.IsDir() {
		content, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("reading inputs.context.fixture file %q: %w", fixturePath, err)
		}
		return []execution.ResourceFile{{
			Path:    filepath.ToSlash(filepath.Base(cleanFixturePath)),
			Content: content,
		}}, nil
	}

	var resources []execution.ResourceFile
	err = filepath.WalkDir(fullPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(fullPath, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		resources = append(resources, execution.ResourceFile{
			Path:    filepath.ToSlash(rel),
			Content: content,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading inputs.context.fixture %q: %w", fixturePath, err)
	}

	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Path < resources[j].Path
	})
	return resources, nil
}

func (r *EvalRunner) loadInstructionFiles(tc *models.TestCase) ([]execution.InstructionFile, []execution.ResourceFile, error) {
	spec := r.cfg.Spec()
	paths := append([]string{}, spec.Config.InstructionFiles...)
	paths = append(paths, tc.InstructionFiles...)
	if len(paths) == 0 {
		return nil, nil, nil
	}

	fixtureDir := r.cfg.FixtureDir()
	if tc.ContextRoot != "" {
		fixtureDir = tc.ContextRoot
	}
	if fixtureDir == "" {
		return nil, nil, fmt.Errorf("instruction_files require a context/fixtures directory")
	}

	instructions := make([]execution.InstructionFile, 0, len(paths))
	resources := make([]execution.ResourceFile, 0, len(paths))
	for _, path := range paths {
		cleanPath, fullPath, err := resolveContextFile(fixtureDir, path, "instruction_files")
		if err != nil {
			return nil, nil, err
		}

		content, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, nil, fmt.Errorf("reading instruction file %q: %w", path, err)
		}

		instructions = append(instructions, execution.InstructionFile{
			Path:    filepath.ToSlash(cleanPath),
			Content: content,
		})
		resources = append(resources, execution.ResourceFile{
			Path:    filepath.ToSlash(cleanPath),
			Content: content,
		})
	}

	return instructions, resources, nil
}

func resolveContextFile(baseDir, relPath, field string) (string, string, error) {
	if relPath == "" {
		return "", "", fmt.Errorf("%s path must not be empty", field)
	}
	if filepath.IsAbs(relPath) {
		return "", "", fmt.Errorf("%s path %q must be relative", field, relPath)
	}
	if containsPathTraversal(relPath) {
		return "", "", fmt.Errorf("%s path %q must not contain path traversal", field, relPath)
	}

	cleanPath := filepath.Clean(relPath)
	if cleanPath == "." {
		return "", "", fmt.Errorf("%s path must not be empty", field)
	}

	fullPath := filepath.Join(baseDir, cleanPath)
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return "", "", fmt.Errorf("resolving context directory: %w", err)
	}
	absFullPath, err := filepath.Abs(fullPath)
	if err != nil {
		return "", "", fmt.Errorf("resolving %s path %q: %w", field, relPath, err)
	}

	if absFullPath != absBaseDir && !strings.HasPrefix(absFullPath, absBaseDir+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%s path %q escapes context directory", field, relPath)
	}

	return cleanPath, fullPath, nil
}

func containsPathTraversal(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return true
		}
	}
	return false
}

// convertMCPServers converts the eval YAML mcp_servers config (map[string]any)
// into the copilot SDK's MCPServerConfig type. Returns nil if no servers configured.
func convertMCPServers(serverConfigs map[string]any) map[string]copilot.MCPServerConfig {
	return copilotconfig.ConvertMCPServers(serverConfigs, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format, args...)
	})
}

func (r *EvalRunner) buildGraderContext(tc *models.TestCase, resp *execution.ExecutionResponse) *graders.Context {
	// Convert events to transcript entries
	var transcript []models.TranscriptEvent
	for _, evt := range resp.Events {
		entry := models.TranscriptEvent{SessionEvent: evt}
		transcript = append(transcript, entry)
	}

	sessionDigest := r.buildSessionDigest(resp)

	return &graders.Context{
		TestCase:         tc,
		Transcript:       transcript,
		Output:           resp.FinalOutput,
		Outcome:          make(map[string]any),
		DurationMS:       resp.DurationMs,
		Metadata:         make(map[string]any),
		WorkspaceDir:     resp.WorkspaceDir,
		WorkspaceFiles:   resp.WorkspaceFiles,
		SkillInvocations: resp.SkillInvocations,
		SessionID:        resp.SessionID,
		Session:          &sessionDigest,
		Executor:         r.engine,
	}
}

func (r *EvalRunner) runGraders(ctx context.Context, tc *models.TestCase, gradersContext *graders.Context) (map[string]models.GraderResults, error) {
	spec := r.cfg.Spec()
	return graders.RunAll(ctx, spec.Graders, tc, gradersContext, spec.Config.JudgeModel, r.updateSnapshots)
}

func (r *EvalRunner) buildSessionDigest(resp *execution.ExecutionResponse) models.SessionDigest {
	toolsUsed := make([]string, 0)
	for _, call := range resp.ToolCalls {
		toolsUsed = append(toolsUsed, call.Name)
	}

	digest := models.SessionDigest{
		ToolCallCount: len(resp.ToolCalls),
		ToolsUsed:     toolsUsed,
		ToolCalls:     resp.ToolCalls,
		Errors:        []string{},
		Usage:         resp.Usage,
		SessionID:     resp.SessionID,
	}

	return digest
}

func (r *EvalRunner) buildTranscript(resp *execution.ExecutionResponse) []models.TranscriptEvent {
	return transcript.BuildFromSessionEvents(resp.Events)
}

// resolveGroup returns the group value for the current benchmark configuration.
// Currently only "model" is supported; CSV column grouping will be added with #187.
func (r *EvalRunner) resolveGroup() string {
	spec := r.cfg.Spec()
	switch spec.Config.GroupBy {
	case "model":
		return spec.Config.ModelID
	case "":
		return ""
	default:
		fmt.Printf("[WARN] unknown group_by value %q, grouping disabled\n", spec.Config.GroupBy)
		return ""
	}
}

// Deprecated: Use EvalRunner instead.
type TestRunner = EvalRunner

// Deprecated: Use NewEvalRunner instead.
func NewTestRunner(cfg *config.EvalConfig, engine execution.AgentEngine, opts ...RunnerOption) *EvalRunner {
	return NewEvalRunner(cfg, engine, opts...)
}
