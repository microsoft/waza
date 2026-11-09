package runnerunit

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
)

func TestHistoricalSourceAndSnapshotPreserveLargeObservationNumbers(t *testing.T) {
	backend := submissionBackend(t)
	request := fixture(t)
	service := NewService(nil)
	var report Report
	original, err := service.Submit(t.Context(), submissionWire(t, request, nil), backend, SubmissionHooks{
		Artifacts: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, snapshot SubmissionSnapshot) ([]SubmissionArtifact, error) {
			report = snapshot.ObservedReport
			return nil, nil
		},
	})
	if err != nil || report.Observation == nil || len(report.Observation.Validations) == 0 {
		t.Fatalf("fixture did not produce actual grader observations: %+v %v", original, err)
	}
	const large = "9007199254740993"
	for name, grade := range report.Observation.Validations {
		grade.Details = map[string]any{"large": json.Number(large)}
		report.Observation.Validations[name] = grade
	}
	raw, err := reportBytes(report)
	if err != nil || !bytes.Contains(raw, []byte(large)) {
		t.Fatalf("historical fixture lost authored numeric token: %s %v", raw, err)
	}
	detached, err := cloneSubmissionReport(report, raw)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := reportBytes(detached)
	if err != nil || !bytes.Equal(raw, copied) {
		t.Errorf("capture snapshot changed numeric evidence: %s %v", copied, err)
	}
	source := publishSubmissionReport(t, backend, original, raw)
	request.Selection.Attempt++
	reused, err := service.Submit(t.Context(), submissionWire(t, request, &SubmissionPolicy{Mode: "reuse", Source: &source}), backend, SubmissionHooks{})
	if err != nil || reused.Status != "reused" || reused.EngineCalls != 0 {
		t.Fatalf("valid exact historical report could not be reused: %+v %v", reused, err)
	}
	reused.Report = raw
	if _, err := EncodeSubmissionResult(reused); err != nil {
		t.Fatalf("receipt rejected valid exact historical report: %v", err)
	}
}

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
