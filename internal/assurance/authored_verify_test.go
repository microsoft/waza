package assurance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func authoredVerificationFixture(t *testing.T) (VerifyRequest, ReferenceDocument, string) {
	t.Helper()
	request, document, root := fileVerificationFixture(t)
	request.Spec.Graders[0].Kind = models.GraderKindText
	request.Spec.Graders[0].Parameters = models.TextGraderParameters{
		RegexMatch: []string{`(?m)^state:\s*ready$`}, RegexNotMatch: []string{`forbidden:\s*yes`},
	}
	config, err := evidence.JSONDigest(request.Spec)
	require.NoError(t, err)
	declaration, err := evidence.JSONDigest(request.Spec.Graders[0])
	require.NoError(t, err)
	document.EvalResolvedConfigSHA256 = config.SHA256
	for index, output := range []string{
		"state: ready\n", "note: alternate explanation\nstate: ready\n",
		"state: wrong\n", "state: ready\nforbidden: yes\n",
	} {
		candidate := &document.Cases[index]
		input, err := NewAuthoredOutput(document.ID, candidate.ID, candidate.TaskID, index+1, new(output))
		require.NoError(t, err)
		name := candidate.ID + "-authored.json"
		require.NoError(t, os.WriteFile(filepath.Join(root, name), input.Document(), 0o600))
		candidate.Snapshot = ""
		candidate.AuthoredInput = &AuthoredSource{Path: name, DocumentSHA256: byteSHA256(input.Document())}
		candidate.ManifestSHA256 = input.Manifest().SHA256
		reference, err := evidence.Reference(input.Manifest(), "authored-output")
		require.NoError(t, err)
		reference.Pointer = "/output"
		candidate.Checks[0].Evidence = []models.EvidenceReference{reference}
		candidate.Checks[0].GraderDeclarationSHA256 = declaration.SHA256
	}
	rebindSyntheticReview(t, &request, document)
	return request, document, root
}

func TestVerifyFiniteAuthoredOutputUsesActualNativeTextVerdicts(t *testing.T) {
	request, document, _ := authoredVerificationFixture(t)
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentPassed, report.State)
	for index, observation := range report.Requirements[0].Observations {
		require.Equal(t, Observed, observation.State)
		require.Equal(t, "authored_finite_output", observation.SourceScope)
		require.Equal(t, models.GraderKindText, observation.Result.Type)
		require.Equal(t, *document.Cases[index].Checks[0].ExpectedPassed, observation.Result.Passed)
		require.True(t, *observation.Agreement)
		require.NotEmpty(t, observation.Result.Feedback)
	}
	require.Nil(t, report.Calibration.Usage)
	require.Nil(t, report.Calibration.Credits)
}

func TestVerifyAuthoredEmptyWhitespaceAndUnavailableAreDistinct(t *testing.T) {
	for _, output := range []*string{new(""), new(" \n\t"), nil} {
		request, document, root := authoredVerificationFixture(t)
		candidate := &document.Cases[2]
		input, err := NewAuthoredOutput(document.ID, candidate.ID, candidate.TaskID, 3, output)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, candidate.AuthoredInput.Path), input.Document(), 0o600))
		candidate.AuthoredInput.DocumentSHA256 = byteSHA256(input.Document())
		candidate.ManifestSHA256 = input.Manifest().SHA256
		rebindSyntheticReview(t, &request, document)
		report, err := Verify(t.Context(), request)
		require.NoError(t, err)
		observation := report.Requirements[0].Observations[2]
		if output == nil {
			require.Equal(t, InsufficientEvidence, observation.State)
			require.Nil(t, observation.Result)
			require.NotEqual(t, AssessmentPassed, report.State)
		} else {
			require.Equal(t, Observed, observation.State)
			require.False(t, observation.Result.Passed)
			require.True(t, *observation.Agreement)
			require.Equal(t, AssessmentPassed, report.State)
		}
	}
}

func TestVerifyAuthoredSourceRequiresExactEnvelopeAndProfileBinding(t *testing.T) {
	for _, mutation := range []string{"bytes", "case", "pointer", "missing"} {
		t.Run(mutation, func(t *testing.T) {
			request, document, root := authoredVerificationFixture(t)
			candidate := &document.Cases[0]
			switch mutation {
			case "bytes":
				data, err := os.ReadFile(filepath.Join(root, candidate.AuthoredInput.Path))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, candidate.AuthoredInput.Path), append(data, '\n'), 0o600))
			case "case":
				candidate.ID = "other"
			case "pointer":
				candidate.Checks[0].Evidence[0].Pointer = ""
			case "missing":
				require.NoError(t, os.Remove(filepath.Join(root, candidate.AuthoredInput.Path)))
			}
			rebindSyntheticReview(t, &request, document)
			report, err := Verify(t.Context(), request)
			require.NoError(t, err)
			require.NotEqual(t, AssessmentPassed, report.State)
			require.Nil(t, report.Requirements[0].Observations[0].Result)
		})
	}
}
