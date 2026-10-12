package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCompatibilityOfflineMakeTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Make recipe isolation requires /bin/sh; Windows corpus is covered by CI")
	}
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skip("Make is required to exercise the contributor recipe")
	}
	makePath, err = filepath.Abs(makePath)
	require.NoError(t, err)
	for _, c := range []struct {
		name, inherited, missing string
	}{
		{name: "live enabled by caller", inherited: "true"},
		{name: "live disabled by caller", inherited: "false"},
		{name: "live unset by caller"},
		{name: "missing git", inherited: "true", missing: "git"},
		{name: "missing node", inherited: "true", missing: "node"},
		{name: "missing python", inherited: "true", missing: "python"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, tool := range []string{"git", "node", "python"} {
				if tool != c.missing {
					require.NoError(t, os.WriteFile(filepath.Join(dir, tool), []byte("#!/bin/sh\nexit 0\n"), 0o700))
				}
			}
			marker := filepath.Join(dir, "go-called")
			fakeGo := "#!/bin/sh\nprintf '%s' \"$ENABLE_COPILOT_TESTS\" > \"$COMPAT_GO_MARKER\"\n" +
				"[ \"$ENABLE_COPILOT_TESTS\" = false ] && [ \"$1\" = test ] && [ \"$2\" = -count=1 ]\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "go"), []byte(fakeGo), 0o700))
			cmd := exec.CommandContext(t.Context(), makePath, "-f", testutil.RepoFile(t, "Makefile"), "test-compat")
			cmd.Dir = dir
			cmd.Env = []string{"PATH=" + dir, "COMPAT_GO_MARKER=" + marker}
			if c.inherited != "" {
				cmd.Env = append(cmd.Env, "ENABLE_COPILOT_TESTS="+c.inherited)
			}
			output, err := cmd.CombinedOutput()
			if c.missing != "" {
				require.Error(t, err)
				require.Contains(t, string(output), "test-compat requires "+c.missing+" on PATH")
				_, err := os.Stat(marker)
				require.ErrorIs(t, err, os.ErrNotExist, "prerequisite failure must prevent Go execution")
				return
			}
			require.NoError(t, err, "%s", output)
			received, err := os.ReadFile(marker)
			require.NoError(t, err, "actual Make recipe must invoke the fake Go subprocess")
			require.Equal(t, "false", string(received))
		})
	}
}
