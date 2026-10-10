package assurance

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/stretchr/testify/require"
)

func TestQualificationStrictTokensAndImmutableJSON(t *testing.T) {
	type probe struct {
		N    uint64          `json:"n"`
		R    json.RawMessage `json:"r"`
		S    []string        `json:"s"`
		B    bool            `json:"b"`
		Name string          `json:"name"`
		P    *int            `json:"p"`
	}
	input := []byte(`{"n":1,"r":{"exact":9007199254740993},"s":[],"b":true,"name":"é","p":null}`)
	value, err := qualificationDecode[probe](input, 1024)
	require.NoError(t, err)
	require.Contains(t, string(value.R), "9007199254740993")
	document, err := qualificationSeal(value)
	require.NoError(t, err)
	digest, err := evidence.JSONDigest(value)
	require.NoError(t, err)
	require.Equal(t, digest.SHA256, document.sha256())
	copy := document.bytes()
	copy[0] = '!'
	input[0] = '!'
	value.R[0] = '!'
	require.True(t, strings.HasPrefix(string(document.bytes()), "{"))
	require.Contains(t, document.canonical, "9007199254740993")

	base := `{"n":1,"r":{},"s":[],"b":true,"name":"é","p":null}`
	for _, invalid := range []string{
		`null`, `{}`, base + `{}`, strings.Replace(base, `"n":1`, `"n":null`, 1),
		strings.Replace(base, `"n":1`, `"n":1.0`, 1), strings.Replace(base, `"n":1`, `"n":1e0`, 1),
		strings.Replace(base, `"n":1`, `"n":-1`, 1), strings.Replace(base, `"n":1`, `"n":9007199254740992`, 1),
		strings.Replace(base, `"r":{}`, `"r":null`, 1), strings.Replace(base, `"s":[]`, `"s":null`, 1),
		strings.Replace(base, `"b":true`, `"b":null`, 1), strings.Replace(base, `"name":"é"`, `"name":"\ud800"`, 1),
		strings.Replace(base, `"n":1`, `"N":1`, 1), strings.Replace(base, `"n":1`, `"n":1,"n":1`, 1),
		strings.Replace(base, `"n":1`, `"extra":0,"n":1`, 1), strings.Replace(base, `"p":null`, `"p":1.5`, 1),
		string([]byte{0xff}),
	} {
		t.Run(invalid, func(t *testing.T) {
			_, err := qualificationDecode[probe]([]byte(invalid), 1024)
			require.Error(t, err)
		})
	}
	_, err = qualificationDecode[probe]([]byte(base), 1)
	require.Error(t, err)
	_, err = qualificationCanonical(nil)
	require.Error(t, err)
	_, err = qualificationCanonical([]byte(`"\ud800"`))
	require.Error(t, err)
	_, err = qualificationSeal(json.RawMessage(`{`))
	require.Error(t, err)
	err = qualificationShape("x", reflect.TypeFor[float64]())
	require.Error(t, err)
}

func TestQualificationProjectionBudgetBeforeSerialization(t *testing.T) {
	type embedded struct {
		Visible string
	}
	type projection struct {
		embedded
		Name   string          `json:"name"`
		Number json.Number     `json:"number"`
		Raw    json.RawMessage `json:"raw"`
		Empty  []string        `json:"empty,omitempty"`
	}
	for _, value := range []any{
		"é<>&\u2028\n\\\"",
		map[string]any{"native": json.Number("9007199254740993"), "nested": []any{true, false, nil, float64(1e-8), int64(-1)}},
		projection{Name: "é", Number: json.Number("9007199254740993"), Raw: json.RawMessage(`{"html":"<","unicode":"\u00e9","pair":"\ud83d\ude00"}`)},
		[]string{"é", "alternate"},
		(*string)(nil),
	} {
		data, err := json.Marshal(value)
		require.NoError(t, err)
		limit := len(data)
		reached := false
		_, err = qualificationMarshalBounded(value, limit, func() { reached = true })
		require.NoError(t, err)
		require.True(t, reached)
		reached = false
		_, err = qualificationMarshalBounded(value, limit-1, func() { reached = true })
		require.Error(t, err)
		require.False(t, reached)
	}
	reached := false
	_, err := qualificationSealBounded(strings.Repeat("x", 65), 64, func() { reached = true })
	require.Error(t, err)
	require.False(t, reached)
	for _, value := range []any{[]byte("implicit binary"), map[int]string{1: "bad"}, make(chan int),
		json.Number("nan"), json.RawMessage(`"\ud800"`), json.RawMessage(`{`), "\xff"} {
		_, err := qualificationMarshalBounded(value, 1024, nil)
		require.Error(t, err)
	}
	_, err = qualificationMarshalBounded("x", 0, nil)
	require.Error(t, err)
}

