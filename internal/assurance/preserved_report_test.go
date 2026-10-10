package assurance

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestParsePreservedReportStrictClaims(t *testing.T) {
	request, _, _ := preservedVerificationFixture(t)
	report, err := VerifyPreserved(t.Context(), request)
	require.NoError(t, err)
	original := marshalReferenceTest(t, report)
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong kind", func(root map[string]any) { root["kind"] = ReferenceKind }},
		{"version 1.0", func(root map[string]any) { root["schema_version"] = "1.0" }},
		{"version 1.1", func(root map[string]any) { root["schema_version"] = "1.1" }},
		{"missing mode", func(root map[string]any) { delete(root, "assessment_mode") }},
		{"wrong mode", func(root map[string]any) { root["assessment_mode"] = CalibrationOperation }},
		{"unknown top property", func(root map[string]any) { root["schemaVersion"] = "1.2" }},
		{"authored scope", func(root map[string]any) { preservedWireObservation(root)["source_scope"] = "authored_finite_output" }},
		{"mislabeled event scope", func(root map[string]any) { preservedWireObservation(root)["source_scope"] = "preserved_file_subset" }},
		{"mislabeled file scope", func(root map[string]any) {
			preservedWireObservationAt(root, 1)["source_scope"] = PreservedNativeSourceScope
		}},
		{"null passed result", func(root map[string]any) { preservedWireObservation(root)["result"] = nil }},
		{"null passed agreement", func(root map[string]any) { preservedWireObservation(root)["agreement"] = nil }},
		{"contradictory agreement", func(root map[string]any) { preservedWireObservation(root)["agreement"] = false }},
		{"contradictory observation reason", func(root map[string]any) { preservedWireObservation(root)["reason"] = "label_disagreement" }},
		{"nonobserved agreement", func(root map[string]any) { preservedWireObservation(root)["state"] = string(InsufficientEvidence) }},
		{"result scope", func(root map[string]any) {
			preservedWireObject(preservedWireObservation(root)["result"])["identifier"] = "other"
		}},
		{"paid type", func(root map[string]any) {
			preservedWireObject(preservedWireObservation(root)["result"])["type"] = "prompt"
		}},
		{"blank type arbitrary details", func(root map[string]any) {
			preservedWireObject(preservedWireObservation(root)["result"])["details"] = map[string]any{}
		}},
		{"blank type wrong score", func(root map[string]any) { preservedWireObject(preservedWireObservation(root)["result"])["score"] = 0 }},
		{"declared coverage", func(root map[string]any) {
			preservedWireObject(preservedWireRequirement(root, 0)["declared_coverage"])["good"] = 2
		}},
		{"observed coverage", func(root map[string]any) {
			preservedWireObject(preservedWireRequirement(root, 0)["observed_coverage"])["critical_bad"] = 1
		}},
		{"duplicate scoped case", func(root map[string]any) {
			requirement := preservedWireRequirement(root, 0)
			observations := preservedWireArray(requirement["observations"])
			requirement["observations"] = append(observations, observations[0])
		}},
		{"duplicate scoped requirement", func(root map[string]any) {
			requirements := preservedWireArray(root["requirements"])
			root["requirements"] = append(requirements, requirements[0])
		}},
		{"case scenario differs by check", func(root map[string]any) {
			preservedWireObservationAt(root, 1)["scenario_id"] = "other"
		}},
		{"undeclared domain", func(root map[string]any) { preservedWireObservation(root)["domain"] = "other" }},
		{"duplicate domain", func(root map[string]any) {
			domains := preservedWireArray(root["domains"])
			root["domains"] = append(domains, domains[0])
		}},
		{"domain observed case count", func(root map[string]any) { preservedWireDomain(root)["observed_cases"] = 3 }},
		{"domain declared case count", func(root map[string]any) { preservedWireDomain(root)["declared_cases"] = 5 }},
		{"domain expected check count", func(root map[string]any) { preservedWireDomain(root)["expected_checks"] = 7 }},
		{"domain observed check count", func(root map[string]any) { preservedWireDomain(root)["observed_checks"] = 7 }},
		{"domain agreements", func(root map[string]any) { preservedWireDomain(root)["agreements"] = 7 }},
		{"domain ratio", func(root map[string]any) { preservedWireDomain(root)["agreement"] = 0.9 }},
		{"domain contradictory state", func(root map[string]any) { preservedWireDomain(root)["state"] = "failed" }},
		{"report contradictory state", func(root map[string]any) { root["state"] = "failed" }},
		{"report contradictory reason", func(root map[string]any) { root["reason"] = "label_disagreement" }},
		{"requirement contradictory reason", func(root map[string]any) { preservedWireRequirement(root, 0)["reason"] = "label_disagreement" }},
		{"eligible review with unaccepted source", func(root map[string]any) { preservedWireObject(root["review"])["current_source_accepted"] = false }},
		{"eligible review revoked", func(root map[string]any) { preservedWireObject(root["review"])["declared_state"] = "revoked" }},
		{"eligible review negative reason", func(root map[string]any) {
			preservedWireObject(root["review"])["reason"] = string(ReviewDigestMismatch)
		}},
		{"missing pass bindings", func(root map[string]any) { root["bindings"] = nil }},
		{"duplicate binding", func(root map[string]any) {
			bindings := preservedWireArray(root["bindings"])
			root["bindings"] = append(bindings, bindings[0])
		}},
		{"verified unequal digests", func(root map[string]any) {
			preservedWireObject(preservedWireArray(root["bindings"])[0])["actual"] = strings.Repeat("a", 64)
		}},
		{"missing equal digests", func(root map[string]any) {
			preservedWireObject(preservedWireArray(root["bindings"])[0])["state"] = "missing"
		}},
		{"nonapplicable supplied digest", func(root map[string]any) {
			preservedWireObject(preservedWireArray(root["bindings"])[0])["applicable"] = false
		}},
		{"missing manifest binding", func(root map[string]any) {
			observation := preservedWireObservation(root)
			observation["bindings"] = preservedWireArray(observation["bindings"])[:3]
		}},
		{"different origin", func(root map[string]any) {
			ref := preservedWireObject(preservedWireArray(preservedWireObservation(root)["evidence"])[0])
			preservedWireObject(ref["origin"])["task_id"] = "other"
		}},
		{"authored origin cannot claim native observation", func(root map[string]any) {
			for _, requirement := range preservedWireArray(root["requirements"]) {
				for _, observation := range preservedWireArray(preservedWireObject(requirement)["observations"]) {
					for _, ref := range preservedWireArray(preservedWireObject(observation)["evidence"]) {
						preservedWireObject(preservedWireObject(ref)["origin"])["eval_id"] = "reference:subject"
					}
				}
			}
		}},
		{"no selected references", func(root map[string]any) { preservedWireObservation(root)["evidence"] = []any{} }},
		{"no whole tape", func(root map[string]any) {
			preservedWireObject(preservedWireArray(preservedWireObservation(root)["evidence"])[0])["pointer"] = "/0"
		}},
		{"duplicate reference", func(root map[string]any) {
			observation := preservedWireObservation(root)
			refs := preservedWireArray(observation["evidence"])
			observation["evidence"] = append(refs, refs[0])
		}},
		{"executions", func(root map[string]any) { preservedWireObject(root["calibration"])["executions"] = 1 }},
		{"samples", func(root map[string]any) { preservedWireObject(root["calibration"])["samples"] = 1 }},
		{"known zero credits", func(root map[string]any) { preservedWireObject(root["calibration"])["credits"] = 0 }},
		{"known usage", func(root map[string]any) { preservedWireObject(root["calibration"])["usage"] = map[string]any{} }},
		{"ledger", func(root map[string]any) { preservedWireObject(root["calibration"])["execution_ledger"] = []any{} }},
		{"calibration protocol", func(root map[string]any) { preservedWireObject(root["calibration"])["protocol"] = AgreementProtocol }},
		{"calibration reason", func(root map[string]any) { preservedWireObject(root["calibration"])["reason"] = "executor_unavailable" }},
		{"fractional integer", func(root map[string]any) { preservedWireDomain(root)["observed_checks"] = 7.5 }},
		{"wrong bool type", func(root map[string]any) { preservedWireObservation(root)["expected_passed"] = "true" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed map[string]any
			require.NoError(t, json.Unmarshal(original, &changed))
			test.mutate(changed)
			_, err := ParsePreservedReport(marshalReferenceTest(t, changed))
			require.Error(t, err)
		})
	}
	for _, raw := range [][]byte{
		nil, []byte("null"), []byte("{}"), bytes.Repeat([]byte(" "), maxSnapshotBytes+1),
		bytes.Replace(original, []byte(`"kind":`), []byte(`"kind":"waza.grader-assurance","kind":`), 1),
		bytes.Replace(original, []byte(`"reason":"corpus_agreement"`), []byte(`"reason":"\ud800"`), 1),
		bytes.Replace(original, []byte(`"reason":"corpus_agreement"`), []byte(`"reason":"\udc00"`), 1),
		bytes.Replace(original, []byte(`"score":1`), []byte(`"score":1e400`), 1),
	} {
		_, err := ParsePreservedReport(raw)
		require.Error(t, err)
	}
}

