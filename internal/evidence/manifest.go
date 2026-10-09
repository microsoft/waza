package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
)

const Version = "1.0"

// JSONDigest hashes JSON normalized to sorted object keys and exact number tokens.
// It is sensitive to values inside the artifact, including timing fields.
func JSONDigest(value any) (*models.EvidenceDigest, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("evidence: content cannot be encoded as JSON")
	}
	value, err = jsonutil.Parse(data)
	if err != nil {
		return nil, err
	}
	data, err = json.Marshal(value)
	if err != nil {
		return nil, errors.New("evidence: content cannot be normalized")
	}
	return &models.EvidenceDigest{SHA256: sum(data), Encoding: "json-v1"}, nil
}

func sum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// Seal identifies the complete serialized metadata excluding only SHA256.
// Host file paths and capture timestamps do not belong in this manifest.
// Included content digests deliberately make identity sensitive to that content.
func Seal(manifest *models.EvidenceManifest) error {
	if manifest == nil {
		return errors.New("evidence: manifest is required")
	}
	manifest.Version = Version
	digest, err := manifestDigest(manifest)
	if err != nil {
		return err
	}
	manifest.SHA256 = digest.SHA256
	return Validate(manifest)
}

func Validate(manifest *models.EvidenceManifest) error {
	if manifest == nil || manifest.Version != Version || manifest.HasUnknownFields() {
		return errors.New("evidence: unsupported or absent manifest version")
	}
	digest, err := manifestDigest(manifest)
	if err != nil {
		return err
	}
	if digest.SHA256 != manifest.SHA256 {
		return errors.New("evidence: manifest identity does not match its content")
	}
	if manifest.SourceManifestSHA256 != "" && !validSHA(manifest.SourceManifestSHA256) {
		return errors.New("evidence: invalid source manifest SHA256")
	}
	if manifest.Origin.RunNumber < 0 || manifest.Origin.AttemptCount < 0 || manifest.Redaction.MatchCount < 0 {
		return errors.New("evidence: negative lineage or redaction count")
	}
	switch manifest.Runtime.ExecutionMode {
	case "live", "mock", "unknown":
	default:
		return errors.New("evidence: invalid execution mode")
	}
	switch manifest.Runtime.NativeSkillControl {
	case "unknown", "sdk_default", "requested_sdk_disable", "mock_not_applicable":
	default:
		return errors.New("evidence: invalid native skill control")
	}
	seen := make(map[string]bool)
	for _, artifact := range manifest.Artifacts {
		if artifact.ID == "" || seen[artifact.ID] {
			return errors.New("evidence: artifact IDs must be nonempty and unique")
		}
		seen[artifact.ID] = true
		switch artifact.Completeness {
		case "complete", "partial", "unknown":
		default:
			return errors.New("evidence: invalid artifact completeness")
		}
		for _, digest := range []*models.EvidenceDigest{artifact.ContentDigest, artifact.SourceDigest} {
			if digest == nil {
				continue
			}
			if !validSHA(digest.SHA256) {
				return errors.New("evidence: invalid SHA256 digest")
			}
			if digest.Encoding != "json-v1" && digest.Encoding != "utf8" && digest.Encoding != "source-bytes" {
				return errors.New("evidence: unsupported digest encoding")
			}
		}
		switch artifact.Availability {
		case "captured":
			if artifact.Document != "snapshot" || artifact.ContentDigest == nil || artifact.ContentDigest.Encoding == "source-bytes" || !validPointer(artifact.Pointer) {
				return errors.New("evidence: captured content requires a snapshot locator and content digest")
			}
			if artifact.Completeness != "complete" && artifact.Reason == "" {
				return errors.New("evidence: partial or unknown capture requires a reason")
			}
		case "digest_only":
			if artifact.SourceDigest == nil || artifact.ContentDigest != nil || artifact.Document != "" || artifact.Pointer != "" || artifact.Reason == "" {
				return errors.New("evidence: digest-only artifacts require a source digest, reason and no content locator")
			}
		case "unavailable", "not_requested":
			if artifact.ContentDigest != nil || artifact.SourceDigest != nil || artifact.Document != "" || artifact.Pointer != "" || artifact.Reason == "" || artifact.Completeness == "complete" {
				return errors.New("evidence: absent artifacts require a reason and cannot claim content or digests")
			}
		default:
			return errors.New("evidence: invalid artifact availability")
		}
	}
	return nil
}

func validSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func manifestDigest(manifest *models.EvidenceManifest) (*models.EvidenceDigest, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, errors.New("evidence: manifest cannot be encoded")
	}
	value, err := jsonutil.Parse(data)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("evidence: manifest must be an object")
	}
	delete(object, "sha256")
	return JSONDigest(object)
}

