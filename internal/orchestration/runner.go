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

	"github.com/spboyer/waza/internal/config"
	"github.com/spboyer/waza/internal/execution"
	"github.com/spboyer/waza/internal/graders"
	"github.com/spboyer/waza/internal/models"
	"github.com/spboyer/waza/internal/utils"
)

// TestRunner orchestrates the execution of tests
type TestRunner struct {
	cfg     *config.BenchmarkConfig
	engine  execution.AgentEngine
	verbose bool

	// Task filtering
	taskFilters []string

	// Progress tracking
	progressMu sync.Mutex
	listeners  []ProgressListener
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

// RunnerOption configures a TestRunner.
type RunnerOption func(*TestRunner)

// WithTaskFilters sets glob patterns used to filter test cases by DisplayName or TestID.
func WithTaskFilters(patterns ...string) RunnerOption {
	return func(r *TestRunner) {
		r.taskFilters = patterns
	}
}

// NewTestRunner creates a new test runner
func NewTestRunner(cfg *config.BenchmarkConfig, engine execution.AgentEngine, opts ...RunnerOption) *TestRunner {
	r := &TestRunner{
		cfg:       cfg,
		engine:    engine,
		verbose:   cfg.Verbose(),
		listeners: []ProgressListener{},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// OnProgress registers a progress listener
func (r *TestRunner) OnProgress(listener ProgressListener) {
	r.progressMu.Lock()
	defer r.progressMu.Unlock()
	r.listeners = append(r.listeners, listener)
}

func (r *TestRunner) notifyProgress(event ProgressEvent) {
	r.progressMu.Lock()
	listeners := make([]ProgressListener, len(r.listeners))
	copy(listeners, r.listeners)
	r.progressMu.Unlock()

	for _, listener := range listeners {
		listener(event)
	}
}

// RunBenchmark executes the entire benchmark
func (r *TestRunner) RunBenchmark(ctx context.Context) (*models.EvaluationOutcome, error) {
	startTime := time.Now()

	// Initialize engine
	if err := r.engine.Initialize(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize engine: %w", err)
	}
	defer func() {
		if err := r.engine.Shutdown(ctx); err != nil {
			fmt.Printf("warning: failed to shutdown engine: %v\n", err)
		}
	}()

	// Preflight check: validate required skills
	if err := r.validateRequiredSkills(); err != nil {
		return nil, err
	}

	// Load test cases
	testCases, err := r.loadTestCases()
	if err != nil {
		return nil, fmt.Errorf("failed to load test cases: %w", err)
	}

	// Apply task filters
	if len(r.taskFilters) > 0 {
		testCases, err = FilterTestCases(testCases, r.taskFilters)
		if err != nil {
			return nil, fmt.Errorf("task filter error: %w", err)
		}
		fmt.Printf("Task filter matched %d test(s):\n", len(testCases))
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

	spec := r.cfg.Spec()
	// Now that CopilotEngine is concurrency-safe (protected by mutex),
	// we can safely use concurrent execution when configured
	if spec.Config.Concurrent {
		testOutcomes = r.runConcurrent(ctx, testCases)
	} else {
		testOutcomes = r.runSequential(ctx, testCases)
	}

	// Compute statistics
	outcome := r.buildOutcome(testOutcomes, startTime)

	r.notifyProgress(ProgressEvent{
		EventType:  EventBenchmarkComplete,
		DurationMs: time.Since(startTime).Milliseconds(),
	})

	return outcome, nil
}

func (r *TestRunner) loadTestCases() ([]*models.TestCase, error) {
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
func (r *TestRunner) validateRequiredSkills() error {
	spec := r.cfg.Spec()

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

func (r *TestRunner) runSequential(ctx context.Context, testCases []*models.TestCase) []models.TestOutcome {
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

		r.notifyProgress(ProgressEvent{
			EventType:  EventTestStart,
			TestName:   tc.DisplayName,
			TestNum:    i + 1,
			TotalTests: len(testCases),
		})

		outcome := r.runTest(ctx, tc, i+1, len(testCases))
		outcomes = append(outcomes, outcome)

		r.notifyProgress(ProgressEvent{
			EventType:  EventTestComplete,
			TestName:   tc.DisplayName,
			TestNum:    i + 1,
			TotalTests: len(testCases),
			Status:     outcome.Status,
		})
	}

	return outcomes
}

func (r *TestRunner) runConcurrent(ctx context.Context, testCases []*models.TestCase) []models.TestOutcome {
	// Simple concurrent implementation
	spec := r.cfg.Spec()
	workers := spec.Config.Workers
	if workers <= 0 {
		workers = 4
	}

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

			r.notifyProgress(ProgressEvent{
				EventType:  EventTestStart,
				TestName:   test.DisplayName,
				TestNum:    idx + 1,
				TotalTests: len(testCases),
			})

			outcome := r.runTest(ctx, test, idx+1, len(testCases))
			resultChan <- result{index: idx, outcome: outcome}

			r.notifyProgress(ProgressEvent{
				EventType:  EventTestComplete,
				TestName:   test.DisplayName,
				TestNum:    idx + 1,
				TotalTests: len(testCases),
				Status:     outcome.Status,
			})
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

func (r *TestRunner) runTest(ctx context.Context, tc *models.TestCase, testNum, totalTests int) models.TestOutcome {
	spec := r.cfg.Spec()
	runsPerTest := spec.Config.RunsPerTest

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

		run := r.executeRun(ctx, tc, runNum)
		// surface errors even in non-verbose mode because they're critical for understanding test failures
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
		})
	}

	// Compute test statistics
	stats := r.computeTestStats(runs)

	// Determine overall status
	status := models.StatusPassed
	for _, run := range runs {
		if run.Status != models.StatusPassed {
			status = models.StatusFailed
			break
		}
	}

	return models.TestOutcome{
		TestID:      tc.TestID,
		DisplayName: tc.DisplayName,
		Status:      status,
		Runs:        runs,
		Stats:       stats,
	}
}

func (r *TestRunner) executeRun(ctx context.Context, tc *models.TestCase, runNum int) models.RunResult {
	startTime := time.Now()

	// Prepare execution request
	req := r.buildExecutionRequest(tc)

	// Emit agent prompt event before execution
	if r.verbose {
		r.notifyProgress(ProgressEvent{
			EventType: EventAgentPrompt,
			TestName:  tc.DisplayName,
			Details:   map[string]any{"message": req.Message},
		})
	}

	// Execute
	resp, err := r.engine.Execute(ctx, req)
	if err != nil {
		return models.RunResult{
			RunNumber:  runNum,
			Status:     models.StatusError,
			DurationMs: time.Since(startTime).Milliseconds(),
			ErrorMsg:   err.Error(),
		}
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

	// Build validation context
	vCtx := r.buildGraderContext(tc, resp)

	gradersResults, err := r.runGraders(ctx, tc, vCtx)

	if err != nil {
		return models.RunResult{
			RunNumber:  runNum,
			Status:     models.StatusError,
			DurationMs: time.Since(startTime).Milliseconds(),
			ErrorMsg:   "running graders: " + err.Error(),
		}
	}

	// Emit grader result events for verbose mode (sorted for stable output)
	if r.verbose {
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
					"grader":   name,
					"type":     gr.Type,
					"passed":   gr.Passed,
					"score":    gr.Score,
					"feedback": gr.Feedback,
				},
			})
		}
	}

	// Determine status
	status := models.StatusPassed
	if resp.ErrorMsg != "" {
		status = models.StatusError
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

	return models.RunResult{
		RunNumber:     runNum,
		Status:        status,
		DurationMs:    resp.DurationMs,
		Validations:   gradersResults,
		SessionDigest: r.buildSessionDigest(resp),
		Transcript:    transcript,
		FinalOutput:   resp.FinalOutput,
		ErrorMsg:      resp.ErrorMsg,
	}
}

func (r *TestRunner) buildExecutionRequest(tc *models.TestCase) *execution.ExecutionRequest {
	// Load resource files
	resources := r.loadResources(tc)

	spec := r.cfg.Spec()
	timeout := spec.Config.TimeoutSec
	if tc.TimeoutSec != nil {
		timeout = *tc.TimeoutSec
	}

	// Resolve skill paths relative to spec directory
	resolvedSkillPaths := utils.ResolvePaths(spec.Config.SkillPaths, r.cfg.SpecDir())

	return &execution.ExecutionRequest{
		TestID:     tc.TestID,
		Message:    tc.Stimulus.Message,
		Context:    tc.Stimulus.Metadata,
		Resources:  resources,
		SkillName:  spec.SkillName,
		SkillPaths: resolvedSkillPaths,
		TimeoutSec: timeout,
	}
}

func (r *TestRunner) loadResources(tc *models.TestCase) []execution.ResourceFile {
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
				Content: ref.Body,
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
				Content: string(content),
			})
		}
	}

	return resources
}

