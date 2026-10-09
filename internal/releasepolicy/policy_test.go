package releasepolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
)

func testDigest(t *testing.T, value any) models.EvidenceDigest {
	t.Helper()
	digest, err := evidence.JSONDigest(value)
	if err != nil {
		t.Fatal(err)
	}
	return *digest
}

func testPolicy(t *testing.T, count int) *Policy {
	t.Helper()
	p := &Policy{Kind: PolicyKind, Version: Version, GoldenRule: "both_arms_first_attempt_pass",
		FamilyRule: "single_contrast_external_family_bound", Changes: []AllowedChange{},
		GoldenIDs: []string{}, Requirements: Requirements{IdentityDomains: append(append([]string{}, taskDomains...), suiteDomains...), Billing: []BillingRequirement{}},
		Design: Design{Estimand: "fixed_suite_repeated_execution", Endpoint: "first_attempt_pass",
			Alpha: "0.05", FamilySize: 1, MaximumHalfWidth: "1", Margin: "1", Accept: "noninferiority",
			Independence: Independence{Assessment: "justified", Justification: "Independent synthetic test clusters",
				Limitations: []string{"Synthetic tests exercise admission only, not live quality claims."}},
			Allocation: Allocation{Mechanism: "sha256_seed_cluster_first_bit", Seed: strings.Repeat("ab", 32)}},
		Arms: map[Arm]PolicyArm{}}
	plan := ResolvedPlan{Kind: PlanKind, Version: Version, Tasks: []TaskPlan{}, Identities: []Identity{}}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("task-%d", i)
		cid := fmt.Sprintf("cluster-%d", i)
		p.Design.Clusters = append(p.Design.Clusters, Cluster{ID: cid,
			Weight: json.Number(strconv.FormatFloat(1/float64(count), 'g', -1, 64)),
			Tasks:  []PlannedTask{{ID: id, Weight: "1", TrialOrdinals: []int{1, 2}}}})
		order, err := ArmOrder(p.Design.Allocation.Seed, cid)
		if err != nil {
			t.Fatal(err)
		}
		p.Design.Allocation.Assignments = append(p.Design.Allocation.Assignments, Assignment{ClusterID: cid, Order: order})
		plan.Tasks = append(plan.Tasks, TaskPlan{ID: id, Settings: Settings{Engine: "test-engine",
			Model: "test-model", MaxAttempts: 2, TrialsPerTask: 2, TimeoutSeconds: 60, OtherSettingsDigest: testDigest(t, map[string]string{})},
			ExpectedRuntime: ExpectedRuntime{Availability: "expected", EngineImplementation: "test-build", ModelVersion: "test-version"}})
		for _, domain := range taskDomains {
			digest := testDigest(t, map[string]string{"test_task": id, "domain": domain})
			plan.Identities = append(plan.Identities, Identity{Domain: domain, TaskID: id, Availability: "available", Digest: &digest})
		}
	}
	for _, domain := range suiteDomains {
		digest := testDigest(t, map[string]string{"test_suite_domain": domain})
		availability := "available"
		if domain != "grader_implementation" {
			var err error
			digest, err = EmptyIdentity(domain)
			if err != nil {
				t.Fatal(err)
			}
			availability = "not_applicable"
		}
		plan.Identities = append(plan.Identities, Identity{Domain: domain, Availability: availability, Digest: &digest})
	}
	digest := testDigest(t, plan)
	p.Arms[Baseline], p.Arms[Candidate] = PolicyArm{Plan: plan, Digest: digest}, PolicyArm{Plan: plan, Digest: digest}
	return sealTestPolicy(t, p)
}

func sealTestPolicy(t *testing.T, p *Policy) *Policy {
	t.Helper()
	for _, arm := range []Arm{Baseline, Candidate} {
		a := p.Arms[arm]
		a.Digest = testDigest(t, a.Plan)
		p.Arms[arm] = a
	}
	data, err := SealJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	var result Policy
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return &result
}

