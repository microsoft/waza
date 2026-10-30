package runnercontract

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/microsoft/waza/schemas"
	"github.com/stretchr/testify/require"
)

func TestNegotiationConformance(t *testing.T) {
	data, err := os.ReadFile("testdata/negotiation.json")
	require.NoError(t, err)
	var cases []struct {
		Name, Status, Version        string
		Versions, Required, Optional []string
		Selected, Unavailable        []string
		Exit                         int
	}
	require.NoError(t, json.Unmarshal(data, &cases))
	require.Len(t, cases, 7)
	schema, err := compileSchema(schemas.RunnerNegotiationResponseSchemaJSON)
	require.NoError(t, err)
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			request := NegotiationRequest{NegotiationVersion, tc.Versions, tc.Required, tc.Optional}
			data, err := json.Marshal(request)
			require.NoError(t, err)
			response := Negotiate(data)
			require.Equal(t, tc.Status, response.Status)
			require.Equal(t, tc.Exit, response.ExitCode())
			require.Equal(t, tc.Version, response.Negotiation.Version)
			require.Equal(t, tc.Selected, response.Negotiation.Selected)
			require.Equal(t, tc.Unavailable, response.Negotiation.UnavailableOptional)
			require.Equal(t, tc.Versions, response.Negotiation.RequestedVersions)
			require.Equal(t, tc.Required, response.Negotiation.RequiredCapabilities)
			require.Equal(t, tc.Optional, response.Negotiation.OptionalCapabilities)
			require.Equal(t, Capabilities().Capabilities, response.Negotiation.Available)
			encoded, err := json.Marshal(response)
			require.NoError(t, err)
			var doc any
			require.NoError(t, json.Unmarshal(encoded, &doc))
			require.NoError(t, schema.Validate(doc))
			require.Equal(t, tc.Status == "compatible", len(response.Diagnostics) == 0)
		})
	}
}

func TestNegotiationRejectsWireAmbiguityAndPresence(t *testing.T) {
	base := `{"schema_version":"1.0","versions":["1.0"],"required_capabilities":[],"optional_capabilities":[]}`
	for _, field := range []string{"schema_version", "versions", "required_capabilities", "optional_capabilities"} {
		for _, replacement := range []string{"omitted", "null", `""`, "false", "0"} {
			t.Run(field+"/"+replacement, func(t *testing.T) {
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(base), &fields))
				if replacement == "omitted" {
					delete(fields, field)
				} else {
					fields[field] = json.RawMessage(replacement)
				}
				data, err := json.Marshal(fields)
				require.NoError(t, err)
				response := Negotiate(data)
				require.Equal(t, "invalid_request", response.Status)
				require.Empty(t, response.Negotiation.Version)
				require.Empty(t, response.Negotiation.Selected)
				require.NotEmpty(t, response.Diagnostics)
			})
		}
	}
	for _, data := range []string{
		`null`, `{}`, base + `{}`, strings.Replace(base, `"versions":["1.0"]`, `"versions":[]`, 1),
		strings.Replace(base, `"1.0"`, `"2.0"`, 1),
		strings.Replace(base, `"versions":["1.0"]`, `"versions":["1.0","1.0"]`, 1),
		strings.Replace(base, `"optional_capabilities":[]`, `"optional_capabilities":["negotiation","negotiation"]`, 1),
		strings.Replace(base, `"versions":`, `"versions":["2.0"],"versions":`, 1),
		strings.Replace(base, `"versions":`, `"ver\u0073ions":["2.0"],"versions":`, 1),
		strings.Replace(base, `"optional_capabilities":[]`, `"optional_capabilities":["\ud800"]`, 1),
		strings.Replace(base, `"optional_capabilities":[]`, `"optional_capabilities":[" negotiation"]`, 1),
		strings.Replace(base, `"optional_capabilities":[]`, `"optional_capabilities":["negotiation\n"]`, 1),
		strings.Replace(base, `"optional_capabilities":[]`, `"optional_capabilities":[],"allow_paid_execution":false`, 1),
		strings.Repeat(" ", MaxRequestBytes+1),
	} {
		response := Negotiate([]byte(data))
		require.Equal(t, "invalid_request", response.Status, data[:min(200, len(data))])
		require.Empty(t, response.Negotiation.Selected)
		require.NotEmpty(t, response.Diagnostics)
	}
	require.Equal(t, "compatible", Negotiate([]byte(base)).Status)
}