func (r *TestRunner) buildGraderContext(tc *models.TestCase, resp *execution.ExecutionResponse) *graders.Context {
	// Convert events to transcript entries
	var transcript []models.TranscriptEvent
	for _, evt := range resp.Events {
		entry := models.TranscriptEvent{SessionEvent: evt}
		transcript = append(transcript, entry)
	}

	return &graders.Context{
		TestCase:         tc,
		Transcript:       transcript,
		Output:           resp.FinalOutput,
		Outcome:          make(map[string]any),
		DurationMS:       resp.DurationMs,
		Metadata:         make(map[string]any),
		WorkspaceDir:     resp.WorkspaceDir,
		SkillInvocations: resp.SkillInvocations,
	}
}

func (r *TestRunner) runGraders(ctx context.Context, tc *models.TestCase, gradersContext *graders.Context) (map[string]models.GraderResults, error) {
	graderResults := make(map[string]models.GraderResults)

	// Run global validators
	spec := r.cfg.Spec()
	for _, vCfg := range spec.Graders {
		grader, err := graders.Create(vCfg.Kind, vCfg.Identifier, vCfg.Parameters)

		if err != nil {
			return nil, err
		}

		result, err := grader.Grade(ctx, gradersContext)

		if err != nil {
			return nil, err
		}

		graderResults[result.Name] = *result
	}

	// Run test-specific validators
	for _, vCfg := range tc.Validators {
		kind := vCfg.Kind
		if kind == "" {
			return nil, fmt.Errorf("no kind associated with grader %s", vCfg.Identifier)
		}

		params := vCfg.Parameters
		if params == nil {
			params = make(map[string]any)
		}
		if len(vCfg.Checks) > 0 {
			params["assertions"] = vCfg.Checks
		}

		grader, err := graders.Create(kind, vCfg.Identifier, params)

		if err != nil {
			return nil, fmt.Errorf("failed to create grader %s: %w", vCfg.Identifier, err)
		}

		result, err := grader.Grade(ctx, gradersContext)

		if err != nil {
			return nil, fmt.Errorf("failed to run grader %s: %w", vCfg.Identifier, err)
		}

		graderResults[result.Name] = *result
	}

	return graderResults, nil
}

