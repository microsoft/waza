package jsonutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func tokenParse(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("JSON is not UTF-8")
	}
	if err := ValidateUnicodeEscapes(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := read(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("JSON contains trailing data")
	}
	return value, nil
}

func requireTokenParity(t *testing.T, data []byte) {
	t.Helper()
	before := bytes.Clone(data)
	want, wantErr := tokenParse(data)
	got, gotErr := Parse(data)
	require.Equal(t, before, data, "parsing must not mutate caller bytes")
	if wantErr != nil {
		require.EqualError(t, gotErr, wantErr.Error(), "%q", data)
	} else {
		require.NoError(t, gotErr, "%q", data)
		require.Equal(t, want, got, "%q", data)
	}
}

func TestParseMatchesTokenDecoderBoundaries(t *testing.T) {
	for _, value := range []string{
		`null`, `true`, `false`, `-0`, `1.00`, `1e0`, `9007199254740993`,
		`""`, `"\ud83d\ude00"`, `"\\\"[]{}:,"`, `[]`, `{}`,
		`{"unknown":[null,{},[],{"n":9007199254740993}]}`,
	} {
		for _, whitespace := range []string{"", " \t\r\n"} {
			input := whitespace + value + whitespace
			requireTokenParity(t, []byte(input))
			for depth := range 67 {
				requireTokenParity(t, []byte(strings.Repeat("[", depth)+input+strings.Repeat("]", depth)))
				requireTokenParity(t, []byte(strings.Repeat(`{"nested":`, depth)+input+strings.Repeat("}", depth)))
			}
		}
	}
	keys := []string{`"a"`, `"\u0061"`, `"/"`, `"\/"`, `"é"`, `"\u00e9"`, `"😀"`, `"\ud83d\ude00"`, `"\\u0061"`, `""`}
	for _, first := range keys {
		for _, second := range keys {
			input := []byte(fmt.Sprintf(`{"unknown":[{%s:0,%s:1}]}`, first, second))
			requireTokenParity(t, input)
		}
	}
	for _, invalid := range []string{
		``, ` `, `{`, `[`, `{"a":}`, `[1,]`, `{"a":1,}`, `{"a":1 "b":2}`,
		`[1 2]`, `{} {}`, `true false`, `{"a":1,"a":2} trailing`,
		`{"a":1,"a":}`, `"\ud800"`, `"\udfff"`, `"\ud800\\udc00"`,
		`"\u+001"`, `"\x00"`, `"unterminated`, `"trailing\`,
	} {
		requireTokenParity(t, []byte(invalid))
	}
}

func TestParseDetachesAdmittedValues(t *testing.T) {
	data := []byte(`{"key":["value",9007199254740993,null,{},[]]}`)
	value, err := Parse(data)
	require.NoError(t, err)
	for i := range data {
		data[i] = '!'
	}
	require.Equal(t, map[string]any{"key": []any{
		"value", json.Number("9007199254740993"), nil, map[string]any{}, []any{},
	}}, value)
}

func TestParseTokenPathBoundary(t *testing.T) {
	for _, size := range []int{(64 << 10) - 1, 64 << 10, (64 << 10) + 1} {
		for _, suffix := range []string{`}`, `,"key":0,"\u006bey":1}`, `,"unknown":[{"x":1,"x":2}]}`, `,"invalid":]}`} {
			prefix := `{"content":"`
			middle := strings.Repeat("x", size-len(prefix)-len(`"`)-len(suffix))
			data := []byte(prefix + middle + `"` + suffix)
			require.Len(t, data, size)
			requireTokenParity(t, data)
		}
	}
}

func TestParseObjectKeySetBoundary(t *testing.T) {
	for count := range 21 {
		members := make([]string, count)
		for index := range members {
			members[index] = fmt.Sprintf(`"key%d":0`, index)
		}
		prefix := strings.Join(members, ",")
		requireTokenParity(t, []byte("{"+prefix+"}"))
		if count > 0 {
			for _, duplicate := range []string{`"key0"`, `"\u006bey0"`, fmt.Sprintf(`"key%d"`, count-1)} {
				requireTokenParity(t, []byte("{"+prefix+","+duplicate+":1}"))
			}
		}
		for _, escaped := range []string{`"\u0061"`, `"a"`} {
			withAlias := append(append([]string{}, members...), escaped+`:null`, `"a":0`)
			requireTokenParity(t, []byte("{"+strings.Join(withAlias, ",")+"}"))
		}
	}
}

