package nativesnapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/microsoft/waza/internal/testutil/readeroracle"
	"github.com/stretchr/testify/require"
)

type readerOracleCase struct {
	Name     string `json:"name"`
	Raw      []byte `json:"raw"`
	Canceled bool   `json:"canceled"`
	Want     string `json:"want"`
}

type readerOracleObservation struct {
	Name       string              `json:"name"`
	Error      string              `json:"error"`
	ErrorClass *readeroracle.Error `json:"error_class"`
	Nil        bool                `json:"nil"`
	Wire       json.RawMessage     `json:"wire"`
	Projection json.RawMessage     `json:"projection"`
	Requests   json.RawMessage     `json:"requests"`
}

func observeFrozenCollectionReader(t *testing.T, test readerOracleCase) readerOracleObservation {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if test.Canceled {
		cancel()
	}
	before := sha256.Sum256(test.Raw)
	var got *CollectionCapture
	var err error
	require.NotPanics(t, func() { got, err = ReadCollectionCapture(ctx, test.Raw) }, "grammar validation must precede unsafe range traversal")
	require.Equal(t, before, sha256.Sum256(test.Raw), "reader must not mutate input")
	result := readerOracleObservation{Name: test.Name, Nil: got == nil}
	if err != nil {
		result.Error = err.Error()
		result.ErrorClass = readeroracle.Describe(err)
		require.Nil(t, got)
		return result
	}
	require.NotNil(t, got)
	result.Wire, err = got.Marshal(t.Context())
	require.NoError(t, err)
	projections, err := got.Projections(t.Context())
	require.NoError(t, err)
	result.Projection, err = json.Marshal(projections)
	require.NoError(t, err)
	requests := map[releasepolicy.Arm]json.RawMessage{}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		request, err := got.Request(t.Context(), arm, "task")
		require.NoError(t, err)
		require.Equal(t, "hello", request.Message)
		requests[arm], err = orchestration.FreezeCapturedNativeRequest(request)
		require.NoError(t, err)
		primitive, err := got.Primitive(t.Context(), arm, "task")
		require.NoError(t, err)
		require.NotNil(t, primitive)
	}
	result.Requests, err = json.Marshal(requests)
	require.NoError(t, err)
	for i := range test.Raw {
		test.Raw[i] = '!'
	}
	retained, err := got.Marshal(t.Context())
	require.NoError(t, err)
	require.Equal(t, []byte(result.Wire), retained, "retained wire must detach caller input")
	return result
}

func frozenCollectionReaderProcess(t *testing.T, raw []byte) {
	var cases []readerOracleCase
	require.NoError(t, json.Unmarshal(raw, &cases))
	require.NotEmpty(t, cases)
	results := make([]readerOracleObservation, 0, len(cases))
	for _, test := range cases {
		results = append(results, observeFrozenCollectionReader(t, test))
	}
	readeroracle.Output(t, results)
}

