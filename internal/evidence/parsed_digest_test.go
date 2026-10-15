package evidence

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/stretchr/testify/require"
)

func TestParsedDigestPreservesCanonicalIdentity(t *testing.T) {
	for _, content := range []string{
		`null`, `[]`, `{}`, `""`,
		`{"z":[9007199254740993,1.00,1e0,-0],"a":"<>&\ud83d\ude00"}`,
		`{"nested":{"empty":null,"array":[],"object":{}}}`,
	} {
		t.Run(content, func(t *testing.T) {
			value, err := jsonutil.Parse([]byte(content))
			require.NoError(t, err)
			want, err := JSONDigest(json.RawMessage(content))
			require.NoError(t, err)
			got, err := parsedJSONDigest(value)
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}
	_, err := parsedJSONDigest(func() {})
	require.EqualError(t, err, "evidence: content cannot be normalized")
}

func TestContentVerificationAlwaysAdmitsFreshJSON(t *testing.T) {
	manifest := sampleManifest(t)
	resolver, err := NewResolver(manifest)
	require.NoError(t, err)
	reference, err := Reference(manifest, "receipt")
	require.NoError(t, err)
	reference.Pointer = "/a~1b/0"
	content := []byte(`{"count":9007199254740993,"a/b":["ok"]}`)
	require.NoError(t, resolver.VerifyContent(reference, content, true, true))
	for _, invalid := range []string{
		`{"count":9007199254740992,"a/b":["ok"]}`,
		`{"count":9007199254740993,"a/b":null}`,
		`{"count":9007199254740993,"a/b":[]}`,
		`{"count":9007199254740993,"a/b":["ok"],"count":9007199254740993}`,
		`{"count":9007199254740993,"a/b":["ok"]} {}`,
		`{"count":9007199254740993,"a/b":["\ud800"]}`,
	} {
		t.Run(invalid, func(t *testing.T) {
			require.Error(t, resolver.VerifyContent(reference, []byte(invalid), true, true))
			require.Error(t, VerifyContent(manifest, reference, []byte(invalid), true, true))
		})
	}

	content[len(content)-5] = 'n'
	require.Error(t, resolver.VerifyContent(reference, content, true, true), "no cross-call content cache")
	reference.Pointer = "/a~1b/1"
	require.Error(t, resolver.VerifyContent(reference,
		[]byte(`{"count":9007199254740993,"a/b":["ok"]}`), true, true), "digest equality does not bypass pointer admission")
}

func BenchmarkJSONDigestParsedTree(b *testing.B) {
	for _, size := range []int{256, 4 * 1024 * 1024} {
		data, err := json.Marshal(map[string]any{"content": strings.Repeat("r", size), "count": json.Number("9007199254740993")})
		require.NoError(b, err)
		value, err := jsonutil.Parse(data)
		require.NoError(b, err)
		expected, err := JSONDigest(value)
		require.NoError(b, err)
		for _, name := range []string{"round-trip", "parsed-tree"} {
			b.Run(fmt.Sprintf("%d/%s", size, name), func(b *testing.B) {
				digest := JSONDigest
				if name == "parsed-tree" {
					digest = parsedJSONDigest
				}
				b.SetBytes(int64(len(data)))
				b.ReportAllocs()
				for b.Loop() {
					got, err := digest(value)
					if err != nil || got.SHA256 != expected.SHA256 {
						b.Fatalf("canonical identity changed: %v, %v", got, err)
					}
				}
			})
		}
	}
}
