package schemas_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func releaseAssuranceSchema(t *testing.T, resource string) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	for name, source := range map[string]string{
		"release-assurance-contract-1.0.schema.json": schemas.ReleaseAssuranceContractSchemaJSON,
		"release-assurance-attempt-1.0.schema.json":  schemas.ReleaseAssuranceAttemptSchemaJSON,
		"release-assurance-ledger-1.0.schema.json":   schemas.ReleaseAssuranceLedgerSchemaJSON,
		"release-assured-decision-1.0.schema.json":   schemas.ReleaseAssuredDecisionSchemaJSON,
		"release-artifacts-1.0.schema.json":          schemas.ReleaseArtifactsSchemaJSON,
		"grader-assurance-1.0.schema.json":           schemas.GraderAssuranceSchemaJSON,
		"grader-reference-1.0.schema.json":           schemas.GraderReferenceSchemaJSON,
		"evidence-manifest-1.0.schema.json":          schemas.EvidenceManifestSchemaJSON,
	} {
		var document any
		require.NoError(t, json.Unmarshal([]byte(source), &document))
		require.NoError(t, compiler.AddResource(name, document))
		require.NoError(t, compiler.AddResource("https://raw.githubusercontent.com/microsoft/waza/main/schemas/"+name, document))
	}
	schema, err := compiler.Compile(resource)
	require.NoError(t, err)
	return schema
}

func assuranceJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	require.NoError(t, err)
	return assuranceObject(t, document)
}

func assuranceObject(t *testing.T, value any, path ...string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok, "expected JSON object")
	for _, key := range path {
		object, ok = object[key].(map[string]any)
		require.True(t, ok, "expected JSON object at %s", key)
	}
	return object
}

func assuranceArtifacts(t *testing.T) map[string]any {
	t.Helper()
	digest := models.EvidenceDigest{Encoding: "json-v1", SHA256: strings.Repeat("a", 64)}
	source := releasepolicy.SourceDigest([]byte("exact source bytes\n"))
	arm := releasepolicy.AssuranceArm{
		Mode: "authored_finite_output", PlanDigest: digest, EvalSourceDigest: source,
		ResolvedConfigDigest: digest, ExecutableDigest: source, LabelsDigest: source,
		Tasks: []releasepolicy.AssuranceTask{{TaskID: "task", Digest: digest}},
		Checks: []releasepolicy.AssuranceCheck{{TaskID: "task", RequirementID: "requirement",
			Check:       models.RequirementCheck{Scope: "task", Grader: "text"},
			Declaration: digest, ValidationKey: "text"}},
		Inputs: []releasepolicy.AssuranceInput{{Path: "authored.json", Digest: source}},
	}
	contract := releasepolicy.AssuranceContract{Kind: releasepolicy.AssuranceContractKind,
		Version: releasepolicy.AssuranceVersion, Nonce: strings.Repeat("b", 64), PolicyDigest: digest,
		Arms: map[releasepolicy.Arm]releasepolicy.AssuranceArm{releasepolicy.Baseline: arm, releasepolicy.Candidate: arm}}
	sealed, err := releasepolicy.SealJSON(contract)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(sealed, &contract))
	empty := ""
	row := releasepolicy.AssuranceRow{Kind: releasepolicy.AssuranceRowKind, Version: releasepolicy.AssuranceVersion,
		Sequence: 1, CollectionID: contract.Digest.SHA256, RowDigest: digest,
		Key: releasepolicy.AttemptKey{SampleKey: releasepolicy.SampleKey{ClusterID: "cluster", TaskID: "task", Trial: 1},
			Arm: releasepolicy.Baseline, EvalID: "eval", Attempt: 1},
		Origin: models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "captured"},
		Output: releasepolicy.AssuranceOutput{Availability: "available", Value: &empty}}
	ledger := releasepolicy.AssuranceLedger{Kind: releasepolicy.AssuranceLedgerKind, Version: releasepolicy.AssuranceVersion,
		CollectionID: contract.Digest.SHA256, ContractDigest: contract.Digest, Rows: []releasepolicy.AssuranceRow{row},
		CoreJournalDigest: digest, StreamDigest: source,
		RawResults: map[releasepolicy.Arm]models.EvidenceDigest{releasepolicy.Baseline: source, releasepolicy.Candidate: source}}
	sealed, err = releasepolicy.SealJSON(ledger)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(sealed, &ledger))
	decision := releasepolicy.AssuredDecision{Kind: releasepolicy.AssuredDecisionKind, Version: releasepolicy.AssuranceVersion,
		BaseDecision: releasepolicy.InitialDecision(), ContractDigest: contract.Digest, CollectionID: contract.Digest.SHA256,
		Assurance: releasepolicy.Dimension{State: "not_assessed", Reasons: []string{"unreviewed synthetic fixture"}},
		Regrade:   releasepolicy.Dimension{State: "not_assessed", Reasons: []string{}},
		Reports:   map[releasepolicy.Arm]*assurance.Report{}, Limitations: []string{"Schema fixture, not review evidence."}}
	return map[string]any{
		"release-assurance-contract": contract, "release-assurance-attempt": row,
		"release-assurance-ledger": ledger, "release-assured-decision": decision,
	}
}