func (r *TestRunner) buildSessionDigest(resp *execution.ExecutionResponse) models.SessionDigest {
	toolsUsed := make([]string, 0)
	for _, call := range resp.ToolCalls {
		toolsUsed = append(toolsUsed, call.Name)
	}

	return models.SessionDigest{
		TotalTurns:    len(resp.Events),
		ToolCallCount: len(resp.ToolCalls),
		ToolsUsed:     toolsUsed,
		Errors:        []string{},
	}
}

func (r *TestRunner) buildTranscript(resp *execution.ExecutionResponse) []models.TranscriptEvent {
	entries := make([]models.TranscriptEvent, 0, len(resp.Events))
	for _, evt := range resp.Events {
		entries = append(entries, models.TranscriptEvent{SessionEvent: evt})
	}
	return entries
}

func (r *TestRunner) computeTestStats(runs []models.RunResult) *models.TestStats {
	if len(runs) == 0 {
		return nil
	}

	passed := 0
	totalScore := 0.0
	minScore := math.Inf(1)
	maxScore := math.Inf(-1)
	totalDuration := int64(0)
	scores := make([]float64, 0, len(runs))

	for _, run := range runs {
		score := run.ComputeRunScore()
		totalScore += score
		scores = append(scores, score)

		if score < minScore {
			minScore = score
		}
		if score > maxScore {
			maxScore = score
		}

		if run.AllValidationsPassed() {
			passed++
		}

		totalDuration += run.DurationMs
	}

	return &models.TestStats{
		PassRate:      float64(passed) / float64(len(runs)),
		AvgScore:      totalScore / float64(len(runs)),
		MinScore:      minScore,
		MaxScore:      maxScore,
		StdDevScore:   models.ComputeStdDev(scores),
		AvgDurationMs: totalDuration / int64(len(runs)),
	}
}

