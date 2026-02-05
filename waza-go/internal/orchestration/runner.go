package orchestration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	
	"github.com/spboyer/waza/waza-go/internal/config"
	"github.com/spboyer/waza/waza-go/internal/execution"
	"github.com/spboyer/waza/waza-go/internal/models"
	"github.com/spboyer/waza/waza-go/internal/scoring"
)

// TestRunner orchestrates the execution of tests
type TestRunner struct {
	cfg     *config.BenchmarkConfig
	engine  execution.AgentEngine
	verbose bool
	
	// Progress tracking
	progressMu sync.Mutex
	listeners  []ProgressListener
}

// ProgressListener receives progress updates
type ProgressListener func(event ProgressEvent)

// ProgressEvent represents a progress update
type ProgressEvent struct {
	EventType   string
	TestName    string
	TestNum     int
	TotalTests  int
	RunNum      int
	TotalRuns   int
	Status      string
	DurationMs  int64
	Details     map[string]any
}

// NewTestRunner creates a new test runner
func NewTestRunner(cfg *config.BenchmarkConfig, engine execution.AgentEngine) *TestRunner {
	return &TestRunner{
		cfg:       cfg,
		engine:    engine,
		verbose:   cfg.Verbose(),
		listeners: []ProgressListener{},
	}
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
	defer r.engine.Shutdown(ctx)
	
	// Load test cases
	testCases, err := r.loadTestCases()
	if err != nil {
		return nil, fmt.Errorf("failed to load test cases: %w", err)
	}
	
	if len(testCases) == 0 {
		return nil, fmt.Errorf("no test cases found")
	}
	
	r.notifyProgress(ProgressEvent{
		EventType:  "benchmark_start",
		TotalTests: len(testCases),
	})
	
	// Execute tests
	testOutcomes := make([]models.TestOutcome, 0, len(testCases))
	
	spec := r.cfg.Spec()
	if spec.RuntimeOptions.Concurrent {
		testOutcomes = r.runConcurrent(ctx, testCases)
	} else {
		testOutcomes = r.runSequential(ctx, testCases)
	}
	
	// Compute statistics
	outcome := r.buildOutcome(testOutcomes, startTime)
	
	r.notifyProgress(ProgressEvent{
		EventType: "benchmark_complete",
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
	for _, pattern := range spec.TestPatterns {
		fullPattern := filepath.Join(baseDir, pattern)
		matches, err := filepath.Glob(fullPattern)
		if err != nil {
			return nil, err
		}
		testFiles = append(testFiles, matches...)
	}
	
	if len(testFiles) == 0 {
		return nil, fmt.Errorf("no test files matched patterns: %v in directory: %s", spec.TestPatterns, baseDir)
	}
	
	var testCases []*models.TestCase
	for _, path := range testFiles {
		tc, err := models.LoadTestCase(path)
		if err != nil {
			return nil, fmt.Errorf("failed to load test case %s: %w", path, err)
		}
		if tc.Active {
			testCases = append(testCases, tc)
		}
	}
	
	return testCases, nil
}

func (r *TestRunner) runSequential(ctx context.Context, testCases []*models.TestCase) []models.TestOutcome {
	outcomes := make([]models.TestOutcome, 0, len(testCases))
	
	for i, tc := range testCases {
		r.notifyProgress(ProgressEvent{
			EventType:  "test_start",
			TestName:   tc.DisplayName,
			TestNum:    i + 1,
			TotalTests: len(testCases),
		})
		
		outcome := r.runTest(ctx, tc, i+1, len(testCases))
		outcomes = append(outcomes, outcome)
		
		r.notifyProgress(ProgressEvent{
			EventType:  "test_complete",
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
	workers := spec.RuntimeOptions.Workers
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
				EventType:  "test_start",
				TestName:   test.DisplayName,
				TestNum:    idx + 1,
				TotalTests: len(testCases),
			})
			
			outcome := r.runTest(ctx, test, idx+1, len(testCases))
			resultChan <- result{index: idx, outcome: outcome}
			
			r.notifyProgress(ProgressEvent{
				EventType:  "test_complete",
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
	runsPerTest := spec.RuntimeOptions.RunsPerTest
	
	runs := make([]models.RunResult, 0, runsPerTest)
	
	for runNum := 1; runNum <= runsPerTest; runNum++ {
		r.notifyProgress(ProgressEvent{
			EventType:  "run_start",
			TestName:   tc.DisplayName,
			TestNum:    testNum,
			TotalTests: totalTests,
			RunNum:     runNum,
			TotalRuns:  runsPerTest,
		})
		
		run := r.executeRun(ctx, tc, runNum)
		runs = append(runs, run)
		
		r.notifyProgress(ProgressEvent{
			EventType:  "run_complete",
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
	status := "passed"
	for _, run := range runs {
		if run.Status != "passed" {
			status = "failed"
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
	
	// Execute
	resp, err := r.engine.Execute(ctx, req)
	if err != nil {
		return models.RunResult{
			RunNumber:  runNum,
			Status:     "error",
			DurationMs: time.Since(startTime).Milliseconds(),
			ErrorMsg:   err.Error(),
		}
	}
	
	// Build validation context
	vCtx := r.buildValidationContext(tc, resp)
	
	// Run validators
	validations := r.runValidators(tc, vCtx)
	
	// Determine status
	status := "passed"
	if resp.ErrorMsg != "" {
		status = "error"
	} else {
		for _, v := range validations {
			if !v.Passed {
				status = "failed"
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
		Validations:   validations,
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
	timeout := spec.RuntimeOptions.TimeoutSec
	if tc.TimeoutSec != nil {
		timeout = *tc.TimeoutSec
	}
	
	return &execution.ExecutionRequest{
		TestID:     tc.TestID,
		Message:    tc.Stimulus.Message,
		Context:    tc.Stimulus.Metadata,
		Resources:  resources,
		SkillName:  spec.SkillName,
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
			// Load from file
			fullPath := filepath.Join(fixtureDir, ref.Location)
			content, err := os.ReadFile(fullPath)
			if err == nil {
				resources = append(resources, execution.ResourceFile{
					Path:    ref.Location,
					Content: string(content),
				})
			}
		}
	}
	
	return resources
}

func (r *TestRunner) buildValidationContext(tc *models.TestCase, resp *execution.ExecutionResponse) *scoring.ValidationContext {
	// Convert events to transcript entries
	var transcript []models.TranscriptEntry
	for _, evt := range resp.Events {
		entry := models.TranscriptEntry{
			Type: evt.EventType,
			Data: evt.Payload,
		}
		transcript = append(transcript, entry)
	}
	
	return &scoring.ValidationContext{
		TestCase:   tc,
		Transcript: transcript,
		Output:     resp.FinalOutput,
		Outcome:    make(map[string]any),
		DurationMs: resp.DurationMs,
		Metadata:   make(map[string]any),
	}
}

func (r *TestRunner) runValidators(tc *models.TestCase, ctx *scoring.ValidationContext) map[string]models.ValidationOut {
	validations := make(map[string]models.ValidationOut)
	
	// Run global validators
	spec := r.cfg.Spec()
	for _, vCfg := range spec.Validators {
		validator := scoring.CreateValidator(vCfg.Kind, vCfg.Identifier, vCfg.Parameters)
		result := validator.Validate(ctx)
		validations[result.Identifier] = *result
	}
	
	// Run test-specific validators
	for _, vCfg := range tc.Validators {
		kind := vCfg.Kind
		if kind == "" {
			kind = "code"
		}
		
		params := vCfg.Parameters
		if params == nil {
			params = make(map[string]any)
		}
		if len(vCfg.Checks) > 0 {
			params["assertions"] = vCfg.Checks
		}
		
		validator := scoring.CreateValidator(kind, vCfg.Identifier, params)
		result := validator.Validate(ctx)
		validations[result.Identifier] = *result
	}
	
	return validations
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

func (r *TestRunner) buildTranscript(resp *execution.ExecutionResponse) []models.TranscriptEntry {
	entries := make([]models.TranscriptEntry, 0, len(resp.Events))
	for _, evt := range resp.Events {
		entries = append(entries, models.TranscriptEntry{
			Type: evt.EventType,
			Data: evt.Payload,
		})
	}
	return entries
}

func (r *TestRunner) computeTestStats(runs []models.RunResult) *models.TestStats {
	if len(runs) == 0 {
		return nil
	}
	
	passed := 0
	totalScore := 0.0
	minScore := 1.0
	maxScore := 0.0
	totalDuration := int64(0)
	
	for _, run := range runs {
		score := run.ComputeRunScore()
		totalScore += score
		
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
		case "passed":
			succeeded++
		case "failed":
			failed++
		case "error":
			errors++
		}
	}
	
	totalTests := len(testOutcomes)
	successRate := 0.0
	if totalTests > 0 {
		successRate = float64(succeeded) / float64(totalTests)
	}
	
	// Compute aggregate score
	aggregateScore := r.computeAggregateScore(testOutcomes)
	
	return &models.EvaluationOutcome{
		RunID:       fmt.Sprintf("run-%d", time.Now().Unix()),
		SkillTested: spec.SkillName,
		BenchName:   spec.Identity.Name,
		Timestamp:   startTime,
		Setup: models.OutcomeSetup{
			RunsPerTest: spec.RuntimeOptions.RunsPerTest,
			ModelID:     spec.RuntimeOptions.ModelID,
			EngineType:  spec.RuntimeOptions.EngineType,
			TimeoutSec:  spec.RuntimeOptions.TimeoutSec,
		},
		Digest: models.OutcomeDigest{
			TotalTests:     totalTests,
			Succeeded:      succeeded,
			Failed:         failed,
			Errors:         errors,
			Skipped:        0,
			SuccessRate:    successRate,
			AggregateScore: aggregateScore,
			DurationMs:     time.Since(startTime).Milliseconds(),
		},
		Measures:     make(map[string]models.MeasureResult),
		TestOutcomes: testOutcomes,
		Metadata:     make(map[string]any),
	}
}

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
