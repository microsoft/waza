package releasepolicy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReleaseUnicodePolicyIdentity(t *testing.T) {
	p := testPolicy(t, 8)
	p.Design.Independence.Justification = "Unicode observation \ufffd."
	p = sealTestPolicy(t, p)
	good := testBytes(t, p)
	require.Equal(t, 1, bytes.Count(good, []byte("\ufffd")))
	decoded, err := DecodePolicy(good)
	require.NoError(t, err)
	require.Equal(t, p, decoded)

	for _, escape := range []string{`\ud800`, `\udfff`} {
		bad := bytes.Replace(good, []byte("\ufffd"), []byte(escape), 1)
		require.NotEqual(t, good, bad)
		t.Run(escape+"/decode", func(t *testing.T) {
			decoded, err := DecodePolicy(bad)
			t.Logf("malformed policy retained declared identity=%t, error=%v", decoded != nil && decoded.Digest == p.Digest, err)
			require.ErrorContains(t, err, "unpaired Unicode surrogate")
			require.Nil(t, decoded)
		})
		t.Run(escape+"/decision", func(t *testing.T) {
			directory := authoredRootCollection(t, p, true)
			original, err := ReadDecision(directory)
			require.NoError(t, err)
			require.True(t, original.Accepted)
			require.NoError(t, os.WriteFile(filepath.Join(directory, "policy.json"), bad, 0o600))
			decision, err := ReadDecision(directory)
			t.Logf("malformed policy decision accepted=%t, error=%v", decision.Accepted, err)
			require.ErrorContains(t, err, "unpaired Unicode surrogate")
			require.False(t, decision.Accepted)
			require.Equal(t, "invalid", decision.Compatibility.State)
		})
	}
}

func TestReleaseUnicodeJournalIdentity(t *testing.T) {
	p := testPolicy(t, 8)
	events := retryTestEvents(t, p, "passed")
	for i := range events {
		if events[i].Type == "end_arm" {
			events[i].Usage[0].Reason = "Unicode observation \ufffd."
			break
		}
	}
	good := retryTestJournal(t, p, events, retryTestStream(t, events))
	require.Equal(t, 1, bytes.Count(good, []byte("\ufffd")))
	journal, err := DecodeJournal(good, p)
	require.NoError(t, err)
	require.Equal(t, events, journal.Events)
	for _, escape := range []string{`\ud800`, `\udfff`} {
		t.Run(escape, func(t *testing.T) {
			bad := bytes.Replace(good, []byte("\ufffd"), []byte(escape), 1)
			require.NotEqual(t, good, bad)
			journal, err := DecodeJournal(bad, p)
			require.ErrorContains(t, err, "unpaired Unicode surrogate")
			require.Nil(t, journal)
		})
	}
}

func TestReleaseUnicodePartialTape(t *testing.T) {
	p := testPolicy(t, 8)
	events := retryTestEvents(t, p, "passed")[:2]
	events[0].EvalID = "Unicode observation \ufffd."
	good := retryTestStream(t, events)
	require.Equal(t, 1, bytes.Count(good, []byte("\ufffd")))
	for _, escape := range []string{`\ud800`, `\udfff`} {
		t.Run(escape, func(t *testing.T) {
			directory := authoredRootCollection(t, p, false)
			name := filepath.Join(directory, "journal.ndjson")
			require.NoError(t, os.WriteFile(name, good, 0o600))
			original, err := ReadDecision(directory)
			require.ErrorContains(t, err, "reading completed shared journal")
			require.False(t, original.Accepted)
			require.NotEqual(t, "invalid", original.Compatibility.State)
			bad := bytes.Replace(good, []byte("\ufffd"), []byte(escape), 1)
			require.NotEqual(t, good, bad)
			require.NoError(t, os.WriteFile(name, bad, 0o600))
			decision, err := ReadDecision(directory)
			require.ErrorContains(t, err, "unpaired Unicode surrogate")
			require.False(t, decision.Accepted)
			require.Equal(t, "invalid", decision.Compatibility.State)
		})
	}
}

func TestReleaseUnicodeResultDecoders(t *testing.T) {
	directory := authoredRootCollection(t, testPolicy(t, 8), true)
	rows, err := os.ReadFile(filepath.Join(directory, "baseline.results.json"))
	require.NoError(t, err)
	result, err := DecodeResults(rows)
	require.NoError(t, err)
	bindingBytes, err := os.ReadFile(filepath.Join(directory, "baseline.result-binding.json"))
	require.NoError(t, err)
	binding, err := DecodeResult(bindingBytes)
	require.NoError(t, err)
	for _, text := range []string{"\ufffd", "\U00010000", "\U0010ffff", `\ud800`, "\\\"\U0001f600"} {
		t.Run(text, func(t *testing.T) {
			result.Rows[0].Run.FinalOutput = text
			goodRows, err := json.Marshal(result)
			require.NoError(t, err)
			actual, err := DecodeResults(goodRows)
			require.NoError(t, err)
			require.Equal(t, result, actual)
			binding.Attempts[0].Checks[0].Grader = text
			binding.Attempts[0].Checks[0].Score = json.Number("1.000")
			goodBinding, err := json.Marshal(binding)
			require.NoError(t, err)
			actualBinding, err := DecodeResult(goodBinding)
			require.NoError(t, err)
			require.Equal(t, binding, actualBinding)
			if text != "\ufffd" {
				return
			}
			for _, escape := range []string{`\ud800`, `\udfff`} {
				t.Run(escape+"/rows", func(t *testing.T) {
					require.Equal(t, 1, bytes.Count(goodRows, []byte(text)))
					actual, err := DecodeResults(bytes.Replace(goodRows, []byte(text), []byte(escape), 1))
					require.ErrorContains(t, err, "unpaired Unicode surrogate")
					require.Nil(t, actual)
				})
				t.Run(escape+"/binding", func(t *testing.T) {
					require.Equal(t, 1, bytes.Count(goodBinding, []byte(text)))
					actual, err := DecodeResult(bytes.Replace(goodBinding, []byte(text), []byte(escape), 1))
					require.ErrorContains(t, err, "unpaired Unicode surrogate")
					require.Nil(t, actual)
				})
			}
		})
	}
}

func TestReleaseUnicodeObjectAdmission(t *testing.T) {
	for _, input := range []string{`{"\ud800":1}`, `{"value":"\udfff"}`, `{"value":"\ud800\u0041"}`} {
		object, err := objectJSON([]byte(input))
		require.ErrorContains(t, err, "unpaired Unicode surrogate")
		require.Nil(t, object)
	}
	for _, input := range []string{
		`{"\ud800\udc00":"\ufffd","n":9007199254740993}`,
		`{"\\ud800":"\\udfff","n":9007199254740993}`,
		`{"value":"\"\ud83d\ude00","n":9007199254740993}`,
	} {
		object, err := objectJSON([]byte(input))
		require.NoError(t, err)
		require.Equal(t, json.Number("9007199254740993"), object["n"])
	}
	_, err := objectJSON([]byte(`{"a":1,"\u0061":2}`))
	require.ErrorContains(t, err, "duplicate JSON object key")
}
