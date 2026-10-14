package webapi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestEvidenceAPILegacyJSONOmissionAndInvalidObservationWithholding(t *testing.T) {
	outcome := &models.EvaluationOutcome{RunID: "eval", TestOutcomes: []models.TestOutcome{{
		TestID: "task", Runs: []models.RunResult{{RunNumber: 1, Attempts: 1}},
	}}}
	data, err := json.Marshal(outcomeToDetail(outcome))
	require.NoError(t, err)
	require.NotContains(t, string(data), `"evidenceRuns"`)
	run := &outcome.TestOutcomes[0].Runs[0]
	run.RequirementExplanations = []models.RequirementExplanation{{TaskID: "task", RequirementID: "req",
		Checks: []models.CheckExplanation{{Observation: "unresolved", Category: "insufficient_evidence"}},
	}}
	rows := outcomeToDetail(outcome).Tasks[0].EvidenceRuns
	require.Len(t, rows, 1)
	require.Equal(t, "unassessed", rows[0].Assessment)
	require.Equal(t, run.RequirementExplanations, rows[0].Explanations)
	for _, invalid := range []string{"wrong-origin", "tampered"} {
		t.Run(invalid, func(t *testing.T) {
			manifest := &models.EvidenceManifest{
				Origin:  models.EvidenceOrigin{EvalID: "other", TaskID: "task", RunNumber: 1, AttemptCount: 1},
				Runtime: models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
			}
			require.NoError(t, evidence.Seal(manifest))
			if invalid == "tampered" {
				manifest.SHA256 = "tampered"
			}
			run.Evidence = manifest
			row := outcomeToDetail(outcome).Tasks[0].EvidenceRuns[0]
			require.Equal(t, "invalid", row.Assessment)
			require.Nil(t, row.Manifest)
			require.Empty(t, row.Explanations)
			data, err := json.Marshal(row)
			require.NoError(t, err)
			require.NotContains(t, string(data), `"manifest"`)
			require.NotContains(t, string(data), `"explanations"`)
			require.Equal(t, "req", run.RequirementExplanations[0].RequirementID)
		})
	}
}

func TestEvidenceAPIIncludesEveryRunAndHistoricalAbsence(t *testing.T) {
	manifest := &models.EvidenceManifest{
		Origin:    models.EvidenceOrigin{EvalID: "source", TaskID: "task", RunNumber: 2, AttemptCount: 3},
		Runtime:   models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
		Artifacts: []models.EvidenceArtifact{{ID: "workspace", Availability: "not_requested", Completeness: "unknown", Reason: "No workspace requested."}},
	}
	require.NoError(t, evidence.Seal(manifest))
	outcome := &models.EvaluationOutcome{RunID: "current", TestOutcomes: []models.TestOutcome{{
		TestID: "task", Cached: true, Runs: []models.RunResult{{RunNumber: 1}, {RunNumber: 2, Attempts: 3, Evidence: manifest}},
	}}}
	rows := outcomeToDetail(outcome).Tasks[0].EvidenceRuns
	require.Len(t, rows, 2)
	require.Equal(t, "unassessed", rows[0].Assessment)
	require.Nil(t, rows[0].Manifest)
	require.Equal(t, "recorded", rows[1].Assessment)
	require.True(t, rows[1].Cached)
	require.Equal(t, "source", rows[1].Manifest.Origin.EvalID)
	manifest.SHA256 = "tampered"
	rows = outcomeToDetail(outcome).Tasks[0].EvidenceRuns
	require.Equal(t, "invalid", rows[1].Assessment)
	require.Nil(t, rows[1].Manifest)
}

