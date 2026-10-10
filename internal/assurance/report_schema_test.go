package assurance

import (
	"bytes"
	"encoding/json"
	"testing"

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