type qualificationCustomMarshalProbe struct{ calls *int }

func (probe qualificationCustomMarshalProbe) MarshalJSON() ([]byte, error) {
	*probe.calls++
	return []byte(`"unexpected"`), nil
}

type qualificationTextMarshalProbe string

func (qualificationTextMarshalProbe) MarshalText() ([]byte, error) {
	panic("preflight must reject unsupported text marshalers before invocation")
}

func TestQualificationUnsupportedCustomProjectionNeverInvokesMarshalers(t *testing.T) {
	calls := 0
	reached := false
	_, err := qualificationMarshalBounded(qualificationCustomMarshalProbe{&calls}, 1024, func() { reached = true })
	require.ErrorContains(t, err, "unsupported")
	require.Zero(t, calls)
	require.False(t, reached)
	_, err = qualificationMarshalBounded(qualificationTextMarshalProbe("value"), 1024, func() { reached = true })
	require.ErrorContains(t, err, "unsupported")
	require.False(t, reached)
}

func TestQualificationBase64BeforeAllocation(t *testing.T) {
	for _, size := range []int{0, 1, 2, 3, 64} {
		value := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("é", size)))
		count, err := qualificationBase64Size(value, uint64(size*2))
		require.NoError(t, err)
		require.Equal(t, uint64(size*2), count)
		if size > 0 {
			_, err = qualificationBase64Size(value, uint64(size*2-1))
			require.Error(t, err)
		}
	}
	for _, invalid := range []string{"=", "====", "a", "AA=A", "AB==", "AAB=", "AA\n=", "AA-_", "éAAA"} {
		_, err := qualificationBase64Size(invalid, 100)
		require.Error(t, err, invalid)
	}
	claim := qualificationEncodedClaim{base64.StdEncoding.EncodeToString([]byte("é")), 2, 2}
	require.NoError(t, qualificationPreflightEncoded([]qualificationEncodedClaim{claim, claim}, 4))
	require.Error(t, qualificationPreflightEncoded([]qualificationEncodedClaim{claim, claim}, 3), "repeated blobs consume aggregate budget")
	claim.length = 1
	require.Error(t, qualificationPreflightEncoded([]qualificationEncodedClaim{claim}, 4))
}

