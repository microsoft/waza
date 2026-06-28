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
	if !strings.Contains(string(data), `"schemaVersion":"1.1"`) {
		t.Fatalf("marshaled outcome missing default schemaVersion: %s", data)
	}
}
