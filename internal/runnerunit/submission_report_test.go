package runnerunit

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func TestSubmissionReportSnapshotPreservesProducerPresenceAndOriginalBinding(t *testing.T) {
	for _, control := range []string{"unavailable", "known-empty", "no-observation"} {
		t.Run(control, func(t *testing.T) {
			report, input := admit(wire(t, fixture(t)))
			if input == nil || report.binding == nil {
				t.Fatal("actual admission did not retain original binding")
			}
			originalIdentity := *report.binding.Identity
			switch control {
			case "unavailable":
				report.Observation = &models.RunResult{}
			case "known-empty":
				zero := 0.0
				report.Observation = &models.RunResult{
					Transcript: []models.TranscriptEvent{}, ToolEvents: []models.ToolEvent{},
					Validations: map[string]models.GraderResults{}, Usage: &models.UsageStats{AICredits: &zero},
				}
			}
			data, err := reportBytes(report)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte(`"transcript":[]`)) || bytes.Contains(data, []byte(`"tool_events":[]`)) {
				t.Fatal("snapshot work changed legacy raw writer omission behavior")
			}
			copy, err := cloneSubmissionReport(report, data)
			if err != nil {
				t.Fatal(err)
			}
			if (copy.Observation == nil) != (report.Observation == nil) || copy.binding == report.binding ||
				copy.binding.Identity == report.binding.Identity || *copy.binding.Identity != originalIdentity {
				t.Fatal("snapshot lost producer presence or aliased original comparison binding")
			}
			if report.Observation != nil {
				if (copy.Observation.Transcript == nil) != (report.Observation.Transcript == nil) ||
					(copy.Observation.ToolEvents == nil) != (report.Observation.ToolEvents == nil) ||
					(copy.Observation.Validations == nil) != (report.Observation.Validations == nil) ||
					(copy.Observation.Usage == nil) != (report.Observation.Usage == nil) {
					t.Fatal("snapshot invented empty or lost explicit producer evidence")
				}
				if control == "known-empty" {
					if copy.Observation.Usage.AICredits == nil || *copy.Observation.Usage.AICredits != 0 {
						t.Fatal("recorded zero credits became unavailable")
					}
					*copy.Observation.Usage.AICredits = 1
					copy.Observation.Validations["mutated"] = models.GraderResults{}
					if *report.Observation.Usage.AICredits != 0 || len(report.Observation.Validations) != 0 {
						t.Fatal("callback mutation affected original report")
					}
				}
			}
			copy.binding.Identity.Run = "callback-mutated"
			if *report.binding.Identity != originalIdentity {
				t.Fatal("callback mutated original retained binding")
			}
			if _, err := json.Marshal(copy); err != nil {
				t.Fatal(err)
			}
		})
	}
}
