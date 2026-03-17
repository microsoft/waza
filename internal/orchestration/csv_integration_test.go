package orchestration

import (
	"os"
	"path/filepath"
	"testing"

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
		Config:    models.EvalConfig{ModelID: "test-model"},
	}
	cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
	runner := NewTestRunner(cfg, nil)

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
		Config:    models.EvalConfig{ModelID: "test-model"},
	}
	cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
	runner := NewTestRunner(cfg, nil)

	cases, err := runner.loadTestCasesFromCSV()
	require.NoError(t, err)
	assert.Len(t, cases, 2)
	assert.Equal(t, "Explain Go", cases[0].Stimulus.Message)
	assert.Equal(t, "Explain Rust", cases[1].Stimulus.Message)
}

func TestLoadTestCasesFromCSV_RangeFiltering(t *testing.T) {
	tmpDir := t.TempDir()
	writeCSV(t, tmpDir, "data.csv", "id,prompt\nA,one\nB,two\nC,three\nD,four\nE,five\n")

	spec := &models.EvalSpec{
		TasksFrom: "data.csv",
		Range:     [2]int{2, 4},
		Config:    models.EvalConfig{ModelID: "test-model"},
	}
	cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
	runner := NewTestRunner(cfg, nil)

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
				Config:    models.EvalConfig{ModelID: "test-model"},
			}
			cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
			runner := NewTestRunner(cfg, nil)

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
		Config:    models.EvalConfig{ModelID: "test-model"},
	}
	cfg := config.NewBenchmarkConfig(spec, config.WithSpecDir(tmpDir))
	runner := NewTestRunner(cfg, nil)

	_, err := runner.loadTestCasesFromCSV()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes spec directory")
}
