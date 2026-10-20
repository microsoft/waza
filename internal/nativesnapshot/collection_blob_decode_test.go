package nativesnapshot

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/stretchr/testify/require"
)

func assertCollectionBlobDecode(t *testing.T, raw []byte) {
	t.Helper()
	var expected map[string][]byte
	wantErr := json.Unmarshal(raw, &expected)
	actual, err := decodeCollectionBlobs(raw)
	if wantErr != nil {
		require.Equal(t, wantErr, err)
	} else {
		require.NoError(t, err)
	}
	require.Equal(t, expected, actual)
}

func TestCollectionBlobDecodeMatchesTypedDecoder(t *testing.T) {
	key := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	for _, test := range []struct {
		name string
		raw  string
	}{
		{"compact", `{"` + key + `":"aGVsbG8=","` + other + `":""}`},
		{"empty", `{}`},
		{"null", `null`},
		{"indented", `{ "` + key + `" : "aGVsbG8=" }`},
		{"short key", `{"short":"aGVsbG8="}`},
		{"escaped key", `{"\u0061` + key[1:] + `":"aGVsbG8="}`},
		{"escaped value", `{"` + key + `":"\u0061GVsbG8="}`},
		{"escaped slash", `{"` + key + `":"\/\/\/\/"}`},
		{"escaped newline", `{"` + key + `":"YWJj\nYWJj"}`},
		{"null value", `{"` + key + `":null}`},
		{"array value", `{"` + key + `":[0,1,255]}`},
		{"mixed values", `{"` + key + `":"aGVsbG8=","` + other + `":[0,1,255]}`},
		{"invalid base64", `{"` + key + `":"!"}`},
		{"invalid base64 suffix", `{"` + key + `":"aGVsbG8=!"}`},
		{"invalid number", `{"` + key + `":1}`},
		{"invalid array", `{"` + key + `":[256]}`},
		{"wrong shape", `[]`},
		{"large payload", `{"` + key + `":"` + strings.Repeat("YWJj", 1<<18) + `"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.True(t, json.Valid([]byte(test.raw)))
			assertCollectionBlobDecode(t, []byte(test.raw))
		})
	}
}

func FuzzCollectionBlobDecodeMatchesTypedDecoder(f *testing.F) {
	key := strings.Repeat("a", 64)
	for _, raw := range []string{
		`{"` + key + `":"aGVsbG8="}`, `{"` + key + `":""}`,
		`{"` + key + `":[0,1,255]}`, `{"` + key + `":"!"}`, `{}`, `null`,
	} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		envelope := []byte(`{"blobs":` + raw + `}`)
		start, end, found, err := jsonutil.ObjectMemberRange(envelope, "blobs")
		if err != nil || !found {
			return
		}
		assertCollectionBlobDecode(t, envelope[start:end])
	})
}
