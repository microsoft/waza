package assurance

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualificationTerminalNativeResultRequiresActualScalarTokens(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	m, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	ordinal := uint64(0)
	for _, job := range m.Jobs {
		if !job.ExpectedPassed {
			ordinal = job.Ordinal
			break
		}
	}
	require.False(t, m.Jobs[ordinal].ExpectedPassed, "false verdict would otherwise be supplied by decoder default")
	terminal := qualificationTestObservedTerminal(t, manifest, ordinal)
	wire, err := qualificationDecode[qualificationTerminalWire](terminal.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	for _, key := range []string{"identifier", "type", "score", "weight", "passed", "duration_ms", "feedback"} {
		for _, null := range []bool{false, true} {
			name := key + "/missing"
			if null {
				name = key + "/null"
			}
			t.Run(name, func(t *testing.T) {
				copy := wire
				result := map[string]json.RawMessage{}
				require.NoError(t, json.Unmarshal(*wire.NativeResult, &result))
				if null {
					result[key] = json.RawMessage("null")
				} else {
					delete(result, key)
				}
				data, err := json.Marshal(result)
				require.NoError(t, err)
				copy.NativeResult = new(json.RawMessage(data))
				document, err := qualificationSeal(copy)
				require.NoError(t, err)
				_, err = qualificationParseTerminal(document.bytes(), manifest)
				require.Error(t, err, "actual native token required, not decoder zero/false default")
			})
		}
	}
	wire.NativeResult = new(json.RawMessage("null"))
	document, err := qualificationSeal(wire)
	require.NoError(t, err)
	_, err = qualificationParseTerminal(document.bytes(), manifest)
	require.Error(t, err, "optional pointer cannot hide raw null")
}
