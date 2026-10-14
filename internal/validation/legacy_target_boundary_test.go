package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestLegacyTargetAuthoringAndLoadingBoundary(t *testing.T) {
	for _, version := range []string{"", "1.0", "1.1", "1.2", "1.3", "1.4"} {
		for _, target := range []struct {
			name, field, value string
			schemaValid        bool
		}{
			{name: "absent"},
			{name: "empty", field: "skill: \"\"\n"},
			{name: "whitespace", field: "skill: \" \"\n", value: " ", schemaValid: true},
		} {
			t.Run(version+"/"+target.name, func(t *testing.T) {
				data := target.field + targetBoundaryFixture
				if version != "" {
					data = "schemaVersion: \"" + version + "\"\n" + data
				}
				path := filepath.Join(t.TempDir(), "eval.yaml")
				require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
				spec, err := models.LoadEvalSpec(path)
				require.NoError(t, err)
				require.NoError(t, spec.Validate())
				require.Equal(t, target.value, spec.SkillName)
				wantVersion := version
				if wantVersion == "" {
					wantVersion = "1.4"
				}
				require.Equal(t, wantVersion, spec.SchemaVersion)
				require.False(t, spec.SkillsDisabledForTask(nil))
				schemaErrors := ValidateEvalBytes([]byte(data))
				if target.schemaValid {
					require.Empty(t, schemaErrors)
				} else {
					require.NotEmpty(t, schemaErrors)
					require.Contains(t, strings.Join(schemaErrors, "\n"), "skill")
				}
				evalErrors, taskErrors, err := ValidateEvalFile(path)
				require.NoError(t, err)
				require.Equal(t, schemaErrors, evalErrors)
				require.Empty(t, taskErrors)
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, []byte(data), after)
			})
		}
	}
}

func TestScenarioTargetAuthoringAndLoadingBoundary(t *testing.T) {
	for _, scenario := range []bool{false, true} {
		t.Run(fmt.Sprintf("scenario=%t", scenario), func(t *testing.T) {
			data := "schemaVersion: \"2.0\"\n" + targetBoundaryFixture
			if scenario {
				data = "scenario: inventory\n" + data
			}
			path := filepath.Join(t.TempDir(), "eval.yaml")
			require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
			spec, err := models.LoadEvalSpec(path)
			if scenario {
				require.NoError(t, err)
				require.NoError(t, spec.Validate())
				require.True(t, spec.SkillsDisabledForTask(nil))
				require.Empty(t, ValidateEvalBytes([]byte(data)))
			} else {
				require.Error(t, err)
				require.Nil(t, spec)
				require.NotEmpty(t, ValidateEvalBytes([]byte(data)))
			}
		})
	}
}

// The unmatched glob excludes task execution/validation from these target-boundary controls.
const targetBoundaryFixture = `name: target-boundary
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: mock
metrics:
  - name: completion
    weight: 1
    threshold: 1
tasks: ["tasks/*.yaml"]
`
