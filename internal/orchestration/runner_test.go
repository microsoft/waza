package orchestration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spboyer/waza/internal/config"
	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildExecutionRequest_SkillPaths(t *testing.T) {
	root := t.TempDir()
	specDir := filepath.Join(root, "home", "user", "evals")
	abs1 := filepath.Join(root, "absolute", "path", "one")
	abs2 := filepath.Join(root, "absolute", "path", "two")
	absSkills := filepath.Join(root, "absolute", "skills")

	tests := []struct {
		name          string
		specDir       string
		skillPaths    []string
		expectedPaths []string
		description   string
	}{
		{
			name:          "no skill paths",
			specDir:       specDir,
			skillPaths:    nil,
			expectedPaths: []string{},
			description:   "empty skill paths should result in empty list",
		},
		{
			name:          "absolute paths",
			specDir:       specDir,
			skillPaths:    []string{abs1, abs2},
			expectedPaths: []string{abs1, abs2},
			description:   "absolute paths should be passed through unchanged",
		},
		{
			name:          "relative paths",
			specDir:       specDir,
			skillPaths:    []string{"skills", "../shared-skills"},
			expectedPaths: []string{filepath.Join(specDir, "skills"), filepath.Join(root, "home", "user", "shared-skills")},
			description:   "relative paths should be resolved relative to spec directory",
		},
		{
			name:          "mixed paths",
			specDir:       specDir,
			skillPaths:    []string{absSkills, "relative/skills"},
			expectedPaths: []string{absSkills, filepath.Join(specDir, "relative", "skills")},
			description:   "mixed absolute and relative paths should be handled correctly",
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
	assert.Equal(t, "Hello world", req.Message)
	assert.Equal(t, "my-skill", req.SkillName)
	assert.Equal(t, float64(120), req.Timeout.Seconds())
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
	assert.Equal(t, float64(300), req.Timeout.Seconds(), "test case timeout should override spec timeout")
}

func TestValidateRequiredSkills_Integration(t *testing.T) {
	// Create temporary directories for testing
	tmpDir := t.TempDir()

	// Create skill directories
	skill1Dir := filepath.Join(tmpDir, "skill1")
	skill2Dir := filepath.Join(tmpDir, "skill2")
	skill3Dir := filepath.Join(tmpDir, "skill3")
	require.NoError(t, os.MkdirAll(skill1Dir, 0755))
	require.NoError(t, os.MkdirAll(skill2Dir, 0755))
	require.NoError(t, os.MkdirAll(skill3Dir, 0755))

	// Write SKILL.md files
	skill1Content := `---
name: azure-deploy
description: Deploy to Azure
---
`
	skill2Content := `---
name: azure-prepare
description: Prepare for Azure
---
`
	skill3Content := `---
name: azure-validate
description: Validate Azure config
---
`
	require.NoError(t, os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte(skill1Content), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(skill2Dir, "SKILL.md"), []byte(skill2Content), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(skill3Dir, "SKILL.md"), []byte(skill3Content), 0644))

	t.Run("all required skills found", func(t *testing.T) {
		spec := &models.BenchmarkSpec{
			SpecIdentity: models.SpecIdentity{
				Name: "test-benchmark",
			},
			SkillName: "azure-deploy",
			Config: models.Config{
				EngineType:     "mock",
				ModelID:        "gpt-4",
				TimeoutSec:     60,
				RunsPerTest:    1,
				SkillPaths:     []string{skill1Dir, skill2Dir, skill3Dir},
				RequiredSkills: []string{"azure-deploy", "azure-prepare", "azure-validate"},
			},
		}

		cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
		runner := NewTestRunner(cfg, nil)

		err := runner.validateRequiredSkills()
		assert.NoError(t, err)
	})

	t.Run("some required skills missing", func(t *testing.T) {
		spec := &models.BenchmarkSpec{
			SpecIdentity: models.SpecIdentity{
				Name: "test-benchmark",
			},
			SkillName: "azure-deploy",
			Config: models.Config{
				EngineType:     "mock",
				ModelID:        "gpt-4",
				TimeoutSec:     60,
				RunsPerTest:    1,
				SkillPaths:     []string{skill1Dir}, // Only has azure-deploy
				RequiredSkills: []string{"azure-deploy", "azure-prepare", "azure-validate"},
			},
		}

		cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
		runner := NewTestRunner(cfg, nil)

		err := runner.validateRequiredSkills()
		require.Error(t, err)
		errMsg := err.Error()
		assert.Contains(t, errMsg, "skill validation failed")
		assert.Contains(t, errMsg, "azure-prepare")
		assert.Contains(t, errMsg, "azure-validate")
	})

	t.Run("empty required_skills list skips validation", func(t *testing.T) {
		spec := &models.BenchmarkSpec{
			SpecIdentity: models.SpecIdentity{
				Name: "test-benchmark",
			},
			SkillName: "azure-deploy",
			Config: models.Config{
				EngineType:     "mock",
				ModelID:        "gpt-4",
				TimeoutSec:     60,
				RunsPerTest:    1,
				SkillPaths:     []string{skill1Dir},
				RequiredSkills: []string{}, // Empty list
			},
		}

		cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
		runner := NewTestRunner(cfg, nil)

		err := runner.validateRequiredSkills()
		assert.NoError(t, err)
	})

	t.Run("nil required_skills skips validation", func(t *testing.T) {
		spec := &models.BenchmarkSpec{
			SpecIdentity: models.SpecIdentity{
				Name: "test-benchmark",
			},
			SkillName: "azure-deploy",
			Config: models.Config{
				EngineType:  "mock",
				ModelID:     "gpt-4",
				TimeoutSec:  60,
				RunsPerTest: 1,
				SkillPaths:  []string{skill1Dir},
				// RequiredSkills not set (nil)
			},
		}

		cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
		runner := NewTestRunner(cfg, nil)

		err := runner.validateRequiredSkills()
		assert.NoError(t, err)
	})

	t.Run("empty skill_directories with required_skills returns error", func(t *testing.T) {
		spec := &models.BenchmarkSpec{
			SpecIdentity: models.SpecIdentity{
				Name: "test-benchmark",
			},
			SkillName: "azure-deploy",
			Config: models.Config{
				EngineType:     "mock",
				ModelID:        "gpt-4",
				TimeoutSec:     60,
				RunsPerTest:    1,
				SkillPaths:     []string{}, // Empty
				RequiredSkills: []string{"azure-deploy"},
			},
		}

		cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
		runner := NewTestRunner(cfg, nil)

		err := runner.validateRequiredSkills()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "required_skills specified but no skill_directories configured")
	})

	t.Run("relative skill paths are resolved correctly", func(t *testing.T) {
		spec := &models.BenchmarkSpec{
			SpecIdentity: models.SpecIdentity{
				Name: "test-benchmark",
			},
			SkillName: "azure-deploy",
			Config: models.Config{
				EngineType:     "mock",
				ModelID:        "gpt-4",
				TimeoutSec:     60,
				RunsPerTest:    1,
				SkillPaths:     []string{"skill1", "skill2"}, // Relative paths
				RequiredSkills: []string{"azure-deploy", "azure-prepare"},
			},
		}

		cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
		runner := NewTestRunner(cfg, nil)

		err := runner.validateRequiredSkills()
		assert.NoError(t, err)
	})
}

