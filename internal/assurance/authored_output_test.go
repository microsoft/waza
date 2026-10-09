package assurance

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func authoredFixture(t *testing.T, output *string) *AuthoredOutput {
	t.Helper()
	input, err := NewAuthoredOutput("subject", "case", "native-task", 2, output)
	require.NoError(t, err)
	return input
}

func TestAuthoredOutputNullEmptyAndSupplied(t *testing.T) {
	empty, text := "", "finite output\n9007199254740993 ✅"
	for _, output := range []*string{nil, &empty, &text} {
		input := authoredFixture(t, output)
		require.Equal(t, output, input.Output())
		require.Equal(t, "case", input.CaseID())
		var envelope map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(input.Document(), &envelope))
		require.Len(t, envelope, 4)
		require.JSONEq(t, `"2.0"`, string(envelope["schemaVersion"]))
		require.JSONEq(t, `"waza.grader-reference-input"`, string(envelope["kind"]))
		manifest := input.Manifest()
		require.NoError(t, evidence.Validate(manifest))
		require.Equal(t, models.EvidenceOrigin{EvalID: "reference:subject", TaskID: "native-task", RunNumber: 2, AttemptCount: 1, PriorAttempts: "none"}, manifest.Origin)
		require.Equal(t, models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"}, manifest.Runtime)
		reference, err := evidence.Reference(manifest, evidence.ReferenceInputArtifactID)
		require.NoError(t, err)
		require.Equal(t, "/output", reference.Pointer)
		if output == nil {
			require.Equal(t, "unavailable", manifest.Artifacts[0].Availability)
			require.Nil(t, manifest.Artifacts[0].ContentDigest)
			require.Empty(t, manifest.Artifacts[0].Document)
			require.Empty(t, manifest.Artifacts[0].Pointer)
			require.Error(t, evidence.VerifyContent(manifest, reference, input.ProjectionBytes(), true, true))
		} else {
			require.Equal(t, "complete", manifest.Artifacts[0].Completeness)
			require.Equal(t, "/payload", manifest.Artifacts[0].Pointer)
			require.NoError(t, evidence.VerifyContent(manifest, reference, input.ProjectionBytes(), true, true))
			require.Error(t, evidence.VerifyContent(manifest, reference, []byte(*output), true, true))
		}
		parsed, err := ParseAuthoredOutput(input.Document())
		require.NoError(t, err)
		require.Equal(t, input.Document(), parsed.Document())
		encoded, err := input.MarshalJSON()
		require.NoError(t, err)
		require.Equal(t, input.Document(), encoded)
	}
}

func TestAuthoredOutputImmutabilityAndOriginalBytes(t *testing.T) {
	output := "original"
	input := authoredFixture(t, &output)
	output = "caller changed"
	require.Equal(t, "original", *input.Output())
	*input.Output() = "getter changed"
	document := input.Document()
	projection := input.ProjectionBytes()
	document[0], projection[0] = 'x', 'x'
	manifest := input.Manifest()
	manifest.Origin.EvalID = "changed"
	manifest.Artifacts[0].ContentDigest.SHA256 = "changed"
	require.Equal(t, "original", *input.Output())
	require.NoError(t, evidence.Validate(input.Manifest()))
	require.Equal(t, byte('{'), input.Document()[0])
	require.Equal(t, byte('{'), input.ProjectionBytes()[0])
	source := append([]byte(" \n"), input.Document()...)
	source = bytes.Replace(source, []byte(`"output":"original"`), []byte(`"output" : "\u006friginal"`), 1)
	admitted, err := ParseAuthoredOutput(source)
	require.NoError(t, err)
	require.Equal(t, source, admitted.Document())
	require.Contains(t, string(admitted.ProjectionBytes()), `"\u006friginal"`)
	source[0] = 'x'
	require.Equal(t, byte(' '), admitted.Document()[0])
}

