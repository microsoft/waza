package orchestration

import (
	"path/filepath"
	"testing"

	"github.com/spboyer/waza/internal/config"
	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildExecutionRequest_SkillPaths(t *testing.T) {
	tests := []struct {
		name           string
		specDir        string
		skillPaths     []string
		expectedPaths  []string
		description    string
	}{
		{
			name:           "no skill paths",
			specDir:        "/home/user/evals",
			skillPaths:     nil,
			expectedPaths:  []string{},
			description:    "empty skill paths should result in empty list",
		},
		{
			name:           "absolute paths",
			specDir:        "/home/user/evals",
			skillPaths:     []string{"/absolute/path/one", "/absolute/path/two"},
			expectedPaths:  []string{"/absolute/path/one", "/absolute/path/two"},
			description:    "absolute paths should be passed through unchanged",
		},
		{
			name:           "relative paths",
			specDir:        "/home/user/evals",
			skillPaths:     []string{"skills", "../shared-skills"},
			expectedPaths:  []string{"/home/user/evals/skills", "/home/user/shared-skills"},
			description:    "relative paths should be resolved relative to spec directory",
		},
		{
			name:           "mixed paths",
			specDir:        "/home/user/evals",
			skillPaths:     []string{"/absolute/skills", "relative/skills"},
			expectedPaths:  []string{"/absolute/skills", "/home/user/evals/relative/skills"},
			description:    "mixed absolute and relative paths should be handled correctly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a minimal spec
			spec := &models.BenchmarkSpec{
				SpecIdentity: models.SpecIdentity{
					Name: "test-benchmark",
				},
				SkillName: "test-skill",
				Config: models.Config{
					EngineType: "mock",
					ModelID:    "gpt-4",
					SkillPaths: tt.skillPaths,
					TimeoutSec: 60,
				},
			}

			// Create config
			cfg := config.NewBenchmarkConfig(
				spec,
				config.WithSpecDir(tt.specDir),
			)

			// Create a test case
			tc := &models.TestCase{
				TestID:      "test-001",
				DisplayName: "Test Case",
				Stimulus: models.TestStimulus{
					Message: "Test message",
				},
			}

			// Create runner (engine can be nil for this test)
			runner := NewTestRunner(cfg, nil)

			// Build execution request
			req := runner.buildExecutionRequest(tc)

			// Verify skill paths
			require.NotNil(t, req, "execution request should not be nil")
			assert.Equal(t, len(tt.expectedPaths), len(req.SkillPaths), tt.description)
			
			// Clean paths for comparison (handle different path separators)
			for i, expectedPath := range tt.expectedPaths {
				if i < len(req.SkillPaths) {
					expected := filepath.Clean(expectedPath)
					actual := filepath.Clean(req.SkillPaths[i])
					assert.Equal(t, expected, actual, "path at index %d: %s", i, tt.description)
				}
			}
		})
	}
}

func TestBuildExecutionRequest_BasicFields(t *testing.T) {
	// Create a spec
	spec := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{
			Name: "test-benchmark",
		},
		SkillName: "my-skill",
		Config: models.Config{
			EngineType: "mock",
			ModelID:    "gpt-4",
			TimeoutSec: 120,
		},
	}

	cfg := config.NewBenchmarkConfig(spec)

	// Create a test case
	tc := &models.TestCase{
		TestID:      "test-001",
		DisplayName: "Test Case",
		Stimulus: models.TestStimulus{
			Message: "Hello world",
			Metadata: map[string]any{
				"key": "value",
			},
		},
	}

	runner := NewTestRunner(cfg, nil)
	req := runner.buildExecutionRequest(tc)

	// Verify basic fields
	assert.Equal(t, "test-001", req.TestID)
	assert.Equal(t, "Hello world", req.Message)
	assert.Equal(t, "my-skill", req.SkillName)
	assert.Equal(t, 120, req.TimeoutSec)
	assert.Equal(t, "value", req.Context["key"])
}

func TestBuildExecutionRequest_TimeoutOverride(t *testing.T) {
	// Create a spec with default timeout
	spec := &models.BenchmarkSpec{
		SpecIdentity: models.SpecIdentity{
			Name: "test-benchmark",
		},
		SkillName: "my-skill",
		Config: models.Config{
			EngineType: "mock",
			ModelID:    "gpt-4",
			TimeoutSec: 120,
		},
	}

	cfg := config.NewBenchmarkConfig(spec)

	// Create a test case with custom timeout
	customTimeout := 300
	tc := &models.TestCase{
		TestID:      "test-001",
		DisplayName: "Test Case",
		Stimulus: models.TestStimulus{
			Message: "Hello world",
		},
		TimeoutSec: &customTimeout,
	}

	runner := NewTestRunner(cfg, nil)
	req := runner.buildExecutionRequest(tc)

	// Verify timeout is overridden
	assert.Equal(t, 300, req.TimeoutSec, "test case timeout should override spec timeout")
}