func CompleteOrigin(origin models.EvidenceOrigin) bool {
	return origin.EvalID != "" && origin.TaskID != "" && origin.RunNumber > 0 && origin.AttemptCount > 0
}

// Bind checks the selected result row without relabeling cached source lineage.
func Bind(manifest *models.EvidenceManifest, evalID, taskID string, runNumber, attempts int, cached bool) error {
	if err := Validate(manifest); err != nil {
		return err
	}
	origin := manifest.Origin
	if origin.TaskID != "" && origin.TaskID != taskID ||
		origin.RunNumber != 0 && origin.RunNumber != runNumber ||
		origin.AttemptCount != 0 && origin.AttemptCount != attempts ||
		!cached && origin.EvalID != "" && origin.EvalID != evalID {
		return errors.New("evidence: attribution does not match the selected result")
	}
	return nil
}

func validPointer(pointer string) bool {
	if pointer == "" || !strings.HasPrefix(pointer, "/") {
		return false
	}
	for i := 0; i < len(pointer); i++ {
		if pointer[i] == '~' {
			i++
			if i == len(pointer) || pointer[i] != '0' && pointer[i] != '1' {
				return false
			}
		}
	}
	return true
}

func Reference(manifest *models.EvidenceManifest, id string) (models.EvidenceReference, error) {
	if manifest == nil {
		return models.EvidenceReference{}, errors.New("evidence: manifest is required")
	}
	reference := models.EvidenceReference{Origin: manifest.Origin, ArtifactID: id}
	if _, err := Resolve(manifest, reference); err != nil {
		return models.EvidenceReference{}, err
	}
	return reference, nil
}

func Resolve(manifest *models.EvidenceManifest, reference models.EvidenceReference) (*models.EvidenceArtifact, error) {
	if err := Validate(manifest); err != nil {
		return nil, err
	}
	if !CompleteOrigin(manifest.Origin) {
		return nil, errors.New("evidence: execution attribution is incomplete")
	}
	if reference.Origin != manifest.Origin {
		return nil, errors.New("evidence: reference belongs to a different eval/task/run/attempt")
	}
	if reference.Pointer != "" && !validPointer(reference.Pointer) {
		return nil, errors.New("evidence: invalid artifact-relative pointer")
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.ID == reference.ArtifactID {
			return &artifact, nil
		}
	}
	return nil, errors.New("evidence: referenced artifact is absent")
}

// VerifyContent requires actual content, not a source hash. Required evidence
// consumers must request complete, unredacted artifacts independently.
func VerifyContent(manifest *models.EvidenceManifest, reference models.EvidenceReference, data []byte, requireComplete, requireUnredacted bool) error {
	artifact, err := Resolve(manifest, reference)
	if err != nil {
		return err
	}
	if artifact.Availability != "captured" || artifact.ContentDigest == nil {
		return errors.New("evidence: required content was not preserved")
	}
	if requireComplete && artifact.Completeness != "complete" {
		return errors.New("evidence: required content is incomplete or unassessed")
	}
	if requireUnredacted && artifact.Redacted {
		return errors.New("evidence: required content was changed by redaction")
	}
	switch artifact.ContentDigest.Encoding {
	case "json-v1":
		value, err := jsonutil.Parse(data)
		if err != nil {
			return err
		}
		digest, err := JSONDigest(value)
		if err != nil {
			return err
		}
		if digest.SHA256 != artifact.ContentDigest.SHA256 {
			return errors.New("evidence: preserved content digest mismatch")
		}
		if reference.Pointer != "" {
			if err := resolvePointer(value, reference.Pointer); err != nil {
				return err
			}
		}
	case "utf8":
		if !utf8.Valid(data) {
			return errors.New("evidence: preserved text is not UTF-8")
		}
		if sum(data) != artifact.ContentDigest.SHA256 {
			return errors.New("evidence: preserved content digest mismatch")
		}
	default:
		return errors.New("evidence: source digests cannot verify preserved content")
	}
	if reference.Pointer != "" && artifact.ContentDigest.Encoding != "json-v1" {
		return errors.New("evidence: only JSON artifacts support artifact-relative pointers")
	}
	return nil
}

func resolvePointer(value any, pointer string) error {
	for _, encoded := range strings.Split(pointer[1:], "/") {
		key := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch item := value.(type) {
		case map[string]any:
			next, ok := item[key]
			if !ok {
				return errors.New("evidence: referenced JSON value is absent")
			}
			value = next
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(item) || strconv.Itoa(index) != key {
				return errors.New("evidence: invalid referenced array index")
			}
			value = item[index]
		default:
			return errors.New("evidence: referenced value is not a container")
		}
	}
	return nil
}