func (r *TestRunner) buildOutcome(testOutcomes []models.TestOutcome, startTime time.Time) *models.EvaluationOutcome {
	spec := r.cfg.Spec()

	// Compute digest
	succeeded := 0
	failed := 0
	errors := 0

	for _, to := range testOutcomes {
		switch to.Status {
		case models.StatusPassed:
			succeeded++
		case models.StatusFailed:
			failed++
		case models.StatusError:
			errors++
		}
	}

	totalTests := len(testOutcomes)
	successRate := 0.0
	if totalTests > 0 {
		successRate = float64(succeeded) / float64(totalTests)
	}

	// Compute aggregate score, min, max, and stddev across tests
	aggregateScore := r.computeAggregateScore(testOutcomes)
	digestMin, digestMax, digestStdDev := r.computeDigestScoreStats(testOutcomes)

	return &models.EvaluationOutcome{
		RunID:       fmt.Sprintf("run-%d", time.Now().Unix()),
		SkillTested: spec.SkillName,
		BenchName:   spec.Name,
		Timestamp:   startTime,
		Setup: models.OutcomeSetup{
			RunsPerTest: spec.Config.RunsPerTest,
			ModelID:     spec.Config.ModelID,
			EngineType:  spec.Config.EngineType,
			TimeoutSec:  spec.Config.TimeoutSec,
		},
		Digest: models.OutcomeDigest{
			TotalTests:     totalTests,
			Succeeded:      succeeded,
			Failed:         failed,
			Errors:         errors,
			Skipped:        0,
			SuccessRate:    successRate,
			AggregateScore: aggregateScore,
			MinScore:       digestMin,
			MaxScore:       digestMax,
			StdDev:         digestStdDev,
			DurationMs:     time.Since(startTime).Milliseconds(),
		},
		Measures:     make(map[string]models.MeasureResult),
		TestOutcomes: testOutcomes,
		Metadata:     make(map[string]any),
	}
}

// computeAggregateScore returns the mean of per-test average scores.
// Tests with nil Stats are treated as having an average score of 0.0.
func (r *TestRunner) computeAggregateScore(testOutcomes []models.TestOutcome) float64 {
	if len(testOutcomes) == 0 {
		return 0.0
	}

	totalScore := 0.0
	for _, to := range testOutcomes {
		if to.Stats != nil {
			totalScore += to.Stats.AvgScore
		}
	}

	return totalScore / float64(len(testOutcomes))
}

// computeDigestScoreStats returns min, max, and stddev of per-test average scores.
func (r *TestRunner) computeDigestScoreStats(testOutcomes []models.TestOutcome) (float64, float64, float64) {
	if len(testOutcomes) == 0 {
		return 0.0, 0.0, 0.0
	}

	scores := make([]float64, 0, len(testOutcomes))
	minScore := 1.0
	maxScore := 0.0

	for _, to := range testOutcomes {
		s := 0.0
		if to.Stats != nil {
			s = to.Stats.AvgScore
		}
		scores = append(scores, s)
		if s < minScore {
			minScore = s
		}
		if s > maxScore {
			maxScore = s
		}
	}

	return minScore, maxScore, models.ComputeStdDev(scores)
}
