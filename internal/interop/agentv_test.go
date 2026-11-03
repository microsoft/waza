package interop

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPinnedAgentVFiles(t *testing.T) {
	for name, digest := range map[string]string{
		"LICENSE":       "a310bb09289f002d4d27912f9254926afb9510ce2289b6c6154816ab21fc577f",
		"assertions.ts": "152446e80042b29597eb9d124860a6eb67587be1d642b50c101af3711c9ecb02",
		"suite.yaml":    "5abd706017618e14cf80222a922a9ec04746466f1f5535dfd41557af23841b88",
	} {
		data, err := os.ReadFile("testdata/agentv/" + name)
		require.NoError(t, err)
		require.Equal(t, digest, fmt.Sprintf("%x", sha256.Sum256(data)), name)
	}
}

func TestAgentVProjectionAndSemanticRoundTrip(t *testing.T) {
	data, err := os.ReadFile("testdata/agentv/suite.yaml")
	require.NoError(t, err)
	native, err := ImportContains(data, "contains-check", 1)
	require.NoError(t, err)
	require.Contains(t, string(native), "NOT a converted task or suite")
	external, err := ExportContains(native)
	require.NoError(t, err)
	var assertion map[string]any
	require.NoError(t, yaml.Unmarshal(external, &assertion))
	require.Equal(t, "contains", assertion["type"])
	require.Equal(t, "Hello", assertion["value"])
	require.Equal(t, "agentv-contains", assertion["metric"])
	// Export emits a single assertion, not an invented externally runnable suite.
	envelope := "tests:\n  - id: selected\n    assert:\n"
	for _, line := range strings.Split(strings.TrimSpace(string(external)), "\n") {
		if strings.HasPrefix(line, "metric:") {
			envelope += "      - " + line + "\n"
		} else {
			envelope += "        " + line + "\n"
		}
	}
	again, err := ImportContains([]byte(envelope), "selected", 1)
	require.NoError(t, err)
	var first, second map[string]any
	require.NoError(t, yaml.Unmarshal(native, &first))
	require.NoError(t, yaml.Unmarshal(again, &second))
	require.Equal(t, first, second)
}

func TestAgentVRejectsLossAndAmbiguity(t *testing.T) {
	base := "tests:\n  - id: selected\n    assert:\n      - type: contains\n        value: Hello\n"
	for _, tc := range []struct {
		name, input, diagnostic string
	}{
		{"unknown assertion option", base + "        threshold: 0.5\n", "unsupported field"},
		{"negation", base + "        negate: true\n", "unsupported field"},
		{"weight", base + "        weight: 2\n", "unsupported field"},
		{"required", base + "        required: true\n", "unsupported field"},
		{"script", strings.Replace(base, "type: contains", "type: script", 1), "only literal"},
		{"case folding", strings.Replace(base, "type: contains", "type: icontains", 1), "only literal"},
		{"null", strings.Replace(base, "value: Hello", "value: null", 1), "nonempty literal"},
		{"number", strings.Replace(base, "value: Hello", "value: 42", 1), "nonempty literal"},
		{"empty", strings.Replace(base, "value: Hello", `value: ""`, 1), "nonempty literal"},
		{"environment interpolation", strings.Replace(base, "value: Hello", "value: ${EXPECTED}", 1), "literal"},
		{"template interpolation", strings.Replace(base, "value: Hello", `value: "{{ expected_output }}"`, 1), "literal"},
		{"metric interpolation", base + "        metric: ${METRIC}\n", "nonblank string"},
		{"mapping", strings.Replace(base, "value: Hello", "value: {file: /etc/passwd}", 1), "nonempty literal"},
		{"duplicate", base + "        value: goodbye\n", "unique string keys"},
		{"duplicate test", base + "  - id: selected\n", "duplicate selected"},
		{"alias", strings.Replace(base, "value: Hello", "value: &a Hello", 1), "aliases"},
		{"merge", base + "        <<: {negate: true}\n", "merge keys"},
		{"tag", strings.Replace(base, "value: Hello", "value: !execute shell", 1), "unsupported YAML tag"},
		{"mapping tag", "!execute\n" + base, "unsupported YAML tag"},
		{"sequence tag", strings.Replace(base, "tests:", "tests: !execute", 1), "unsupported YAML tag"},
		{"documents", base + "---\n{}\n", "exactly one document"},
		{"scalar", "null", "nonnull mapping"},
		{"external dataset", "tests: [file://../../secret]\n", "not found"},
		{"malformed", "[", "parsing definition"},
		{"oversized", strings.Repeat("x", MaxBytes+1), "at most"},
		{"invalid utf8", string([]byte{0xff}), "UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := ImportContains([]byte(tc.input), "selected", 1)
			require.ErrorContains(t, err, tc.diagnostic)
			require.Empty(t, output)
		})
	}
	for _, tc := range []struct {
		id string
		n  int
	}{
		{"", 1}, {"selected", 0}, {"missing", 1}, {"selected", 2},
	} {
		_, err := ImportContains([]byte(base), tc.id, tc.n)
		require.Error(t, err)
	}
	t.Run("literal path text is not dereferenced", func(t *testing.T) {
		output, err := ImportContains([]byte(strings.Replace(base, "Hello", "file://../../secret", 1)), "selected", 1)
		require.NoError(t, err)
		require.Contains(t, string(output), "file://../../secret")
	})
	t.Run("metric and CRLF", func(t *testing.T) {
		output, err := ImportContains([]byte(strings.ReplaceAll(base+"        metric: greeting\n", "\n", "\r\n")), "selected", 1)
		require.NoError(t, err)
		require.Contains(t, string(output), "name: greeting")
	})
	t.Run("interpolated selected identity", func(t *testing.T) {
		for _, id := range []string{"${TEST_ID}", "{{ vars.id }}"} {
			source := strings.Replace(base, "id: selected", "id: "+strconv.Quote(id), 1)
			output, err := ImportContains([]byte(source), id, 1)
			require.ErrorContains(t, err, "literal identity without interpolation")
			require.Empty(t, output)
		}
	})
}

func TestAgentVExportRejectsUnsupportedNativeSemantics(t *testing.T) {
	base := "name: greeting\ntype: text\nconfig:\n  contains_cs: [Hello]\n"
	for _, input := range []string{
		base + "weight: 2\n",
		strings.Replace(base, "[Hello]", "[Hello, world]", 1),
		strings.Replace(base, "[Hello]", "[]", 1),
		strings.Replace(base, "[Hello]", "[null]", 1),
		strings.Replace(base, "[Hello]", `[""]`, 1),
		strings.Replace(base, "[Hello]", `["${EXPECTED}"]`, 1),
		strings.Replace(base, "name: greeting", "name: null", 1),
		strings.Replace(base, "type: text", "type: program", 1),
		strings.Replace(base, "contains_cs", "contains", 1),
		base + "  not_contains: [bad]\n",
		"null",
		"---\n{}\n---\n{}\n",
	} {
		out, err := ExportContains([]byte(input))
		require.Error(t, err, input)
		require.Empty(t, out)
	}
}