func TestCollectionReaderFrozen15cParity(t *testing.T) {
	if raw, subprocess := readeroracle.Input(t); subprocess {
		frozenCollectionReaderProcess(t, raw)
		return
	}
	oracle := readeroracle.Build(t, "nativesnapshot")
	binaryBefore, err := os.Stat(oracle)
	require.NoError(t, err)
	base, locations := fixture(t)
	write(t, base, "baseline/waza.lock", "")
	captured, err := CaptureCollection(t.Context(), locations)
	require.NoError(t, err)
	raw, err := captured.Marshal(t.Context())
	require.NoError(t, err)
	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &members))
	replace := func(name, value string) []byte {
		t.Helper()
		changed := make(map[string]json.RawMessage, len(members))
		for key, data := range members {
			changed[key] = data
		}
		changed[name] = json.RawMessage(value)
		result, err := json.Marshal(changed)
		require.NoError(t, err)
		require.NotEqual(t, raw, result)
		return result
	}
	appendMember := func(data []byte, member string) []byte {
		return append(append(bytes.Clone(data[:len(data)-1]), ','), append([]byte(member), '}')...)
	}
	cases := []readerOracleCase{{Name: "actual_writer", Raw: bytes.Clone(raw)}}
	add := func(name string, data []byte, want string) {
		cases = append(cases, readerOracleCase{Name: name, Raw: data, Want: want})
	}
	add("valid_whitespace", append(append([]byte(" \r\n\t"), raw...), []byte(" \r\n\t")...), "")
	var pretty bytes.Buffer
	require.NoError(t, json.Indent(&pretty, raw, "", " \t"))
	add("nested_indentation_changes_raw_snapshot", pretty.Bytes(), "declaration bytes changed")
	reordered := []byte("{")
	for i, key := range []string{"version", "sources", "snapshot", "kind", "blobs", "associations"} {
		if i != 0 {
			reordered = append(reordered, ',')
		}
		reordered = append(reordered, []byte(`"`+key+`":`)...)
		reordered = append(reordered, members[key]...)
	}
	reordered = append(reordered, '}')
	add("valid_member_order", reordered, "")
	escaped := bytes.Replace(raw, []byte(`"blobs":`), []byte(`"\u0062lobs":`), 1)
	require.NotEqual(t, raw, escaped)
	add("valid_escaped_member", escaped, "")
	var blobs map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(members["blobs"], &blobs))
	require.NotEmpty(t, blobs)
	blobName := ""
	for key := range blobs {
		if blobName == "" || key < blobName {
			blobName = key
		}
	}
	nameJSON, err := json.Marshal(blobName)
	require.NoError(t, err)
	var blobBytes []byte
	require.NoError(t, json.Unmarshal(blobs[blobName], &blobBytes))
	arrayBytes := make([]int, len(blobBytes))
	for i, value := range blobBytes {
		arrayBytes[i] = int(value)
	}
	arrayRaw, err := json.Marshal(arrayBytes)
	require.NoError(t, err)
	alternativeBlobs := make(map[string]json.RawMessage, len(blobs))
	for name, value := range blobs {
		alternativeBlobs[name] = value
	}
	alternativeBlobs[blobName] = arrayRaw
	alternativeRaw, err := json.Marshal(alternativeBlobs)
	require.NoError(t, err)
	add("valid_blob_byte_array", replace("blobs", string(alternativeRaw)), "")
	emptyFound := false
	for name, value := range blobs {
		if bytes.Equal(value, []byte("null")) {
			emptyFound = true
			for _, spelling := range []string{`""`, `[]`} {
				alternativeBlobs[name] = json.RawMessage(spelling)
				alternativeRaw, err = json.Marshal(alternativeBlobs)
				require.NoError(t, err)
				add("valid_empty_blob_"+spelling, replace("blobs", string(alternativeRaw)), "")
			}
			break
		}
	}
	require.True(t, emptyFound, "fixture must retain an actual empty lock blob")
	add("duplicate_blob_otherwise_valid", replace("blobs", string(appendMember(members["blobs"], string(nameJSON)+":"+string(blobs[blobName])))), "duplicate JSON object key")
	duplicateBlob := []byte("{")
	duplicateBlob = append(duplicateBlob, []byte(string(nameJSON)+`:`+string(blobs[blobName])+`,`+string(nameJSON)+`:`+string(blobs[blobName]))...)
	duplicateBlob = append(duplicateBlob, '}')
	add("duplicate_blob", replace("blobs", string(duplicateBlob)), "duplicate JSON object key")
	escapedName := `"\u00` + strings.ToUpper(string([]byte{"0123456789abcdef"[blobName[0]>>4], "0123456789abcdef"[blobName[0]&15]})) + blobName[1:] + `"`
	add("escaped_duplicate_blob", replace("blobs", `{`+string(nameJSON)+`:`+string(blobs[blobName])+`,`+escapedName+`:`+string(blobs[blobName])+`}`), "duplicate JSON object key")
	add("duplicate_outer", appendMember(raw, `"kind":"second"`), "duplicate JSON object key")
	add("escaped_duplicate_outer", appendMember(raw, `"\u006bind":"second"`), "duplicate JSON object key")
	add("duplicate_nested", replace("snapshot", `{"x":0,"x":1}`), "duplicate JSON object key")
	add("blob_alias_only", appendMember(raw, `"Blobs":{}`), "must use exact spelling")
	add("kind_alias_only", bytes.Replace(raw, []byte(`"kind":`), []byte(`"Kind":`), 1), "must use exact spelling")
	for _, value := range []string{`null`, `{}`, `[]`, `""`, `1`, `1.0`, `false`, `"!!!"`} {
		add("blobs_type_"+value, replace("blobs", value), "!")
	}
	for _, value := range []string{`null`, `0`, `false`, `[]`, `{}`} {
		add("kind_type_"+value, replace("kind", value), "!")
	}
	for _, value := range []string{`null`, `[]`, `0`, `false`, `{}`} {
		add("sources_type_"+value, replace("sources", value), "!")
	}
	add("base64_bad", replace("blobs", `{`+string(nameJSON)+`:"!!!"}`), "illegal base64")
	add("base64_number", replace("blobs", `{`+string(nameJSON)+`:9007199254740993}`), "!")
	add("unknown_before_type", []byte(`{"unknown":true,"kind":1,"blobs":{}}`), `unknown field "unknown"`)
	add("type_before_unknown", []byte(`{"kind":1,"unknown":true,"blobs":{}}`), "cannot unmarshal number")
	add("blob_error_before_type", []byte(`{"blobs":{"a":"!!!"},"kind":1}`), "illegal base64")
	add("type_before_blob_error", []byte(`{"kind":1,"blobs":{"a":"!!!"}}`), "cannot unmarshal number")
	add("duplicate_before_unknown", []byte(`{"unknown":true,"blobs":{"a":"","a":""}}`), "duplicate JSON object key")
	add("unicode_before_syntax", []byte(`{"kind":"\ud800",`), "unpaired Unicode surrogate")
	add("syntax_before_duplicate", []byte(`{"blobs":{"a":"","a":""},`), "invalid JSON")
	for name, value := range map[string]string{
		"unicode_surrogate_high":    `{"blobs":{"a":"\ud800"}}`,
		"unicode_surrogate_low":     `{"blobs":{"a":"\udc00"}}`,
		"unicode_bad_escape":        `{"blobs":{"a":"\u+001"}}`,
		"unicode_paired_key":        `{"blobs":{"\ud83d\ude00":""}}`,
		"unicode_utf8_key":          `{"blobs":{"😀":""}}`,
		"unknown_numeric_precision": `{"unknown":9007199254740993}`,
		"unknown_negative_zero":     `{"unknown":-0}`,
		"unknown_decimal":           `{"unknown":1.00}`,
		"null_root":                 `null`,
		"array_root":                `[]`,
		"number_root":               `1`,
		"empty":                     ``,
		"whitespace_only":           " \n\t",
		"unterminated_blob":         `{"blobs":{"a":"`,
		"missing_colon":             `{"blobs" {}}`,
		"missing_value":             `{"blobs":}`,
		"trailing_comma":            `{"blobs":{},}`,
		"truncated_escape":          `{"blobs":{"a":"\`,
		"invalid_escape":            `{"blobs":{"a":"\x00"}}`,
	} {
		add(name, []byte(value), "!")
	}
	add("invalid_utf8", []byte{'{', '"', 'b', 'l', 'o', 'b', 's', '"', ':', '{', '"', 0xff, '"', ':', '"', '"', '}', '}'}, "JSON is not UTF-8")
	for _, suffix := range []string{"x", "{}", " null", " true", "\x00", ` "\ud800"`} {
		add("trailing_"+suffix, append(bytes.Clone(raw), []byte(suffix)...), "!")
	}
	for _, depth := range []int{63, 64, 65, 10001} {
		add("depth_"+strings.Repeat("x", min(depth, 65)), []byte(`{"blobs":`+strings.Repeat("[", depth)+`0`+strings.Repeat("]", depth)+`}`), "!")
	}
	withoutBlobs := make(map[string]json.RawMessage, len(members))
	for key, value := range members {
		if key != "blobs" {
			withoutBlobs[key] = value
		}
	}
	absent, err := json.Marshal(withoutBlobs)
	require.NoError(t, err)
	add("absent_blobs", absent, "!")
	cases = append(cases, readerOracleCase{Name: "canceled_before_invalid", Raw: []byte("invalid"), Canceled: true, Want: "context canceled"})
	encoded, err := json.Marshal(cases)
	require.NoError(t, err)
	wantRaw := readeroracle.Run(t, oracle, "TestCollectionReaderFrozen15cParity", cases)
	var expected []readerOracleObservation
	require.NoError(t, json.Unmarshal(wantRaw, &expected))
	require.Len(t, expected, len(cases))
	passed := 0
	for index, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			if test.Want == "" {
				require.Empty(t, expected[index].Error)
				require.False(t, expected[index].Nil)
			} else {
				require.NotEmpty(t, expected[index].Error)
				if test.Want != "!" {
					require.Contains(t, expected[index].Error, test.Want)
				}
				require.True(t, expected[index].Nil)
			}
			got := observeFrozenCollectionReader(t, test)
			wantEncoded, err := json.Marshal(expected[index])
			require.NoError(t, err)
			gotEncoded, err := json.Marshal(got)
			require.NoError(t, err)
			require.Equal(t, wantEncoded, gotEncoded, "exact first failure and all remarshal/projection/request values must match frozen15c")
			passed++
		})
	}
	require.Equal(t, len(cases), passed)
	// A different observation must execute again against current bytes, while
	// retaining exactly the same compiled original reader.
	changed := readerOracleCase{Name: "new_input", Raw: []byte(`{"kind":1}`)}
	again := readeroracle.Run(t, oracle, "TestCollectionReaderFrozen15cParity", []readerOracleCase{changed})
	var fresh []readerOracleObservation
	require.NoError(t, json.Unmarshal(again, &fresh))
	require.Len(t, fresh, 1)
	require.Equal(t, changed.Name, fresh[0].Name)
	require.Contains(t, fresh[0].Error, "cannot unmarshal number")
	require.NotEqual(t, expected[0].Error, fresh[0].Error)
	binaryAfter, err := os.Stat(oracle)
	require.NoError(t, err)
	require.True(t, os.SameFile(binaryBefore, binaryAfter))
	require.Equal(t, binaryBefore.ModTime(), binaryAfter.ModTime())
	if directory := os.Getenv("WAZA_READER_PROOF_DIR"); directory != "" {
		require.NoError(t, os.WriteFile(filepath.Join(directory, "semantic-cases.json"), encoded, 0600))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "semantic-frozen15c-observations.json"), wantRaw, 0600))
	}
}
