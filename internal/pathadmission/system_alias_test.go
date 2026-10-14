package pathadmission

import (
	"io/fs"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSystemRootAliasMapping(t *testing.T) {
	for _, alias := range []string{"/var", "/tmp"} {
		for _, target := range []string{"/private" + alias, "private" + alias, "/untrusted", "/private" + alias + "/other"} {
			for _, suffix := range []string{"", "/workspace"} {
				t.Run(alias+target+suffix, func(t *testing.T) {
					actual, err := systemRootAlias(alias+suffix, func(name string) (string, error) {
						require.Equal(t, alias, name)
						return target, nil
					})
					if target == "/private"+alias || target == "private"+alias {
						require.NoError(t, err)
						require.Equal(t, "/private"+alias+suffix, actual)
					} else {
						require.Error(t, err)
						require.Empty(t, actual)
					}
				})
			}
		}
		_, err := systemRootAlias(alias, func(string) (string, error) { return "", fs.ErrPermission })
		require.ErrorIs(t, err, fs.ErrPermission)
	}
	for _, path := range []string{"/tmp-untrusted/workspace", "/var-other", "/private/tmp/workspace", "/workspace"} {
		actual, err := systemRootAlias(path, func(string) (string, error) {
			t.Fatal("non-system paths must not be rewritten")
			return "", nil
		})
		require.NoError(t, err)
		require.Equal(t, path, actual)
	}
}

func TestSystemRootAliasPlatform(t *testing.T) {
	if runtime.GOOS != "darwin" {
		path, err := SystemRootAlias("/tmp/workspace")
		require.NoError(t, err)
		require.Equal(t, "/tmp/workspace", path)
		return
	}
	for _, alias := range []string{"/var", "/tmp"} {
		path, err := SystemRootAlias(alias)
		require.NoError(t, err)
		require.Equal(t, "/private"+alias, path)
	}
}