func TestAuthoredOutputStrictAdmission(t *testing.T) {
	output := "original"
	input := authoredFixture(t, &output)
	source := string(input.Document())
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(input.Document(), &fields))
	for name, data := range map[string]string{
		"missing output":         strings.Replace(source, `,"output":"original"`, "", 1),
		"missing marker":         strings.Replace(source, `"schemaVersion":"2.0",`, "", 1),
		"old native marker":      strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":"1.4"`, 1),
		"future marker":          strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":"2.1"`, 1),
		"null marker":            strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":null`, 1),
		"numeric marker":         strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":2.0`, 1),
		"boolean marker":         strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":true`, 1),
		"object marker":          strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":{}`, 1),
		"wrong-case marker":      strings.Replace(source, `"schemaVersion":"2.0"`, `"SchemaVersion":"2.0"`, 1),
		"snake-case marker":      strings.Replace(source, `"schemaVersion":"2.0"`, `"schema_version":"2.0"`, 1),
		"marker alias":           strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":"2.0","SchemaVersion":"1.4"`, 1),
		"duplicate marker":       strings.Replace(source, `"schemaVersion":"2.0"`, `"schemaVersion":"2.0","schemaVersion":"2.0"`, 1),
		"missing kind marker":    strings.Replace(source, `"kind":"waza.grader-reference-input",`, "", 1),
		"null kind marker":       strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":null`, 1),
		"numeric kind marker":    strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":2`, 1),
		"boolean kind marker":    strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":true`, 1),
		"object kind marker":     strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":{}`, 1),
		"array kind marker":      strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":[]`, 1),
		"native kind marker":     strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":"task-snapshot"`, 1),
		"wrong-case kind key":    strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"Kind":"waza.grader-reference-input"`, 1),
		"wrong-case kind value":  strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":"WAZA.GRADER-REFERENCE-INPUT"`, 1),
		"kind marker alias":      strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":"waza.grader-reference-input","Kind":null`, 1),
		"escaped kind alias":     strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"\u004bind":"waza.grader-reference-input"`, 1),
		"duplicate kind marker":  strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":"waza.grader-reference-input","kind":"waza.grader-reference-input"`, 1),
		"escaped duplicate kind": strings.Replace(source, `"kind":"waza.grader-reference-input"`, `"kind":"waza.grader-reference-input","\u006bind":"waza.grader-reference-input"`, 1),
		"extra payload field":    strings.Replace(source, `"output":"original"`, `"output":"original","usage":null`, 1),
		"extra envelope field":   strings.TrimSuffix(source, "}") + `,"history":[]}`,
		"extra manifest field":   strings.Replace(source, `"evidence":{`, `"evidence":{"history":[] ,`, 1),
		"duplicate":              strings.Replace(source, `"output":"original"`, `"output":"original","output":"original"`, 1),
		"trailing":               source + `{}`,
		"wrong envelope":         `[]`,
		"null payload":           strings.Replace(source, string(input.ProjectionBytes()), "null", 1),
		"null evidence":          strings.Replace(source, `"evidence":`+string(fields["evidence"]), `"evidence":null`, 1),
		"wrong kind":             strings.Replace(source, AuthoredOutputKind, "other", 1),
		"wrong payload kind":     strings.Replace(source, `"payload":{"kind":"waza.grader-reference-input"`, `"payload":{"kind":"other"`, 1),
		"wrong scope":            strings.Replace(source, AuthoredOutputScope, "original_history", 1),
		"payload version":        strings.Replace(source, `"schema_version":"1.0"`, `"schema_version":"2.0"`, 1),
		"manifest version":       strings.Replace(source, `"version":"1.1"`, `"version":"1.0"`, 1),
		"future version":         strings.Replace(source, `"version":"1.1"`, `"version":"1.2"`, 1),
		"tampered projection":    strings.Replace(source, "original", "tampered", 1),
		"tampered case":          strings.Replace(source, `"id":"case"`, `"id":"other"`, 1),
		"tampered origin":        strings.Replace(source, "reference:subject", "reference:other", 1),
		"numeric output":         strings.Replace(source, `"output":"original"`, `"output":9007199254740993`, 1),
		"null captured":          strings.Replace(source, `"output":"original"`, `"output":null`, 1),
		"omitted runtime key":    strings.Replace(source, `"sdk_version":null,`, "", 1),
		"null artifact":          strings.Replace(source, `"artifacts":[{`, `"artifacts":[null,{`, 1),
		"invalid utf8":           strings.Replace(source, "original", string([]byte{0xff}), 1),
		"high surrogate":         strings.Replace(source, "original", `\ud800`, 1),
		"low surrogate":          strings.Replace(source, "original", `\udfff`, 1),
		"unpaired surrogate":     strings.Replace(source, "original", `\ud800\u0061`, 1),
		"bad ordinal token":      strings.Replace(source, `"run_number":2`, `"run_number":2.0`, 1),
		"rounded ordinal":        strings.Replace(source, `"run_number":2`, `"run_number":9007199254740993`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseAuthoredOutput([]byte(data))
			require.Error(t, err)
		})
	}
	unavailable := authoredFixture(t, nil)
	data := bytes.Replace(unavailable.Document(), []byte(`"output":null`), []byte(`"output":""`), 1)
	_, err := ParseAuthoredOutput(data)
	require.Error(t, err)
}