func TestEvidenceAPIRejectsNullArtifactsWithoutRelabelingThem(t *testing.T) {
	manifest := &models.EvidenceManifest{
		Origin:  models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1},
		Runtime: models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
	}
	require.NoError(t, evidence.Seal(manifest))
	outcome := &models.EvaluationOutcome{RunID: "eval", TestOutcomes: []models.TestOutcome{{
		TestID: "task", Runs: []models.RunResult{{RunNumber: 1, Attempts: 1, Evidence: manifest}},
	}}}
	row := outcomeToDetail(outcome).Tasks[0].EvidenceRuns[0]
	require.Equal(t, "recorded", row.Assessment)
	require.NotNil(t, row.Manifest.Artifacts)
	data, err := json.Marshal(manifest)
	require.NoError(t, err)
	var object map[string]any
	require.NoError(t, json.Unmarshal(data, &object))
	object["artifacts"] = nil
	delete(object, "sha256")
	digest, err := evidence.JSONDigest(object)
	require.NoError(t, err)
	object["sha256"] = digest.SHA256
	data, err = json.Marshal(object)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, manifest))
	before, err := json.Marshal(outcome)
	require.NoError(t, err)
	row = outcomeToDetail(outcome).Tasks[0].EvidenceRuns[0]
	require.Equal(t, "invalid", row.Assessment)
	require.Nil(t, row.Manifest)
	require.Empty(t, row.Explanations)
	require.Contains(t, row.Message, "invalid")
	after, err := json.Marshal(outcome)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestEvidenceAPIWithholdsAbsentRequiredObjects(t *testing.T) {
	for _, field := range []string{"origin", "runtime", "redaction"} {
		for _, absent := range []bool{false, true} {
			for _, cached := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/missing=%t/cached=%t", field, absent, cached), func(t *testing.T) {
					manifest := &models.EvidenceManifest{
						Origin:  models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1},
						Runtime: models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
					}
					require.NoError(t, evidence.Seal(manifest))
					data, err := json.Marshal(manifest)
					require.NoError(t, err)
					var object map[string]json.RawMessage
					require.NoError(t, json.Unmarshal(data, &object))
					object[field] = json.RawMessage("null")
					if absent {
						delete(object, field)
					}
					data, err = json.Marshal(object)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(data, manifest))
					outcome := &models.EvaluationOutcome{RunID: "eval", TestOutcomes: []models.TestOutcome{{
						TestID: "task", Cached: cached, Runs: []models.RunResult{{
							RunNumber: 1, Attempts: 1, Evidence: manifest,
							RequirementExplanations: []models.RequirementExplanation{{RequirementID: "do-not-display"}},
						}},
					}}}
					before, err := json.Marshal(outcome)
					require.NoError(t, err)
					row := outcomeToDetail(outcome).Tasks[0].EvidenceRuns[0]
					require.Equal(t, "invalid", row.Assessment)
					require.Nil(t, row.Manifest)
					require.Empty(t, row.Explanations)
					after, err := json.Marshal(outcome)
					require.NoError(t, err)
					require.Equal(t, before, after)
					retained, err := json.Marshal(manifest)
					require.NoError(t, err)
					require.JSONEq(t, string(data), string(retained))
				})
			}
		}
	}
}

