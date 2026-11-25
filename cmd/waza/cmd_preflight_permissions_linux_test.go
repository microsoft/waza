package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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

func TestPreflightWorkspaceRejectsNonregularSkillDefinitions(t *testing.T) {
	for _, tc := range []struct {
		name              string
		layout            string
		definition        string
		fifo              bool
		linked            bool
		definitionSymlink bool
		unreadable        bool
		wantError         string
	}{
		{name: "recursive linked FIFO skill", layout: "recursive", definition: "SKILL.md", fifo: true, linked: true, wantError: "not a regular file"},
		{name: "recursive linked FIFO agent", layout: "recursive", definition: "helper.agent.md", fifo: true, linked: true, wantError: "not a regular file"},
		{name: "immediate linked FIFO skill", layout: "immediate", definition: "SKILL.md", fifo: true, linked: true, wantError: "not a regular file"},
		{name: "immediate linked FIFO agent", layout: "immediate", definition: "helper.agent.md", fifo: true, linked: true, wantError: "not a regular file"},
		{name: "admitted FIFO skill", layout: "recursive", definition: "SKILL.md", fifo: true, wantError: "not a regular file"},
		{name: "admitted FIFO agent", layout: "recursive", definition: "helper.agent.md", fifo: true, wantError: "not a regular file"},
		{name: "linked FIFO skill via file symlink", layout: "recursive", definition: "SKILL.md", fifo: true, linked: true, definitionSymlink: true, wantError: "not a regular file"},
		{name: "admitted FIFO skill via file symlink", layout: "recursive", definition: "SKILL.md", fifo: true, definitionSymlink: true, wantError: "not a regular file"},
		{name: "admitted FIFO agent via file symlink", layout: "recursive", definition: "helper.agent.md", fifo: true, definitionSymlink: true, wantError: "not a regular file"},
		{name: "unreadable skill", layout: "recursive", definition: "SKILL.md", unreadable: true, wantError: "permission denied"},
		{name: "unreadable agent", layout: "recursive", definition: "helper.agent.md", unreadable: true, wantError: "permission denied"},
		{name: "unrelated resource link", layout: "recursive", linked: true},
		{name: "linked regular skill", layout: "recursive", definition: "SKILL.md", linked: true, wantError: "symlinked directory"},
		{name: "linked regular agent", layout: "immediate", definition: "helper.agent.md", linked: true, wantError: "symlinked directory"},
		{name: "admitted regular skill", layout: "recursive", definition: "SKILL.md"},
		{name: "admitted regular agent", layout: "recursive", definition: "helper.agent.md"},
		{name: "admitted regular skill via file symlink", layout: "recursive", definition: "SKILL.md", definitionSymlink: true},
		{name: "admitted regular agent via file symlink", layout: "recursive", definition: "helper.agent.md", definitionSymlink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.unreadable && os.Geteuid() == 0 {
				t.Skip("permission control requires an unprivileged user")
			}
			parent := t.TempDir()
			dir := filepath.Join(parent, "project")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, validateCLIIsolation(dir))
			reviewed := filepath.Join(dir, "skills", "reviewed")
			if tc.layout == "immediate" {
				reviewed = filepath.Join(dir, "reviewed")
			}
			require.NoError(t, os.MkdirAll(reviewed, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(reviewed, "SKILL.md"), []byte("# reviewed\n"), 0o600))
			eval := filepath.Join(dir, "evals", "reviewed", "eval.yaml")
			initCIWriteEvalAt(t, filepath.Dir(eval), "eval.yaml", "")

			definitionDir := filepath.Join(dir, "skills", "bad")
			if tc.linked {
				definitionDir = filepath.Join(parent, "resources")
				require.NoError(t, os.MkdirAll(definitionDir, 0o700))
				link := filepath.Join(dir, "skills", "docs")
				if tc.layout == "immediate" {
					link = filepath.Join(dir, "docs")
				}
				target, err := filepath.Rel(filepath.Dir(link), definitionDir)
				require.NoError(t, err)
				require.NoError(t, os.Symlink(target, link))
			} else {
				require.NoError(t, os.MkdirAll(definitionDir, 0o700))
			}
			if tc.definition != "" {
				path := filepath.Join(definitionDir, tc.definition)
				target := path
				if tc.definitionSymlink {
					target = filepath.Join(parent, "definition-target")
				}
				if tc.fifo {
					require.NoError(t, syscall.Mkfifo(target, 0o600))
				} else {
					require.NoError(t, os.WriteFile(target, []byte("# definition\n"), 0o600))
				}
				if tc.unreadable {
					require.NoError(t, os.Chmod(target, 0o000))
					file, err := os.Open(target)
					if err == nil {
						require.NoError(t, file.Close())
					}
					require.ErrorIs(t, err, os.ErrPermission, "fixture must deny definition reads")
				}
				if tc.definitionSymlink {
					relative, err := filepath.Rel(definitionDir, target)
					require.NoError(t, err)
					require.NoError(t, os.Symlink(relative, path))
				}
				if tc.fifo {
					info, err := os.Stat(path)
					require.NoError(t, err)
					require.NotZero(t, info.Mode()&os.ModeNamedPipe, "fixture must resolve to a FIFO")
				}
			}

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			out, err := runWorkspacePreflightPermissionControlContext(t, ctx, dir)
			require.NoError(t, ctx.Err(), "preflight must not block reading a definition: %s", out)
			if tc.wantError == "" {
				require.NoError(t, err, string(out))
			} else {
				require.Error(t, err, string(out))
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, ExitError, exitErr.ExitCode(), string(out))
				require.Contains(t, string(out), tc.wantError)
				if tc.fifo || tc.unreadable {
					require.Contains(t, string(out), filepath.Join(definitionDir, tc.definition))
				}
			}
			require.Contains(t, string(out), "Offline preflight: "+eval)
			require.NoFileExists(t, filepath.Join(dir, "runtime-invoked"))
		})
	}
}

