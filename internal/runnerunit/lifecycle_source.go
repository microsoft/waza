package runnerunit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/jsonutil"
)

// ParseLifecycleCapture admits the fixed canonical v1 writer profile, not an
// authenticated worker observation or an ownership capability.
func ParseLifecycleCapture(data []byte) (*LifecycleCapture, error) {
	if len(data) > executionlifecycle.MaxBytes {
		return nil, fmt.Errorf("%w: lifecycle capture exceeds byte bound", executionlifecycle.ErrInvalid)
	}
	if _, err := jsonutil.Parse(data); err != nil {
		return nil, fmt.Errorf("%w: lifecycle capture JSON: %w", executionlifecycle.ErrInvalid, err)
	}
	var capture LifecycleCapture
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&capture); err != nil {
		return nil, fmt.Errorf("%w: lifecycle capture fields: %w", executionlifecycle.ErrInvalid, err)
	}
	canonical, err := json.Marshal(capture)
	if err != nil {
		return nil, fmt.Errorf("encoding lifecycle capture: %w", err)
	}
	if !bytes.Equal(data, canonical) || capture.Kind != "waza.execution-lifecycle-capture" ||
		capture.SchemaVersion != executionlifecycle.Version ||
		len(capture.Observations) < 2 || len(capture.Observations) > executionlifecycle.MaxEvents ||
		len(capture.ObservationDigests) != len(capture.Observations) {
		return nil, fmt.Errorf("%w: lifecycle capture is not the complete canonical v1 profile", executionlifecycle.ErrInvalid)
	}
	if err := validateRuntimeCapture(capture.Runtime); err != nil {
		return nil, err
	}
	return &capture, nil
}

// ValidateLifecycleCaptureSource reuses first-publication admission against a
// verified original source. Store has already read/hash-checked every artifact;
// matching canonical event descriptors therefore needs no second source read.
func ValidateLifecycleCaptureSource(source *executionledger.CompletedExecution) (*LifecycleCapture, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: original lifecycle source is unavailable", executionlifecycle.ErrContext)
	}
	capture, err := ParseLifecycleCapture(source.Publication.ArtifactBytes[LifecycleCaptureArtifact])
	if err != nil {
		return nil, err
	}
	terminal := source.Publication.ArtifactBytes[source.Publication.Manifest.TerminalArtifact]
	if len(terminal) == 0 {
		return nil, fmt.Errorf("%w: original lifecycle terminal was not retained", executionlifecycle.ErrContext)
	}
	expected, err := admitLifecycleCapture(*capture, source.Reservation, terminal, capture.Deadline)
	if err != nil {
		return nil, err
	}
	fixed := make(map[string]artifactmanifest.Artifact, len(expected)+1)
	fixed["execution-attempt.json"] = artifactmanifest.Artifact{
		Name: "execution-attempt.json", SHA256: source.Reservation.Digest,
		SizeBytes: int64(len(source.Reservation.Bytes)), MediaType: "application/json",
	}
	for _, artifact := range expected {
		fixed[artifact.Name] = artifactmanifest.Artifact{
			Name: artifact.Name, SHA256: artifactmanifest.Hash(artifact.Data),
			SizeBytes: int64(len(artifact.Data)), MediaType: artifact.MediaType,
		}
	}
	for _, artifact := range source.Publication.Manifest.Artifacts {
		expected, selected := fixed[artifact.Name]
		if selected {
			if artifact != expected {
				return nil, fmt.Errorf("%w: original lifecycle artifact contradicts canonical capture", executionlifecycle.ErrContext)
			}
			delete(fixed, artifact.Name)
		} else if artifact.Name == LifecycleCaptureArtifact || strings.HasPrefix(artifact.Name, "lifecycle-observation-") {
			return nil, fmt.Errorf("%w: original lifecycle has an undeclared fixed artifact", executionlifecycle.ErrContext)
		}
	}
	if len(fixed) != 0 {
		return nil, fmt.Errorf("%w: original lifecycle fixed inventory is incomplete", executionlifecycle.ErrContext)
	}
	return capture, nil
}