func TestAuthoredOutputUnicodeAndBoundedConstructor(t *testing.T) {
	text := "😀"
	input := authoredFixture(t, &text)
	escaped := bytes.Replace(input.Document(), []byte(text), []byte(`\ud83d\ude00`), 1)
	parsed, err := ParseAuthoredOutput(escaped)
	require.NoError(t, err)
	require.Equal(t, text, *parsed.Output())
	literal := `\ud800`
	require.Equal(t, literal, *authoredFixture(t, &literal).Output())
	for _, id := range []string{"", "../subject", "with space", "subject/task", "é", strings.Repeat("a", 129), "reference:x"} {
		_, err := NewAuthoredOutput(id, "case", "task", 1, nil)
		require.Error(t, err)
		_, err = NewAuthoredOutput("subject", id, "task", 1, nil)
		require.Error(t, err)
		_, err = NewAuthoredOutput("subject", "case", id, 1, nil)
		require.Error(t, err)
	}
	for _, ordinal := range []int{-1, 0} {
		_, err := NewAuthoredOutput("subject", "case", "task", ordinal, nil)
		require.Error(t, err)
	}
	invalid := string([]byte{0xff})
	_, err = NewAuthoredOutput("subject", "case", "task", 1, &invalid)
	require.Error(t, err)
	large := strings.Repeat("x", MaxAuthoredOutputBytes)
	_, err = NewAuthoredOutput("subject", "case", "task", 1, &large)
	require.Error(t, err)
	large += "x"
	_, err = NewAuthoredOutput("subject", "case", "task", 1, &large)
	require.Error(t, err)
	_, err = ParseAuthoredOutput(nil)
	require.Error(t, err)
	_, err = ParseAuthoredOutput(bytes.Repeat([]byte(" "), MaxAuthoredOutputBytes+1))
	require.Error(t, err)
	bounded := strings.Repeat("x", MaxAuthoredOutputBytes-2048)
	require.NotNil(t, authoredFixture(t, &bounded))
	empty := ""
	overhead := len(authoredFixture(t, &empty).Document())
	exact := strings.Repeat("x", MaxAuthoredOutputBytes-overhead)
	require.Len(t, authoredFixture(t, &exact).Document(), MaxAuthoredOutputBytes)
	exact += "x"
	_, err = NewAuthoredOutput("subject", "case", "native-task", 2, &exact)
	require.Error(t, err)
}

func TestAuthoredOutputNilAndZeroGuards(t *testing.T) {
	for _, input := range []*AuthoredOutput{nil, {}} {
		require.Nil(t, input.Document())
		require.Nil(t, input.ProjectionBytes())
		require.Nil(t, input.Output())
		require.Nil(t, input.Manifest())
		require.Empty(t, input.CaseID())
		_, err := input.MarshalJSON()
		require.Error(t, err)
	}

	var zero AuthoredOutput
	_, err := json.Marshal(&zero)
	require.Error(t, err)
}

func TestAuthoredOutputMarkersBindEnvelopeNotPayloadDigest(t *testing.T) {
	text := "original"
	input := authoredFixture(t, &text)
	for _, marker := range []string{`"schemaVersion":"2.0",`, `"kind":"waza.grader-reference-input",`} {
		unmarked := bytes.Replace(input.Document(), []byte(marker), nil, 1)
		require.NotEqual(t, sha256.Sum256(input.Document()), sha256.Sum256(unmarked))
		var old struct {
			Payload  json.RawMessage         `json:"payload"`
			Evidence models.EvidenceManifest `json:"evidence"`
		}
		require.NoError(t, json.Unmarshal(unmarked, &old))
		require.Equal(t, input.ProjectionBytes(), []byte(old.Payload))
		require.Equal(t, input.Manifest().SHA256, old.Evidence.SHA256)
		digest, err := evidence.JSONDigest(old.Payload)
		require.NoError(t, err)
		require.Equal(t, input.Manifest().Artifacts[0].ContentDigest, digest)
		_, err = ParseAuthoredOutput(unmarked)
		require.Error(t, err)
	}
}