func runWorkspacePreflightPermissionControl(t *testing.T, dir string, arguments ...string) ([]byte, error) {
	return runWorkspacePreflightPermissionControlContext(t, t.Context(), dir, arguments...)
}

func runWorkspacePreflightPermissionControlContext(t *testing.T, ctx context.Context, dir string, arguments ...string) ([]byte, error) {
	t.Helper()
	setup := time.Now()
	binary, err := os.Executable()
	require.NoError(t, err)
	args := []string{"-test.run=^TestInitCIHelperProcess$", "-test.count=1"}
	if cover := flag.Lookup("test.gocoverdir"); cover != nil && cover.Value.String() != "" {
		args = append(args, "-test.gocoverdir="+cover.Value.String())
	}
	args = append(args, "--", "--no-telemetry", "--no-update-check", "preflight", "--workspace")
	args = append(args, arguments...)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "WAZA_INIT_CI_HELPER=1", "WAZA_INIT_CI_MODE=reject", "NO_COLOR=1")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	setupElapsed := time.Since(setup)
	start := time.Now()
	err = cmd.Start()
	startElapsed := time.Since(start)
	postStartCtxErr := ctx.Err()
	if err != nil {
		t.Logf("preflight setup_elapsed=%s start_elapsed=%s post_start_ctx_err=%v", setupElapsed, startElapsed, postStartCtxErr)
		return nil, err
	}

	done := make(chan struct{})
	snapshot := make(chan string, 1)
	if deadline, ok := ctx.Deadline(); ok {
		go func() {
			timer := time.NewTimer(max(time.Until(deadline)-time.Second, 0))
			defer timer.Stop()
			select {
			case <-timer.C:
				path := filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "wchan")
				data, err := os.ReadFile(path)
				snapshot <- fmt.Sprintf("wchan=%q error=%v", strings.TrimSpace(string(data)), err)
			case <-done:
				snapshot <- "process exited before snapshot"
			}
		}()
	} else {
		snapshot <- "no deadline"
	}
	wait := time.Now()
	err = cmd.Wait()
	waitElapsed := time.Since(wait)
	close(done)
	t.Logf("preflight setup_elapsed=%s start_elapsed=%s post_start_ctx_err=%v wait_elapsed=%s post_wait_ctx_err=%v %s",
		setupElapsed, startElapsed, postStartCtxErr, waitElapsed, ctx.Err(), <-snapshot)
	return output.Bytes(), err
}
