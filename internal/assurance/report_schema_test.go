package assurance

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func assuranceSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	for resource, source := range map[string]string{
		"evidence-manifest-1.0.schema.json": schemas.EvidenceManifestSchemaJSON,
		"grader-reference-1.0.schema.json":  schemas.GraderReferenceSchemaJSON,
		"grader-review-1.0.schema.json":     schemas.GraderReviewSchemaJSON,
		"grader-assurance-1.0.schema.json":  schemas.GraderAssuranceSchemaJSON,
		"grader-assurance-1.1.schema.json":  schemas.CalibratedGraderAssuranceSchemaJSON,
	} {
		var value any
		require.NoError(t, json.Unmarshal([]byte(source), &value))
		require.NoError(t, compiler.AddResource(resource, value))
		require.NoError(t, compiler.AddResource("https://raw.githubusercontent.com/microsoft/waza/main/schemas/"+resource, value))
	}
	compiled, err := compiler.Compile(name)
	require.NoError(t, err)
	return compiled
}

func TestCalibratedLedgerTypedShapeAndOfflineVersionFence(t *testing.T) {
	// These exact a4dc1597 resources compile the frozen 1.0 reader, not a
	// newly widened schema that merely retains the original file name.
	for _, resource := range []struct {
		source string
		sha256 string
	}{
		{schemas.GraderAssuranceSchemaJSON, "6da27acc2c920a992b7e8c92dd88ff328aaebd0cf470afc55d5ec3361d78bda3"},
		{schemas.GraderReferenceSchemaJSON, "456b93323d956ed3c0551135c3c59d6556180bb42b3fd803a57021316496b663"},
		{schemas.EvidenceManifestSchemaJSON, "bf4e4822b59e65e5caca2eefcb9cdef066fc710a560bd3b4798d4e21ad233e5a"},
	} {
		require.Equal(t, resource.sha256, byteSHA256([]byte(resource.source)))
	}
	request, _, _ := authoredVerificationFixture(t)
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	offline := marshalReferenceTest(t, report)
	require.NotContains(t, string(offline), "execution_ledger")
	require.NotContains(t, string(offline), "assessment_mode")

	report.SchemaVersion = CalibratedReportVersion
	report.AssessmentMode = CalibrationOperation
	report.State = AssessmentNotAssessed
	report.Calibration.Reason = "synthetic_calibration_not_executed"
	report.Calibration.ExecutionLedger = new([]JudgeExecution{})
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(marshalReferenceTest(t, report)))
	require.NoError(t, err)
	current := assuranceSchema(t, "grader-assurance-1.1.schema.json")
	require.NoError(t, current.Validate(value))
	require.Error(t, assuranceSchema(t, "grader-assurance-1.0.schema.json").Validate(value))

	entry := JudgeExecution{
		CaseID: "good", TaskID: request.Tasks["task"].TestID, RequirementID: "state",
		Check:          models.RequirementCheck{Scope: "eval", Grader: "check"},
		RequestedModel: "synthetic-model", State: AssessmentError,
		Reason: "initialize_failed", Diagnostics: []JudgeDiagnostic{{Stage: "initialize", Code: "start_failed"}},
	}
	report.Calibration.ExecutionLedger = new([]JudgeExecution{entry})
	valid, err := jsonschema.UnmarshalJSON(bytes.NewReader(marshalReferenceTest(t, report)))
	require.NoError(t, err)
	require.NoError(t, current.Validate(valid))
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing ledger", func(root map[string]any) { delete(fixtureObject(t, root["calibration"]), "execution_ledger") }},
		{"missing operation", func(root map[string]any) { delete(root, "assessment_mode") }},
		{"unknown operation", func(root map[string]any) { root["assessment_mode"] = "automatic" }},
		{"null ledger", func(root map[string]any) { fixtureObject(t, root["calibration"])["execution_ledger"] = nil }},
		{"alias", func(root map[string]any) { root["executionLedger"] = []any{} }},
		{"unknown entry", func(root map[string]any) { ledgerEntry(root)["session_id"] = "private" }},
		{"negative callbacks", func(root map[string]any) { ledgerEntry(root)["callbacks"] = -1 }},
		{"unentered callbacks", func(root map[string]any) { ledgerEntry(root)["callbacks"] = 1 }},
		{"unavailable complete events", func(root map[string]any) { ledgerEntry(root)["event_model_attribution_complete"] = true }},
		{"empty complete accounting", func(root map[string]any) {
			ledgerEntry(root)["accounting_models"] = []any{}
			ledgerEntry(root)["accounting_model_attribution_complete"] = true
		}},
		{"unavailable complete usage", func(root map[string]any) { ledgerEntry(root)["usage_complete"] = true }},
		{"incomplete known credits", func(root map[string]any) { ledgerEntry(root)["credits"] = 0 }},
		{"incomplete known usage", func(root map[string]any) { ledgerEntry(root)["usage"] = map[string]any{} }},
		{"impossible pass", func(root map[string]any) { ledgerEntry(root)["state"] = "passed" }},
		{"execute before initialize", func(root map[string]any) { ledgerEntry(root)["executed"] = true }},
		{"raw diagnostic", func(root map[string]any) {
			ledgerEntry(root)["diagnostics"] = []any{map[string]any{"stage": "initialize", "code": "raw private exception"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed map[string]any
			require.NoError(t, json.Unmarshal(marshalReferenceTest(t, report), &changed))
			test.mutate(changed)
			require.Error(t, current.Validate(changed))
		})
	}
}

