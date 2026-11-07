package evidencestate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const CaptureArtifactName = "execution-evidence-capture.json"

// CaptureIndex is recorded before origin finalization. It contains no origin
// digest or captured non-empty payload. Category objects are bounded separately.
// Explicit empty payloads retain evidence that legacy omitempty cannot supply.
type CaptureIndex struct {
	Kind             string                       `json:"kind"`
	SchemaVersion    string                       `json:"schema_version"`
	Binding          artifactmanifest.Binding     `json:"binding"`
	TerminalArtifact string                       `json:"terminal_artifact"`
	Categories       []Record                     `json:"categories"`
	EmptyPayloads    map[Category]json.RawMessage `json:"empty_payloads"`
}

var captureSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"execution-evidence-capture-1.0.schema.json": schemas.ExecutionEvidenceCaptureSchemaJSON,
		"execution-evidence-1.0.schema.json":         schemas.ExecutionEvidenceSchemaJSON,
		"execution-attempt-1.0.schema.json":          schemas.ExecutionAttemptSchemaJSON,
		"artifact-manifest-1.0.schema.json":          schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":            schemas.RunnerTerminalSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, err
		}
		if err := c.AddResource("https://microsoft.github.io/waza/schemas/"+name, doc); err != nil {
			return nil, err
		}
	}
	return c.Compile("https://microsoft.github.io/waza/schemas/execution-evidence-capture-1.0.schema.json")
})

func ParseCapture(data []byte) (*CaptureIndex, error) {
	if len(data) > MaxDocumentBytes {
		return nil, fmt.Errorf("%w: capture index exceeds %d bytes", ErrInvalid, MaxDocumentBytes)
	}
	value, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: capture index JSON: %w", ErrInvalid, err)
	}
	schema, err := captureSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(value); err != nil {
		return nil, fmt.Errorf("%w: capture index shape: %w", ErrInvalid, err)
	}
	var capture CaptureIndex
	if err := json.Unmarshal(data, &capture); err != nil {
		return nil, fmt.Errorf("%w: capture index decoding: %w", ErrInvalid, err)
	}
	provenance := false
	for _, record := range capture.Categories {
		if record.Limit != nil && record.Limit.Total != nil && *record.Limit.Total < record.Limit.Observed {
			return nil, fmt.Errorf("%w: exact capture bound is below observed bound", ErrInvalid)
		}
		empty := record.State == Present && record.ObservedCount != nil && *record.ObservedCount == 0
		_, proof := capture.EmptyPayloads[record.Category]
		if empty != proof {
			return nil, fmt.Errorf("%w: empty payload and category state disagree", ErrInvalid)
		}
		if record.Category == Provenance {
			provenance = record.State == Present && record.ObservedCount != nil && *record.ObservedCount == 1 &&
				record.Context == Declared && len(record.Artifacts) == 2 &&
				slices.Contains(record.Artifacts, CaptureArtifactName) &&
				slices.Contains(record.Artifacts, capture.TerminalArtifact)
		} else if slices.Contains(record.Artifacts, CaptureArtifactName) {
			return nil, fmt.Errorf("%w: category payload cannot alias the capture index", ErrInvalid)
		}
	}
	if !provenance || capture.TerminalArtifact == CaptureArtifactName {
		return nil, fmt.Errorf("%w: capture provenance must reference index and original terminal", ErrInvalid)
	}
	// Apply the actual native binding contract, including byte and identifier
	// bounds, without inventing another identity representation.
	manifest := artifactmanifest.Manifest{
		Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version,
		Binding: capture.Binding, TerminalArtifact: capture.TerminalArtifact,
		Artifacts: []artifactmanifest.Artifact{{
			Name: capture.TerminalArtifact, SHA256: artifactmanifest.Hash(nil), MediaType: "application/json",
		}},
	}
	if _, err := artifactmanifest.Encode(manifest); err != nil {
		return nil, fmt.Errorf("%w: capture binding: %w", ErrInvalid, err)
	}
	return &capture, nil
}

func EncodeCapture(capture CaptureIndex) ([]byte, error) {
	// Clone through JSON so nested record references/proofs remain caller-owned.
	data, err := json.Marshal(capture)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding capture index: %w", ErrInvalid, err)
	}
	copy, err := ParseCapture(data)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(copy.Categories, func(a, b Record) int {
		return strings.Compare(string(a.Category), string(b.Category))
	})
	for i := range copy.Categories {
		slices.Sort(copy.Categories[i].Artifacts)
	}
	return json.Marshal(copy)
}

func verifyCaptureIndex(capture *CaptureIndex, d *Document, source *artifactmanifest.CompletedPublication) error {
	a, err := artifactmanifest.Encode(artifactmanifest.Manifest{
		Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version,
		Binding: capture.Binding, TerminalArtifact: source.Manifest.TerminalArtifact,
		Artifacts: source.Manifest.Artifacts,
	})
	if err != nil {
		return err
	}
	if !bytes.Equal(a, source.ManifestBytes) || capture.TerminalArtifact != source.Manifest.TerminalArtifact {
		return fmt.Errorf("%w: capture and source binding disagree", artifactmanifest.ErrConflict)
	}
	records := make(map[Category]Record, len(capture.Categories))
	for _, record := range capture.Categories {
		records[record.Category] = record
	}
	for _, record := range d.Categories {
		other, ok := records[record.Category]
		left, right := slices.Clone(record.Artifacts), slices.Clone(other.Artifacts)
		slices.Sort(left)
		slices.Sort(right)
		record.Artifacts, other.Artifacts = left, right
		if !ok || !reflect.DeepEqual(record, other) {
			return fmt.Errorf("%w: sidecar and recorded category disagree", artifactmanifest.ErrConflict)
		}
	}
	return nil
}
