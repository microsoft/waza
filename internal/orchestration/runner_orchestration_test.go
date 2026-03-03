package orchestration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTaskFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestRunBenchmark_SequentialOrchestrationAndStats(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")
	fixtureDir := filepath.Join(tmpDir, "fixtures")
	require.NoError(t, os.MkdirAll(tasksDir, 0o755))
	require.NoError(t, os.MkdirAll(fixtureDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fixtureDir, "resource-a.txt"), []byte("A"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(fixtureDir, "resource-b.txt"), []byte("B"), 0o644))

	writeTaskFile(t, filepath.Join(tasksDir, "task-a.yaml"), `id: task-a
name: Task A
inputs:
  prompt: "Inspect task a"
  files:
    - path: "resource-a.txt"
    - path: "inline-a.txt"
      content: "inline-a"
graders:
  - name: task-a-regex
    type: text
    config:
      regex_match:
        - "Mock response"
`)
	writeTaskFile(t, filepath.Join(tasksDir, "task-b.yaml"), `id: task-b
name: Task B
inputs:
  prompt: "Inspect task b"
  files:
    - path: "resource-b.txt"
graders:
  - name: task-b-regex
    type: text
    weight: 0.25
    config:
      regex_match:
        - "Mock response"
`)

	spec := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "orchestration-sequential"},
		SkillName:    "test-skill",
		Config: models.Config{
			RunsPerTest: 2,
			TimeoutSec:  30,
			EngineType:  "mock",
			ModelID:     "mock-model",
			GroupBy:     "model",
		},
		Graders: []models.GraderConfig{
			{
				Kind:       models.GraderKindText,
				Identifier: "global-regex",
				Weight:     2.5,
				Parameters: map[string]any{
					"regex_match": []string{"Mock response"},
				},
			},
		},
		Tasks: []string{"tasks/*.yaml"},
	}

	cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir), config.WithFixtureDir(fixtureDir))
	runner := NewTestRunner(cfg, execution.NewMockEngine("mock-model"))

	var events []ProgressEvent
	runner.OnProgress(func(event ProgressEvent) {
		events = append(events, event)
	})

	outcome, err := runner.RunBenchmark(context.Background())
	require.NoError(t, err)
	require.NotNil(t, outcome)
	require.Len(t, outcome.TestOutcomes, 2)
	require.NotNil(t, outcome.Digest.Statistics)
	assert.Equal(t, 2, outcome.Digest.TotalTests)
	assert.Equal(t, 2, outcome.Digest.Succeeded)
	assert.Equal(t, 0, outcome.Digest.Failed)
	assert.Equal(t, 0, outcome.Digest.Errors)

	for _, testOutcome := range outcome.TestOutcomes {
		assert.Equal(t, models.StatusPassed, testOutcome.Status)
		assert.Equal(t, "mock-model", testOutcome.Group)
		require.NotNil(t, testOutcome.Stats)
		require.NotNil(t, testOutcome.Stats.BootstrapCI)
		require.NotNil(t, testOutcome.Stats.IsSignificant)
		require.Len(t, testOutcome.Runs, 2)

		for _, run := range testOutcome.Runs {
			assert.Equal(t, models.StatusPassed, run.Status)
			assert.Contains(t, run.Validations, "global-regex")
			assert.Equal(t, 2.5, run.Validations["global-regex"].Weight)
			assert.Nil(t, run.SessionDigest.Usage)
			assert.Equal(t, 0, run.SessionDigest.ToolCallCount)
			assert.Empty(t, run.SessionDigest.ToolsUsed)
			assert.Empty(t, run.SessionDigest.Errors)
			assert.Empty(t, run.Transcript)
		}

		switch testOutcome.TestID {
		case "task-a":
			assert.Equal(t, 1.0, testOutcome.Runs[0].Validations["task-a-regex"].Weight)
		case "task-b":
			assert.Equal(t, 0.25, testOutcome.Runs[0].Validations["task-b-regex"].Weight)
		default:
			t.Fatalf("unexpected test id: %s", testOutcome.TestID)
		}
	}

	eventTypes := make(map[EventType]int)
	for _, event := range events {
		eventTypes[event.EventType]++
	}
	assert.GreaterOrEqual(t, eventTypes[EventBenchmarkStart], 1)
	assert.GreaterOrEqual(t, eventTypes[EventBenchmarkComplete], 1)
	assert.GreaterOrEqual(t, eventTypes[EventTestStart], 2)
	assert.GreaterOrEqual(t, eventTypes[EventRunStart], 4)
	assert.GreaterOrEqual(t, eventTypes[EventRunComplete], 4)
	assert.GreaterOrEqual(t, eventTypes[EventTestComplete], 2)
}

