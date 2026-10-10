package evidence

import (
	"errors"
	"regexp"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

const (
	ReferenceInputVersion    = "1.1"
	ReferenceInputDocument   = "supplied-finite-input"
	ReferenceInputArtifactID = "authored-output"
	ReferenceInputKind       = "grader_reference_input"
)

var safeReferenceIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func ValidReferenceIdentifier(value string) bool {
	return safeReferenceIdentifier.MatchString(value)
}

// SealReferenceInput seals only explicitly selected authored input metadata.
// It cannot convert historical execution evidence to a reference profile.
func SealReferenceInput(manifest *models.EvidenceManifest) error {
	if manifest == nil || manifest.Version != ReferenceInputVersion || manifest.HasUnknownFields() {
		return errors.New("evidence: reference sealing requires explicit manifest version 1.1")
	}
	if err := validateReferenceInputProfile(manifest); err != nil {
		return err
	}
	digest, err := manifestDigest(manifest)
	if err != nil {
		return err
	}
	manifest.SHA256 = digest.SHA256
	return Validate(manifest)
}

// ValidateNative is required before attributing a manifest to an actual run,
// including cached attribution and regrading.
func ValidateNative(manifest *models.EvidenceManifest) error {
	if err := models.ValidateNativeEvidenceProfile(manifest); err != nil {
		return err
	}
	return Validate(manifest)
}

func validateReferenceInputProfile(manifest *models.EvidenceManifest) error {
	origin := manifest.Origin
	if !strings.HasPrefix(origin.EvalID, "reference:") ||
		!ValidReferenceIdentifier(strings.TrimPrefix(origin.EvalID, "reference:")) ||
		!ValidReferenceIdentifier(origin.TaskID) || origin.RunNumber < 1 ||
		origin.AttemptCount != 1 || origin.PriorAttempts != "none" {
		return errors.New("evidence: invalid authored input origin")
	}
	// No execution, SDK, skill-control or billing facts are inferred from input.
	expectedRuntime := models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"}
	if manifest.Runtime != expectedRuntime || manifest.SourceManifestSHA256 != "" ||
		len(manifest.Diagnostics) != 0 || manifest.Redaction.Policy != "none" ||
		manifest.Redaction.MatchCount != 0 || len(manifest.Redaction.AppliedRules) != 0 ||
		len(manifest.Redaction.Limitations) != 0 {
		return errors.New("evidence: authored input cannot claim runtime history or redaction")
	}
	if len(manifest.Artifacts) != 1 {
		return errors.New("evidence: authored input requires exactly one output artifact")
	}
	artifact := manifest.Artifacts[0]
	if artifact.ID != ReferenceInputArtifactID || artifact.Kind != ReferenceInputKind ||
		artifact.SourceDigest != nil || artifact.Redacted {
		return errors.New("evidence: invalid authored output artifact")
	}
	switch artifact.Availability {
	case "captured":
		if artifact.Document != ReferenceInputDocument || artifact.Pointer != "/payload" ||
			artifact.Completeness != "complete" || artifact.Reason != "" ||
			artifact.ContentDigest == nil || artifact.ContentDigest.Encoding != "json-v1" ||
			!validSHA(artifact.ContentDigest.SHA256) {
			return errors.New("evidence: authored output requires a complete raw payload JSON digest and locator")
		}
	case "unavailable":
		if artifact.Document != "" || artifact.Pointer != "" || artifact.ContentDigest != nil ||
			artifact.Completeness != "unknown" || artifact.Reason == "" {
			return errors.New("evidence: unavailable authored output cannot claim content or a locator")
		}
	default:
		return errors.New("evidence: unsupported authored output availability")
	}
	return nil
}