func TestQualificationArtifactParserPreflightsBeforeMaterialization(t *testing.T) {
	makeArtifact := func(role, content string) qualificationArtifact {
		return qualificationArtifact{Role: role, Encoding: "source-bytes-sha256", ByteLength: uint64(len(content)),
			SHA256: byteSHA256([]byte(content)), BytesBase64: base64.StdEncoding.EncodeToString([]byte(content))}
	}
	bounds := qualificationBlobBounds{document: 4096, count: 4, total: 4, role: func(string) uint64 { return 4 }}
	for _, failure := range []string{"boundary", "role+1", "aggregate+1", "declared+1", "duplicate",
		"unicode", "null", "number", "count+1", "escaped ASCII", "oversized metadata", "oversized scalar",
		"invalid raw UTF8", "duplicate escaped key", "depth", "document+1"} {
		t.Run(failure, func(t *testing.T) {
			artifact := makeArtifact("actual_calibration_report", "1234")
			list := []qualificationArtifact{artifact}
			testBounds := bounds
			switch failure {
			case "role+1":
				list[0] = makeArtifact("actual_calibration_report", "12345")
				testBounds.total = 100
			case "aggregate+1":
				testBounds.total = 3
			case "declared+1":
				list[0].ByteLength++
			case "count+1":
				testBounds.count = 0
			}
			data := marshalReferenceTest(t, list)
			switch failure {
			case "duplicate":
				data = []byte(strings.Replace(string(data), `"byte_length":4`, `"byte_length":4,"byte_length":4`, 1))
			case "unicode":
				data = []byte(strings.Replace(string(data), `"role":"actual_calibration_report"`, `"role":"\ud800"`, 1))
			case "null":
				data = []byte(strings.Replace(string(data), `"byte_length":4`, `"byte_length":null`, 1))
			case "number":
				data = []byte(strings.Replace(string(data), `"byte_length":4`, `"byte_length":4.0`, 1))
			case "escaped ASCII":
				data = []byte(strings.Replace(string(data), `"bytes_base64":"MTIzNA=="`, `"bytes_base64":"\u004dTIzNA=="`, 1))
			case "oversized metadata":
				data = []byte(strings.Replace(string(data), `"sha256":`, `"extra":"`+strings.Repeat("x", 1539)+`","sha256":`, 1))
			case "oversized scalar":
				data = []byte(strings.Replace(string(data), `"byte_length":4`, `"byte_length":111111111111111111111`, 1))
			case "invalid raw UTF8":
				data = []byte(strings.Replace(string(data), `"role":"actual_calibration_report"`, "\"role\":\"\xff\"", 1))
			case "duplicate escaped key":
				data = []byte(strings.Replace(string(data), `"byte_length":4`, `"byte_length":4,"byte_\u006cength":4`, 1))
			case "depth":
				data = []byte(strings.Replace(string(data), `"byte_length":4`, `"extra":`+strings.Repeat("[", 65)+"0"+strings.Repeat("]", 65)+`,"byte_length":4`, 1))
			case "document+1":
				testBounds.document = len(data) - 1
			}
			reached := false
			_, err := qualificationParseArtifactsBounded(data, testBounds, func() { reached = true })
			if failure == "boundary" || failure == "escaped ASCII" {
				require.NoError(t, err)
				require.True(t, reached)
			} else {
				require.Error(t, err)
				require.False(t, reached, "tree/struct/string decoding must not begin")
			}
		})
	}
	// Distinct repeated blobs each count; the aggregate is not a dedup budget.
	list := []qualificationArtifact{
		{Role: "core_journal", Encoding: "json-v1", ByteLength: 2, SHA256: byteSHA256([]byte("{}")), BytesBase64: "e30="},
		{Role: "ordered_job_tape", Encoding: "json-v1", ByteLength: 2, SHA256: byteSHA256([]byte("{}")), BytesBase64: "e30="},
	}
	data := marshalReferenceTest(t, list)
	reached := false
	_, err := qualificationParseArtifactsBounded(data, bounds, func() { reached = true })
	require.NoError(t, err)
	require.True(t, reached)
	bounds.total = 3
	reached = false
	_, err = qualificationParseArtifactsBounded(data, bounds, func() { reached = true })
	require.Error(t, err)
	require.False(t, reached)
}

func TestQualificationArtifactProjectionHelperRejectsBeforeMaterialization(t *testing.T) {
	for _, failure := range []string{"boundary", "role+1", "declared+1", "role", "digest", "encoding", "base64", "ordinal"} {
		t.Run(failure, func(t *testing.T) {
			content := []byte("{}")
			artifact := qualificationArtifact{
				Role: "core_journal", Encoding: "json-v1", ByteLength: 2,
				SHA256: byteSHA256(content), BytesBase64: base64.StdEncoding.EncodeToString(content),
			}
			switch failure {
			case "role+1":
				content = []byte("{} ")
				artifact.ByteLength = 3
				artifact.SHA256 = byteSHA256(content)
				artifact.BytesBase64 = base64.StdEncoding.EncodeToString(content)
			case "declared+1":
				artifact.ByteLength++
			case "role":
				artifact.Role = "unknown"
			case "digest":
				artifact.SHA256 = "invalid"
			case "encoding":
				artifact.Encoding = "source-bytes-sha256"
			case "base64":
				artifact.BytesBase64 = "AB=="
			case "ordinal":
				artifact.Ordinal = new(uint64(0))
			}
			reached := false
			err := qualificationVerifyArtifactProjectionBounded(artifact, map[string]any{}, 2, func() { reached = true })
			if failure == "boundary" {
				require.NoError(t, err)
				require.True(t, reached)
			} else {
				require.Error(t, err)
				require.False(t, reached, "actual helper must not serialize an envelope or decode oversized payloads")
			}
		})
	}
}

