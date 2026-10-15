package assurance

import (
	"os"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestAcquiredRubricIgnoresDeletedOrReplacedRetainedCachePath(t *testing.T) {
	for _, mutation := range []string{"delete", "replace"} {
		t.Run(mutation, func(t *testing.T) {
			request, _, _, harness := calibrationFixture(t)
			store, err := newCalibrationRubricStore()
			require.NoError(t, err)
			defer func() { require.NoError(t, store.close()) }()
			data := []byte(calibrationTestRubric)
			name, err := store.write(data)
			require.NoError(t, err)
			config, err := calibrationConfigCopy(request.Spec.Graders[0])
			require.NoError(t, err)
			parameters, ok := config.Parameters.(models.PromptGraderParameters)
			require.True(t, ok)
			parameters.Rubric = name
			config.Parameters = parameters
			declaration, err := evidence.JSONDigest(config)
			require.NoError(t, err)
			acquired, err := graders.AcquireRubric(config, declaration.SHA256, byteSHA256(data), data)
			require.NoError(t, err)
			control := &calibrationCapture{}
			context := &graders.Context{TestCase: request.Tasks["task"], Output: " \nexact\n ", OutputPresent: true, Executor: control}
			_, err = graders.RunAll(t.Context(), []models.GraderConfig{config}, &models.TestCase{}, context, calibrationTestModel, "low", false)
			require.ErrorIs(t, err, errCalibrationCapture)
			if mutation == "delete" {
				require.NoError(t, os.Remove(name))
			} else {
				require.NoError(t, os.WriteFile(name, []byte("replaced invalid rubric"), 0o600))
			}
			for index := range data {
				data[index] = 'x'
			}
			actual := &calibrationCapture{}
			context.Executor = actual
			_, err = graders.RunAllWithAcquiredRubrics(t.Context(), []models.GraderConfig{config}, &models.TestCase{}, context,
				calibrationTestModel, "low", false, []*graders.AcquiredRubric{acquired})
			require.ErrorIs(t, err, errCalibrationCapture)
			require.Equal(t, control.identity, actual.identity)
			require.Equal(t, 1, actual.calls)
			require.Zero(t, harness.factories)
		})
	}
}
