package assurance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	AuthoredOutputKind    = "waza.grader-reference-input"
	AuthoredOutputVersion = "1.0"
	// Native historical readers recognize this camelCase version boundary.
	// Together with the distinct outer kind, it fences supplied inputs from
	// native import without upgrading historical, payload or manifest schemas.
	AuthoredOutputEnvelopeVersion = "2.0"
	AuthoredOutputScope           = "authored_finite_output"
	MaxAuthoredOutputBytes        = 1 << 20
)

// AuthoredOutput retains admitted source bytes privately. Completeness means
// exactly this authored input, never an original session or execution history.
type AuthoredOutput struct {
	document   []byte
	projection []byte
	output     *string
	manifest   models.EvidenceManifest
	caseID     string
}

type authoredOutputPayload struct {
	Kind          string  `json:"kind"`
	SchemaVersion string  `json:"schema_version"`
	SourceScope   string  `json:"source_scope"`
	ID            string  `json:"id"`
	Output        *string `json:"output"`
}

type authoredOutputEnvelope struct {
	SchemaVersion string                  `json:"schemaVersion"`
	Kind          string                  `json:"kind"`
	Payload       json.RawMessage         `json:"payload"`
	Evidence      models.EvidenceManifest `json:"evidence"`
}

func NewAuthoredOutput(subjectID, caseID, taskID string, ordinal int, output *string) (*AuthoredOutput, error) {
	if !evidence.ValidReferenceIdentifier(subjectID) || !evidence.ValidReferenceIdentifier(caseID) ||
		!evidence.ValidReferenceIdentifier(taskID) || ordinal < 1 {
		return nil, errors.New("assurance: authored output requires safe subject, case and task identifiers and a positive ordinal")
	}
	if output != nil && (!utf8.ValidString(*output) || len(*output) > MaxAuthoredOutputBytes) {
		return nil, errors.New("assurance: authored output must be bounded UTF-8 text")
	}
	var supplied *string
	if output != nil {
		copy := *output
		supplied = &copy
	}
	payload, err := json.Marshal(authoredOutputPayload{
		Kind: AuthoredOutputKind, SchemaVersion: AuthoredOutputVersion,
		SourceScope: AuthoredOutputScope, ID: caseID, Output: supplied,
	})
	if err != nil {
		return nil, fmt.Errorf("assurance: encode authored output: %w", err)
	}
	artifact := models.EvidenceArtifact{
		ID: evidence.ReferenceInputArtifactID, Kind: evidence.ReferenceInputKind,
		Availability: "unavailable", Completeness: "unknown", Reason: "Authored output was explicitly unavailable.",
	}
	if supplied != nil {
		artifact.Availability, artifact.Completeness, artifact.Reason = "captured", "complete", ""
		artifact.Document, artifact.Pointer = evidence.ReferenceInputDocument, "/payload"
		artifact.ContentDigest, err = evidence.JSONDigest(json.RawMessage(payload))
		if err != nil {
			return nil, err
		}
	}
	manifest := models.EvidenceManifest{
		Version:   evidence.ReferenceInputVersion,
		Origin:    models.EvidenceOrigin{EvalID: "reference:" + subjectID, TaskID: taskID, RunNumber: ordinal, AttemptCount: 1, PriorAttempts: "none"},
		Runtime:   models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
		Redaction: models.EvidenceRedaction{Policy: "none"},
		Artifacts: []models.EvidenceArtifact{artifact},
	}
	if err := evidence.SealReferenceInput(&manifest); err != nil {
		return nil, err
	}
	data, err := json.Marshal(authoredOutputEnvelope{
		SchemaVersion: AuthoredOutputEnvelopeVersion, Kind: AuthoredOutputKind, Payload: payload, Evidence: manifest,
	})
	if err != nil {
		return nil, fmt.Errorf("assurance: encode authored envelope: %w", err)
	}
	return ParseAuthoredOutput(data)
}

var referenceInputSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	for resource, source := range map[string]string{
		"evidence-manifest-1.1.schema.json":      schemas.ReferenceInputEvidenceManifestSchemaJSON,
		"grader-reference-input-1.0.schema.json": schemas.GraderReferenceInputSchemaJSON,
	} {
		var value any
		if err := json.Unmarshal([]byte(source), &value); err != nil {
			return nil, err
		}
		if err := compiler.AddResource(resource, value); err != nil {
			return nil, err
		}
		if err := compiler.AddResource("https://raw.githubusercontent.com/microsoft/waza/main/schemas/"+resource, value); err != nil {
			return nil, err
		}
	}
	return compiler.Compile("grader-reference-input-1.0.schema.json")
})

