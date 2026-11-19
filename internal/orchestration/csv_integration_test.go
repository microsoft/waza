package orchestration

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCSV(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func TestLoadTestCasesFromCSV_BasicLoading(t *testing.T) {
	tmpDir := t.TempDir()
	writeCSV(t, tmpDir, "data.csv", "id,prompt\nA,hello\nB,world\nC,foo\n")

	spec := &models.EvalSpec{
		TasksFrom: "data.csv",
		Config:    models.Config{ModelID: "test-model"},
	}
	cfg := config.NewEvalConfig(spec, config.WithSpecDir(tmpDir))
	runner := NewEvalRunner(cfg, nil)

	cases, err := runner.loadTestCasesFromCSV()
	require.NoError(t, err)
	assert.Len(t, cases, 3)
	assert.Equal(t, "A", cases[0].TestID)
	assert.Equal(t, "hello", cases[0].Stimulus.Message)
	assert.Equal(t, "B", cases[1].TestID)
	assert.Equal(t, "world", cases[1].Stimulus.Message)
}

func TestLoadTestCasesFromCSV_TemplateResolution(t *testing.T) {
	tmpDir := t.TempDir()
	writeCSV(t, tmpDir, "data.csv", "id,lang,prompt\n1,Go,Explain {{.Vars.lang}}\n2,Rust,Explain {{.Vars.lang}}\n")

	spec := &models.EvalSpec{
		TasksFrom: "data.csv",
		Config:    models.Config{ModelID: "test-model"},
	}
	cfg := config.NewEvalConfig(spec, config.WithSpecDir(tmpDir))
	runner := NewEvalRunner(cfg, nil)

	cases, err := runner.loadTestCasesFromCSV()
	require.NoError(t, err)
	assert.Len(t, cases, 2)
	assert.Equal(t, "Explain Go", cases[0].Stimulus.Message)
	assert.Equal(t, "Explain Rust", cases[1].Stimulus.Message)
}

func TestRunBenchmarkCSVJobIDMatchesEachModelEvaluationID(t *testing.T) {
	tmpDir := t.TempDir()
	writeCSV(t, tmpDir, "data.csv", "id,prompt\nA,evaluation {{.JobID}}\n")
	now := time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC)

	var runIDs []string
	for _, modelID := range []string{"model-a", "model-b"} {
		spec := &models.EvalSpec{
			TasksFrom: "data.csv",
			Config: models.Config{
				EngineType:     "mock",
				ModelID:        modelID,
				TrialsPerTask:  1,
				TimeoutSec:     30,
				DisabledSkills: []string{"*"},
			},
		}
		engine := &trackingEngine{}
		runner := NewEvalRunner(
			config.NewEvalConfig(spec, config.WithSpecDir(tmpDir)),
			engine,
			WithSkipGraders(),
			WithClock(func() time.Time { return now }),
		)

		outcome, err := runner.RunBenchmark(t.Context())
		require.NoError(t, err)
		require.NotEmpty(t, outcome.RunID)
		require.Len(t, engine.calls, 1)
		assert.Equal(t, "evaluation "+outcome.RunID, engine.calls[0].Message)
		runIDs = append(runIDs, outcome.RunID)
	}

	assert.NotEqual(t, runIDs[0], runIDs[1], "each model evaluation must receive a distinct ID")
}

func TestLoadTestCasesFromCSV_RangeFiltering(t *testing.T) {
	tmpDir := t.TempDir()
	writeCSV(t, tmpDir, "data.csv", "id,prompt\nA,one\nB,two\nC,three\nD,four\nE,five\n")

	spec := &models.EvalSpec{
		TasksFrom: "data.csv",
		Range:     [2]int{2, 4},
		Config:    models.Config{ModelID: "test-model"},
	}
	cfg := config.NewEvalConfig(spec, config.WithSpecDir(tmpDir))
	runner := NewEvalRunner(cfg, nil)

	cases, err := runner.loadTestCasesFromCSV()
	require.NoError(t, err)
	assert.Len(t, cases, 3)
	assert.Equal(t, "B", cases[0].TestID)
	assert.Equal(t, "D", cases[2].TestID)
}

func TestLoadTestCasesFromCSV_InvalidRange(t *testing.T) {
	tmpDir := t.TempDir()
	writeCSV(t, tmpDir, "data.csv", "id,prompt\nA,one\n")

	tests := []struct {
		name  string
		rng   [2]int
		errAt string
	}{
		{"zero start", [2]int{0, 3}, "must be > 0"},
		{"negative end", [2]int{1, -1}, "must be > 0"},
		{"start > end", [2]int{5, 2}, "must be <= end"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &models.EvalSpec{
				TasksFrom: "data.csv",
				Range:     tt.rng,
				Config:    models.Config{ModelID: "test-model"},
			}
			cfg := config.NewEvalConfig(spec, config.WithSpecDir(tmpDir))
			runner := NewEvalRunner(cfg, nil)

			_, err := runner.loadTestCasesFromCSV()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errAt)
		})
	}
}

func TestLoadTestCasesFromCSV_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a CSV outside the spec directory
	parentDir := filepath.Dir(tmpDir)
	writeCSV(t, parentDir, "escape.csv", "id,prompt\nA,bad\n")

	spec := &models.EvalSpec{
		TasksFrom: "../escape.csv",
		Config:    models.Config{ModelID: "test-model"},
	}
	cfg := config.NewEvalConfig(spec, config.WithSpecDir(tmpDir))
	runner := NewEvalRunner(cfg, nil)

	_, err := runner.loadTestCasesFromCSV()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes spec directory")
}
