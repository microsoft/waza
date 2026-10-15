package controlledcomparison

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/releasepolicy"
)

func fixture(t *testing.T, model string) Source {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "fixtures"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"eval.yaml": "schemaVersion: '2.0'\nname: controlled\nscenario: controlled-offline\nversion: '1.0'\nconfig:\n  executor: mock\n  model: " + model + "\n  trials_per_task: 1\n  timeout_seconds: 30\n  max_attempts: 2\nmetrics:\n  - name: accuracy\n    weight: 1\n    threshold: 0.8\ntasks: [task.yaml]\n",
		"task.yaml": "id: task-0\nname: hello\ngolden: true\ninputs:\n  prompt: hello\n  context:\n    fixture: fixtures\nexpected:\n  output_contains: [hello]\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Source{EvalPath: filepath.Join(dir, "eval.yaml")}
}

func design() releasepolicy.Design {
	return releasepolicy.Design{Estimand: "fixed_suite_repeated_execution", Endpoint: "first_attempt_pass",
		Alpha: "0.05", FamilySize: 1, MaximumHalfWidth: "1", Margin: "1", Accept: "noninferiority",
		Independence: releasepolicy.Independence{Assessment: "unjustified",
			Justification: "This one-cluster offline test exercises collection, not independent agent quality.",
			Limitations:   []string{"Deterministic mock outcomes are not live quality evidence."}},
		Clusters: []releasepolicy.Cluster{{ID: "cluster", Weight: "1", Tasks: []releasepolicy.PlannedTask{
			{ID: "task-0", Weight: "1", TrialOrdinals: []int{1}}}}}}
}

func requirements() releasepolicy.Requirements {
	return releasepolicy.Requirements{Runtime: true, IdentityDomains: []string{
		"task_definition", "resolved_prompt", "fixture_inventory", "instruction_inventory",
		"grader_configuration", "dependency_mode", "grader_implementation", "rubric_inventory", "lock_inventory"},
		Billing: []releasepolicy.BillingRequirement{}}
}

func TestSourceBoundCollection(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("new durable collector unsupported on this platform")
	}
	baseline, candidate := fixture(t, "baseline-label"), fixture(t, "candidate-label")
	data, err := Plan(baseline, candidate, design(), requirements(), []string{"model"})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := releasepolicy.DecodePolicy(data)
	if err != nil || len(policy.Changes) != 1 || len(policy.GoldenIDs) != 1 {
		t.Fatalf("actual source plans were not bound: %+v %v", policy, err)
	}
	directory := filepath.Join(t.TempDir(), "collection")
	if err := Collect(t.Context(), data, baseline, candidate, directory); err != nil {
		t.Fatal(err)
	}
	decision, err := releasepolicy.ReadDecision(directory)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Accepted || decision.Statistics.State != "inconclusive" ||
		decision.Golden.State != "passed" || decision.Accounting[releasepolicy.Baseline].Attempts != 1 {
		t.Fatalf("mock/one-cluster collection must remain honestly inconclusive: %+v", decision)
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		for _, usage := range decision.Usage[arm] {
			if usage.Availability != "unavailable" || usage.Value != nil {
				t.Fatalf("mock billing fabricated: %+v", usage)
			}
		}
	}
	if err := Collect(t.Context(), data, baseline, candidate, directory); err == nil {
		t.Fatal("existing collection adopted")
	}
}

func TestSourcePlanBoundaries(t *testing.T) {
	for _, name := range []string{"undeclared_model", "bad_allowed_field", "duplicate_allowed_field", "trial_count", "missing_task", "preselected_allocation", "unsupported_executor", "fixture_drift", "missing_resource"} {
		t.Run(name, func(t *testing.T) {
			baseline, candidate := fixture(t, "baseline-label"), fixture(t, "candidate-label")
			d, allowed := design(), []string{"model"}
			switch name {
			case "undeclared_model":
				allowed = nil
			case "bad_allowed_field":
				allowed = []string{"timeout"}
			case "duplicate_allowed_field":
				allowed = []string{"model", "model"}
			case "trial_count":
				d.Clusters[0].Tasks[0].TrialOrdinals = []int{1, 2}
			case "missing_task":
				d.Clusters[0].Tasks[0].ID = "absent"
			case "preselected_allocation":
				d.Allocation.Seed = strings.Repeat("ab", 32)
			case "unsupported_executor":
				data, err := os.ReadFile(candidate.EvalPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(candidate.EvalPath, []byte(strings.ReplaceAll(string(data), "executor: mock", "executor: copilot-sdk")), 0o600); err != nil {
					t.Fatal(err)
				}
			case "fixture_drift":
				if err := os.WriteFile(filepath.Join(filepath.Dir(candidate.EvalPath), "fixtures", "changed.txt"), []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing_resource":
				task := "id: task-0\nname: hello\ninputs:\n  prompt: hello\n  files: [missing.txt]\nexpected:\n  output_contains: [hello]\n"
				if err := os.WriteFile(filepath.Join(filepath.Dir(candidate.EvalPath), "task.yaml"), []byte(task), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Plan(baseline, candidate, d, requirements(), allowed); err == nil {
				t.Fatal("invalid/unbound plan admitted")
			}
		})
	}
}

func TestSourceDriftBeforeBegin(t *testing.T) {
	source := fixture(t, "mock-label")
	data, err := Plan(source, source, design(), requirements(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(source.EvalPath), "fixtures", "new.txt"), []byte("drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "collection")
	if err := Collect(t.Context(), data, source, source, directory); err == nil {
		t.Fatal("source drift adopted")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("drifted collection wrote a BEGIN: %v", err)
	}
}

func TestPlanningInputRequiresExplicitFields(t *testing.T) {
	data, err := json.Marshal(design())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := releasepolicy.DecodeDesignInput(data); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{`{}`, `{"assurance":false}`, `{"assurance":null,"runtime":false,"required_identity_domains":[],"billing":[]}`,
		`{"assurance":false,"runtime":false,"required_identity_domains":[],"billing":[],"surprise":1}`} {
		if _, err := releasepolicy.DecodeRequirementsInput([]byte(data)); err == nil {
			t.Fatalf("incomplete planning requirements admitted: %s", data)
		}
	}
}
