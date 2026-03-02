package trigger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestDiscover(t *testing.T) {
	t.Run("finds trigger_tests.yaml", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("skill: test-skill\nshould_trigger_prompts:\n  - prompt: hello\n")
		if err := os.WriteFile(filepath.Join(dir, "trigger_tests.yaml"), content, 0644); err != nil {
			t.Fatal(err)
		}
		spec, err := Discover(dir)
		if err != nil {
			t.Fatal(err)
		}
		require.NotNil(t, spec, "expected spec, got nil")
		if spec.Skill != "test-skill" {
			t.Errorf("skill = %q, want %q", spec.Skill, "test-skill")
		}
		if len(spec.ShouldTriggerPrompts) != 1 {
			t.Errorf("should_trigger_prompts len = %d, want 1", len(spec.ShouldTriggerPrompts))
		}
	})

	t.Run("returns nil when no file exists", func(t *testing.T) {
		dir := t.TempDir()
		spec, err := Discover(dir)
		if err != nil {
			t.Fatal(err)
		}
		if spec != nil {
			t.Errorf("expected nil, got %+v", spec)
		}
	})

	t.Run("returns error for invalid YAML", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "trigger_tests.yaml"), []byte("not: valid: yaml: ["), 0644); err != nil {
			t.Fatal(err)
		}
		_, err := Discover(dir)
		if err == nil {
			t.Fatal("expected error for invalid YAML")
		}
	})

	t.Run("returns error when skill field missing", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("should_trigger_prompts:\n  - prompt: hello\n")
		if err := os.WriteFile(filepath.Join(dir, "trigger_tests.yaml"), content, 0644); err != nil {
			t.Fatal(err)
		}
		_, err := Discover(dir)
		if err == nil {
			t.Fatal("expected error for missing skill field")
		}
	})
}

func TestDiscoverExampleFixture(t *testing.T) {
	// Verify the example trigger_tests.yaml in the repo can be discovered and parsed.
	dir := filepath.Join("..", "..", "examples", "code-explainer")
	spec, err := Discover(dir)
	if err != nil {
		t.Fatalf("Discover(%q) error: %v", dir, err)
	}
	require.NotNil(t, spec, "examples/code-explainer/trigger_tests.yaml not found")
	if spec.Skill == "" {
		t.Error("spec.Skill is empty")
	}
	if len(spec.ShouldTriggerPrompts)+len(spec.ShouldNotTriggerPrompts) == 0 {
		t.Error("expected at least one prompt")
	}
	_ = spec // ensure it parses without error
}

func TestRunnerWithMockEngine(t *testing.T) {
	spec := &TestSpec{
		Skill: "mock-skill",
		ShouldTriggerPrompts: []TestPrompt{
			{Prompt: "hello"},
		},
		ShouldNotTriggerPrompts: []TestPrompt{
			{Prompt: "goodbye"},
		},
	}

	engine := &stubEngine{skill: "mock-skill"}
	cfg := config.NewBenchmarkConfig(&models.BenchmarkSpec{SkillName: "mock-skill"})
	r := NewRunner(spec, engine, cfg, nil)
	m, err := r.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// stubEngine always invokes the skill, so:
	// "hello" (should trigger, did trigger) → correct
	// "goodbye" (should NOT trigger, did trigger) → incorrect
	total := m.TP + m.FP + m.TN + m.FN
	if total != 2 {
		t.Errorf("Total = %d, want 2", total)
	}
	if m.TP != 1 {
		t.Errorf("TP = %d, want 1", m.TP)
	}
}

// stubEngine always returns a response with a SkillInvocation matching the given skill name.
type stubEngine struct {
	skill string
}

func (e *stubEngine) Initialize(_ context.Context) error     { return nil }
func (e *stubEngine) Shutdown(_ context.Context) error       { return nil }
func (e *stubEngine) SessionUsage(string) *models.UsageStats { return nil }

func (e *stubEngine) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return &execution.ExecutionResponse{
		FinalOutput: "stub response",
		SkillInvocations: []execution.SkillInvocation{
			{Name: e.skill},
		},
		Success: true,
	}, nil
}

func TestRunnerRunConfig(t *testing.T) {
	spec := &TestSpec{
		Skill: "my-skill",
		ShouldTriggerPrompts: []TestPrompt{
			{Prompt: "hello"},
		},
	}

	engine := &capturingEngine{}
	cfg := config.NewBenchmarkConfig(
		&models.BenchmarkSpec{
			SkillName: "my-skill",
			Config: models.Config{
				TimeoutSec: 120,
				SkillPaths: []string{"skills/a", "skills/b"},
			},
		},
		config.WithSpecDir("/base"),
	)
	r := NewRunner(spec, engine, cfg, nil)
	if _, err := r.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	require.NotNil(t, engine.lastReq, "expected a captured request")
	require.Equal(t, float64(120), engine.lastReq.Timeout.Seconds())
	if len(engine.lastReq.SkillPaths) != 2 {
		t.Errorf("SkillPaths = %v, want 2 entries", engine.lastReq.SkillPaths)
	}
}

