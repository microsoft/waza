package evidence

import "github.com/microsoft/waza/internal/models"

func Regrade(original *models.EvidenceManifest) (*models.EvidenceManifest, error) {
	if err := Validate(original); err != nil {
		return nil, err
	}
	copy := *original
	copy.Artifacts = append([]models.EvidenceArtifact(nil), original.Artifacts...)
	copy.Diagnostics = append([]models.EvidenceDiagnostic(nil), original.Diagnostics...)
	if copy.SourceManifestSHA256 == "" {
		copy.SourceManifestSHA256 = original.SHA256
	}
	for i, artifact := range copy.Artifacts {
		switch artifact.ID {
		case "validations", "result", "grader-config", "checkpoints":
			copy.Artifacts[i] = models.EvidenceArtifact{
				ID: artifact.ID, Kind: artifact.Kind, Availability: "unavailable", Completeness: "unknown",
				Reason: "Regrading changed the assessment; the original snapshot is provenance only for this artifact.",
			}
		}
	}
	copy.Diagnostics = append(copy.Diagnostics, models.EvidenceDiagnostic{
		Category: "regrade", Code: "assessment_changed",
		Message: "Grading was repeated using explicitly selected inputs; original execution evidence does not certify the new assessment.",
	})
	if err := Seal(&copy); err != nil {
		return nil, err
	}
	return &copy, nil
}
