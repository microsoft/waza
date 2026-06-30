package argmatcher

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestUnmarshalJSON_AllKindsAndErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Kind
	}{
		{"equals", `{"equals":"v"}`, KindEquals},
		{"regex", `{"regex":"^foo$"}`, KindRegex},
		{"contains", `{"contains":"sub"}`, KindContains},
		{"range", `{"range":{"gte":1,"lte":2}}`, KindRange},
		{"schema", `{"json_schema":{"type":"string"}}`, KindJSONSchema},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m Matcher
			require.NoError(t, json.Unmarshal([]byte(c.in), &m))
			require.Equal(t, c.want, m.Kind)
		})
	}

	errCases := []string{
		`{}`,                         // empty
		`{"equals":"a","regex":"b"}`, // multi
		`{"unknown":"x"}`,            // unknown kind
		`{"equals":<bad>}`,           // invalid json
		`{"regex":123}`,              // wrong type
		`{"contains":123}`,           // wrong type
		`{"range":"bad"}`,            // wrong type
		`{"json_schema":"bad"}`,      // wrong type
	}
	for _, in := range errCases {
		var m Matcher
		require.Error(t, json.Unmarshal([]byte(in), &m), in)
	}
}

func TestMarshalJSON_AllKinds(t *testing.T) {
	cases := []Matcher{
		{Kind: KindEquals, Equals: "x"},
		{Kind: KindRegex, Regex: "^a$"},
		{Kind: KindContains, Contains: "sub"},
		{Kind: KindRange, Range: &RangeSpec{}},
		{Kind: KindJSONSchema, JSONSchema: map[string]any{"type": "string"}},
		{Kind: ""},
	}
	for _, m := range cases {
		b, err := json.Marshal(m)
		require.NoError(t, err)
		require.NotEmpty(t, b)
	}
	bad := Matcher{Kind: "nope"}
	_, err := json.Marshal(bad)
	require.Error(t, err)
}

func TestIsCompiled(t *testing.T) {
	m := Matcher{Kind: KindRegex, Regex: "^x$"}
	require.False(t, m.IsCompiled())
	require.NoError(t, m.Compile())
	require.True(t, m.IsCompiled())

	s := Matcher{Kind: KindJSONSchema, JSONSchema: map[string]any{"type": "string"}}
	require.False(t, s.IsCompiled())
	require.NoError(t, s.Compile())
	require.True(t, s.IsCompiled())

	// Other kinds: IsCompiled returns true unconditionally
	require.True(t, (&Matcher{Kind: KindEquals}).IsCompiled())
	require.True(t, (&Matcher{Kind: KindContains}).IsCompiled())
}

func TestToFloatNumericKinds(t *testing.T) {
	cases := []any{
		float64(1.5), float32(2.5),
		int(3), int32(4), int64(5),
		uint(6), uint32(7), uint64(8),
		json.Number("9.5"),
	}
	for _, v := range cases {
		_, ok := toFloat(v)
		require.True(t, ok, "%v", v)
	}

	if _, ok := toFloat(json.Number("notnum")); ok {
		t.Fatal("bad json.Number should fail")
	}
	if _, ok := toFloat("string"); ok {
		t.Fatal("string should fail")
	}
}

func TestYamlKindName(t *testing.T) {
	for _, k := range []yaml.Kind{yaml.DocumentNode, yaml.SequenceNode, yaml.MappingNode, yaml.ScalarNode, yaml.AliasNode, yaml.Kind(99)} {
		_ = yamlKindName(k)
	}
}