type capturingEngine struct {
	lastReq *execution.ExecutionRequest
}

func (e *capturingEngine) Initialize(context.Context) error       { return nil }
func (e *capturingEngine) Shutdown(context.Context) error         { return nil }
func (e *capturingEngine) SessionUsage(string) *models.UsageStats { return nil }

func (e *capturingEngine) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.lastReq = req
	return &execution.ExecutionResponse{FinalOutput: "ok", Success: true}, nil
}

func TestRunnerNeverTriggers(t *testing.T) {
	spec := &TestSpec{
		Skill: "my-skill",
		ShouldTriggerPrompts: []TestPrompt{
			{Prompt: "hello"},
		},
		ShouldNotTriggerPrompts: []TestPrompt{
			{Prompt: "goodbye"},
		},
	}

	engine := &noTriggerEngine{}
	cfg := config.NewBenchmarkConfig(&models.BenchmarkSpec{SkillName: "my-skill"})
	r := NewRunner(spec, engine, cfg, nil)
	m, err := r.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// "hello" should trigger but didn't → FN
	// "goodbye" should not trigger and didn't → TN
	if m.FN != 1 {
		t.Errorf("FN = %d, want 1", m.FN)
	}
	if m.TN != 1 {
		t.Errorf("TN = %d, want 1", m.TN)
	}
	if m.TP != 0 {
		t.Errorf("TP = %d, want 0", m.TP)
	}
	if m.FP != 0 {
		t.Errorf("FP = %d, want 0", m.FP)
	}
}

func TestRunnerPartialErrors(t *testing.T) {
	spec := &TestSpec{
		Skill: "my-skill",
		ShouldTriggerPrompts: []TestPrompt{
			{Prompt: "good"},
			{Prompt: "bad"},
		},
	}

	engine := &errorOnPromptEngine{errorPrompt: "bad", skill: "my-skill"}
	cfg := config.NewBenchmarkConfig(&models.BenchmarkSpec{SkillName: "my-skill"})
	r := NewRunner(spec, engine, cfg, nil)
	m, err := r.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// "good" → TP, "bad" → error counted as incorrect (FN for should-trigger)
	total := m.TP + m.FP + m.TN + m.FN
	if total != 2 {
		t.Errorf("Total = %d, want 2", total)
	}
	if m.TP != 1 {
		t.Errorf("TP = %d, want 1", m.TP)
	}
	if m.FN != 1 {
		t.Errorf("FN = %d, want 1", m.FN)
	}
	if m.Errors != 1 {
		t.Errorf("Errors = %d, want 1", m.Errors)
	}
}

func TestRunnerAllErrors(t *testing.T) {
	spec := &TestSpec{
		Skill: "my-skill",
		ShouldTriggerPrompts: []TestPrompt{
			{Prompt: "bad"},
		},
	}

	engine := &errorOnPromptEngine{errorPrompt: "bad", skill: "my-skill"}
	cfg := config.NewBenchmarkConfig(&models.BenchmarkSpec{SkillName: "my-skill"})
	r := NewRunner(spec, engine, cfg, nil)
	m, err := r.Run(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// error is counted as incorrect (FN for should-trigger)
	if m.FN != 1 {
		t.Errorf("FN = %d, want 1", m.FN)
	}
	if m.Errors != 1 {
		t.Errorf("Errors = %d, want 1", m.Errors)
	}
}

type noTriggerEngine struct{}

func (e *noTriggerEngine) Initialize(context.Context) error       { return nil }
func (e *noTriggerEngine) Shutdown(context.Context) error         { return nil }
func (e *noTriggerEngine) SessionUsage(string) *models.UsageStats { return nil }

func (e *noTriggerEngine) Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return &execution.ExecutionResponse{
		FinalOutput: "no skill invoked",
		Success:     true,
	}, nil
}

type errorOnPromptEngine struct {
	errorPrompt string
	skill       string
}

func (e *errorOnPromptEngine) Initialize(context.Context) error       { return nil }
func (e *errorOnPromptEngine) Shutdown(context.Context) error         { return nil }
func (e *errorOnPromptEngine) SessionUsage(string) *models.UsageStats { return nil }

func (e *errorOnPromptEngine) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if req.Message == e.errorPrompt {
		return nil, fmt.Errorf("simulated error")
	}
	return &execution.ExecutionResponse{
		FinalOutput: "ok",
		SkillInvocations: []execution.SkillInvocation{
			{Name: e.skill},
		},
		Success: true,
	}, nil
}
