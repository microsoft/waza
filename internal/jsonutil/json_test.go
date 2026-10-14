package jsonutil

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStrictJSONAdmission(t *testing.T) {
	for _, data := range []string{
		`{"a":1,"a":2}`, `{"nested":{"a":1,"a":2}}`, `{} {}`, `{`,
		strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66), string([]byte{'"', 0xff, '"'}),
	} {
		_, err := Parse([]byte(data))
		require.Error(t, err)
	}

	value, err := Parse([]byte(`{"a":[null,true,9007199254740993]}`))
	require.NoError(t, err)
	object, ok := value.(map[string]any)
	require.True(t, ok)
	items, ok := object["a"].([]any)
	require.True(t, ok)
	require.Equal(t, json.Number("9007199254740993"), items[2])
}

func TestStrictJSONUnicodeEscapes(t *testing.T) {
	for _, input := range []string{
		`"\ud800"`, `"\udfff"`, `"\uD800\u0041"`, `"\ud800\\udc00"`,
		`{"\ud800":1}`, `{"value":"\udc00"}`, `"\u+001"`, `"\u000x"`,
		`"\x00"`, `"unterminated`, `"trailing\`,
	} {
		t.Run(input, func(t *testing.T) {
			require.NotPanics(t, func() {
				_, err := Parse([]byte(input))
				require.Error(t, err)
			})
		})
	}
	for _, input := range []string{`"\ud800\udc00"`, `"\uDBFF\uDFFF"`} {
		for length := 1; length < len(input); length++ {
			require.NotPanics(t, func() {
				_, err := Parse([]byte(input[:length]))
				require.Error(t, err)
			})
		}
	}
	for input, expected := range map[string]string{
		`"\ud83d\ude00"`:   "\U0001f600",
		`"\uDBFF\uDFFF"`:   "\U0010ffff",
		`"\ufffd"`:         "\ufffd",
		`"\\ud800"`:        `\ud800`,
		`"\\\\ud800"`:      `\\ud800`,
		`"\\\ud800\udc00"`: "\\\U00010000",
	} {
		value, err := Parse([]byte(input))
		require.NoError(t, err, input)
		require.Equal(t, expected, value)
	}
	value, err := Parse([]byte(`{"\ud83d\ude00":"\uFFFD"}`))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"\U0001f600": "\ufffd"}, value)
}