func TestQualificationArtifactRequiresExactCanonicalRoleBytes(t *testing.T) {
	projection := map[string]any{"a": json.Number("9007199254740993"), "é": true}
	canonical, err := qualificationSeal(projection)
	require.NoError(t, err)
	artifact := qualificationArtifact{
		Role: "core_journal", Encoding: "json-v1", ByteLength: uint64(len(canonical.bytes())),
		SHA256: canonical.sha256(), BytesBase64: base64.StdEncoding.EncodeToString(canonical.bytes()),
	}
	qualificationArtifactControls(t, projection, canonical, artifact)
}

func TestQualificationAcknowledgmentsAreOnlyImmutableSuppliedClaims(t *testing.T) {
	sha := strings.Repeat("a", 64)
	ack := qualificationAcknowledgment{
		Kind: "waza.qualification-acknowledgment", Version: qualificationVersion, InvocationID: "é",
		ManifestSHA256: sha, BackendProfileSHA256: sha, EventSHA256: sha, ReceiptToken: "synthetic-token",
	}
	document, err := qualificationParseAcknowledgment(marshalReferenceTest(t, ack))
	require.NoError(t, err)
	require.Contains(t, document.canonical, "synthetic-token")
	for _, mutate := range []func(*qualificationAcknowledgment){
		func(a *qualificationAcknowledgment) { a.Kind = "other" },
		func(a *qualificationAcknowledgment) { a.Version = "2.0" },
		func(a *qualificationAcknowledgment) { a.InvocationID = "" },
		func(a *qualificationAcknowledgment) { a.ManifestSHA256 = "" },
		func(a *qualificationAcknowledgment) { a.BackendProfileSHA256 = "" },
		func(a *qualificationAcknowledgment) { a.EventSHA256 = "" },
		func(a *qualificationAcknowledgment) { a.ReceiptToken = "" },
		func(a *qualificationAcknowledgment) { a.PreviousSHA256 = new(sha) },
		func(a *qualificationAcknowledgment) { a.PayloadSHA256 = new(sha) },
		func(a *qualificationAcknowledgment) { a.Sequence = 1 },
	} {
		copy := ack
		mutate(&copy)
		_, err := qualificationParseAcknowledgment(marshalReferenceTest(t, copy))
		require.Error(t, err)
	}
	ack.Sequence, ack.PreviousSHA256, ack.PayloadSHA256 = 1, new(sha), new(sha)
	_, err = qualificationParseAcknowledgment(marshalReferenceTest(t, ack))
	require.NoError(t, err)
	ack.PreviousSHA256 = new("bad")
	_, err = qualificationParseAcknowledgment(marshalReferenceTest(t, ack))
	require.Error(t, err)

	current := qualificationCurrentnessAcknowledgment{
		Kind: "waza.qualification-currentness-acknowledgment", Version: qualificationVersion,
		RequestSHA256: sha, CurrentnessProfileSHA256: sha, Stage: "before_job",
		CheckedAt: "2026-10-09T12:00:00Z", ValidUntil: "2026-10-09T12:01:00Z",
		AuthorityID: "synthetic-unverified-authority", ReceiptToken: "synthetic-token",
		AttestationEvidenceBase64: base64.StdEncoding.EncodeToString([]byte("unverified-é")),
	}
	_, err = qualificationParseCurrentnessAcknowledgment(marshalReferenceTest(t, current))
	require.NoError(t, err, "negative supplied currentness claim remains representable, not approved")
	for _, mutate := range []func(*qualificationCurrentnessAcknowledgment){
		func(a *qualificationCurrentnessAcknowledgment) { a.Kind = "other" },
		func(a *qualificationCurrentnessAcknowledgment) { a.Stage = "before_paid" },
		func(a *qualificationCurrentnessAcknowledgment) { a.RequestSHA256 = "" },
		func(a *qualificationCurrentnessAcknowledgment) { a.CurrentnessProfileSHA256 = "" },
		func(a *qualificationCurrentnessAcknowledgment) { a.AuthorityID = "" },
		func(a *qualificationCurrentnessAcknowledgment) { a.ReceiptToken = "" },
		func(a *qualificationCurrentnessAcknowledgment) { a.CheckedAt = "bad" },
		func(a *qualificationCurrentnessAcknowledgment) { a.ValidUntil = a.CheckedAt },
		func(a *qualificationCurrentnessAcknowledgment) { a.ValidUntil = "2026-10-09T12:01:00+00:00" },
		func(a *qualificationCurrentnessAcknowledgment) { a.AttestationEvidenceBase64 = "AB==" },
	} {
		copy := current
		mutate(&copy)
		_, err := qualificationParseCurrentnessAcknowledgment(marshalReferenceTest(t, copy))
		require.Error(t, err)
	}
}