func testBytes(t *testing.T, p *Policy) []byte {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testCollector(p *Policy) Collector {
	return Collector{
		Initialize: func(context.Context) error { return nil },
		Shutdown:   func(context.Context) error { return nil },
		Usage: func(context.Context, Arm) ([]UsageAxis, error) {
			return unknownTestUsage(), nil
		},
		Attempt: func(_ context.Context, key AttemptKey) (AttemptObservation, error) {
			s := settingsFor(p, key.Arm, key.TaskID)
			summary := AttemptSummary{Key: key, Origin: models.EvidenceOrigin{EvalID: key.EvalID, TaskID: key.TaskID,
				RunNumber: key.Trial, AttemptCount: key.Attempt},
				Status: "passed", Category: "behavioral",
				Checks:     []CheckSummary{{Scope: "task", Grader: "test-check", Passed: true, Score: "1", OperationalState: "observed"}},
				References: []models.EvidenceReference{},
				Runtime: RuntimeObservation{TaskID: key.TaskID, RequestedEngine: s.Engine, RequestedModel: s.Model,
					RequestedReasoning: s.ReasoningEffort, Availability: "available",
					EngineImplementation: "test-build", ModelVersion: "test-version"}}
			return syntheticObservation(summary)
		},
	}
}

func syntheticObservation(a AttemptSummary) (AttemptObservation, error) {
	run := models.RunResult{RunNumber: a.Key.Trial, Attempts: a.Key.Attempt, Status: models.Status(a.Status),
		Validations: map[string]models.GraderResults{}}
	switch a.Category {
	case "operational":
		run.Status, run.ErrorMsg = models.StatusError, "Synthetic operational failure."
	case "unknown":
		run.Status = models.StatusPassed
	}
	for _, c := range a.Checks {
		score, err := c.Score.Float64()
		if err != nil {
			return AttemptObservation{}, err
		}
		run.Validations[c.Grader] = models.GraderResults{Name: c.Grader, Passed: c.Passed, Score: score}
	}
	return AttemptObservation{Summary: a, Result: ActualRunRow{Origin: a.Origin, Run: run}}, nil
}

func unknownTestUsage() []UsageAxis {
	usage := []UsageAxis{}
	for _, axis := range []string{"input_tokens", "output_tokens", "ai_credits"} {
		usage = append(usage, UsageAxis{Axis: axis, Availability: "unavailable", Observation: "unknown",
			Reason: "Synthetic test has no billing observation."})
	}
	return usage
}

func collectTest(t *testing.T, p *Policy, collector Collector) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("new private durable collection is unsupported on this platform")
	}
	directory := filepath.Join(t.TempDir(), "collection")
	if err := Collect(t.Context(), testBytes(t, p), directory, collector); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestPolicyUniformAllocations(t *testing.T) {
	for _, n := range []int{3, 12, 738} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			p := testPolicy(t, n)
			if _, err := DecodePolicy(testBytes(t, p)); err != nil {
				t.Fatal(err)
			}
			p.Design.Clusters = p.Design.Clusters[:n-1]
			p.Design.Allocation.Assignments = p.Design.Allocation.Assignments[:n-1]
			if err := ValidatePolicy(p); err == nil {
				t.Fatal("omitted cluster must not be normalized away")
			}
		})
	}
}

