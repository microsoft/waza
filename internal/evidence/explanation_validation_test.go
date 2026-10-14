package evidence

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestValidateExplanations(t *testing.T) {
	manifest := sampleManifest(t)
	manifest.Artifacts = append(manifest.Artifacts,
		models.EvidenceArtifact{ID: "unavailable", Availability: "unavailable", Completeness: "unknown", Reason: "Not captured."},
		models.EvidenceArtifact{ID: "not-requested", Availability: "not_requested", Completeness: "unknown", Reason: "Not requested."},
		models.EvidenceArtifact{ID: "digest-only", Availability: "digest_only", Completeness: "unknown", Reason: "Source identity only.", SourceDigest: manifest.Artifacts[0].ContentDigest},
	)
	require.NoError(t, Seal(manifest))
	reference, err := Reference(manifest, "receipt")
	require.NoError(t, err)
	require.NoError(t, ValidateExplanations(nil, "task", nil))
	for _, checks := range [][]models.CheckExplanation{nil, {}, {{Observation: "unresolved"}}} {
		require.NoError(t, ValidateExplanations(nil, "task", []models.RequirementExplanation{{Checks: checks}}))
	}
	for _, test := range []struct {
		name, taskID, wantError string
		mutate                  func(*models.EvidenceReference)
		noManifest              bool
	}{
		{name: "matching", taskID: "task"},
		{name: "historical missing task"},
		{name: "valid pointer", mutate: func(r *models.EvidenceReference) { r.Pointer = "/item~1name/~0key" }},
		{name: "foreign task declaration", taskID: "foreign", wantError: "different task"},
		{name: "foreign eval", mutate: func(r *models.EvidenceReference) { r.Origin.EvalID = "other" }, wantError: "different eval/task/run/attempt"},
		{name: "foreign task", mutate: func(r *models.EvidenceReference) { r.Origin.TaskID = "other" }, wantError: "different eval/task/run/attempt"},
		{name: "foreign run", mutate: func(r *models.EvidenceReference) { r.Origin.RunNumber++ }, wantError: "different eval/task/run/attempt"},
		{name: "foreign attempt", mutate: func(r *models.EvidenceReference) { r.Origin.AttemptCount++ }, wantError: "different eval/task/run/attempt"},
		{name: "missing artifact", mutate: func(r *models.EvidenceReference) { r.ArtifactID = "absent" }, wantError: "artifact is absent"},
		{name: "unavailable artifact", mutate: func(r *models.EvidenceReference) { r.ArtifactID = "unavailable" }, wantError: "without captured content"},
		{name: "unrequested artifact", mutate: func(r *models.EvidenceReference) { r.ArtifactID = "not-requested" }, wantError: "without captured content"},
		{name: "digest-only artifact", mutate: func(r *models.EvidenceReference) { r.ArtifactID = "digest-only" }, wantError: "without captured content"},
		{name: "malformed pointer", mutate: func(r *models.EvidenceReference) { r.Pointer = "/bad~2escape" }, wantError: "invalid artifact-relative pointer"},
		{name: "missing manifest", noManifest: true, wantError: "absent manifest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ref := reference
			if test.mutate != nil {
				test.mutate(&ref)
			}
			input := manifest
			if test.noManifest {
				input = nil
			}
			err := ValidateExplanations(input, "task", []models.RequirementExplanation{{
				TaskID: test.taskID, Checks: []models.CheckExplanation{{References: []models.EvidenceReference{ref}}},
			}})
			if test.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.wantError)
			}
		})
	}
}

func TestValidateExplanationsAllowsPartialAndRedactedCapturedArtifacts(t *testing.T) {
	manifest := sampleManifest(t)
	manifest.Artifacts[0].Completeness = "partial"
	manifest.Artifacts[0].Redacted = true
	manifest.Artifacts[0].Reason = "Partial capture changed by redaction."
	require.NoError(t, Seal(manifest))
	reference, err := Reference(manifest, "receipt")
	require.NoError(t, err)
	require.NoError(t, ValidateExplanations(manifest, "task", []models.RequirementExplanation{{
		TaskID: "task", Checks: []models.CheckExplanation{{References: []models.EvidenceReference{reference}}},
	}}))
	require.ErrorContains(t, VerifyContent(manifest, reference, nil, true, false), "incomplete")
	require.ErrorContains(t, VerifyContent(manifest, reference, nil, false, true), "redaction")
}