func TestReleaseAssuranceSchemasProducerShapes(t *testing.T) {
	for name, artifact := range assuranceArtifacts(t) {
		t.Run(name, func(t *testing.T) {
			resource := name + "-1.0.schema.json"
			for _, resource := range []string{resource, "https://raw.githubusercontent.com/microsoft/waza/main/schemas/" + resource} {
				schema := releaseAssuranceSchema(t, resource)
				document := assuranceJSON(t, artifact)
				require.NoError(t, schema.Validate(document))
				document["unexpected"] = true
				require.Error(t, schema.Validate(document))
			}
			document := assuranceJSON(t, artifact)
			require.Error(t, releaseAssuranceSchema(t, "release-artifacts-1.0.schema.json").Validate(document),
				"frozen base schema must never adopt independent assurance artifacts")
		})
	}
}

func TestReleaseAssuranceSchemasStrictAdmission(t *testing.T) {
	cases := []struct {
		name, artifact string
		mutate         func(map[string]any)
	}{
		{"wrong version", "release-assurance-contract", func(v map[string]any) { v["version"] = "2.0" }},
		{"missing kind", "release-assurance-attempt", func(v map[string]any) { delete(v, "kind") }},
		{"kind alias", "release-assurance-ledger", func(v map[string]any) { v["Kind"] = v["kind"]; delete(v, "kind") }},
		{"third arm", "release-assurance-contract", func(v map[string]any) { assuranceObject(t, v, "arms")["other"] = nil }},
		{"missing arm", "release-assurance-contract", func(v map[string]any) { delete(assuranceObject(t, v, "arms"), "candidate") }},
		{"bad nonce", "release-assurance-contract", func(v map[string]any) { v["nonce"] = strings.Repeat("B", 64) }},
		{"uppercase digest", "release-assurance-contract", func(v map[string]any) {
			assuranceObject(t, v, "policy_digest")["sha256"] = strings.Repeat("A", 64)
		}},
		{"digest unknown property", "release-assurance-contract", func(v map[string]any) {
			assuranceObject(t, v, "policy_digest")["unknown"] = "ignored"
		}},
		{"unsupported mode", "release-assurance-contract", func(v map[string]any) {
			assuranceObject(t, v, "arms", "baseline")["mode"] = "native_history"
		}},
		{"unknown nested field", "release-assurance-contract", func(v map[string]any) {
			assuranceObject(t, v, "arms", "baseline")["unknown"] = true
		}},
		{"source domain confusion", "release-assurance-contract", func(v map[string]any) {
			assuranceObject(t, v, "arms", "baseline", "labels_digest")["encoding"] = "json-v1"
		}},
		{"json domain confusion", "release-assurance-contract", func(v map[string]any) {
			assuranceObject(t, v, "policy_digest")["encoding"] = "source-bytes"
		}},
		{"null review digest", "release-assurance-contract", func(v map[string]any) {
			assuranceObject(t, v, "arms", "baseline")["review_digest"] = nil
		}},
		{"null tasks", "release-assurance-contract", func(v map[string]any) {
			assuranceObject(t, v, "arms", "baseline")["tasks"] = nil
		}},
		{"sequence zero", "release-assurance-attempt", func(v map[string]any) { v["sequence"] = 0 }},
		{"trial zero", "release-assurance-attempt", func(v map[string]any) { assuranceObject(t, v, "key")["trial_ordinal"] = 0 }},
		{"stream domain confusion", "release-assurance-ledger", func(v map[string]any) {
			assuranceObject(t, v, "stream_digest")["encoding"] = "json-v1"
		}},
		{"missing result arm", "release-assurance-ledger", func(v map[string]any) { delete(assuranceObject(t, v, "raw_result_digests"), "baseline") }},
		{"null ledger rows", "release-assurance-ledger", func(v map[string]any) { v["rows"] = nil }},
		{"report third arm", "release-assured-decision", func(v map[string]any) { assuranceObject(t, v, "reports")["other"] = nil }},
		{"null report", "release-assured-decision", func(v map[string]any) { assuranceObject(t, v, "reports")["baseline"] = nil }},
		{"base pass adoption", "release-assured-decision", func(v map[string]any) { assuranceObject(t, v, "base_decision")["accepted"] = true }},
		{"outer pass without qualification", "release-assured-decision", func(v map[string]any) { v["accepted"] = true }},
		{"unknown assurance state", "release-assured-decision", func(v map[string]any) {
			assuranceObject(t, v, "assurance")["state"] = "assumed"
		}},
		{"unknown regrade state", "release-assured-decision", func(v map[string]any) {
			assuranceObject(t, v, "regrade")["state"] = "assumed"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document := assuranceJSON(t, assuranceArtifacts(t)[tc.artifact])
			tc.mutate(document)
			require.Error(t, releaseAssuranceSchema(t, tc.artifact+"-1.0.schema.json").Validate(document))
		})
	}
}

