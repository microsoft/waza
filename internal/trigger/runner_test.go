package trigger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
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

func TestEvalRunnerWithMockEngine(t *testing.T) {
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
	cfg := config.NewEvalConfig(&models.EvalSpec{SkillName: "mock-skill"})
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

func TestEvalRunnerRunConfig(t *testing.T) {
	injectSkillBody := false
	sandbox := &models.SandboxConfig{Enabled: true, AllowOutboundNetwork: true}
	specDir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(specDir, "SKILL.md"),
		[]byte("---\nname: my-skill\ndescription: test\n---\n"),
		0644,
	))
	spec := &TestSpec{
		Skill: "my-skill",
		ShouldTriggerPrompts: []TestPrompt{
			{Prompt: "hello"},
		},
	}

	engine := &capturingEngine{}
	cfg := config.NewEvalConfig(
		&models.EvalSpec{
			SkillName: "my-skill",
			Config: models.Config{
				TimeoutSec:          120,
				SkillPaths:          []string{"skills/a", "skills/b"},
				InjectSkillBody:     &injectSkillBody,
				TriggerSkillRouting: true,
				Sandbox:             sandbox,
			},
		},
		config.WithSpecDir(specDir),
	)
	r := NewRunner(spec, engine, cfg, nil)
	start := time.Now()
	if _, err := r.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	require.NotNil(t, engine.LastReq(), "expected a captured request")
	deadline, ok := engine.LastDeadline()
	require.True(t, ok, "expected trigger timeout as context deadline")
	require.WithinDuration(t, start.Add(120*time.Second), deadline, time.Second)
	require.Equal(t, []string{specDir, filepath.Join(specDir, "skills/a"), filepath.Join(specDir, "skills/b")}, engine.LastReq().SkillPaths)
	require.True(t, engine.LastReq().SuppressSkillBody)
	require.Same(t, sandbox, engine.LastReq().Sandbox)
	require.True(t, engine.LastReq().TriggerSkillRouting)
}

func TestEvalRunnerRunConfig_DisabledTargetDoesNotRoute(t *testing.T) {
	injectSkillBody := false
	specDir := t.TempDir()
	skillDir := filepath.Join(t.TempDir(), "custom-directory-name")
	otherDir := filepath.Join(t.TempDir(), "other")
	require.NoError(t, os.MkdirAll(skillDir, 0755))
	require.NoError(t, os.MkdirAll(otherDir, 0755))
	require.NoError(t, os.WriteFile(
		filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: my-skill\ndescription: test\n---\n"),
		0644,
	))
	spec := &TestSpec{
		Skill: "my-skill",
		ShouldTriggerPrompts: []TestPrompt{
			{Prompt: "hello"},
		},
	}

	engine := &capturingEngine{}
	cfg := config.NewEvalConfig(&models.EvalSpec{
		SkillName: "my-skill",
		Config: models.Config{
			TimeoutSec:          120,
			SkillPaths:          []string{skillDir, otherDir},
			InjectSkillBody:     &injectSkillBody,
			TriggerSkillRouting: true,
			DisabledSkills:      []string{skillDir},
		},
	}, config.WithSpecDir(specDir))
	r := NewRunner(spec, engine, cfg, nil)

	if _, err := r.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	require.NotNil(t, engine.LastReq(), "expected a captured request")
	require.False(t, engine.LastReq().TriggerSkillRouting)
	require.Len(t, engine.LastReq().SkillPaths, 1)
	require.Equal(t, otherDir, engine.LastReq().SkillPaths[0])
}