func TestComputeGroupStats_MixedGroups(t *testing.T) {
	outcomes := []models.TestOutcome{
		{TestID: "t1", Group: "gpt-4o", Status: models.StatusPassed, Stats: &models.TestStats{AvgScore: 0.9}},
		{TestID: "t2", Group: "gpt-4o", Status: models.StatusFailed, Stats: &models.TestStats{AvgScore: 0.4}},
		{TestID: "t3", Group: "claude", Status: models.StatusPassed, Stats: &models.TestStats{AvgScore: 1.0}},
	}

	stats := computeGroupStats(outcomes)
	require.Len(t, stats, 2)

	assert.Equal(t, "gpt-4o", stats[0].Name)
	assert.Equal(t, 1, stats[0].Passed)
	assert.Equal(t, 2, stats[0].Total)
	assert.InDelta(t, 0.65, stats[0].AvgScore, 0.001)

	assert.Equal(t, "claude", stats[1].Name)
	assert.Equal(t, 1, stats[1].Passed)
	assert.Equal(t, 1, stats[1].Total)
	assert.InDelta(t, 1.0, stats[1].AvgScore, 0.001)
}

func TestComputeGroupStats_SingleGroup(t *testing.T) {
	outcomes := []models.TestOutcome{
		{TestID: "t1", Group: "alpha", Status: models.StatusPassed},
		{TestID: "t2", Group: "alpha", Status: models.StatusPassed},
	}

	stats := computeGroupStats(outcomes)
	require.Len(t, stats, 1)
	assert.Equal(t, "alpha", stats[0].Name)
	assert.Equal(t, 2, stats[0].Passed)
	assert.Equal(t, 2, stats[0].Total)
	assert.Equal(t, 0.0, stats[0].AvgScore)
}

func TestComputeGroupStats_EmptyOutcomes(t *testing.T) {
	stats := computeGroupStats(nil)
	assert.Nil(t, stats)

	stats = computeGroupStats([]models.TestOutcome{})
	assert.Nil(t, stats)
}

func TestComputeGroupStats_NoGroupSet(t *testing.T) {
	outcomes := []models.TestOutcome{
		{TestID: "t1", Group: "", Status: models.StatusPassed},
	}
	stats := computeGroupStats(outcomes)
	assert.Nil(t, stats)
}

func TestResolveGroup_Model(t *testing.T) {
	spec := &models.BenchmarkSpec{
		Config: models.Config{
			ModelID: "gpt-4o",
			GroupBy: "model",
		},
	}
	cfg := config.NewBenchmarkConfig(spec)
	runner := NewTestRunner(cfg, nil)

	assert.Equal(t, "gpt-4o", runner.resolveGroup())
}

func TestResolveGroup_Empty(t *testing.T) {
	spec := &models.BenchmarkSpec{
		Config: models.Config{
			ModelID: "gpt-4o",
			GroupBy: "",
		},
	}
	cfg := config.NewBenchmarkConfig(spec)
	runner := NewTestRunner(cfg, nil)

	assert.Equal(t, "", runner.resolveGroup())
}

func TestResolveGroup_Unknown(t *testing.T) {
	spec := &models.BenchmarkSpec{
		Config: models.Config{
			ModelID: "gpt-4o",
			GroupBy: "region",
		},
	}
	cfg := config.NewBenchmarkConfig(spec)
	runner := NewTestRunner(cfg, nil)

	assert.Equal(t, "", runner.resolveGroup())
}