func ParseAuthoredOutput(data []byte) (*AuthoredOutput, error) {
	if len(data) == 0 || len(data) > MaxAuthoredOutputBytes {
		return nil, errors.New("assurance: authored envelope must be nonempty and at most 1 MiB")
	}
	data = bytes.Clone(data)
	value, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("assurance: authored envelope: %w", err)
	}
	if err := validateUnicodeEscapes(data); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok || !exactKeys(object, "schemaVersion", "kind", "payload", "evidence") {
		return nil, errors.New("assurance: authored envelope requires only schemaVersion, kind, payload and evidence")
	}
	version, ok := object["schemaVersion"].(string)
	if !ok || version != AuthoredOutputEnvelopeVersion {
		return nil, errors.New("assurance: authored envelope requires schemaVersion 2.0")
	}
	kind, ok := object["kind"].(string)
	if !ok || kind != AuthoredOutputKind {
		return nil, errors.New("assurance: authored envelope requires kind waza.grader-reference-input")
	}
	payloadObject, ok := object["payload"].(map[string]any)
	if !ok || !exactKeys(payloadObject, "kind", "schema_version", "source_scope", "id", "output") {
		return nil, errors.New("assurance: authored payload requires explicit metadata and output")
	}
	schema, err := referenceInputSchema()
	if err != nil {
		return nil, fmt.Errorf("assurance: authored input schema: %w", err)
	}
	// Schema validation uses its own exact-number parser; no float64 conversion
	// is allowed on digest-bearing or ordinal-bearing bytes.
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(instance); err != nil {
		return nil, fmt.Errorf("assurance: invalid authored input: %w", err)
	}
	var envelope authoredOutputEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("assurance: decode authored envelope: %w", err)
	}
	var payload authoredOutputPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return nil, fmt.Errorf("assurance: decode authored payload: %w", err)
	}
	if payload.Kind != AuthoredOutputKind || payload.SchemaVersion != AuthoredOutputVersion ||
		payload.SourceScope != AuthoredOutputScope || !evidence.ValidReferenceIdentifier(payload.ID) {
		return nil, errors.New("assurance: unsupported authored payload metadata or case identity")
	}
	if err := evidence.Validate(&envelope.Evidence); err != nil {
		return nil, err
	}
	artifact := envelope.Evidence.Artifacts[0]
	if payload.Output == nil {
		if artifact.Availability != "unavailable" {
			return nil, errors.New("assurance: null authored output must be unavailable")
		}
	} else {
		reference, err := evidence.Reference(&envelope.Evidence, evidence.ReferenceInputArtifactID)
		if err != nil {
			return nil, err
		}
		if err := evidence.VerifyContent(&envelope.Evidence, reference, envelope.Payload, true, true); err != nil {
			return nil, err
		}
	}
	return &AuthoredOutput{
		document: data, projection: bytes.Clone(envelope.Payload),
		output: payload.Output, manifest: envelope.Evidence, caseID: payload.ID,
	}, nil
}

func exactKeys(object map[string]any, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

// Encoding/json replaces unpaired UTF-16 surrogates. Reject them instead of
// silently changing authored text while claiming identity for that text.
func validateUnicodeEscapes(data []byte) error {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		code, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return errors.New("assurance: invalid Unicode escape")
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return errors.New("assurance: unpaired Unicode surrogate")
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return errors.New("assurance: unpaired Unicode surrogate")
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return errors.New("assurance: unpaired Unicode surrogate")
			}
			i += 6
		}
	}
	return nil
}

func (input *AuthoredOutput) Document() []byte {
	if input == nil {
		return nil
	}
	return bytes.Clone(input.document)
}

func (input *AuthoredOutput) ProjectionBytes() []byte {
	if input == nil {
		return nil
	}
	return bytes.Clone(input.projection)
}

func (input *AuthoredOutput) Output() *string {
	if input == nil || input.output == nil {
		return nil
	}
	copy := *input.output
	return &copy
}

func (input *AuthoredOutput) Manifest() *models.EvidenceManifest {
	if input == nil || len(input.document) == 0 {
		return nil
	}
	copy := input.manifest
	copy.Artifacts = append([]models.EvidenceArtifact(nil), copy.Artifacts...)
	for i, artifact := range copy.Artifacts {
		if artifact.ContentDigest != nil {
			digest := *artifact.ContentDigest
			copy.Artifacts[i].ContentDigest = &digest
		}
	}
	// The admitted profile permits no runtime pointers or redaction/diagnostic
	// entries; only artifacts and their digest pointers need deep copies.
	return &copy
}

func (input *AuthoredOutput) CaseID() string {
	if input == nil {
		return ""
	}
	return input.caseID
}

func (input *AuthoredOutput) MarshalJSON() ([]byte, error) {
	if input == nil || len(input.document) == 0 {
		return nil, errors.New("assurance: authored output is not admitted")
	}
	return input.Document(), nil
}