func TestRunBenchmark_ConcurrentResultCollectionOrder(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0o755))

	writeTaskFile(t, filepath.Join(tasksDir, "a-first.yaml"), `id: a-first
name: A First
inputs:
  prompt: "first"
`)
	writeTaskFile(t, filepath.Join(tasksDir, "b-second.yaml"), `id: b-second
name: B Second
inputs:
  prompt: "second"
`)

	spec := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{Name: "orchestration-concurrent"},
		SkillName:    "test-skill",
		Config: models.Config{
			RunsPerTest: 1,
			TimeoutSec:  30,
			EngineType:  "mock",
			ModelID:     "mock-model",
			Concurrent:  true,
			Workers:     2,
		},
		Graders: []models.GraderConfig{
			{
				Kind:       models.GraderKindText,
				Identifier: "global-regex",
				Parameters: map[string]any{
					"regex_match": []string{"Mock response"},
				},
			},
		},
		Tasks: []string{"tasks/*.yaml"},
	}

	cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
	runner := NewTestRunner(cfg, execution.NewMockEngine("mock-model"))

	outcome, err := runner.RunBenchmark(context.Background())
	require.NoError(t, err)
	require.Len(t, outcome.TestOutcomes, 2)
	assert.Equal(t, "a-first", outcome.TestOutcomes[0].TestID)
	assert.Equal(t, "b-second", outcome.TestOutcomes[1].TestID)
}

func TestRunGraders_WeightsAndErrors(t *testing.T) {
	spec := &models.BenchmarkSpec{
		Config: models.Config{ModelID: "mock-model"},
		Graders: []models.GraderConfig{
			{
				Kind:       models.GraderKindText,
				Identifier: "global",
				Weight:     3.0,
				Parameters: map[string]any{"regex_match": []string{"Mock"}},
			},
		},
	}
	runner := NewTestRunner(config.NewBenchmarkConfig(spec), nil)
	graderCtx := &graders.Context{Output: "Mock response"}

	testCase := &models.TestCase{
		Validators: []models.ValidatorInline{
			{
				Identifier: "task-default-weight",
				Kind:       models.GraderKindText,
				Parameters: map[string]any{"regex_match": []string{"response"}},
			},
			{
				Identifier: "task-explicit-weight",
				Kind:       models.GraderKindText,
				Weight:     0.5,
				Parameters: map[string]any{"regex_match": []string{"Mock"}},
			},
		},
	}

	results, err := runner.runGraders(context.Background(), testCase, graderCtx)
	require.NoError(t, err)
	assert.Equal(t, 3.0, results["global"].Weight)
	assert.Equal(t, 1.0, results["task-default-weight"].Weight)
	assert.Equal(t, 0.5, results["task-explicit-weight"].Weight)

	_, err = runner.runGraders(context.Background(), &models.TestCase{
		Validators: []models.ValidatorInline{{Identifier: "missing-kind"}},
	}, graderCtx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no kind associated with grader missing-kind")
}

func TestLoadResources_PathValidation(t *testing.T) {
	fixtureDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixtureDir, "ok.txt"), []byte("ok"), 0o644))

	spec := &models.BenchmarkSpec{}
	cfg := config.NewBenchmarkConfig(spec, config.WithFixtureDir(fixtureDir))
	runner := NewTestRunner(cfg, nil)

	testCase := &models.TestCase{
		Stimulus: models.TestStimulus{
			Resources: []models.ResourceRef{
				{Location: "inline.txt", Body: "inline"},
				{Location: "ok.txt"},
				{Location: filepath.Join(fixtureDir, "absolute.txt")},
				{Location: "../escape.txt"},
				{Location: "missing.txt"},
			},
		},
	}

	resources := runner.loadResources(testCase)
	require.Len(t, resources, 2)
	assert.Equal(t, "inline.txt", resources[0].Path)
	assert.Equal(t, "inline", resources[0].Content)
	assert.Equal(t, "ok.txt", resources[1].Path)
	assert.Equal(t, "ok", resources[1].Content)
}

