package assurance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestAcquiredAuthoredInputRetainsCanonicalAdmissionWithoutRoot(t *testing.T) {
	request, document, dir := portableAuthoredVerificationFixture(t)
	candidate := document.Cases[0]
	check := candidate.Checks[0]
	data, err := os.ReadFile(filepath.Join(dir, candidate.AuthoredInput.Path))
	require.NoError(t, err)
	observation := ChallengeObservation{State: Invalid, Reason: "authored_input_invalid"}
	control, controlObservation := verifyAuthoredInput(t.Context(), request, candidate, check, observation)
	require.NotNil(t, control)
	require.NoError(t, os.Remove(filepath.Join(dir, candidate.AuthoredInput.Path)))
	request.SnapshotRoot = nil
	acquired, actualObservation := verifyAcquiredAuthoredInput(t.Context(), request, candidate, check, observation, data)
	require.NotNil(t, acquired)
	require.Equal(t, control.Document(), acquired.Document())
	require.Equal(t, controlObservation, actualObservation)
	before := bytes.Clone(acquired.Document())
	for index := range data {
		data[index] = 'x'
	}
	require.Equal(t, before, acquired.Document())
	for _, invalid := range [][]byte{nil, make([]byte, maxSnapshotBytes+1), data} {
		value, result := verifyAcquiredAuthoredInput(t.Context(), request, candidate, check, observation, invalid)
		require.Nil(t, value)
		require.NotEmpty(t, result.Reason)
	}
}

func TestAcquiredCalibrationNativeRequestAndDomainParity(t *testing.T) {
	request, document, _, _ := calibrationFixture(t)
	config := request.Spec.Graders[0]
	check := document.Cases[0].Checks[0]
	store, err := newCalibrationRubricStore()
	require.NoError(t, err)
	defer func() { require.NoError(t, store.close()) }()
	rubric := []byte(calibrationTestRubric)
	originalDeclaration, err := evidence.JSONDigest(config)
	require.NoError(t, err)
	for _, output := range []string{"", " \nexact\n "} {
		task := request.Tasks["task"]
		if output == "" {
			task = &models.TestCase{}
		}
		name, err := store.write(rubric)
		require.NoError(t, err)
		controlConfig, err := calibrationConfigCopy(config)
		require.NoError(t, err)
		params, ok := controlConfig.Parameters.(models.PromptGraderParameters)
		require.True(t, ok)
		params.Rubric = name
		controlConfig.Parameters = params
		control := &calibrationCapture{}
		_, err = graders.RunAll(t.Context(), []models.GraderConfig{controlConfig}, &models.TestCase{},
			&graders.Context{TestCase: task, Output: output, OutputPresent: true, Executor: control},
			calibrationTestModel, "low", false)
		require.ErrorIs(t, err, errCalibrationCapture)
		actual := &calibrationCapture{}
		job := calibrationJob{config: config, task: task, output: output,
			rubric: rubric, rubricSHA: byteSHA256(rubric), declarationSHA: check.GraderDeclarationSHA256,
			judgeModel: calibrationTestModel, judgeEffort: "low"}
		_, err = runCalibrationGrade(t.Context(), job, store, actual)
		require.ErrorIs(t, err, errCalibrationCapture)
		require.Equal(t, 1, actual.calls)
		require.Equal(t, control.identity, actual.identity, "actual native request binding parity independent of Details metadata")
		oldDomain, err := evidence.JSONDigest(struct{ Rubric, NativeRequest, Protocol string }{
			job.rubricSHA, control.identity, AgreementProtocol})
		require.NoError(t, err)
		newDomain, err := evidence.JSONDigest(struct{ Rubric, NativeRequest, Protocol string }{
			job.rubricSHA, actual.identity, AgreementProtocol})
		require.NoError(t, err)
		require.Equal(t, oldDomain, newDomain)
		oldStimulus, err := evidence.JSONDigest(struct{ Request, Output, Rubric, Protocol string }{
			control.identity, output, job.rubricSHA, AgreementProtocol})
		require.NoError(t, err)
		newStimulus, err := evidence.JSONDigest(struct{ Request, Output, Rubric, Protocol string }{
			actual.identity, output, job.rubricSHA, AgreementProtocol})
		require.NoError(t, err)
		require.Equal(t, oldStimulus, newStimulus)
		after, err := evidence.JSONDigest(config)
		require.NoError(t, err)
		require.Equal(t, originalDeclaration, after)
	}
}
