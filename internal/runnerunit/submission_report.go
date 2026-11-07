package runnerunit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/microsoft/waza/internal/models"
)

func cloneSubmissionReport(original Report, canonical []byte) (Report, error) {
	var detached Report
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	if err := decoder.Decode(&detached); err != nil {
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