func preservedWireRequirement(root map[string]any, index int) map[string]any {
	return preservedWireObject(preservedWireArray(root["requirements"])[index])
}

func preservedWireObservation(root map[string]any) map[string]any {
	return preservedWireObservationAt(root, 0)
}

func preservedWireObservationAt(root map[string]any, requirement int) map[string]any {
	return preservedWireObject(preservedWireArray(preservedWireRequirement(root, requirement)["observations"])[0])
}

func preservedWireObject(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		panic("preserved fixture requires an object")
	}
	return object
}

func preservedWireArray(value any) []any {
	array, ok := value.([]any)
	if !ok {
		panic("preserved fixture requires an array")
	}
	return array
}

func preservedWireDomain(root map[string]any) map[string]any {
	return preservedWireObject(preservedWireArray(root["domains"])[0])
}

func TestPreservedReportNonpassNullAndMissingClaims(t *testing.T) {
	request, _, _ := preservedVerificationFixture(t)
	request.Calibrate = true
	report, err := VerifyPreserved(t.Context(), request)
	require.NoError(t, err)
	require.NotEqual(t, AssessmentPassed, report.State)
	// Missing provenance is a truthful nonpass, not a universal admission error.
	report.Bindings = nil
	for i := range report.Requirements {
		for j := range report.Requirements[i].Observations {
			report.Requirements[i].Observations[j].Bindings = nil
		}
	}
	require.NoError(t, reducePreserved(report))
	_, err = ParsePreservedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)
	// A nonpass envelope must not exempt contradictory nested claims.
	report.Requirements[0].Observations[0].Agreement = new(true)
	_, err = ParsePreservedReport(marshalReferenceTest(t, report))
	require.Error(t, err)
}