func FuzzParseMatchesTokenDecoder(f *testing.F) {
	for _, seed := range []string{
		`{}`, `[]`, `null`, `true`, `9007199254740993`, `{"n":1.00,"e":1e0}`,
		`{"a":1,"\u0061":2}`, `{"é":0,"\u00e9":1}`,
		`{"unknown":[{"\/":[{"\ud83d\ude00":null,"😀":1}]}]}`,
		`{"value":"\\\"[]{}:,"}`, `{"nested":[null,{},[],{"n":9007199254740993}]}`,
		strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65),
		`"\ud800"`, `{"a":1,}`, `"unterminated`, string([]byte{'"', 0xff, '"'}),
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		requireTokenParity(t, data)
	})
}

func FuzzParseStructuredTokenParity(f *testing.F) {
	for _, depth := range []uint8{0, 1, 8, 61, 64, 66} {
		f.Add([]byte(`value\"[]{}:,\ud83d\ude00`), depth, uint8(0), false)
		f.Add([]byte("é 😀"), depth, uint8(1), true)
	}
	f.Fuzz(func(t *testing.T, data []byte, depth, key uint8, duplicate bool) {
		encoded, err := json.Marshal(string(data))
		require.NoError(t, err)
		aliases := [][2]string{
			{`"a"`, `"\u0061"`}, {`"é"`, `"\u00e9"`},
			{`"😀"`, `"\ud83d\ude00"`}, {`"/"`, `"\/"`}, {`""`, `""`},
		}
		names := aliases[int(key)%len(aliases)]
		if !duplicate {
			names[1] = `"different"`
		}
		document := `{"unknown":[{` + names[0] + `:` + string(encoded) + `,` + names[1] +
			`:9007199254740993,"empty":{},"array":[]}]}`
		nesting := int(depth % 67)
		document = strings.Repeat("[", nesting) + document + strings.Repeat("]", nesting)
		requireTokenParity(t, []byte(document))
	})
}

func FuzzUnicodeEscapeScanMatchesByteWalk(f *testing.F) {
	for _, seed := range []string{
		``, `ordinary UTF-8 é 😀`, `\`, `\\`, `\\\`, `\ud800`, `\udfff`,
		`\ud83d\ude00`, `\\ud800`, `\\\ud800\udc00`, `\u+001`, `\u000x`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var want error
		for i := 0; i < len(data); i++ {
			if data[i] != '\\' {
				continue
			}
			i++
			if i >= len(data) {
				want = errors.New("JSON contains a truncated escape")
				break
			}
			if data[i] != 'u' {
				continue
			}
			code, ok := unicodeEscapeCode(data[i+1:])
			if !ok {
				want = errors.New("JSON contains an invalid Unicode escape")
				break
			}
			i += 4
			if code >= 0xdc00 && code <= 0xdfff {
				want = errors.New("JSON contains an unpaired Unicode surrogate")
				break
			}
			if code >= 0xd800 && code <= 0xdbff {
				if len(data)-i < 7 || data[i+1] != '\\' || data[i+2] != 'u' {
					want = errors.New("JSON contains an unpaired Unicode surrogate")
					break
				}
				low, ok := unicodeEscapeCode(data[i+3:])
				if !ok || low < 0xdc00 || low > 0xdfff {
					want = errors.New("JSON contains an unpaired Unicode surrogate")
					break
				}
				i += 6
			}
		}
		got := ValidateUnicodeEscapes(data)
		if want != nil {
			require.EqualError(t, got, want.Error())
		} else {
			require.NoError(t, got)
		}
	})
}

func BenchmarkStrictJSONParse(b *testing.B) {
	for name, data := range map[string][]byte{
		"object":  []byte(`{"n":9007199254740993,"a":[null,true,{},[]],"key":"value"}`),
		"4MiB":    []byte(`{"content":"` + strings.Repeat("r", 4*1024*1024) + `"}`),
		"history": []byte("[" + strings.TrimSuffix(strings.Repeat(`{"n":9007199254740993,"a":[null,true,{},[]],"key":"value"},`, 256), ",") + "]"),
	} {
		want, err := tokenParse(data)
		require.NoError(b, err)
		got, err := Parse(data)
		require.NoError(b, err)
		require.True(b, reflect.DeepEqual(want, got))
		for _, implementation := range []string{"token-decoder", "strict-key-scan"} {
			b.Run(name+"/"+implementation, func(b *testing.B) {
				parse := tokenParse
				if implementation == "strict-key-scan" {
					parse = Parse
				}
				b.SetBytes(int64(len(data)))
				b.ReportAllocs()
				for b.Loop() {
					if _, err := parse(data); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