func TestReleaseAssuranceOutputAvailabilitySchema(t *testing.T) {
	schema := releaseAssuranceSchema(t, "release-assurance-attempt-1.0.schema.json")
	for _, tc := range []struct {
		name   string
		output map[string]any
		valid  bool
	}{
		{"explicit empty available", map[string]any{"availability": "available", "value": ""}, true},
		{"available unicode", map[string]any{"availability": "available", "value": "😀\n"}, true},
		{"unavailable", map[string]any{"availability": "unavailable", "reason": "no_execution_response"}, true},
		{"missing available value", map[string]any{"availability": "available"}, false},
		{"null available value", map[string]any{"availability": "available", "value": nil}, false},
		{"available with reason", map[string]any{"availability": "available", "value": "", "reason": "missing"}, false},
		{"manufactured unavailable empty", map[string]any{"availability": "unavailable", "reason": "missing", "value": ""}, false},
		{"missing unavailable reason", map[string]any{"availability": "unavailable"}, false},
		{"blank unavailable reason", map[string]any{"availability": "unavailable", "reason": " \n"}, false},
		{"unknown availability", map[string]any{"availability": "unknown"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := assuranceJSON(t, assuranceArtifacts(t)["release-assurance-attempt"])
			document["output"] = tc.output
			err := schema.Validate(document)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestReleaseAssuranceDecisionReportAndEarlyFailureSchema(t *testing.T) {
	schema := releaseAssuranceSchema(t, "release-assured-decision-1.0.schema.json")
	document := assuranceJSON(t, assuranceArtifacts(t)["release-assured-decision"])
	document["contract_digest"] = map[string]any{"encoding": "", "sha256": ""}
	document["collection_id"] = ""
	require.NoError(t, schema.Validate(document), "early fail-closed output has no admitted contract")
	report := assurance.Report{Kind: assurance.ReportKind, SchemaVersion: "1.0", CreatedAt: time.Now().UTC(),
		State: assurance.AssessmentNotAssessed, LabelsSHA256: strings.Repeat("a", 64),
		Review:       assurance.ReviewReport{DeclaredState: assurance.ReviewUnreviewed},
		Requirements: []assurance.RequirementAssessment{}, Domains: []assurance.DomainAssessment{},
		Calibration: assurance.CalibrationReport{State: assurance.AssessmentNotAssessed, Reason: "calibration_not_selected"},
		CachePolicy: "no_reuse", Limitations: []string{"Synthetic schema report; not human review."}}
	report.Requirements = []assurance.RequirementAssessment{{
		TaskID: "task", RequirementID: "requirement", Check: models.RequirementCheck{Scope: "task", Grader: "text"},
		State: assurance.AssessmentNotAssessed, Observations: []assurance.ChallengeObservation{{
			CaseID: "authored-case", ScenarioID: "scenario", Domain: "text", Classification: assurance.CaseGood,
			SourceScope: "authored_finite_output", State: assurance.Observed,
			Evidence: []models.EvidenceReference{},
		}},
	}}
	document["reports"] = map[string]any{"baseline": assuranceJSON(t, report)}
	require.NoError(t, schema.Validate(document))
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"unsupported report version", func(v map[string]any) { v["schema_version"] = "1.1" }},
		{"paid execution", func(v map[string]any) { assuranceObject(t, v, "calibration")["executions"] = 1 }},
		{"billing zero is not absent", func(v map[string]any) { assuranceObject(t, v, "calibration")["credits"] = 0 }},
		{"wrong observation source scope", func(v map[string]any) {
			requirements, ok := v["requirements"].([]any)
			require.True(t, ok)
			observations, ok := assuranceObject(t, requirements[0])["observations"].([]any)
			require.True(t, ok)
			assuranceObject(t, observations[0])["source_scope"] = "preserved_file_subset"
		}},
		{"unknown report property", func(v map[string]any) { v["unknown"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			instance := assuranceJSON(t, report)
			tc.mutate(instance)
			document["reports"] = map[string]any{"baseline": instance}
			require.Error(t, schema.Validate(document))
		})
	}

}

func TestReleaseAssuranceOuterPassSchemaConditions(t *testing.T) {
	schema := releaseAssuranceSchema(t, "release-assured-decision-1.0.schema.json")
	makeDocument := func() map[string]any {
		document := assuranceJSON(t, assuranceArtifacts(t)["release-assured-decision"])
		document["accepted"] = true
		assuranceObject(t, document, "assurance")["state"] = "passed"
		assuranceObject(t, document, "regrade")["state"] = "passed"
		base := assuranceObject(t, document, "base_decision")
		for name, state := range map[string]string{
			"compatibility": "compatible", "completeness": "complete", "operations": "observed",
			"golden": "not_required", "billing": "not_required", "statistics": "noninferiority",
		} {
			assuranceObject(t, base, name)["state"] = state
		}
		// A synthetic schema fixture tests structure, never current-source qualification.
		report := assurance.Report{Kind: assurance.ReportKind, SchemaVersion: "1.0", CreatedAt: time.Now().UTC(),
			State: assurance.AssessmentPassed, LabelsSHA256: strings.Repeat("a", 64),
			Review: assurance.ReviewReport{SourceID: "synthetic-schema-review",
				DeclaredState: assurance.ReviewReviewed, CurrentSourceAccepted: true, Eligible: true},
			Requirements: []assurance.RequirementAssessment{}, Domains: []assurance.DomainAssessment{},
			Calibration: assurance.CalibrationReport{State: assurance.AssessmentNotAssessed, Reason: "calibration_not_selected"},
			CachePolicy: "no_reuse", Limitations: []string{"Synthetic schema fixture; not authenticated human review."}}
		document["reports"] = map[string]any{"baseline": assuranceJSON(t, report), "candidate": assuranceJSON(t, report)}
		return document
	}
	require.NoError(t, schema.Validate(makeDocument()))
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing arm report", func(v map[string]any) { delete(assuranceObject(t, v, "reports"), "candidate") }},
		{"nonpassing report", func(v map[string]any) {
			assuranceObject(t, v, "reports", "candidate")["state"] = "not_assessed"
		}},
		{"unaccepted review", func(v map[string]any) {
			assuranceObject(t, v, "reports", "baseline", "review")["current_source_accepted"] = false
		}},
		{"unreviewed declaration", func(v map[string]any) {
			assuranceObject(t, v, "reports", "baseline", "review")["declared_state"] = "unreviewed"
		}},
		{"regrade incomplete", func(v map[string]any) { assuranceObject(t, v, "regrade")["state"] = "incomplete" }},
		{"base statistical veto", func(v map[string]any) {
			assuranceObject(t, v, "base_decision", "statistics")["state"] = "inconclusive"
		}},
		{"base golden veto", func(v map[string]any) {
			assuranceObject(t, v, "base_decision", "golden")["state"] = "failed"
		}},
		{"base missing billing", func(v map[string]any) {
			assuranceObject(t, v, "base_decision", "billing")["state"] = "unavailable"
		}},
		{"no admitted identity", func(v map[string]any) { v["contract_digest"] = map[string]any{"encoding": "", "sha256": ""} }},
		{"no admitted collection", func(v map[string]any) { v["collection_id"] = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := makeDocument()
			tc.mutate(document)
			require.Error(t, schema.Validate(document))
		})
	}
}

func TestReleaseAssuranceSchemasRejectMissingRootMembers(t *testing.T) {
	for name, artifact := range assuranceArtifacts(t) {
		schema := releaseAssuranceSchema(t, name+"-1.0.schema.json")
		for member := range assuranceJSON(t, artifact) {
			t.Run(name+"/"+member, func(t *testing.T) {
				document := assuranceJSON(t, artifact)
				delete(document, member)
				require.Error(t, schema.Validate(document))
			})
		}
	}
}