func TestEvalRunnerRunConfig_SandboxSpecDirFiltering(t *testing.T) {
	for _, tc := range []struct {
		name             string
		configureSpecDir bool
		disableSpecDir   bool
		wantSkillPaths   bool
		wantRouting      bool
	}{
		{
			name:             "explicit disabled spec dir is blocked",
			configureSpecDir: true,
			disableSpecDir:   true,
		},
		{
			name:           "implicit spec dir remains available",
			wantSkillPaths: true,
			wantRouting:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			injectSkillBody := false
			specDir := t.TempDir()
			require.NoError(t, os.WriteFile(
				filepath.Join(specDir, "SKILL.md"),
				[]byte("---\nname: my-skill\ndescription: test\n---\n"),
				0o644,
			))
			configModel := models.Config{
				InjectSkillBody:     &injectSkillBody,
				TriggerSkillRouting: true,
				Sandbox:             &models.SandboxConfig{Enabled: true},
			}
			if tc.configureSpecDir {
				configModel.SkillPaths = []string{specDir}
			}
			if tc.disableSpecDir {
				configModel.DisabledSkills = []string{filepath.Base(specDir)}
			}
			spec := &TestSpec{
				Skill:                "my-skill",
				ShouldTriggerPrompts: []TestPrompt{{Prompt: "hello"}},
			}
			engine := &capturingEngine{}
			cfg := config.NewEvalConfig(
				&models.EvalSpec{SkillName: "my-skill", Config: configModel},
				config.WithSpecDir(specDir),
			)

			_, err := NewRunner(spec, engine, cfg, nil).Run(t.Context())

			require.NoError(t, err)
			require.NotNil(t, engine.LastReq())
			if tc.wantSkillPaths {
				require.Equal(t, []string{specDir}, engine.LastReq().SkillPaths)
			} else {
				require.Empty(t, engine.LastReq().SkillPaths)
			}
			require.Equal(t, tc.wantRouting, engine.LastReq().TriggerSkillRouting)
		})
	}
}

type capturingEngine struct {
	mu           sync.Mutex
	lastReq      *execution.ExecutionRequest
	lastDeadline time.Time
	hasDeadline  bool
}

func (e *capturingEngine) Initialize(context.Context) error       { return nil }
func (e *capturingEngine) Shutdown(context.Context) error         { return nil }
func (e *capturingEngine) SessionUsage(string) *models.UsageStats { return nil }

func (e *capturingEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.mu.Lock()
	e.lastReq = req
	e.lastDeadline, e.hasDeadline = ctx.Deadline()
	e.mu.Unlock()
	return &execution.ExecutionResponse{FinalOutput: "ok", Success: true}, nil
}

func (e *capturingEngine) LastReq() *execution.ExecutionRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastReq
}

func (e *capturingEngine) LastDeadline() (time.Time, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastDeadline, e.hasDeadline
}

func TestEvalRunnerNeverTriggers(t *testing.T) {
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
	cfg := config.NewEvalConfig(&models.EvalSpec{SkillName: "my-skill"})
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

func TestEvalRunnerPartialErrors(t *testing.T) {
	spec := &TestSpec{
		Skill: "my-skill",
		ShouldTriggerPrompts: []TestPrompt{
			{Prompt: "good"},
			{Prompt: "bad"},
		},
	}

	engine := &errorOnPromptEngine{errorPrompt: "bad", skill: "my-skill"}
	cfg := config.NewEvalConfig(&models.EvalSpec{SkillName: "my-skill"})
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

func TestEvalRunnerAllErrors(t *testing.T) {
	spec := &TestSpec{
		Skill: "my-skill",
		ShouldTriggerPrompts: []TestPrompt{
			{Prompt: "bad"},
		},
	}

	engine := &errorOnPromptEngine{errorPrompt: "bad", skill: "my-skill"}
	cfg := config.NewEvalConfig(&models.EvalSpec{SkillName: "my-skill"})
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

func TestEvalRunnerPassesFixturesToExecutionRequest(t *testing.T) {
	// Create a fixture directory with files
	fixtureDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixtureDir, "main.go"), []byte("package main"), 0644))

	subDir := filepath.Join(fixtureDir, "sub")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "helper.go"), []byte("package sub"), 0644))

	spec := &TestSpec{
		Skill:                "my-skill",
		ShouldTriggerPrompts: []TestPrompt{{Prompt: "hello"}},
	}

	engine := &capturingEngine{}
	cfg := config.NewEvalConfig(
		&models.EvalSpec{SkillName: "my-skill"},
		config.WithFixtureDir(fixtureDir),
		config.WithSpecDir("/some/spec/dir"),
	)
	r := NewRunner(spec, engine, cfg, nil)

	_, err := r.Run(t.Context())
	require.NoError(t, err)
	require.NotNil(t, engine.LastReq())

	// Should have loaded 2 fixture files
	require.Len(t, engine.LastReq().Resources, 2, "expected 2 fixture files")

	// Check paths are relative
	paths := make(map[string]bool)
	for _, res := range engine.LastReq().Resources {
		paths[res.Path] = true
	}
	require.True(t, paths["main.go"], "expected main.go in resources")
	require.True(t, paths[filepath.Join("sub", "helper.go")], "expected sub/helper.go in resources")

	// Should have SourceDir set
	require.Equal(t, "/some/spec/dir", engine.LastReq().SourceDir)
}

