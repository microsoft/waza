package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreflightWorkspaceSearchOnlyAncestor(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission control requires an unprivileged user")
	}
	for _, tc := range []struct {
		name                 string
		blockSkills          bool
		blockAPM             bool
		blockAncestorAPMRoot bool
		blockProjectAPM      bool
	}{
		{name: "optional ancestor"},
		{name: "optional ancestor APM probe", blockAPM: true},
		{name: "discovered ancestor APM root", blockAncestorAPMRoot: true},
		{name: "selected skill root", blockSkills: true},
		{name: "selected APM root", blockProjectAPM: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "ancestor")
			project := filepath.Join(parent, "project")
			require.NoError(t, os.MkdirAll(project, 0o700))
			require.NoError(t, validateCLIIsolation(project))
			initCIWriteEval(t, project, "")
			require.NoError(t, os.WriteFile(filepath.Join(project, ".waza.yaml"), []byte("paths:\n  skills: skills\n  evals: evals\n"), 0o600))

			locked := parent
			if tc.blockSkills {
				locked = filepath.Join(project, "skills")
			}
			if tc.blockAPM {
				apm := filepath.Join(parent, ".apm")
				require.NoError(t, os.Mkdir(apm, 0o000))
				t.Cleanup(func() { require.NoError(t, os.Chmod(apm, 0o700)) })
			}
			if tc.blockAncestorAPMRoot {
				apmSkills := filepath.Join(parent, ".apm", "skills")
				require.NoError(t, os.MkdirAll(apmSkills, 0o700))
				require.NoError(t, os.Chmod(apmSkills, 0o000))
				t.Cleanup(func() { require.NoError(t, os.Chmod(apmSkills, 0o700)) })
			}
			if tc.blockProjectAPM {
				apmSkills := filepath.Join(project, ".apm", "skills")
				require.NoError(t, os.MkdirAll(apmSkills, 0o700))
				require.NoError(t, os.Chmod(apmSkills, 0o000))
				t.Cleanup(func() { require.NoError(t, os.Chmod(apmSkills, 0o700)) })
			}
			require.NoError(t, os.Chmod(locked, 0o100))
			t.Cleanup(func() { require.NoError(t, os.Chmod(locked, 0o700)) })
			_, err := os.ReadDir(locked)
			require.ErrorIs(t, err, os.ErrPermission, "setup must deny directory listing")
			require.FileExists(t, filepath.Join(project, "evals", "reviewed", "eval.yaml"))

			binary, err := os.Executable()
			require.NoError(t, err)
			args := []string{"-test.run=^TestInitCIHelperProcess$", "-test.count=1"}
			if cover := flag.Lookup("test.gocoverdir"); cover != nil && cover.Value.String() != "" {
				args = append(args, "-test.gocoverdir="+cover.Value.String())
			}
			args = append(args, "--", "--no-telemetry", "--no-update-check", "preflight", "--workspace")
			cmd := exec.CommandContext(t.Context(), binary, args...)
			cmd.Dir = project
			cmd.Env = append(os.Environ(), "WAZA_INIT_CI_HELPER=1", "WAZA_INIT_CI_MODE=reject", "NO_COLOR=1")
			out, err := cmd.CombinedOutput()
			if tc.blockSkills || tc.blockAncestorAPMRoot || tc.blockProjectAPM {
				require.Error(t, err, string(out))
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, ExitError, exitErr.ExitCode(), string(out))
				require.Contains(t, string(out), "skills")
				require.Contains(t, strings.ToLower(string(out)), "permission denied")
			} else {
				require.NoError(t, err, string(out))
				require.Contains(t, string(out), "Offline preflight: "+filepath.Join(project, "evals", "reviewed", "eval.yaml"))
			}
			require.NoFileExists(t, filepath.Join(project, "runtime-invoked"))
		})
	}
}
