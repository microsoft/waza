package models

import (
	"bytes"
	"encoding/json"

	"github.com/microsoft/waza/internal/jsonutil"
)

// EvidenceManifest is descriptive provenance, not an enforcement or assurance
// verdict. Its version is independent of the result and snapshot schemas.
type EvidenceManifest struct {
	Version              string               `json:"version"`
	SHA256               string               `json:"sha256"`
	SourceManifestSHA256 string               `json:"source_manifest_sha256,omitempty"`
	Origin               EvidenceOrigin       `json:"origin"`
	Runtime              EvidenceRuntime      `json:"runtime"`
	Redaction            EvidenceRedaction    `json:"redaction"`
	Artifacts            []EvidenceArtifact   `json:"artifacts"`
	Diagnostics          []EvidenceDiagnostic `json:"diagnostics,omitempty"`
	unknownFields        bool
	unknownRaw           json.RawMessage
}

func (m *EvidenceManifest) HasUnknownFields() bool { return m.unknownFields }

func (m *EvidenceManifest) UnmarshalJSON(data []byte) error {
	if _, err := jsonutil.Parse(data); err != nil {
		return err
	}
	type alias EvidenceManifest
	var decoded alias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	unknown := decoder.Decode(&decoded) != nil
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*m = EvidenceManifest(decoded)
	m.unknownFields = unknown
	if unknown {
		m.unknownRaw = append(json.RawMessage(nil), data...)
	}
	return nil
}

func (m EvidenceManifest) MarshalJSON() ([]byte, error) {
	if m.unknownFields {
		return append([]byte(nil), m.unknownRaw...), nil
	}
	type alias EvidenceManifest
	return json.Marshal(alias(m))
}

type EvidenceOrigin struct {
	EvalID        string `json:"eval_id"`
	TaskID        string `json:"task_id"`
	RunNumber     int    `json:"run_number"`
	AttemptCount  int    `json:"attempt_count"`
	PriorAttempts string `json:"prior_attempts"`
}

type EvidenceRuntime struct {
	WazaVersion           string  `json:"waza_version"`
	RequestedEngine       string  `json:"requested_engine"`
	RequestedModel        string  `json:"requested_model"`
	ExecutionMode         string  `json:"execution_mode"`
	DependencyMode        string  `json:"dependency_mode"`
	RequestedPolicy       string  `json:"requested_policy"`
	VerifiedEnforcement   string  `json:"verified_enforcement"`
	GoVersion             string  `json:"go_version"`
	Platform              string  `json:"platform"`
	SDKVersion            *string `json:"sdk_version"`
	EffectiveModelVersion *string `json:"effective_model_version"`
	NoSkills              *bool   `json:"no_skills"`
	NativeSkillControl    string  `json:"native_skill_control"`
}

type EvidenceRedaction struct {
	Policy       string   `json:"policy"`
	AppliedRules []string `json:"applied_rules,omitempty"`
	MatchCount   int      `json:"match_count"`
	Limitations  []string `json:"limitations"`
}

type EvidenceDigest struct {
	SHA256   string `json:"sha256"`
	Encoding string `json:"encoding"`
}

type EvidenceArtifact struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	Availability  string          `json:"availability"`
	Completeness  string          `json:"completeness"`
	Document      string          `json:"document,omitempty"`
	Pointer       string          `json:"pointer,omitempty"`
	ContentDigest *EvidenceDigest `json:"content_digest,omitempty"`
	SourceDigest  *EvidenceDigest `json:"source_digest,omitempty"`
	Reason        string          `json:"reason,omitempty"`
	Redacted      bool            `json:"redacted,omitempty"`
}

// References resolve only inside the manifest with the matching full origin.
type EvidenceReference struct {
	Origin     EvidenceOrigin `json:"origin"`
	ArtifactID string         `json:"artifact_id"`
	Pointer    string         `json:"pointer,omitempty"`
}

type EvidenceDiagnostic struct {
	Category string `json:"category"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// RequirementExplanation reports an existing check's recorded observation.
// It must not relabel descriptive requirement metadata as runtime enforcement.
type RequirementExplanation struct {
	TaskID        string             `json:"task_id"`
	RequirementID string             `json:"requirement_id"`
	Checks        []CheckExplanation `json:"checks"`
}

type CheckExplanation struct {
	Check       RequirementCheck    `json:"check"`
	Observation string              `json:"observation"`
	Category    string              `json:"category"`
	Message     string              `json:"message"`
	References  []EvidenceReference `json:"references,omitempty"`
}
