package models

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFaultAdmissionBeforeEagerDecode(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"type":"object"}`))
	}))
	defer server.Close()
	invalid := `command_mocks:
  - name: probe
    responses:
      - args: []
        stdout: null
        sequence: [{exit_code: 0}]
graders:
  - type: json_schema
    name: eager
    config:
      schema: {$ref: "` + server.URL + `"}
`
	eval := "schemaVersion: '2.0'\nscenario: recovery\nname: admission\n" + invalid
	for _, parse := range []func([]byte, string) (*EvalSpec, error){ParseEvalSpec, ParseEvalSpecOffline} {
		spec, err := parse([]byte(eval), "eval.yaml")
		require.ErrorContains(t, err, "cannot mix sequence")
		require.Nil(t, spec)
	}
	task := "id: admission\nname: source\ninputs: {prompt: test}\n" + invalid
	for _, parse := range []func([]byte, string) (*TestCase, error){ParseTestCase, ParseTestCaseOffline} {
		tc, err := parse([]byte(task), "task.yaml")
		require.ErrorContains(t, err, "cannot mix sequence")
		require.Nil(t, tc)
	}
	require.Zero(t, requests.Load())
}

func TestFaultAdmissionScalarPresenceAndEligibility(t *testing.T) {
	for _, fragment := range []string{
		"sequence: null",
		"sequence: []",
		"sequence: [{exit_code: 1.0}]",
		"sequence: [{delay_ms: !!float 1}]",
		"stdout: ''\n        sequence: [{exit_code: 0}]",
		"exit_code: 0\n        sequence: [{exit_code: 0}]",
		"sequence: [{exit_code: 1, exit_code: 2}]",
		"sequence: [{sequence: []}]",
	} {
		t.Run(fragment, func(t *testing.T) {
			document := "command_mocks:\n  - name: probe\n    responses:\n      - args: []\n        " + fragment + "\n"
			require.Error(t, validateDeclaredFaults([]byte(document), ScenarioSchemaVersion, false))
			require.Error(t, validateDeclaredFaults([]byte(document), "", true))
		})
	}
	document := []byte("command_mocks:\n  - name: probe\n    responses:\n      - args: []\n        sequence: [{exit_code: 0}]\n")
	require.NoError(t, validateDeclaredFaults(document, ScenarioSchemaVersion, false))
	require.NoError(t, validateDeclaredFaults(document, "", true), "standalone shape does not prove enclosing eval eligibility")
	require.ErrorContains(t, validateDeclaredFaults(document, "1.4", false), "requires explicit")
	tc, err := ParseTestCase(append([]byte("id: finite\ninputs: {prompt: test}\n"), document...), "task.yaml")
	require.ErrorContains(t, err, "not registered")
	require.Nil(t, tc, "intermediate source admission cannot silently ignore valid finite task configuration")
}

func TestFaultAdmissionKeepsLegacyAndPayloadSemantics(t *testing.T) {
	for _, document := range []string{
		"mcp_mocks: [{name: legacy, tools: {inspect: {responses: null}}}]",
		"mcp_mocks: [{name: legacy, tools: {inspect: {responses: [{return: {sequence: []}}]}}}]",
		"command_mocks: []",
		"command_mocks: [{name: probe, responses: [{args: [], stdout: ready}]}]",
		"irrelevant: &cycle {value: *cycle}\ncommand_mocks: []",
		"mcp_mocks: [{name: legacy, tools: {inspect: {legacy_extension: &cycle {self: *cycle}, responses: null}}}]",
	} {
		require.NoError(t, validateDeclaredFaults([]byte(document), "1.4", false))
	}

	document := `matcher: &matcher {args: [], sequence: [{exit_code: !!float 1}]}
command_mocks:
  - name: probe
    responses: [{<<: *matcher}]
`
	require.Error(t, validateDeclaredFaults([]byte(document), ScenarioSchemaVersion, false))
	require.Error(t, validateDeclaredFaults([]byte(strings.ReplaceAll(document, "!!float 1", "0")), "1.4", false))
	aliasedKey := `key: &key sequence
command_mocks:
  - name: probe
    responses:
      - args: []
        *key: [{exit_code: 0}]
`
	require.ErrorContains(t, validateDeclaredFaults([]byte(aliasedKey), "1.4", false), "requires explicit")
	aliasedMerge := `matcher: &matcher {args: [], sequence: [{exit_code: 0}]}
merge: &merge [*matcher]
command_mocks:
  - name: probe
    responses: [{<<: *merge}]
`
	require.ErrorContains(t, validateDeclaredFaults([]byte(aliasedMerge), "1.4", false), "requires explicit")
}

func TestFaultAdmissionHonorsEffectiveTaskOverrides(t *testing.T) {
	finite := `defaults: &defaults
  command_mocks:
    - name: probe
      responses: [{args: [], sequence: [{exit_code: 0}]}]
`
	for _, override := range []string{
		"<<: *defaults\ncommand_mocks: []\n",
		"empty: &empty {command_mocks: []}\n<<: [*empty, *defaults]\n",
	} {
		document := finite + override + "id: legacy\nname: legacy\ninputs: {prompt: test}\n"
		tc, err := ParseTestCase([]byte(document), "task.yaml")
		require.NoError(t, err)
		require.NotNil(t, tc.CommandMocks)
		require.Empty(t, *tc.CommandMocks)
		require.NoError(t, validateDeclaredFaults([]byte(document), "1.4", false))
	}
}