func TestCalibratedLedgerCompleteZeroAccountingAndFailureRetention(t *testing.T) {
	request, _, _ := authoredVerificationFixture(t)
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	report.SchemaVersion, report.AssessmentMode = CalibratedReportVersion, CalibrationOperation
	entry := syntheticCompleteJudgeExecution()
	report.Calibration.ExecutionLedger = new([]JudgeExecution{entry})
	schema := assuranceSchema(t, "grader-assurance-1.1.schema.json")
	var valid map[string]any
	require.NoError(t, json.Unmarshal(marshalReferenceTest(t, report), &valid))
	require.NoError(t, schema.Validate(valid))
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"incomplete accounting attribution", func(root map[string]any) { ledgerEntry(root)["accounting_model_attribution_complete"] = false }},
		{"missing total credits", func(root map[string]any) { delete(fixtureObject(t, ledgerEntry(root)["usage"]), "ai_credits") }},
		{"empty model metrics", func(root map[string]any) {
			fixtureObject(t, ledgerEntry(root)["usage"])["model_metrics"] = map[string]any{}
		}},
		{"missing model credits", func(root map[string]any) {
			usage := fixtureObject(t, ledgerEntry(root)["usage"])
			metrics := fixtureObject(t, usage["model_metrics"])
			delete(fixtureObject(t, metrics["synthetic-model"]), "ai_credits")
		}},
		{"pass with diagnostic", func(root map[string]any) {
			ledgerEntry(root)["diagnostics"] = []any{map[string]any{"stage": "client_stop", "code": "cleanup_failed"}}
		}},
		{"pass without callback", func(root map[string]any) { ledgerEntry(root)["callbacks"] = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed map[string]any
			require.NoError(t, json.Unmarshal(marshalReferenceTest(t, report), &changed))
			test.mutate(changed)
			require.Error(t, schema.Validate(changed))
		})
	}
	entry.State, entry.Reason = AssessmentError, "synthetic_shutdown_failure"
	entry.Diagnostics = []JudgeDiagnostic{{Stage: "client_stop", Code: "cleanup_failed"}}
	report.Calibration.ExecutionLedger = new([]JudgeExecution{entry})
	var failed map[string]any
	require.NoError(t, json.Unmarshal(marshalReferenceTest(t, report), &failed))
	require.NoError(t, schema.Validate(failed))
	require.Equal(t, 0.0, ledgerEntry(failed)["credits"])
	require.NotNil(t, ledgerEntry(failed)["usage"])
}

func ledgerEntry(root map[string]any) map[string]any {
	calibration, ok := root["calibration"].(map[string]any)
	if !ok {
		panic("invalid calibration fixture object")
	}
	entries, ok := calibration["execution_ledger"].([]any)
	if !ok || len(entries) == 0 {
		panic("invalid calibration fixture ledger")
	}
	entry, ok := entries[0].(map[string]any)
	if !ok {
		panic("invalid calibration fixture entry")
	}
	return entry
}

func fixtureObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok)
	return object
}

func syntheticCompleteJudgeExecution() JudgeExecution {
	return JudgeExecution{
		CaseID: "good", TaskID: "task", RequirementID: "state",
		Check:       models.RequirementCheck{Scope: "eval", Grader: "check"},
		Initialized: true, Executed: true, Callbacks: 1, RequestedModel: "synthetic-model",
		EventModels: []string{"synthetic-model"}, AccountingModels: []string{"synthetic-model"},
		EventModelAttributionComplete: true, AccountingModelAttributionComplete: true,
		UsageSource: "synthetic", UsageComplete: true, State: AssessmentPassed,
		Reason: "synthetic_operational_evidence", Diagnostics: []JudgeDiagnostic{}, Credits: new(0.0),
		Usage: &models.UsageStats{AICredits: new(0.0), ModelMetrics: map[string]models.ModelUsage{
			"synthetic-model": {AICredits: new(0.0)},
		}},
	}
}

func TestActualReferenceAndAssuranceOutputsMatchPublishedSchemas(t *testing.T) {
	labelSchema := assuranceSchema(t, "grader-reference-1.0.schema.json")
	reportSchema := assuranceSchema(t, "grader-assurance-1.0.schema.json")
	for _, fixture := range []func(*testing.T) (VerifyRequest, ReferenceDocument, string){
		fileVerificationFixture, authoredVerificationFixture,
	} {
		request, document, _ := fixture(t)
		labels, err := jsonschema.UnmarshalJSON(bytes.NewReader(marshalReferenceTest(t, document)))
		require.NoError(t, err)
		require.NoError(t, labelSchema.Validate(labels))
		for _, reviewed := range []bool{true, false} {
			request.Acceptance.AcceptCurrentDecision = reviewed
			report, err := Verify(t.Context(), request)
			require.NoError(t, err)
			value, err := jsonschema.UnmarshalJSON(bytes.NewReader(marshalReferenceTest(t, report)))
			require.NoError(t, err)
			require.NoError(t, reportSchema.Validate(value))
		}
	}
}

func TestReferenceSchemaRejectsAmbiguousSourceAndAliases(t *testing.T) {
	schema := assuranceSchema(t, "grader-reference-1.0.schema.json")
	_, document, _ := authoredVerificationFixture(t)
	document.Cases[0].Snapshot = "historical.json"
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(marshalReferenceTest(t, document)))
	require.NoError(t, err)
	require.Error(t, schema.Validate(value))
	_, err = ParseReferences(marshalReferenceTest(t, document))
	require.Error(t, err)
}
