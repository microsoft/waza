package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func buildRecoveryCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "waza")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	log, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".").CombinedOutput()
	require.NoError(t, err, "%s", log)
	return binary
}

func TestRecoveryCommandMockProcessInterruption(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, abrupt := range []bool{false, true} {
		name := "SIGTERM"
		if abrupt {
			name = "abrupt kill"
		}
		t.Run(name, func(t *testing.T) {
			if !abrupt && runtime.GOOS == "windows" {
				t.Skip("Windows does not support sending SIGTERM to an independent process")
			}
			workspace := t.TempDir()
			session, err := commandmock.NewSession(workspace, []models.CommandMockConfig{{
				Name: "az", ExpectCalls: new(2), Responses: []models.CommandMockResponse{{
					Args: []string{"account", "show"},
					Sequence: []models.CommandMockStep{
						{DelayMS: 300000, ExitCode: 1, Stderr: "temporary failure"},
						{Stdout: "recovered", ExitCode: 0},
					},
				}},
			}}, workspace)
			require.NoError(t, err)
			closed := false
			t.Cleanup(func() {
				if !closed {
					_, err := session.Close()
					require.NoError(t, err)
				}
			})
			env, err := commandmock.RuntimeEnvironment()
			require.NoError(t, err)
			root := ""
			for _, item := range env {
				if value, ok := strings.CutPrefix(item, "WAZA_COMMAND_MOCK_ROOT="); ok {
					root = value
				}
			}

			require.NotEmpty(t, root)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			command := func() *exec.Cmd {
				child := exec.CommandContext(ctx, binary, "__command-mock", "--root", root, "--name", "az", "--", "account", "show")
				child.Dir = workspace
				child.Env = append(env, commandmock.SessionEnvironmentVariable+"="+session.ID(), "WAZA_NO_UPDATE_CHECK=1")
				return child
			}
			child := command()
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			require.NoError(t, child.Start())
			waited := false
			t.Cleanup(func() {
				if !waited {
					cancel()
					_ = child.Wait()
				}
			})
			marker := filepath.Join(root, "sessions", session.ID(), "sequences", "mock-0000-response-0000", "step-00000000")
			require.Eventually(t, func() bool {
				_, err := os.Stat(marker)
				return err == nil && len(session.Invocations()) == 1
			}, 5*time.Second, 10*time.Millisecond, "child never started and reserved the delayed step")
			require.Nil(t, session.Invocations()[0].ExitCode)
			if abrupt {
				require.NoError(t, child.Process.Kill())
			} else {
				require.NoError(t, child.Process.Signal(syscall.SIGTERM))
			}
			err = child.Wait()
			waited = true
			var exit *exec.ExitError
			require.True(t, errors.As(err, &exit), "%v; stderr: %s", err, stderr.String())
			if !abrupt {
				require.Equal(t, 130, exit.ExitCode(), "%s", stderr.String())
			}
			recovered, err := command().CombinedOutput()
			require.NoError(t, err, "%s", recovered)
			require.Equal(t, "recovered", string(recovered))
			records, err := session.Close()
			closed = true
			require.NoError(t, err)
			require.Len(t, records, 2)
			require.Equal(t, []string{"account", "show"}, records[0].Args)
			require.Equal(t, 0, records[0].ResponseIndex)
			if abrupt {
				require.Nil(t, records[0].ExitCode, "an uncatchable kill must not invent an exit result")
			} else {
				require.Equal(t, new(130), records[0].ExitCode)
			}
			require.Equal(t, new(0), records[1].ExitCode)
		})
	}
}

func TestRecoveryCommandMockPassThroughInterruption(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support sending SIGTERM to an independent process")
	}
	binary := buildRecoveryCLI(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	workspace := t.TempDir()
	session, err := commandmock.NewSession(workspace, []models.CommandMockConfig{{
		Name: "az", Responses: []models.CommandMockResponse{{Args: []string{}, Stdout: "unused"}},
	}}, workspace)
	require.NoError(t, err)
	defer func() {
		_, err := session.Close()
		require.NoError(t, err)
	}()
	env, err := commandmock.RuntimeEnvironment()
	require.NoError(t, err)
	root := ""
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, "WAZA_COMMAND_MOCK_ROOT="); ok {
			root = value
		}
	}
	require.NotEmpty(t, root)
	ready := filepath.Join(workspace, "host-started")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "__command-mock", "--root", root, "--name", filepath.Base(executable), "--", "-test.run=^TestRecoveryPassThroughHostProcess$")
	child.Dir = workspace
	child.Env = append(env, commandmock.SessionEnvironmentVariable+"="+session.ID(),
		"PATH="+filepath.Dir(executable), "WAZA_TEST_RECOVERY_HOST="+ready, "WAZA_NO_UPDATE_CHECK=1")
	child.WaitDelay = time.Second
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	require.NoError(t, child.Start())
	waited := false
	t.Cleanup(func() {
		if !waited {
			cancel()
			require.Error(t, child.Wait())
		}
	})
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(ready)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(string(data))
		return err == nil && pid > 0
	}, 5*time.Second, 10*time.Millisecond, "pass-through host command never started")
	host, err := os.FindProcess(pid)
	require.NoError(t, err)
	t.Cleanup(func() {
		if host.Signal(syscall.Signal(0)) == nil {
			require.NoError(t, host.Kill())
		}
		require.NoError(t, host.Release())
	})
	require.NoError(t, host.Signal(syscall.Signal(0)), "the real host command must be running before termination")
	require.NoError(t, child.Process.Signal(syscall.SIGTERM))
	err = child.Wait()
	waited = true
	require.NoError(t, ctx.Err(), "shim did not finish before the test safety deadline")
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "%s", output.String())
	require.Equal(t, 130, exit.ExitCode(), "%s", output.String())
	require.Error(t, host.Signal(syscall.Signal(0)), "pass-through host process survived cancellation")
	require.Empty(t, session.Invocations(), "a host command must not fabricate a mock invocation")
}

func TestRecoveryPassThroughHostProcess(t *testing.T) {
	ready := os.Getenv("WAZA_TEST_RECOVERY_HOST")
	if ready == "" {
		return
	}
	require.NoError(t, os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0600))
	time.Sleep(time.Hour)
	os.Exit(0)
}