func TestBuildGraderContextAndScoreHelpers(t *testing.T) {
	spec := &models.BenchmarkSpec{Config: models.Config{RunsPerTest: 2}}
	runner := NewTestRunner(config.NewBenchmarkConfig(spec), nil)

	content := "hi"
	resp := &execution.ExecutionResponse{
		FinalOutput:  "final output",
		DurationMs:   42,
		WorkspaceDir: "/tmp/workspace",
		SessionID:    "session-1",
		SkillInvocations: []execution.SkillInvocation{
			{Name: "azure-prepare", Path: "/skills/azure-prepare/SKILL.md"},
		},
		ToolCalls: []models.ToolCall{{Name: "bash"}, {Name: "view"}},
		Usage:     &models.UsageStats{Turns: 1},
		Events: []copilot.SessionEvent{
			{Type: copilot.UserMessage, Data: copilot.Data{Content: &content}},
		},
	}

	graderCtx := runner.buildGraderContext(&models.TestCase{TestID: "tc"}, resp)
	require.Len(t, graderCtx.Transcript, 1)
	assert.Equal(t, "final output", graderCtx.Output)
	assert.Equal(t, int64(42), graderCtx.DurationMS)
	assert.Equal(t, "/tmp/workspace", graderCtx.WorkspaceDir)
	assert.Equal(t, "session-1", graderCtx.SessionID)
	require.Len(t, graderCtx.SkillInvocations, 1)
	assert.Equal(t, "azure-prepare", graderCtx.SkillInvocations[0].Name)

	digest := runner.buildSessionDigest(resp)
	require.NotNil(t, digest.Usage)
	assert.Equal(t, 1, digest.Usage.Turns)
	assert.Equal(t, 2, digest.ToolCallCount)
	assert.Equal(t, []string{"bash", "view"}, digest.ToolsUsed)

	transcript := runner.buildTranscript(resp)
	require.Len(t, transcript, 1)
	assert.Equal(t, copilot.UserMessage, transcript[0].Type)

	assert.Nil(t, runner.computeTestStats(nil))
	assert.Equal(t, 0.0, runner.computeAggregateScore(nil))
	assert.Equal(t, 0.0, runner.computeWeightedAggregateScore(nil))
	minScore, maxScore, stdDev := runner.computeDigestScoreStats(nil)
	assert.Equal(t, 0.0, minScore)
	assert.Equal(t, 0.0, maxScore)
	assert.Equal(t, 0.0, stdDev)
}

func TestRunTest_CacheHitAndTranscriptWrite(t *testing.T) {
	spec := &models.BenchmarkSpec{
		SkillName: "cache-skill",
		Config: models.Config{
			RunsPerTest: 1,
			TimeoutSec:  30,
			EngineType:  "mock",
			ModelID:     "mock-model",
		},
		Graders: []models.GraderConfig{
			{
				Kind:       models.GraderKindText,
				Identifier: "global-regex",
				Parameters: map[string]any{"regex_match": []string{"Mock response"}},
			},
		},
	}

	transcriptDir := t.TempDir()
	cacheDir := t.TempDir()
	cfg := config.NewBenchmarkConfig(
		spec,
		config.WithTranscriptDir(transcriptDir),
	)
	runner := NewTestRunner(cfg, execution.NewMockEngine("mock-model"), WithCache(cache.New(cacheDir)))

	testCase := &models.TestCase{
		TestID:      "cache-task",
		DisplayName: "Cache Task",
		Stimulus: models.TestStimulus{
			Message: "cache me",
		},
	}

	outcome, wasCached := runner.runTest(context.Background(), testCase, 1, 1)
	assert.False(t, wasCached)
	runner.writeTaskTranscript(testCase, outcome, time.Now())

	entries, err := os.ReadDir(transcriptDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	cachedOutcome, wasCached := runner.runTest(context.Background(), testCase, 1, 1)
	assert.True(t, wasCached)
	assert.Equal(t, outcome.TestID, cachedOutcome.TestID)
	assert.Equal(t, outcome.Status, cachedOutcome.Status)
}
