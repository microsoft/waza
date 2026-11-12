package runnerunit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/microsoft/waza/internal/models"
)

type exactSubmissionObservation models.RunResult

func (observation *exactSubmissionObservation) UnmarshalJSON(data []byte) error {
	return (*models.RunResult)(observation).UnmarshalJSONExact(data)
}

func decodeSubmissionReport(data []byte) (Report, error) {
	type fields Report
	var wire struct {
		fields
		Observation *exactSubmissionObservation `json:"observation"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&wire); err != nil {
		return Report{}, err
	}
	report := Report(wire.fields)
	report.Observation = (*models.RunResult)(wire.Observation)
	return report, nil
}

func cloneSubmissionReport(original Report, canonical []byte) (Report, error) {
	detached, err := decodeSubmissionReport(canonical)
	if err != nil {
		return Report{}, fmt.Errorf("copying observed submission report: %w", err)
	}
	detached.failure, detached.boundary = original.failure, original.boundary
	if original.binding != nil {
		binding := *original.binding
		binding.GraderIDs = slices.Clone(original.binding.GraderIDs)
		if original.binding.Identity != nil {
			identity := *original.binding.Identity
			binding.Identity = &identity
		}
		detached.binding = &binding
	}
	// The legacy writer omits empty event arrays. Retain the actual producer's
	// known-empty versus unavailable distinction only in this typed snapshot.
	if original.Observation != nil && detached.Observation != nil {
		if original.Observation.Transcript != nil && len(original.Observation.Transcript) == 0 {
			detached.Observation.Transcript = []models.TranscriptEvent{}
		}
		if original.Observation.ToolEvents != nil && len(original.Observation.ToolEvents) == 0 {
			detached.Observation.ToolEvents = []models.ToolEvent{}
		}
	}
	return detached, nil
}
