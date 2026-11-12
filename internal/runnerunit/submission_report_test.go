package runnerunit

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func BenchmarkSubmitReportCapture(b *testing.B) {
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	if err != nil {
		b.Fatal(err)
	}
	request, err := runnercontract.Parse(raw)
	if err != nil {
		b.Fatal(err)
	}
	request.Input.Task = strings.Replace(request.Input.Task, "hello", strings.Repeat("hello", 32<<10), 1)
	raw, err = json.Marshal(request)
	if err != nil {
		b.Fatal(err)
	}
	wire, err := json.Marshal(SubmissionRequest{Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw})
	if err != nil {
		b.Fatal(err)
	}
	for _, capture := range []bool{false, true} {
		name := "terminal-only"
		if capture {
			name = "detached-report"
		}
		b.Run(name, func(b *testing.B) {
			backend, err := artifactmanifest.OpenLocal(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := backend.Close(); err != nil {
					b.Error(err)
				}
			})
			hooks := SubmissionHooks{}
			if capture {
				hooks.Artifacts = func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, snapshot SubmissionSnapshot) ([]SubmissionArtifact, error) {
					if len(snapshot.Report) < 160<<10 || snapshot.ObservedReport.Observation == nil {
						b.Fatal("benchmark did not capture the actual large report")
					}
					return nil, nil
				}
			}
			if _, err := NewService(nil).Submit(b.Context(), wire, backend, hooks); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				result, err := NewService(nil).Submit(b.Context(), wire, backend, hooks)
				if err != nil || result.Status != "completed" || result.EngineCalls != 1 || len(result.Report) != 0 {
					b.Fatalf("actual submission failed or disclosed raw report: %+v %v", result, err)
				}
			}
		})
	}
}

func TestHistoricalSourceRequiresOriginalSelectionAndAdmission(t *testing.T) {
	for _, control := range []string{"requested-task", "requested-attempt", "missing-requested", "invalid-validation", "incompatible", "unsupported-negotiation-version", "adjacent-work", "valid-alternate-ordinal"} {
		t.Run(control, func(t *testing.T) {
			backend := submissionBackend(t)
			request := fixture(t)
			var report Report
			original, err := NewService(nil).Submit(t.Context(), submissionWire(t, request, nil), backend, SubmissionHooks{
				Artifacts: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, snapshot SubmissionSnapshot) ([]SubmissionArtifact, error) {
					report = snapshot.ObservedReport
					return nil, nil
				},
			})
			if err != nil || report.Requested == nil || report.Validation != "valid" ||
				report.Compatibility.Status != "compatible" || report.AdjacentWork {
				t.Fatalf("actual source admission control failed: %+v %v", report, err)
			}
			base, err := reportBytes(report)
			if err != nil {
				t.Fatal(err)
			}
			switch control {
			case "requested-task":
				report.Requested.Task = "other-task"
			case "requested-attempt":
				report.Requested.Attempt++
			case "missing-requested":
				report.Requested = nil
			case "invalid-validation":
				report.Validation = "invalid"
			case "incompatible":
				report.Compatibility.Status = "unsupported_version"
			case "unsupported-negotiation-version":
				report.Compatibility.Negotiation.Version = "2.0"
			case "adjacent-work":
				report.AdjacentWork = true
			}
			raw, err := reportBytes(report)
			if err != nil || (control != "valid-alternate-ordinal" && bytes.Equal(raw, base)) {
				t.Fatal("historical mutation did not change actual report")
			}
			source := publishSubmissionReport(t, backend, original, raw)
			records, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
			if err != nil {
				t.Fatal(err)
			}
			before, truncated, err := records.ListUnverified(t.Context())
			if err != nil || truncated {
				t.Fatalf("decision inventory control failed: %v %v", truncated, err)
			}
			request.Selection.Attempt++
			reused, reuseErr := NewService(nil).Submit(t.Context(), submissionWire(t, request, &SubmissionPolicy{Mode: "reuse", Source: &source}), backend, SubmissionHooks{})
			if control == "valid-alternate-ordinal" {
				if reuseErr != nil || reused.Status != "reused" || reused.EngineCalls != 0 ||
					reused.Terminal.Selection.Attempt == request.Selection.Attempt {
					t.Fatalf("valid historical source lost original ordinal: %+v %v", reused, reuseErr)
				}
			} else {
				if reuseErr == nil || reused.Decision != nil || reused.Status != "submission_error" ||
					len(reused.Diagnostics) != 1 || reused.Diagnostics[0].Code != "source_report_invalid" {
					t.Errorf("invalid historical admission became actionable reuse: %+v %v", reused, reuseErr)
				}
				after, truncated, err := records.ListUnverified(t.Context())
				if err != nil || truncated || len(after) != len(before) {
					t.Errorf("invalid source appended a reuse decision: before=%d after=%d %v", len(before), len(after), err)
				}
			}
			receipt, err := NewService(nil).Submit(t.Context(), submissionWire(t, fixture(t), nil), backend, SubmissionHooks{})
			if err != nil {
				t.Fatal(err)
			}
			receipt.Report = raw
			encoded, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := ParseSubmissionResult(encoded)
			if control == "valid-alternate-ordinal" {
				if err != nil {
					t.Fatalf("valid historical receipt rejected: %v", err)
				}
			} else if parsed != nil || err == nil {
				t.Errorf("receipt decoder accepted invalid historical admission: %+v %v", parsed, err)
			}
		})
	}
}

func TestHistoricalSourceRejectsExecutionModeMismatchWithoutObservation(t *testing.T) {
	backend := submissionBackend(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var captured Report
	var terminal runnerterminal.Envelope
	result, err := NewService(nil).Submit(ctx, submissionWire(t, fixture(t), nil), backend, SubmissionHooks{
		Reserved: func(context.Context, *executionledger.Ledger, executionledger.Creator, executionledger.Reservation) error {
			cancel()
			return nil
		},
		Artifacts: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, snapshot SubmissionSnapshot) ([]SubmissionArtifact, error) {
			captured, terminal = snapshot.ObservedReport, snapshot.Terminal
			return nil, nil
		},
	})
	if err != nil || result.Reservation == nil || captured.Observation != nil || terminal.Observation != nil {
		t.Fatalf("fixture did not produce an admitted pre-runtime terminal: %+v %v", result, err)
	}
	captured.Mode = "copilot"
	data, err := reportBytes(captured)
	if err != nil {
		t.Fatal(err)
	}
	source := &executionledger.CompletedExecution{
		Reservation: executionledger.Reservation{Attempt: *result.Reservation},
	}
	if err := validateSourceReport(data, source, &terminal); err == nil || !strings.Contains(err.Error(), "execution mode") {
		t.Fatalf("pre-runtime source report with a different execution mode was admitted: %v", err)
	}
}

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
	report.Observation.Transcript = []models.TranscriptEvent{{
		SessionEvent: copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{
			ToolCallID: "numeric", ToolName: "numeric",
			Arguments: map[string]any{"nested": []any{json.Number(large)}},
		}},
	}}
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