func TestPreservedReportSelectedLocalSchemaAndVersionFence(t *testing.T) {
	request, _, _ := preservedVerificationFixture(t)
	report, err := VerifyPreserved(t.Context(), request)
	require.NoError(t, err)
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(marshalReferenceTest(t, report)))
	require.NoError(t, err)
	schema, err := preservedReportSchema()
	require.NoError(t, err)
	require.NoError(t, schema.Validate(value))
	// Current embedded readers are controls, not publication proof against
	// immutable historical Go/browser sources (the coordinator owns that gate).
	require.Error(t, assuranceSchema(t, "grader-assurance-1.0.schema.json").Validate(value))
	require.Error(t, assuranceSchema(t, "grader-assurance-1.1.schema.json").Validate(value))
	_, err = ParseCalibratedReport(marshalReferenceTest(t, report))
	require.Error(t, err)
	oldRequest, _, _ := fileVerificationFixture(t)
	old, err := Verify(t.Context(), oldRequest)
	require.NoError(t, err)
	_, err = ParsePreservedReport(marshalReferenceTest(t, old))
	require.Error(t, err)
	for _, uri := range []string{"https://invalid.example/schema.json", "file:///forbidden-schema.json"} {
		_, err := compileLocalSchema("selected.json", map[string]string{
			"selected.json": `{"$ref":` + string(marshalReferenceTest(t, uri)) + `}`,
		})
		require.Error(t, err)
	}

	require.Equal(t, "6da27acc2c920a992b7e8c92dd88ff328aaebd0cf470afc55d5ec3361d78bda3", byteSHA256([]byte(schemas.GraderAssuranceSchemaJSON)))
}

func TestPreservedPreObservationResultsAreRejectedByParserAndSchema(t *testing.T) {
	request, _, _ := preservedVerificationFixture(t)
	report, err := VerifyPreserved(t.Context(), request)
	require.NoError(t, err)
	schema, err := preservedReportSchema()
	require.NoError(t, err)
	for _, state := range []ObservationState{NotAssessed, InsufficientEvidence, Invalid} {
		t.Run(string(state), func(t *testing.T) {
			var changed map[string]any
			require.NoError(t, json.Unmarshal(marshalReferenceTest(t, report), &changed))
			observation := preservedWireObservation(changed)
			observation["state"], observation["agreement"] = string(state), nil
			data := marshalReferenceTest(t, changed)
			value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
			require.NoError(t, err)
			require.Error(t, schema.Validate(value), "pre-observation result must be null at the schema boundary")
			_, err = ParsePreservedReport(data)
			require.Error(t, err)
			// Envelope downgrade cannot exempt an impossible nested result.
			changed["state"] = "operational_error"
			_, err = ParsePreservedReport(marshalReferenceTest(t, changed))
			require.Error(t, err)
		})
	}
}
