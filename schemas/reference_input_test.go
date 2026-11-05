package schemas_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestReferenceInputManifestDedicatedSchema(t *testing.T) {
	compile := func(source string) *jsonschema.Schema {
		t.Helper()
		var schema any
		require.NoError(t, json.Unmarshal([]byte(source), &schema))
		compiler := jsonschema.NewCompiler()
		require.NoError(t, compiler.AddResource("manifest.json", schema))
		compiled, err := compiler.Compile("manifest.json")
		require.NoError(t, err)
		return compiled
	}
	current := compile(schemas.ReferenceInputEvidenceManifestSchemaJSON)
	historical := compile(schemas.EvidenceManifestSchemaJSON)
	empty := ""
	for _, output := range []*string{nil, &empty} {
		input, err := assurance.NewAuthoredOutput("subject", "case", "task", 1, output)
		require.NoError(t, err)
		data, err := json.Marshal(input.Manifest())
		require.NoError(t, err)
		instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		require.NoError(t, err)
		require.NoError(t, current.Validate(instance))
		require.Error(t, historical.Validate(instance))
	}
}

func referenceInputEnvelopeSchema(t *testing.T, resource string) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	for name, source := range map[string]string{
		"evidence-manifest-1.1.schema.json":      schemas.ReferenceInputEvidenceManifestSchemaJSON,
		"grader-reference-input-1.0.schema.json": schemas.GraderReferenceInputSchemaJSON,
	} {
		var value any
		require.NoError(t, json.Unmarshal([]byte(source), &value))
		require.NoError(t, compiler.AddResource(name, value))
		require.NoError(t, compiler.AddResource("https://raw.githubusercontent.com/microsoft/waza/main/schemas/"+name, value))
	}
	compiled, err := compiler.Compile(resource)
	require.NoError(t, err)
	return compiled
}

func TestReferenceInputEnvelopeSchemaActualProducerBytes(t *testing.T) {
	const name = "grader-reference-input-1.0.schema.json"
	for _, resource := range []string{name, "https://raw.githubusercontent.com/microsoft/waza/main/schemas/" + name} {
		schema := referenceInputEnvelopeSchema(t, resource)
		empty, text := "", "finite authored output 😀\n9007199254740993"
		for _, output := range []*string{nil, &empty, &text} {
			input, err := assurance.NewAuthoredOutput("subject", "case", "task", 1, output)
			require.NoError(t, err)
			value, err := jsonschema.UnmarshalJSON(bytes.NewReader(input.Document()))
			require.NoError(t, err)
			require.NoError(t, schema.Validate(value))
			admitted, err := assurance.ParseAuthoredOutput(input.Document())
			require.NoError(t, err)
			require.Equal(t, input.Document(), admitted.Document())
		}
	}
}

func TestReferenceInputEnvelopeSchemaAndReaderRejectInvalidContracts(t *testing.T) {
	schema := referenceInputEnvelopeSchema(t, "grader-reference-input-1.0.schema.json")
	empty := ""
	input, err := assurance.NewAuthoredOutput("subject", "case", "task", 1, &empty)
	require.NoError(t, err)
	source := string(input.Document())
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(input.Document(), &fields))
	for name, data := range map[string]string{
		"missing version marker":      strings.Replace(source, `"schemaVersion":"2.0",`, "", 1),
		"wrong version marker":        strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":"1.0"`, 1),
		"null version marker":         strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":null`, 1),
		"version alias":               strings.Replace(source, `"schemaVersion":"2.0"`, `"SchemaVersion":"2.0"`, 1),
		"extra version alias":         strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":"2.0","SchemaVersion":"2.0"`, 1),
		"missing kind marker":         strings.Replace(source, `"kind":"waza.grader-reference-input",`, "", 1),
		"wrong kind marker":           strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":"task-snapshot"`, 1),
		"null kind marker":            strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":null`, 1),
		"kind alias":                  strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"Kind":"waza.grader-reference-input"`, 1),
		"extra kind alias":            strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":"waza.grader-reference-input","Kind":null`, 1),
		"extra envelope member":       strings.TrimSuffix(source, "}") + `,"usage":null}`,
		"missing payload":             `{"schemaVersion":"2.0","kind":"waza.grader-reference-input","evidence":{}}`,
		"null payload":                strings.Replace(source, string(input.ProjectionBytes()), "null", 1),
		"missing output":              strings.Replace(source, `,"output":""`, "", 1),
		"wrong output type":           strings.Replace(source, `"output":""`, `"output":false`, 1),
		"wrong payload kind":          strings.Replace(source, `"payload":{"kind":"waza.grader-reference-input"`, `"payload":{"kind":"other"`, 1),
		"wrong payload version":       strings.Replace(source, `"schema_version":"1.0"`, `"schema_version":"2.0"`, 1),
		"payload version alias":       strings.Replace(source, `"schema_version":"1.0"`, `"schemaVersion":"1.0"`, 1),
		"wrong payload scope":         strings.Replace(source, `"authored_finite_output"`, `"native_history"`, 1),
		"unsafe case":                 strings.Replace(source, `"id":"case"`, `"id":"../case"`, 1),
		"extra payload member":        strings.Replace(source, `"output":""`, `"output":"","history":[]`, 1),
		"missing evidence":            `{"schemaVersion":"2.0","kind":"waza.grader-reference-input","payload":{}}`,
		"null evidence":               strings.Replace(source, `"evidence":`+string(fields["evidence"]), `"evidence":null`, 1),
		"wrong evidence version":      strings.Replace(source, `"version":"1.1"`, `"version":"1.0"`, 1),
		"evidence version alias":      strings.Replace(source, `"version":"1.1"`, `"Version":"1.1"`, 1),
		"extra evidence member":       strings.Replace(source, `"evidence":{`, `"evidence":{"history":[],`, 1),
		"null with captured evidence": strings.Replace(source, `"output":""`, `"output":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			value, err := jsonschema.UnmarshalJSON(strings.NewReader(data))
			require.NoError(t, err)
			require.Error(t, schema.Validate(value))
			_, err = assurance.ParseAuthoredOutput([]byte(data))
			require.Error(t, err)
		})
	}
	for name, data := range map[string]string{
		"duplicate version": strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":"2.0","schemaVersion":"2.0"`, 1),
		"duplicate kind":    strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":"waza.grader-reference-input","kind":"waza.grader-reference-input"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			// Duplicate member rejection is raw JSON admission, not a JSON Schema property.
			_, err := assurance.ParseAuthoredOutput([]byte(data))
			require.Error(t, err)
		})
	}
	data := strings.Replace(source, `"output":""`, `"output":false`, 1)
	_, err = assurance.ParseAuthoredOutput([]byte(data))
	require.ErrorContains(t, err, "invalid authored input")
	unavailable, err := assurance.NewAuthoredOutput("subject", "case", "task", 1, nil)
	require.NoError(t, err)
	data = strings.Replace(string(unavailable.Document()), `"output":null`, `"output":""`, 1)
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(data))
	require.NoError(t, err)
	require.Error(t, schema.Validate(value))
	_, err = assurance.ParseAuthoredOutput([]byte(data))
	require.ErrorContains(t, err, "invalid authored input")
}
