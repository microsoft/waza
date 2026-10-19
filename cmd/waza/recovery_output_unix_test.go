//go:build unix

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRecoveryCommandMockOutput(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, stream := range []string{"stdout", "stderr"} {
		for _, test := range []struct {
			name   string
			size   int
			code   int
			signal os.Signal
		}{
			{name: "small success", size: 32},
			{name: "small failure", size: 32, code: 7},
			{name: "large drained success", size: 8 << 20},
			{name: "blocked SIGTERM", size: 8 << 20, signal: syscall.SIGTERM},
			{name: "blocked SIGINT", size: 8 << 20, signal: os.Interrupt},
		} {
			t.Run(stream+"/"+test.name, func(t *testing.T) {
				workspace := t.TempDir()
				payload := strings.Repeat("x", test.size)
				step := models.CommandMockStep{ExitCode: test.code}
				if stream == "stdout" {
					require.NoError(t, os.WriteFile(filepath.Join(workspace, "output.txt"), []byte(payload), 0600))
					step.Fixture = "output.txt"
				} else {
					step.Stderr = payload
				}
				session, err := commandmock.NewSession(workspace, []models.CommandMockConfig{{
					Name: "az", ExpectCalls: new(1), Responses: []models.CommandMockResponse{{
						Args: []string{"account", "show"}, Sequence: []models.CommandMockStep{step},
					}},
				}}, workspace)
				require.NoError(t, err)
				t.Cleanup(func() {
					records, err := session.Close()
					require.NoError(t, err)
					require.Len(t, records, 1)
					require.Equal(t, []string{"account", "show"}, records[0].Args)
					require.Equal(t, 0, records[0].ResponseIndex)
					require.Equal(t, new(test.code), records[0].ExitCode,
						"invocation evidence records selection, not stream delivery")
				})
				env, err := commandmock.RuntimeEnvironment()
				require.NoError(t, err)
				var root string
				for _, item := range env {
					if value, ok := strings.CutPrefix(item, "WAZA_COMMAND_MOCK_ROOT="); ok {
						root = value
					}
				}
				require.NotEmpty(t, root)

				output, emit, err := os.Pipe()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, output.Close()) })
				emitClosed := false
				t.Cleanup(func() {
					if !emitClosed {
						require.NoError(t, emit.Close())
					}
				})
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, binary, "__command-mock", "--root", root, "--name", "az", "--", "account", "show")
				child.Dir = workspace
				child.Env = append(env, commandmock.SessionEnvironmentVariable+"="+session.ID(), "WAZA_NO_UPDATE_CHECK=1")
				var other bytes.Buffer
				child.Stdout, child.Stderr = emit, &other
				if stream == "stderr" {
					child.Stdout, child.Stderr = &other, emit
				}
				require.NoError(t, child.Start())
				done := make(chan error, 1)
				go func() { done <- child.Wait() }()
				waited := false
				t.Cleanup(func() {
					if !waited {
						cancel()
						<-done
					}
				})
				require.NoError(t, output.SetReadDeadline(time.Now().Add(10*time.Second)))

				var waitErr error
				diagnostic := ""
				if test.code != 0 {
					diagnostic = fmt.Sprintf("Error: exit code %d\n", test.code)
				}
				if test.signal == nil {
					require.NoError(t, emit.Close())
					emitClosed = true
					data, err := io.ReadAll(output)
					require.NoError(t, err)
					expected := payload
					if stream == "stderr" {
						expected += diagnostic
					}
					require.Equal(t, expected, string(data))
					waitErr = <-done
					waited = true
				} else {
					prefix := make([]byte, 64<<10)
					_, err := io.ReadFull(output, prefix)
					require.NoError(t, err)
					require.Equal(t, payload[:len(prefix)], string(prefix))
					require.EventuallyWithT(t, func(collect *assert.CollectT) {
						fds := []unix.PollFd{{Fd: int32(emit.Fd()), Events: unix.POLLOUT}}
						n, err := unix.Poll(fds, 0)
						assert.NoError(collect, err)
						assert.Zero(collect, n)
					}, 5*time.Second, 10*time.Millisecond,
						"the inherited output pipe must fill before signaling")
					select {
					case err := <-done:
						waited = true
						t.Fatalf("shim exited before interruption: %v", err)
					default:
					}
					t.Logf("read %d fixture bytes; inherited %s pipe is now undrained and not writable", len(prefix), stream)
					require.NoError(t, child.Process.Signal(test.signal))
					select {
					case waitErr = <-done:
						waited = true
					case <-time.After(3 * time.Second):
						t.Error("shim remained blocked on output after interruption")
						require.NoError(t, emit.Close())
						emitClosed = true
						n, err := io.Copy(io.Discard, output)
						require.NoError(t, err)
						require.Equal(t, int64(len(payload)-len(prefix)), n)
						waitErr = <-done
						waited = true
						t.Logf("after draining the pipe, process returned: %v", waitErr)
					}
				}
				require.NoError(t, ctx.Err(), "process required the safety timeout")
				if stream == "stdout" {
					require.Equal(t, diagnostic, other.String())
				} else {
					require.Empty(t, other.String())
				}
				if test.signal != nil {
					var exit *exec.ExitError
					require.ErrorAs(t, waitErr, &exit, "interruption must not become configured success after draining")
					status, ok := exit.Sys().(syscall.WaitStatus)
					require.True(t, ok)
					require.True(t, status.Signaled(), "process must terminate without draining or a safety kill")
					require.Equal(t, test.signal, status.Signal())
				} else if test.code == 0 {
					require.NoError(t, waitErr)
				} else {
					var exit *exec.ExitError
					require.ErrorAs(t, waitErr, &exit)
					require.Equal(t, test.code, exit.ExitCode())
				}
			})
		}
	}
}