func qualificationArtifactControls(t *testing.T, projection any, canonical qualificationDocument, artifact qualificationArtifact) {
	t.Helper()
	require.NoError(t, qualificationVerifyArtifactProjection(artifact, projection))
	require.Error(t, qualificationVerifyArtifactProjection(artifact, map[string]any{"a": 1}))
	formatted := []byte("{ \"a\":9007199254740993, \"é\":true }\n")
	artifact.ByteLength, artifact.SHA256, artifact.BytesBase64 = uint64(len(formatted)), byteSHA256(formatted), base64.StdEncoding.EncodeToString(formatted)
	_, err := qualificationParseArtifacts(marshalReferenceTest(t, []qualificationArtifact{artifact}))
	require.ErrorContains(t, err, "not canonical")
	for _, mutate := range []func(*qualificationArtifact){
		func(a *qualificationArtifact) { a.Role = "unknown" },
		func(a *qualificationArtifact) { a.Ordinal = new(uint64(0)) },
		func(a *qualificationArtifact) { a.Encoding = "source-bytes-sha256" },
		func(a *qualificationArtifact) { a.SHA256 = strings.Repeat("A", 64) },
		func(a *qualificationArtifact) { a.ByteLength++ },
		func(a *qualificationArtifact) { a.SHA256 = strings.Repeat("0", 64) },
	} {
		a := qualificationArtifact{"ordered_job_tape", nil, "json-v1", uint64(len(canonical.bytes())), canonical.sha256(), base64.StdEncoding.EncodeToString(canonical.bytes())}
		mutate(&a)
		_, err := qualificationParseArtifacts(marshalReferenceTest(t, []qualificationArtifact{a}))
		require.Error(t, err)
	}
	valid := qualificationArtifact{"job_terminal_payload", new(uint64(0)), "json-v1", uint64(len(canonical.bytes())), canonical.sha256(), base64.StdEncoding.EncodeToString(canonical.bytes())}
	_, err = qualificationParseArtifacts(marshalReferenceTest(t, []qualificationArtifact{valid}))
	require.NoError(t, err)
	_, err = qualificationParseArtifacts(marshalReferenceTest(t, []qualificationArtifact{valid, valid}))
	require.Error(t, err)
	valid.Ordinal = new(uint64(4096))
	_, err = qualificationParseArtifacts(marshalReferenceTest(t, []qualificationArtifact{valid}))
	require.Error(t, err)
	require.False(t, qualificationIdentifier(""))
	require.False(t, qualificationIdentifier("x\n"))
	require.False(t, qualificationIdentifier(string([]byte{0xff})))
	require.False(t, qualificationIdentifier(strings.Repeat("x", 257)))
	require.True(t, qualificationIdentifier("é"))
	require.False(t, qualificationDigest("x"))
}
