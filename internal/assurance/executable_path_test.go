package assurance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCurrentExecutablePathDigestMatchesObservedFile(t *testing.T) {
	path, err := os.Executable()
	require.NoError(t, err)
	bytes, err := os.ReadFile(path)
	require.NoError(t, err)
	observed, err := CurrentExecutableSHA256()
	require.NoError(t, err)
	require.Equal(t, byteSHA256(bytes), observed)
}

func TestCurrentExecutablePathDigestObservesReplacement(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the executing-copy replacement experiment is limited to Linux and macOS; ordinary path-digest observation remains portable")
	}
	if os.Getenv("WAZA_EXECUTABLE_PATH_OBSERVER_CHILD") == "1" {
		path, err := os.Executable()
		require.NoError(t, err)
		before, err := CurrentExecutableSHA256()
		require.NoError(t, err)
		replacement := []byte("replacement file at the executable path, not the executing image")
		require.NoError(t, os.WriteFile(path+".replacement", replacement, 0700))
		require.NoError(t, os.Rename(path+".replacement", path))
		after, err := CurrentExecutableSHA256()
		require.NoError(t, err)
		require.Equal(t, byteSHA256(replacement), after)
		require.NotEqual(t, before, after)
		require.NoError(t, os.Remove(path))
		missing, err := CurrentExecutableSHA256()
		require.EqualError(t, err, "assurance: executable identity unavailable")
		require.Empty(t, missing, "read failure must not yield a zero or cached/startup digest")
		fmt.Printf("PATH_REPLACEMENT_OBSERVED before=%s after=%s missing=explicit_error\n", before, after)
		return
	}
	path, err := os.Executable()
	require.NoError(t, err)
	bytes, err := os.ReadFile(path)
	require.NoError(t, err)
	copyPath := filepath.Join(t.TempDir(), "executable-path-observer")
	require.NoError(t, os.WriteFile(copyPath, bytes, 0700))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, copyPath, "-test.run=^TestCurrentExecutablePathDigestObservesReplacement$", "-test.timeout=20s")
	command.Env = append(os.Environ(), "WAZA_EXECUTABLE_PATH_OBSERVER_CHILD=1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), "PATH_REPLACEMENT_OBSERVED")
	t.Log(string(output))
}