func TestEvidenceAPIValidatesRequirementReferences(t *testing.T) {
	for _, test := range []struct {
		name        string
		mutate      func(*models.EvidenceReference)
		taskID      string
		noManifest  bool
		cached      bool
		wantInvalid bool
	}{
		{name: "valid"},
		{name: "cached valid", cached: true},
		{name: "foreign origin", mutate: func(r *models.EvidenceReference) { r.Origin.EvalID = "foreign" }, wantInvalid: true},
		{name: "missing artifact", mutate: func(r *models.EvidenceReference) { r.ArtifactID = "missing" }, wantInvalid: true},
		{name: "unavailable artifact", mutate: func(r *models.EvidenceReference) { r.ArtifactID = "unavailable" }, wantInvalid: true},
		{name: "unrequested artifact", mutate: func(r *models.EvidenceReference) { r.ArtifactID = "not-requested" }, wantInvalid: true},
		{name: "digest-only artifact", mutate: func(r *models.EvidenceReference) { r.ArtifactID = "digest-only" }, wantInvalid: true},
		{name: "cached unavailable artifact", cached: true, mutate: func(r *models.EvidenceReference) { r.ArtifactID = "unavailable" }, wantInvalid: true},
		{name: "cached unrequested artifact", cached: true, mutate: func(r *models.EvidenceReference) { r.ArtifactID = "not-requested" }, wantInvalid: true},
		{name: "cached digest-only artifact", cached: true, mutate: func(r *models.EvidenceReference) { r.ArtifactID = "digest-only" }, wantInvalid: true},
		{name: "invalid pointer", mutate: func(r *models.EvidenceReference) { r.Pointer = "not-a-pointer" }, wantInvalid: true},
		{name: "foreign explanation task", taskID: "foreign", wantInvalid: true},
		{name: "missing manifest", noManifest: true, wantInvalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			digest, err := evidence.JSONDigest("Captured observation.")
			require.NoError(t, err)
			manifest := &models.EvidenceManifest{
				Origin:  models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1},
				Runtime: models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
				Artifacts: []models.EvidenceArtifact{
					{ID: "receipt", Availability: "captured", Completeness: "complete", Document: "snapshot", Pointer: "/receipt", ContentDigest: digest},
					{ID: "unavailable", Availability: "unavailable", Completeness: "unknown", Reason: "Not captured."},
					{ID: "not-requested", Availability: "not_requested", Completeness: "unknown", Reason: "Not requested."},
					{ID: "digest-only", Availability: "digest_only", Completeness: "unknown", Reason: "Source identity only.", SourceDigest: digest},
				},
			}
			require.NoError(t, evidence.Seal(manifest))
			reference, err := evidence.Reference(manifest, "receipt")
			require.NoError(t, err)
			if test.mutate != nil {
				test.mutate(&reference)
			}
			taskID := test.taskID
			if taskID == "" {
				taskID = "task"
			}
			run := models.RunResult{RunNumber: 1, Attempts: 1, Evidence: manifest,
				RequirementExplanations: []models.RequirementExplanation{{TaskID: taskID, RequirementID: "req",
					Checks: []models.CheckExplanation{{References: []models.EvidenceReference{reference}}}}},
			}
			if test.noManifest {
				run.Evidence = nil
			}
			cached := test.cached
			outcome := &models.EvaluationOutcome{RunID: "eval", TestOutcomes: []models.TestOutcome{{
				TestID: "task", Cached: cached, Runs: []models.RunResult{run},
			}}}
			if cached {
				outcome.RunID = "current"
			}
			before, err := json.Marshal(outcome)
			require.NoError(t, err)
			row := outcomeToDetail(outcome).Tasks[0].EvidenceRuns[0]
			if test.wantInvalid {
				require.Equal(t, "invalid", row.Assessment)
				require.Nil(t, row.Manifest)
				require.Empty(t, row.Explanations)
				require.Contains(t, row.Message, "references are invalid")
			} else {
				require.Equal(t, "recorded", row.Assessment)
				require.Equal(t, run.RequirementExplanations, row.Explanations)
			}
			after, err := json.Marshal(outcome)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestEvidenceAPIPreservesAbsentAndPopulatedCheckObservations(t *testing.T) {
	const populated = `{"check":{"scope":"task","grader":"content"},"observation":"grader_recorded_pass","category":"operational_status_unavailable","message":"A recorded check is not requirement enforcement."}`
	var control models.CheckExplanation
	require.NoError(t, json.Unmarshal([]byte(populated), &control))
	for _, tt := range []struct {
		name   string
		checks string
		count  int
		isNil  bool
	}{
		{name: "omitted", isNil: true},
		{name: "null", checks: `,"checks":null`, isNil: true},
		{name: "empty", checks: `,"checks":[]`},
		{name: "populated", checks: `,"checks":[` + populated + `]`, count: 1},
	} {
		for _, assessment := range []string{"unassessed", "recorded"} {
			t.Run(tt.name+"/"+assessment, func(t *testing.T) {
				var run models.RunResult
				require.NoError(t, json.Unmarshal([]byte(`{"run_number":1,"attempts":1,"requirement_explanations":[{"task_id":"task","requirement_id":"req"`+tt.checks+`}]}`), &run))
				run.RequirementExplanations = append(run.RequirementExplanations, models.RequirementExplanation{
					TaskID: "task", RequirementID: "populated-control", Checks: []models.CheckExplanation{control},
				})
				if assessment == "recorded" {
					run.Evidence = &models.EvidenceManifest{
						Origin:  models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1},
						Runtime: models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
					}
					require.NoError(t, evidence.Seal(run.Evidence))
				}
				outcome := &models.EvaluationOutcome{RunID: "eval", TestOutcomes: []models.TestOutcome{{
					TestID: "task", Runs: []models.RunResult{run},
				}}}
				rows := outcomeToDetail(outcome).Tasks[0].EvidenceRuns
				require.Len(t, rows, 1)
				require.Equal(t, assessment, rows[0].Assessment)
				require.Equal(t, run.Evidence, rows[0].Manifest)
				require.Equal(t, run.RequirementExplanations, rows[0].Explanations)

				data, err := json.Marshal(rows[0])
				require.NoError(t, err)
				var reloaded EvidenceRunResponse
				require.NoError(t, json.Unmarshal(data, &reloaded))
				require.Len(t, reloaded.Explanations, 2)
				require.Len(t, reloaded.Explanations[0].Checks, tt.count)
				if tt.isNil {
					require.Nil(t, reloaded.Explanations[0].Checks)
				} else {
					require.NotNil(t, reloaded.Explanations[0].Checks)
				}
				if tt.count > 0 {
					require.Equal(t, control, reloaded.Explanations[0].Checks[0])
				}
				require.Equal(t, []models.CheckExplanation{control}, reloaded.Explanations[1].Checks)
				require.Equal(t, assessment, reloaded.Assessment)
			})
		}
	}
}
