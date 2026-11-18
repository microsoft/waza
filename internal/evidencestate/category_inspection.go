package evidencestate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
)

// CategoryEvidence reports recorded shape/completeness independently of both
// declared state and recorded verdict. Present alone is never attestation.
type CategoryEvidence struct {
	Category      Category           `json:"category"`
	State         State              `json:"state"`
	DeclaredState State              `json:"declared_state,omitempty"`
	Verification  string             `json:"verification"`
	Reason        string             `json:"reason,omitempty"`
	ObservedCount *int64             `json:"observed_count,omitempty"`
	Usage         *models.UsageStats `json:"usage,omitempty"`
}

// InspectCategories admits every category using the same captured source pass.
// A declaration without the producer index remains explicitly unqualified.
// It never returns captured text/tool/grader payloads to a public consumer.
func InspectCategories(ctx context.Context, backend artifactmanifest.Backend, digest string) (Inspection, []CategoryEvidence, error) {
	inspection, source, err := inspectEvidence(ctx, backend, digest)
	results := make([]CategoryEvidence, 0, 6)
	if inspection.Document == nil {
		for _, category := range []Category{Conversation, Tool, Grader, Reliability, Usage, Provenance} {
			results = append(results, CategoryEvidence{
				Category: category, State: Unavailable, Verification: "unverified", Reason: "source_not_verified",
			})
		}
		return inspection, results, err
	}
	for _, record := range inspection.Document.Categories {
		results = append(results, CategoryEvidence{
			Category: record.Category, State: Unavailable, DeclaredState: record.State,
			Verification: "declared-state-only", Reason: "category_not_verified",
		})
	}
	if err != nil {
		return inspection, results, err
	}
	data, exists := source.ArtifactBytes[CaptureArtifactName]
	if !exists {
		return inspection, results, nil
	}
	mediaType := ""
	for _, artifact := range source.Manifest.Artifacts {
		if artifact.Name == CaptureArtifactName {
			mediaType = artifact.MediaType
		}
	}
	if mediaType != "application/json" {
		return inspection, results, fmt.Errorf("%w: capture index requires application/json", ErrInvalid)
	}
	capture, err := ParseCapture(data)
	if err != nil {
		return inspection, results, err
	}
	canonical, err := EncodeCapture(*capture)
	if err != nil {
		return inspection, results, err
	}
	if !bytes.Equal(data, canonical) {
		return inspection, results, fmt.Errorf("%w: capture index is not canonical", ErrInvalid)
	}
	if err := verifyCaptureIndex(capture, inspection.Document, source); err != nil {
		return inspection, results, err
	}
	terminal, err := runnerterminal.Validate(source.ArtifactBytes[capture.TerminalArtifact], capture.Binding.TerminalBinding())
	if err != nil {
		return inspection, results, err
	}
	var failures []error
	for i, record := range inspection.Document.Categories {
		result := &results[i]
		result.State, result.Reason = record.State, string(record.Reason)
		result.Verification = "source-content-and-recorded-category-shape-only"
		if record.State == Unavailable || record.State == Redacted {
			continue
		}
		if record.Category == Provenance {
			result.ObservedCount = record.ObservedCount
			continue
		}
		if record.Category == Usage {
			usage, usageErr := admitUsage(inspection.Document, source)
			result.State, result.Verification, result.Reason, result.Usage = usage.State, usage.Verification, usage.Reason, usage.Usage
			if usageErr != nil {
				failures = append(failures, usageErr)
			}
			if usage.Usage != nil {
				result.ObservedCount = record.ObservedCount
			}
			continue
		}
		if record.Category == Reliability {
			if record.State != Present || record.ObservedCount == nil || *record.ObservedCount != 1 ||
				len(record.Artifacts) != 1 || record.Artifacts[0] != capture.TerminalArtifact {
				result.State, result.Reason = Unavailable, "reliability_not_verified"
				failures = append(failures, fmt.Errorf("%w: reliability requires the original terminal", ErrInvalid))
			} else {
				result.ObservedCount = record.ObservedCount
			}
			continue
		}
		var payload []byte
		if record.State == Present && record.ObservedCount != nil && *record.ObservedCount == 0 {
			payload = capture.EmptyPayloads[record.Category]
		} else if len(record.Artifacts) == 1 {
			name := record.Artifacts[0]
			if name == capture.TerminalArtifact || name == CaptureArtifactName {
				result.State, result.Reason = Unavailable, "category_payload_alias"
				failures = append(failures, fmt.Errorf("%w: category aliases control metadata", ErrInvalid))
				continue
			}
			for _, a := range source.Manifest.Artifacts {
				if a.Name == name && a.MediaType == "application/json" {
					payload = source.ArtifactBytes[name]
				}
			}
		}
		if len(payload) == 0 {
			// A measured cap may deliberately withhold bytes. It does not
			// prove the declaration's claimed count or full original total.
			if record.State == Incomplete && record.Limit != nil && len(record.Artifacts) == 0 {
				result.Verification = "recorded-degradation-only"
				continue
			}
			result.State, result.Reason = Unavailable, "category_payload_missing"
			failures = append(failures, fmt.Errorf("%w: category payload is missing", ErrInvalid))
			continue
		}
		state, count, shapeErr := categoryShape(record.Category, payload, terminal, capture.Binding.TerminalBinding())
		if shapeErr != nil {
			result.State, result.Reason = Unavailable, "category_shape_invalid"
			failures = append(failures, shapeErr)
			continue
		}
		if record.ObservedCount == nil || count != *record.ObservedCount || state != record.State {
			result.State, result.Reason = Conflicting, "category_state_or_count_conflict"
			failures = append(failures, fmt.Errorf("%w: recorded category count/state conflict", artifactmanifest.ErrConflict))
			continue
		}
		result.State, result.ObservedCount = state, record.ObservedCount
	}
	slices.SortFunc(results, func(a, b CategoryEvidence) int {
		if a.Category < b.Category {
			return -1
		}
		if a.Category > b.Category {
			return 1
		}
		return 0
	})
	return inspection, results, errors.Join(failures...)
}
