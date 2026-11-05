package releasepolicy

import (
	"encoding/json"
	"strings"
	"testing"
)

// These synthetic projections exercise defensive dimension aggregation, not
// raw-artifact admission. Production callers must use ReadDecision.
func dimensionReceipts(t *testing.T, p *Policy) map[Arm]Receipt {
	t.Helper()
	result := map[Arm]Receipt{}
	for _, arm := range []Arm{Baseline, Candidate} {
		digest := testDigest(t, "synthetic-journal")
		r := Receipt{Kind: ReceiptKind, Version: Version, State: "final", Arm: arm,
			PolicyDigest: p.Digest, PlanDigest: p.Arms[arm].Digest,
			Binding: &ResultBinding{}, JournalDigest: &digest, Usage: unknownTestUsage()}
		for _, key := range PlannedSamples(p) {
			first, retry := true, true
			r.Started = append(r.Started, key)
			r.Trials = append(r.Trials, TrialSummary{Key: key, Terminal: 1, State: "complete",
				FirstPass: &first, RetryPass: &retry})
		}
		for _, task := range p.Arms[arm].Plan.Tasks {
			r.Runtime = append(r.Runtime, RuntimeObservation{TaskID: task.ID, RequestedEngine: task.Settings.Engine,
				RequestedModel: task.Settings.Model, RequestedReasoning: task.Settings.ReasoningEffort,
				Availability: "available", EngineImplementation: "test-build", ModelVersion: "test-version"})
		}
		result[arm] = r
	}
	return result
}

func TestMonotonicMixedArmDimensions(t *testing.T) {
	for _, strong := range []Arm{Baseline, Candidate} {
		for _, malformed := range []string{"invalid_final", "missing_arm", "runtime_mismatch"} {
			t.Run(string(strong)+"/"+malformed, func(t *testing.T) {
				weak := Baseline
				if strong == Baseline {
					weak = Candidate
				}
				p := testPolicy(t, 8)
				p.Requirements.Runtime = true
				for _, side := range []Arm{Baseline, Candidate} {
					arm := p.Arms[side]
					identity := &arm.Plan.Identities[0]
					identity.Availability, identity.Digest, identity.Reason = "unavailable", nil, "Synthetic unavailable source."
					p.Arms[side] = arm
				}
				p = sealTestPolicy(t, p)
				receipts := dimensionReceipts(t, p)
				w := receipts[weak]
				w.Trials[0].State, w.Trials[0].FirstPass, w.Trials[0].RetryPass = "incomplete", nil, nil
				w.Runtime[0].Availability, w.Runtime[0].EngineImplementation, w.Runtime[0].ModelVersion = "unavailable", "", ""
				w.Runtime[0].Reason = "Synthetic unavailable runtime."
				receipts[weak] = w
				s := receipts[strong]
				wantCompatibility, wantCompleteness := "invalid", "missing"
				switch malformed {
				case "invalid_final":
					s.Binding = nil
					receipts[strong] = s
				case "missing_arm":
					delete(receipts, strong)
				case "runtime_mismatch":
					s.Runtime[0].ModelVersion = "unexpected-version"
					receipts[strong] = s
					wantCompatibility, wantCompleteness = "mismatched", "partial"
				}
				d, err := Assess(p, receipts)
				if err != nil || d.Accepted || d.Compatibility.State != wantCompatibility ||
					d.Completeness.State != wantCompleteness || d.Operations.State != "incomplete" {
					t.Fatalf("severity lowered by arm order: %+v %v", d, err)
				}
				compatibility := strings.Join(d.Compatibility.Reasons, "\n")
				if !strings.Contains(compatibility, string(strong)) || !strings.Contains(compatibility, string(weak)+" required") ||
					!strings.Contains(compatibility, "required observed runtime is unavailable") {
					t.Fatalf("strong or weaker diagnostic lost: %s", compatibility)
				}
				if !strings.Contains(strings.Join(d.Completeness.Reasons, "\n"), string(weak)+"/task-0") {
					t.Fatalf("partial evidence diagnostic lost: %+v", d.Completeness)
				}
			})
		}
	}
}

func TestMonotonicGoldenAndBillingDimensions(t *testing.T) {
	for _, strong := range []Arm{Baseline, Candidate} {
		for _, condition := range []string{"golden_failed", "billing_exceeded", "billing_invalid"} {
			t.Run(string(strong)+"/"+condition, func(t *testing.T) {
				weak := Baseline
				if strong == Baseline {
					weak = Candidate
				}
				p := testPolicy(t, 8)
				p.GoldenIDs = []string{"task-0"}
				p.Requirements.Billing = []BillingRequirement{{Axis: "ai_credits", Maximum: "1"}}
				p = sealTestPolicy(t, p)
				receipts := dimensionReceipts(t, p)
				s, w := receipts[strong], receipts[weak]
				switch condition {
				case "golden_failed":
					failed := false
					s.Trials[0].FirstPass = &failed
					w.Trials[0].FirstPass, w.Trials[0].RetryPass, w.Trials[0].State = nil, nil, "incomplete"
				default:
					value := json.Number("2")
					if condition == "billing_invalid" {
						value = "not-a-number"
					}
					s.Usage[2] = UsageAxis{Axis: "ai_credits", Availability: "available", Value: &value,
						Observation: "final_complete_attributable"}
				}
				receipts[strong], receipts[weak] = s, w
				d, err := Assess(p, receipts)
				if err != nil || d.Accepted {
					t.Fatalf("mixed evidence strict-passed: %+v %v", d, err)
				}
				dimension, want := d.Billing, "exceeded"
				switch condition {
				case "golden_failed":
					dimension, want = d.Golden, "failed"
				case "billing_invalid":
					want = "invalid"
				}
				reasons := strings.Join(dimension.Reasons, "\n")
				if dimension.State != want || !strings.Contains(reasons, string(strong)+"/") ||
					!strings.Contains(reasons, string(weak)+"/") {
					t.Fatalf("strong condition or arm diagnostic lost: %+v", dimension)
				}
			})
		}
	}
}

func TestRuntimeDiagnosticsRetainBothArms(t *testing.T) {
	p := testPolicy(t, 8)
	receipts := dimensionReceipts(t, p)
	for _, arm := range []Arm{Baseline, Candidate} {
		r := receipts[arm]
		r.Runtime[0].RequestedModel = "unbound-model"
		receipts[arm] = r
	}
	d, err := Assess(p, receipts)
	reasons := strings.Join(d.Compatibility.Reasons, "\n")
	if err != nil || d.Accepted || d.Compatibility.State != "mismatched" ||
		!strings.Contains(reasons, "baseline/task-0") || !strings.Contains(reasons, "candidate/task-0") {
		t.Fatalf("runtime diagnostics lost an arm: %+v %v", d, err)
	}
}

func TestDimensionSeverityRejectsUnregisteredState(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("unregistered internal state silently got a priority")
		}
	}()
	dimensionSeverity("unregistered")
}
