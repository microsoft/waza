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