func TestPolicyStrictAdmission(t *testing.T) {
	p := testPolicy(t, 8)
	good := testBytes(t, p)
	for _, tc := range []struct{ name, data string }{
		{"duplicate", strings.Replace(string(good), `"version":"1.0"`, `"version":"1.0","version":"1.0"`, 1)},
		{"trailing", string(good) + `{}`},
		{"unknown", strings.Replace(string(good), `"alpha":"0.05"`, `"alpha":"0.05","unknown":true`, 1)},
		{"tamper", strings.Replace(string(good), `"alpha":0.05`, `"alpha":0.06`, 1)},
		{"token", strings.Replace(string(good), `"alpha":0.05`, `"alpha":0.050`, 1)},
		{"version", strings.Replace(string(good), `"version":"1.0"`, `"version":"2.0"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := tc.data
			if tc.name == "unknown" {
				data = strings.Replace(string(good), `"alpha":0.05`, `"alpha":0.05,"unknown":true`, 1)
			}
			if data == string(good) {
				t.Fatal("test mutation did not occur")
			}
			if _, err := DecodePolicy([]byte(data)); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
}

func TestPolicyDrift(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Policy)
	}{
		{"task timeout", func(p *Policy) {
			a := p.Arms[Candidate]
			a.Plan.Tasks[0].Settings.TimeoutSeconds++
			p.Arms[Candidate] = a
		}},
		{"undeclared model", func(p *Policy) {
			a := p.Arms[Candidate]
			a.Plan.Tasks[0].Settings.Model = "other"
			p.Arms[Candidate] = a
		}},
		{"unknown allowed field", func(p *Policy) {
			p.Changes = []AllowedChange{{TaskID: "task-0", Field: "timeout_seconds", Before: "60", After: "61"}}
		}},
		{"unknown required domain", func(p *Policy) { p.Requirements.IdentityDomains = []string{"unknown"} }},
		{"missing golden", func(p *Policy) { p.GoldenIDs = []string{"absent"} }},
		{"duplicate golden", func(p *Policy) { p.GoldenIDs = []string{"task-0", "task-0"} }},
		{"zero alpha", func(p *Policy) { p.Design.Alpha = "0" }},
		{"hidden weight precision", func(p *Policy) { p.Design.Clusters[0].Weight = "0.1250000000000000000000000001" }},
		{"post hoc trials", func(p *Policy) { p.Design.Clusters[0].Tasks[0].TrialOrdinals = []int{1, 3} }},
		{"order", func(p *Policy) { p.Design.Allocation.Assignments[0].Order = []Arm{Baseline, Baseline} }},
		{"unknown applicability", func(p *Policy) {
			a := p.Arms[Candidate]
			a.Plan.Identities[0].Availability = "not_applicable"
			p.Arms[Candidate] = a
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testPolicy(t, 8)
			tc.edit(p)
			p = sealTestPolicy(t, p)
			if _, err := DecodePolicy(testBytes(t, p)); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	p := testPolicy(t, 8)
	a := p.Arms[Candidate]
	a.Plan.Tasks[0].Settings.Model = "changed"
	a.Plan.Tasks[0].ExpectedRuntime.ModelVersion = "changed-version"
	p.Arms[Candidate] = a
	p.Changes = []AllowedChange{{TaskID: "task-0", Field: "model", Before: "test-model", After: "changed"}}
	p = sealTestPolicy(t, p)
	if _, err := DecodePolicy(testBytes(t, p)); err != nil {
		t.Fatalf("exact declared model/runtime change must be usable: %v", err)
	}
}

func TestUsageAxes(t *testing.T) {
	for _, tc := range []struct {
		axis, value, observation, availability, currency string
		wantError                                        bool
	}{
		{"input_tokens", "0", "final_complete_attributable", "available", "", false},
		{"output_tokens", "9007199254740993", "final_complete_attributable", "available", "", false},
		{"input_tokens", "1.0", "final_complete_attributable", "available", "", true},
		{"input_tokens", "1e3", "final_complete_attributable", "available", "", true},
		{"input_tokens", "-1", "final_complete_attributable", "available", "", true},
		{"ai_credits", "0.125", "final_complete_attributable", "available", "", false},
		{"ai_credits", "0", "partial", "available", "", false},
		{"provider_currency", "0.25", "final_complete_attributable", "available", "USD", false},
		{"provider_currency", "0", "final_complete_attributable", "available", "", true},
		{"ai_credits", "0", "final_complete_attributable", "available", "USD", true},
		{"ai_credits", "0", "unknown", "unavailable", "", true},
		{"unknown", "0", "final_complete_attributable", "available", "", true},
	} {
		t.Run(fmt.Sprint(tc), func(t *testing.T) {
			value := json.Number(tc.value)
			err := ValidateUsage(UsageAxis{Axis: tc.axis, Value: &value, Observation: tc.observation,
				Availability: tc.availability, Currency: tc.currency})
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, expected error=%v", err, tc.wantError)
			}
		})
	}
}

func TestCollectionAndAssessment(t *testing.T) {
	p := testPolicy(t, 8)
	directory := collectTest(t, p, testCollector(p))
	d, err := ReadDecision(directory)
	if err != nil || !d.Accepted || d.Statistics.State != "noninferiority" {
		t.Fatalf("decision=%+v err=%v", d, err)
	}
	if d.HalfWidth == nil || *d.HalfWidth > 1 || d.PlannedClusters != 8 {
		t.Fatal("wrong precision or independent sample unit")
	}
	// Exclusive collection must not resume or overwrite an existing directory.
	initialized := false
	c := testCollector(p)
	c.Initialize = func(context.Context) error { initialized = true; return nil }
	if err := Collect(t.Context(), testBytes(t, p), directory, c); err == nil || initialized {
		t.Fatal("existing collection must reject before engines start")
	}
}

func TestIncompleteAndSelectedRequiredDimensions(t *testing.T) {
	for _, name := range []string{"operational", "unknown", "golden_recovery", "billing", "partial_billing", "assurance", "independence", "precision"} {
		t.Run(name, func(t *testing.T) {
			p := testPolicy(t, 8)
			if name == "golden_recovery" {
				p.Design.Endpoint = "retry_policy_pass"
				p.GoldenIDs = []string{"task-0"}
			}
			if name == "billing" || name == "partial_billing" {
				p.Requirements.Billing = []BillingRequirement{{Axis: "ai_credits", Maximum: "1"}}
			}
			if name == "assurance" {
				p.Requirements.Assurance = true
			}
			if name == "independence" {
				p.Design.Independence.Assessment = "unjustified"
			}
			if name == "precision" {
				p.Design.MaximumHalfWidth = "0.1"
			}
			p = sealTestPolicy(t, p)
			collector := testCollector(p)
			original := collector.Attempt
			collector.Attempt = func(ctx context.Context, key AttemptKey) (AttemptObservation, error) {
				observation, err := original(ctx, key)
				if err != nil {
					return observation, err
				}
				a := observation.Summary
				if key.TaskID == "task-0" && key.Attempt == 1 {
					switch name {
					case "operational", "unknown":
						a.Status, a.Category = "incomplete", name
					case "golden_recovery":
						a.Status = "failed"
						a.Checks[0].Passed, a.Checks[0].Score = false, "0"
					}
				}
				return syntheticObservation(a)
			}
			if name == "partial_billing" {
				collector.Usage = func(context.Context, Arm) ([]UsageAxis, error) {
					u := unknownTestUsage()
					value := json.Number("0")
					u[2] = UsageAxis{Axis: "ai_credits", Availability: "available", Value: &value, Observation: "partial"}
					return u, nil
				}
			}
			directory := collectTest(t, p, collector)
			d, err := ReadDecision(directory)
			if err != nil || d.Accepted {
				t.Fatalf("decision=%+v err=%v", d, err)
			}
			states := map[string]string{
				"operational": d.Operations.State, "unknown": d.Operations.State,
				"golden_recovery": d.Golden.State, "billing": d.Billing.State,
				"partial_billing": d.Billing.State, "assurance": d.Assurance.State,
				"independence": d.Statistics.State, "precision": d.Statistics.State,
			}
			want := map[string]string{"operational": "incomplete", "unknown": "incomplete", "golden_recovery": "failed",
				"billing": "unavailable", "partial_billing": "unavailable", "assurance": "not_assessed",
				"independence": "inconclusive", "precision": "underpowered"}
			if states[name] != want[name] {
				t.Fatalf("state=%s want=%s", states[name], want[name])
			}
		})
	}
}

func TestPrecollectionAndCrashBoundaries(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("durable collection is unsupported")
	}
	p := testPolicy(t, 8)
	directory := filepath.Join(t.TempDir(), "collection")
	c := testCollector(p)
	c.Initialize = func(context.Context) error {
		for _, name := range []string{"policy.json", "baseline.begin.json", "candidate.begin.json", "journal.ndjson"} {
			if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
				t.Fatalf("engine start preceded durable artifact %s: %v", name, err)
			}
		}
		return nil
	}
	c.Attempt = func(_ context.Context, key AttemptKey) (AttemptObservation, error) {
		data, err := os.ReadFile(filepath.Join(directory, "journal.ndjson"))
		if err != nil || !strings.Contains(string(data), `"type":"attempt_start"`) {
			t.Fatal("attempt executed without persisted start")
		}
		return AttemptObservation{}, errors.New("simulated interruption")
	}
	if err := Collect(t.Context(), testBytes(t, p), directory, c); err == nil {
		t.Fatal("interrupted collection must fail")
	}
	d, err := ReadDecision(directory)
	if err == nil || d.Accepted || d.Completeness.State != "partial" {
		t.Fatalf("interrupted decision=%+v err=%v", d, err)
	}
	order := p.Design.Allocation.Assignments[0].Order
	counts := d.Accounting[order[0]]
	if counts.PlannedTrials != 16 || counts.StartedTrials != 1 || counts.StartedAttempts != 1 ||
		counts.CompleteAttempts != 0 || counts.FirstRate != nil || counts.RetryRate != nil {
		t.Fatalf("interrupted attempt was omitted or treated as independent/complete: %+v", counts)
	}
}

func TestJournalTransitionTampering(t *testing.T) {
	p := testPolicy(t, 8)
	directory := collectTest(t, p, testCollector(p))
	data, err := os.ReadFile(filepath.Join(directory, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*Journal)
	}{
		{"sequence", func(j *Journal) { j.Events[2].Sequence++ }},
		{"missing BEGIN", func(j *Journal) { j.Events = append(j.Events[:1], j.Events[2:]...) }},
		{"wrong collection", func(j *Journal) { j.Events[3].CollectionID = "other" }},
		{"order", func(j *Journal) {
			if j.Events[3].Key.Arm == Candidate {
				j.Events[3].Key.Arm = Baseline
			} else {
				j.Events[3].Key.Arm = Candidate
			}
		}},
		{"old origin", func(j *Journal) { j.Events[4].Attempt.Origin.EvalID = "historical" }},
		{"extra fields", func(j *Journal) { j.Events[0].ClusterID = "unexpected" }},
		{"duplicate terminal", func(j *Journal) { j.Events = append(j.Events, j.Events[len(j.Events)-1]) }},
		{"result contradiction", func(j *Journal) { j.Events[4].Attempt.Checks[0].Passed = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var j Journal
			if err := json.Unmarshal(data, &j); err != nil {
				t.Fatal(err)
			}
			tc.edit(&j)
			sealed, err := SealJSON(j)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeJournal(sealed, p); err == nil {
				t.Fatal("self-consistently resealed invalid transition accepted")
			}
		})
	}
}
