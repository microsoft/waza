package validation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSchemaCompatibilityFixtures(t *testing.T) {
	fixtures := []struct {
		name string
		path string
		load func(string) error
	}{
		{
			name: "eval 1.0",
			path: filepath.Join("testdata", "eval-1.0.yaml"),
			load: func(path string) error {
				spec, err := models.LoadEvalSpec(path)
				if err != nil {
					return err
				}
				require.Equal(t, "1.0", spec.SchemaVersion)
				return nil
			},
		},
		{
			name: "results 1.0",
			path: filepath.Join("testdata", "results-1.0.json"),
			load: func(path string) error {
				outcome, err := models.LoadEvaluationOutcome(path)
				if err != nil {
					return err
				}
				require.Equal(t, "1.0", outcome.SchemaVersion)
				return nil
			},
		},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			require.NoError(t, fixture.load(fixture.path))
		})
	}
}

func TestSchemaCompatibilityAllowsPriorSameMajorMinor(t *testing.T) {
	dir := t.TempDir()

	evalData, err := os.ReadFile(filepath.Join("testdata", "eval-1.0.yaml"))
	require.NoError(t, err)
	evalData = []byte(strings.Replace(string(evalData), `schemaVersion: "1.0"`, `schemaVersion: "1.0"`+"\nfuture_eval_field: true", 1))
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, evalData, 0o644))
	spec, err := models.LoadEvalSpec(evalPath)
	require.NoError(t, err)
	require.Equal(t, "1.0", spec.SchemaVersion)

	resultsData, err := os.ReadFile(filepath.Join("testdata", "results-1.0.json"))
	require.NoError(t, err)
	resultsData = []byte(strings.Replace(string(resultsData), `"tasks": [`, `"future_results_field": true, "tasks": [`, 1))
	outcome, err := models.ParseEvaluationOutcome(resultsData, "results.json")
	require.NoError(t, err)
	require.Equal(t, "1.0", outcome.SchemaVersion)
}
