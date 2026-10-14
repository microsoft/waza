package testutil

import (
	"net/url"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestFileURLPreservesLocalPathAndWindowsDrive(t *testing.T) {
	for _, test := range []struct{ path, encoded, decoded string }{
		{"/tmp/schema.json", "file:///tmp/schema.json", "/tmp/schema.json"},
		{"C:/Users/Runner/schema.json", "file:///C:/Users/Runner/schema.json", "/C:/Users/Runner/schema.json"},
		{"C:/Program Files/schema #1.json", "file:///C:/Program%20Files/schema%20%231.json", "/C:/Program Files/schema #1.json"},
	} {
		t.Run(test.path, func(t *testing.T) {
			location := FileURL(test.path)
			require.Equal(t, test.encoded, location)
			parsed, err := url.Parse(location)
			require.NoError(t, err)
			require.Empty(t, parsed.Host)
			require.Equal(t, test.decoded, parsed.Path)
		})
	}
	path := filepath.Join(t.TempDir(), "valid schema.json")
	actual, err := (jsonschema.FileLoader{}).ToFile(FileURL(path))
	require.NoError(t, err)
	require.Equal(t, path, actual, "native loader retains the actual platform's absolute fixture path")
}
