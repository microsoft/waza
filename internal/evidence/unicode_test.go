package evidence

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvidenceUnicodeIdentity(t *testing.T) {
	for name, good := range map[string][]byte{
		"key":   []byte("{\"\ufffd\":\"ok\",\"n\":9007199254740993}"),
		"value": []byte("{\"value\":\"\ufffd\",\"n\":9007199254740993}"),
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, 1, bytes.Count(good, []byte("\ufffd")))
			digest, err := JSONDigest(json.RawMessage(good))
			require.NoError(t, err)
			manifest := sampleManifest(t)
			manifest.Artifacts[0].ContentDigest = digest
			require.NoError(t, Seal(manifest))
			reference, err := Reference(manifest, "receipt")
			require.NoError(t, err)
			require.NoError(t, VerifyContent(manifest, reference, good, true, true))
			for _, escape := range []string{`\ud800`, `\udfff`} {
				bad := bytes.Replace(good, []byte("\ufffd"), []byte(escape), 1)
				require.NotEqual(t, good, bad)
				t.Run(escape+"/digest", func(t *testing.T) {
					actual, err := JSONDigest(json.RawMessage(bad))
					t.Logf("malformed content retained canonical digest=%t, error=%v", actual != nil && *actual == *digest, err)
					require.ErrorContains(t, err, "unpaired Unicode surrogate")
					require.Nil(t, actual)
				})
				t.Run(escape+"/content", func(t *testing.T) {
					err := VerifyContent(manifest, reference, bad, true, true)
					t.Logf("malformed preserved content verified=%t", err == nil)
					require.ErrorContains(t, err, "unpaired Unicode surrogate")
				})
			}
		})
	}
}

func TestEvidenceUnicodeWellFormedControls(t *testing.T) {
	for _, good := range []string{
		`{"\ud800\udc00":"\ufffd","n":9007199254740993}`,
		`{"value":"\uDBFF\uDFFF","n":9007199254740993}`,
		`{"\\ud800":"\\udfff","n":9007199254740993}`,
		`{"value":"\"\ud83d\ude00","n":9007199254740993}`,
	} {
		t.Run(good, func(t *testing.T) {
			digest, err := JSONDigest(json.RawMessage(good))
			require.NoError(t, err)
			manifest := sampleManifest(t)
			manifest.Artifacts[0].ContentDigest = digest
			require.NoError(t, Seal(manifest))
			reference, err := Reference(manifest, "receipt")
			require.NoError(t, err)
			require.NoError(t, VerifyContent(manifest, reference, []byte(good), true, true))
			var decoded any
			decoder := json.NewDecoder(bytes.NewBufferString(good))
			decoder.UseNumber()
			require.NoError(t, decoder.Decode(&decoded))
			expected, err := JSONDigest(decoded)
			require.NoError(t, err)
			require.Equal(t, expected, digest)
		})
	}
}
