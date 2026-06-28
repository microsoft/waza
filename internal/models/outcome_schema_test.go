package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func minimalOutcomeJSON(schemaVersion string) string {
	versionField := ""
	if schemaVersion != "" {
		versionField = `"schemaVersion": "` + schemaVersion + `",`
	}
	return `{
  ` + versionField + `
  "eval_id": "run-1",
  "skill": "test-skill",
  "eval_name": "test-eval",
  "timestamp": "2026-01-01T00:00:00Z",
  "config": {
    "runs_per_test": 1,
    "model_id": "gpt-4o",
    "engine_type": "mock",
    "timeout_sec": 60
  },
  "summary": {
    "total_tests": 1,
    "succeeded": 1,
    "failed": 0,
    "errors": 0,
    "skipped": 0,
    "success_rate": 1,
    "aggregate_score": 1,
    "weighted_score": 1,
    "min_score": 1,
    "max_score": 1,
    "std_dev": 0,
    "duration_ms": 100
  },
  "metrics": {},
  "tasks": []
}`
}

func TestParseEvaluationOutcome_DefaultsSchemaVersion(t *testing.T) {
	outcome, err := ParseEvaluationOutcome([]byte(minimalOutcomeJSON("")), "results.json")
	if err != nil {
		t.Fatalf("ParseEvaluationOutcome() error = %v", err)
	}
	if outcome.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("schemaVersion = %q, want %q", outcome.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestParseEvaluationOutcome_AllowsSameMajorFutureMinorAndUnknownField(t *testing.T) {
	data := strings.Replace(minimalOutcomeJSON("1.1"), `"tasks": []`, `"futureField": true, "tasks": []`, 1)
	outcome, err := ParseEvaluationOutcome([]byte(data), "results.json")
	if err != nil {
		t.Fatalf("ParseEvaluationOutcome() error = %v", err)
	}
	if outcome.SchemaVersion != "1.1" {
		t.Fatalf("schemaVersion = %q, want 1.1", outcome.SchemaVersion)
	}
}

func TestParseEvaluationOutcome_RejectsDifferentMajorSchemaVersion(t *testing.T) {
	_, err := ParseEvaluationOutcome([]byte(minimalOutcomeJSON("2.0")), "results.json")
	if err == nil {
		t.Fatal("expected different major schemaVersion to be rejected")
	}
	if !strings.Contains(err.Error(), "waza migrate") {
		t.Fatalf("error %q did not include migration hint", err)
	}
}

func TestEvaluationOutcomeMarshalDefaultsSchemaVersion(t *testing.T) {
	data, err := json.Marshal(EvaluationOutcome{
		RunID:       "run-1",
		SkillTested: "test-skill",
		BenchName:   "test-eval",
		Timestamp:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"schemaVersion":"`+CurrentSchemaVersion+`"`) {
		t.Fatalf("marshaled outcome missing default schemaVersion: %s", data)
	}
}

// TestEvaluationOutcome_ToolEventsRoundTrip verifies the schema 1.1 additive
// tool_events[] field round-trips through JSON without loss (issue #366).
func TestEvaluationOutcome_ToolEventsRoundTrip(t *testing.T) {
	original := &EvaluationOutcome{
		SkillTested:   "test-skill",
		BenchName:     "test-eval",
		SchemaVersion: CurrentSchemaVersion,
		Timestamp:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		TestOutcomes: []TestOutcome{{
			TestID: "t1",
			Runs: []RunResult{{
				RunNumber: 1,
				ToolEvents: []ToolEvent{
					{
						Turn: 1, Sequence: 1,
						ToolCallID: "call-a", ToolName: "bash",
						Args:    map[string]any{"command": "go test"},
						Success: true, DurationMs: 42,
					},
					{
						Turn: 1, Sequence: 2,
						ToolCallID: "call-b", ToolName: "view",
						Args:    map[string]any{"path": "/tmp/x"},
						Success: false, Error: "not found",
					},
				},
			}},
		}},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// The wire field name must be `tool_events` per the documented schema.
	if !strings.Contains(string(data), `"tool_events"`) {
		t.Fatalf("expected tool_events field in marshaled JSON: %s", data)
	}

	var restored EvaluationOutcome
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := len(restored.TestOutcomes[0].Runs[0].ToolEvents); got != 2 {
		t.Fatalf("ToolEvents length = %d, want 2", got)
	}
	te := restored.TestOutcomes[0].Runs[0].ToolEvents[0]
	if te.ToolCallID != "call-a" || te.ToolName != "bash" || !te.Success || te.DurationMs != 42 {
		t.Fatalf("first tool event lost fidelity: %+v", te)
	}
}