func TestEvalRunnerSkipsHiddenAndVendorInFixtures(t *testing.T) {
	fixtureDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixtureDir, "visible.txt"), []byte("ok"), 0644))

	// Hidden dir should be skipped
	hidden := filepath.Join(fixtureDir, ".hidden")
	require.NoError(t, os.MkdirAll(hidden, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(hidden, "secret.txt"), []byte("skip"), 0644))

	// vendor dir should be skipped
	vendor := filepath.Join(fixtureDir, "vendor")
	require.NoError(t, os.MkdirAll(vendor, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(vendor, "dep.go"), []byte("skip"), 0644))

	resources, err := loadFixtureDir(fixtureDir)
	require.NoError(t, err)
	require.Len(t, resources, 1)
	require.Equal(t, "visible.txt", resources[0].Path)
}

func TestLoadFixtureDir_RejectsSymlinkEscapes(t *testing.T) {
	fixtureDir := t.TempDir()
	outsideDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fixtureDir, "visible.txt"), []byte("safe"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(outsideDir, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "nested", "secret.txt"), []byte("nested secret"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(fixtureDir, "secret.txt")))
	require.NoError(t, os.Symlink(filepath.Join(outsideDir, "nested"), filepath.Join(fixtureDir, "linked-dir")))

	resources, err := loadFixtureDir(fixtureDir)
	require.NoError(t, err)
	require.Equal(t, []execution.ResourceFile{{Path: "visible.txt", Content: []byte("safe")}}, resources)
}

func TestEvalRunnerPassesMCPServers(t *testing.T) {
	spec := &TestSpec{
		Skill:                "my-skill",
		ShouldTriggerPrompts: []TestPrompt{{Prompt: "hello"}},
	}

	engine := &capturingEngine{}
	cfg := config.NewEvalConfig(
		&models.EvalSpec{
			SkillName: "my-skill",
			Config: models.Config{
				ServerConfigs: map[string]any{
					"test-mcp": map[string]any{
						"type":    "stdio",
						"command": "echo",
					},
				},
			},
		},
	)
	r := NewRunner(spec, engine, cfg, nil)

	_, err := r.Run(t.Context())
	require.NoError(t, err)
	require.NotNil(t, engine.LastReq())
	require.Len(t, engine.LastReq().MCPServers, 1, "expected 1 MCP server")
	require.Contains(t, engine.LastReq().MCPServers, "test-mcp")
	stdio, ok := engine.LastReq().MCPServers["test-mcp"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Equal(t, []string{"*"}, stdio.Tools)
}

func TestLoadFixtureDir_EmptyDir(t *testing.T) {
	resources, err := loadFixtureDir("")
	require.NoError(t, err)
	require.Nil(t, resources)

	resources, err = loadFixtureDir("/nonexistent/path")
	require.NoError(t, err)
	require.Nil(t, resources)

	resources, err = loadFixtureDir(t.TempDir())
	require.NoError(t, err)
	require.Nil(t, resources)
}

func TestConvertMCPServers_SkipsNonMapEntries(t *testing.T) {
	result := convertMCPServers(map[string]any{
		"good":  map[string]any{"type": "stdio"},
		"bad":   "not-a-map",
		"good2": map[string]any{"type": "sse"},
	}, nil, "")
	require.Len(t, result, 2)
	require.Contains(t, result, "good")
	require.Contains(t, result, "good2")
}

func TestEvalRunnerSetsCancelOnSkillInvocation(t *testing.T) {
	spec := &TestSpec{
		Skill:                   "my-skill",
		ShouldTriggerPrompts:    []TestPrompt{{Prompt: "trigger me"}},
		ShouldNotTriggerPrompts: []TestPrompt{{Prompt: "don't trigger"}},
	}

	engine := &capturingEngine{}
	cfg := config.NewEvalConfig(&models.EvalSpec{
		SkillName: "my-skill",
		Config:    models.Config{TimeoutSec: 10},
	})

	r := NewRunner(spec, engine, cfg, nil)
	_, err := r.Run(t.Context())
	require.NoError(t, err)

	require.NotNil(t, engine.LastReq(), "engine should have received a request")
	require.True(t, engine.LastReq().CancelOnSkillInvocation,
		"trigger runner must set CancelOnSkillInvocation=true")
}