func TestCapturedRequestShapeRemainsCompatibleWithoutExecution(t *testing.T) {
	data, err := os.ReadFile("testdata/request.json")
	require.NoError(t, err)
	request, err := Parse(data)
	require.NoError(t, err)
	require.Equal(t, 2, request.Selection.Trial)
	require.False(t, request.AllowPaidExecution)
	response := Negotiate(data)
	require.Equal(t, "unsupported_capability", response.Status)
	require.Len(t, response.Diagnostics, 1)
	require.Contains(t, response.Diagnostics[0].Message, "terminal-result")
	require.Empty(t, response.Negotiation.Selected)
	request.AllowPaidExecution = true
	request.RequiredCapabilities = []string{"negotiation"}
	paidShape, err := json.Marshal(request)
	require.NoError(t, err)
	require.Equal(t, "compatible", Negotiate(paidShape).Status, "paid permission never implies execution during negotiation")
	for _, replacement := range []string{"omitted", "null", "0", `""`} {
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(data, &fields))
		if replacement == "omitted" {
			delete(fields, "allow_paid_execution")
		} else {
			fields["allow_paid_execution"] = json.RawMessage(replacement)
		}
		invalid, err := json.Marshal(fields)
		require.NoError(t, err)
		_, err = Parse(invalid)
		require.Error(t, err)
		require.Equal(t, "invalid_request", Negotiate(invalid).Status)
	}
}

func TestNegotiationSelectsCompleteProfileByPresence(t *testing.T) {
	compact := `{"schema_version":"1.0","versions":["1.0"],"required_capabilities":[],"optional_capabilities":[]}`
	require.Equal(t, "compatible", Negotiate([]byte(compact)).Status)
	for _, field := range []string{"run_key", "selection", "input", "timeout_ms", "allow_paid_execution"} {
		for _, value := range []string{"null", "false", "0", `""`, "{}"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				data := strings.TrimSuffix(compact, "}") + fmt.Sprintf(",%q:%s}", field, value)
				response := Negotiate([]byte(data))
				require.Equal(t, "invalid_request", response.Status)
				require.Empty(t, response.Negotiation.Selected)
				require.Len(t, response.Diagnostics, 1)
				require.Contains(t, response.Diagnostics[0].Message, "missing properties")
				require.NotContains(t, response.Diagnostics[0].Message, "additional properties")
			})
		}
	}
	response := Negotiate([]byte(strings.TrimSuffix(compact, "}") + `,"unrecognized":null}`))
	require.Equal(t, "invalid_request", response.Status)
	require.Contains(t, response.Diagnostics[0].Message, "additional properties 'unrecognized'")
	require.NotContains(t, response.Diagnostics[0].Message, "missing properties")
}

func TestNegotiationExampleAndDetachedAdvertisement(t *testing.T) {
	data, err := os.ReadFile("../../examples/runner/negotiation.json")
	require.NoError(t, err)
	response := Negotiate(data)
	require.Equal(t, "compatible", response.Status)
	require.Empty(t, response.Negotiation.UnavailableOptional)
	first := Capabilities()
	first.Versions[0] = "99.0"
	first.Capabilities[0] = "exact-unit"
	require.Equal(t, []string{"1.0"}, Capabilities().Versions)
	require.Equal(t, []string{"negotiation", "exact-unit", "inline.mock-text"}, Capabilities().Capabilities)
	schema, err := compileSchema(schemas.RunnerCapabilitiesSchemaJSON)
	require.NoError(t, err)
	encoded, err := json.Marshal(Capabilities())
	require.NoError(t, err)
	var doc any
	require.NoError(t, json.Unmarshal(encoded, &doc))
	require.NoError(t, schema.Validate(doc))
}

func TestNegotiationDiagnosticsStayBoundedWithoutDroppingRequirements(t *testing.T) {
	data, err := os.ReadFile("testdata/request.json")
	require.NoError(t, err)
	request, err := Parse(data)
	require.NoError(t, err)
	request.RequiredCapabilities = nil
	for i := range 200 {
		request.RequiredCapabilities = append(request.RequiredCapabilities, fmt.Sprintf("future-%d", i))
	}
	for _, conflicts := range []bool{false, true} {
		if conflicts {
			request.OptionalCapabilities = request.RequiredCapabilities
		}
		data, err := json.Marshal(request)
		require.NoError(t, err)
		response := Negotiate(data)
		require.NotEqual(t, "compatible", response.Status)
		require.Len(t, response.Diagnostics, 65)
		require.Contains(t, response.Diagnostics[64].Message, "136 additional")
		require.Equal(t, request.RequiredCapabilities, response.Negotiation.RequiredCapabilities)
		require.Empty(t, response.Negotiation.Selected)
	}
	response := Rejected(strings.Repeat("界", 10000))
	require.True(t, utf8.ValidString(response.Diagnostics[0].Message))
	require.Contains(t, response.Diagnostics[0].Message, "diagnostic truncated")
	require.Less(t, len([]rune(response.Diagnostics[0].Message)), 4200)
}

func TestNegotiationResponseSchemaRejectsContradictions(t *testing.T) {
	data, err := os.ReadFile("../../examples/runner/negotiation.json")
	require.NoError(t, err)
	schema, err := compileSchema(schemas.RunnerNegotiationResponseSchemaJSON)
	require.NoError(t, err)
	for _, status := range []string{"unsupported_version", "unsupported_capability", "invalid_request"} {
		response := Negotiate(data)
		response.Status = status
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		var doc any
		require.NoError(t, json.Unmarshal(encoded, &doc))
		require.Error(t, schema.Validate(doc), "failure must not retain selected capabilities or omit diagnostics")
	}
}
