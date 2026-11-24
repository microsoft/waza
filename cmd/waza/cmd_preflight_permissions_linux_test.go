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

			out, err := runWorkspacePreflightPermissionControl(t, project)
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

func TestPreflightWorkspaceEvalFilenamePermissions(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission control requires an unprivileged user")
	}
	for _, tc := range []struct {
		name        string
		target      string
		permissions bool
	}{
		{name: "missing fallback"},
		{name: "dangling fallback", target: "missing"},
		{name: "readable fallback directory", target: "readable"},
		{name: "inaccessible fallback directory", target: "private", permissions: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := initCIFixture(t)
			initCIAddSkill(t, dir, "reviewed")
			evals := filepath.Join(dir, "evals")
			rootEval := filepath.Join(evals, "suite.yml")
			initCIWriteEvalAt(t, evals, "suite.yml", "")
			fallback := filepath.Join(evals, "eval.yaml")
			var otherEval string
			switch tc.target {
			case "missing":
				require.NoError(t, os.Symlink(filepath.Join("..", "missing"), fallback))
			case "readable", "private":
				parent := filepath.Join(dir, tc.target)
				target := filepath.Join(parent, "nested")
				otherEval = filepath.Join(fallback, "suite.yml")
				initCIWriteEvalAt(t, target, "suite.yml", "")
				require.NoError(t, os.Symlink(filepath.Join("..", tc.target, "nested"), fallback))
				if tc.permissions {
					require.NoError(t, os.Chmod(parent, 0o000))
					t.Cleanup(func() { require.NoError(t, os.Chmod(parent, 0o700)) })
					_, err := os.Stat(fallback)
					require.ErrorIs(t, err, os.ErrPermission, "setup must deny fallback target traversal")
				}
			}

			out, err := runWorkspacePreflightPermissionControl(t, dir, "--eval-file", "suite.yml")
			if tc.permissions {
				require.Error(t, err, string(out))
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, ExitError, exitErr.ExitCode(), string(out))
				require.Contains(t, strings.ToLower(string(out)), "permission denied")
				require.Contains(t, string(out), fallback)
			} else {
				require.NoError(t, err, string(out))
				if tc.target == "readable" {
					require.Contains(t, string(out), "Offline preflight: "+otherEval)
				}
			}
			require.Contains(t, string(out), "Offline preflight: "+rootEval)
			require.NotContains(t, string(out), "No evaluation files found")
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func runWorkspacePreflightPermissionControl(t *testing.T, dir string, arguments ...string) ([]byte, error) {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	args := []string{"-test.run=^TestInitCIHelperProcess$", "-test.count=1"}
	if cover := flag.Lookup("test.gocoverdir"); cover != nil && cover.Value.String() != "" {
		args = append(args, "-test.gocoverdir="+cover.Value.String())
	}
	args = append(args, "--", "--no-telemetry", "--no-update-check", "preflight", "--workspace")
	args = append(args, arguments...)
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "WAZA_INIT_CI_HELPER=1", "WAZA_INIT_CI_MODE=reject", "NO_COLOR=1")
	return cmd.CombinedOutput()
}
