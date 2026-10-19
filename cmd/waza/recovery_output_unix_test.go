//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

func TestRecoveryCommandMockDelayCancellationWithFullStderr(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, signal := range []os.Signal{syscall.SIGTERM, os.Interrupt} {
		t.Run(signal.String(), func(t *testing.T) {
			workspace := t.TempDir()
			session, err := commandmock.NewSession(workspace, []models.CommandMockConfig{{
				Name: "az", ExpectCalls: new(2), Responses: []models.CommandMockResponse{
					{Args: []string{"unused"}, Stdout: "not selected"},
					{Args: []string{"account", "show"}, Sequence: []models.CommandMockStep{
						{DelayMS: 300000, ExitCode: 7, Stdout: "not delivered", Stderr: "not delivered"},
						{Stdout: "recovered"},
					}},
				},
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
			fd := int(emit.Fd())
			require.NoError(t, unix.SetNonblock(fd, true))
			filled := 0
			for _, chunk := range [][]byte{bytes.Repeat([]byte("x"), 4096), []byte("x")} {
				for {
					n, err := unix.Write(fd, chunk)
					if errors.Is(err, unix.EAGAIN) {
						break
					}
					require.NoError(t, err)
					require.Positive(t, n)
					filled += n
				}
			}
			require.Positive(t, filled)
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
			n, err := unix.Poll(fds, 0)
			require.NoError(t, err)
			require.Zero(t, n, "inherited stderr must be non-writable before starting the shim")
			require.NoError(t, unix.SetNonblock(fd, false))
			t.Logf("prefilled %d bytes; even a one-byte write returned EAGAIN; no reader drains stderr", filled)

			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			command := func() *exec.Cmd {
				child := exec.CommandContext(ctx, binary, "__command-mock", "--root", root, "--name", "az", "--", "account", "show")
				child.Dir = workspace
				child.Env = append(env, commandmock.SessionEnvironmentVariable+"="+session.ID(), "WAZA_NO_UPDATE_CHECK=1")
				return child
			}
			child := command()
			var stdout bytes.Buffer
			child.Stdout, child.Stderr = &stdout, emit
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
			marker := filepath.Join(root, "sessions", session.ID(), "sequences", "mock-0000-response-0001", "step-00000000")
			require.Eventually(t, func() bool {
				_, err := os.Stat(marker)
				records := session.Invocations()
				return err == nil && len(records) == 1 && records[0].ExitCode == nil
			}, 5*time.Second, 10*time.Millisecond, "child must reserve the selected delayed step before cancellation")
			require.NoError(t, child.Process.Signal(signal))
			select {
			case err = <-done:
				waited = true
			case <-time.After(3 * time.Second):
				t.Fatal("delay cancellation remained blocked on full stderr; no drainage or second signal is permitted")
			}
			require.NoError(t, ctx.Err(), "cancellation required the safety kill")
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			require.Equal(t, 130, exit.ExitCode(), "catchable delay cancellation must return its cancellation status")
			require.Empty(t, stdout.String(), "a canceled delay must not emit selected output")
			require.NoError(t, emit.Close())
			emitClosed = true
			data, err := io.ReadAll(output)
			require.NoError(t, err)
			require.Equal(t, strings.Repeat("x", filled), string(data), "neither runtime nor Cobra may append a cancellation diagnostic")

			recovered, err := command().CombinedOutput()
			require.NoError(t, err, "%s", recovered)
			require.Equal(t, "recovered", string(recovered), "retry must consume the next step")
			records, err := session.Close()
			closed = true
			require.NoError(t, err)
			require.Len(t, records, 2)
			for _, record := range records {
				require.Equal(t, "az", record.Command)
				require.Equal(t, []string{"account", "show"}, record.Args)
				require.Equal(t, 1, record.ResponseIndex)
			}
			require.Equal(t, new(130), records[0].ExitCode)
			require.Equal(t, new(0), records[1].ExitCode)
		})
	}
}

func TestRecoveryCommandMockPassThroughOutput(t *testing.T) {
	binary := buildRecoveryCLI(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, test := range []struct {
		name   string
		size   int
		code   int
		signal os.Signal
	}{
		{name: "small success", size: 32},
		{name: "small failure", size: 32, code: 7},
		{name: "ordinary exit 124", size: 32, code: 124},
		{name: "ordinary exit 130", size: 32, code: 130},
		{name: "large drained success", size: 8 << 20},
		{name: "drained SIGTERM", size: 32, signal: syscall.SIGTERM},
		{name: "drained SIGINT", size: 32, signal: os.Interrupt},
		{name: "blocked SIGTERM", size: 8 << 20, signal: syscall.SIGTERM},
		{name: "blocked SIGINT", size: 8 << 20, signal: os.Interrupt},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			session, err := commandmock.NewSession(workspace, []models.CommandMockConfig{{
				Name: "az", Responses: []models.CommandMockResponse{{Args: []string{}, Stdout: "unused"}},
			}}, workspace)
			require.NoError(t, err)
			t.Cleanup(func() {
				records, err := session.Close()
				require.NoError(t, err)
				require.Empty(t, records, "pass-through must not fabricate mock invocation evidence")
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
			ready := filepath.Join(workspace, "host-started")
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
			child := exec.CommandContext(ctx, binary, "__command-mock", "--root", root, "--name", filepath.Base(executable), "--",
				"-test.run=^TestRecoveryPassThroughOutputHostProcess$", "--", strconv.Itoa(test.size), strconv.Itoa(test.code))
			child.Dir = workspace
			child.Env = append(env, commandmock.SessionEnvironmentVariable+"="+session.ID(),
				"PATH="+filepath.Dir(executable), "WAZA_TEST_RECOVERY_OUTPUT_HOST="+ready,
				"WAZA_TEST_RECOVERY_OUTPUT_WAIT="+strconv.FormatBool(test.signal != nil), "WAZA_NO_UPDATE_CHECK=1")
			var stdout bytes.Buffer
			child.Stdout, child.Stderr = &stdout, emit
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
			var pid int
			require.Eventually(t, func() bool {
				data, err := os.ReadFile(ready)
				if err != nil {
					return false
				}
				pid, err = strconv.Atoi(string(data))
				return err == nil && pid > 0
			}, 5*time.Second, 10*time.Millisecond, "unconfigured pass-through host never started")
			host, err := os.FindProcess(pid)
			require.NoError(t, err)
			t.Cleanup(func() {
				if host.Signal(syscall.Signal(0)) == nil {
					require.NoError(t, host.Kill())
				}
				require.NoError(t, host.Release())
			})
			require.NoError(t, output.SetReadDeadline(time.Now().Add(10*time.Second)))
			payload := strings.Repeat("x", test.size)
			var waitErr error
			if test.signal == nil {
				require.NoError(t, emit.Close())
				emitClosed = true
				data, err := io.ReadAll(output)
				require.NoError(t, err)
				expected := payload
				if test.code != 0 {
					expected += fmt.Sprintf("Error: exit code %d\n", test.code)
				}
				require.Equal(t, expected, string(data))
				waitErr = <-done
				waited = true
				require.FileExists(t, ready+".finished", "ordinary host must complete its output")
			} else {
				prefix := make([]byte, min(test.size, 64<<10))
				_, err := io.ReadFull(output, prefix)
				require.NoError(t, err)
				require.Equal(t, payload[:len(prefix)], string(prefix))
				blocked := test.size > len(prefix)
				if blocked {
					require.EventuallyWithT(t, func(collect *assert.CollectT) {
						fds := []unix.PollFd{{Fd: int32(emit.Fd()), Events: unix.POLLOUT}}
						n, err := unix.Poll(fds, 0)
						assert.NoError(collect, err)
						assert.Zero(collect, n)
					}, 5*time.Second, 10*time.Millisecond, "inherited host stderr must be full before signaling")
					require.NoFileExists(t, ready+".finished", "host output must still be blocked")
					t.Logf("host PID %d emitted %d bytes; inherited stderr is undrained and not writable", pid, len(prefix))
				} else {
					require.Eventually(t, func() bool {
						_, err := os.Stat(ready + ".finished")
						return err == nil
					}, 5*time.Second, 10*time.Millisecond, "drained host never finished its output")
				}
				require.NoError(t, host.Signal(syscall.Signal(0)), "actual host must be alive before cancellation")
				require.NoError(t, child.Process.Signal(test.signal))
				select {
				case waitErr = <-done:
					waited = true
				case <-time.After(3 * time.Second):
					t.Error("shim remained blocked after host cancellation")
					assert.Error(t, host.Signal(syscall.Signal(0)), "cancellation must stop and reap the host before diagnostic output")
					require.NoError(t, emit.Close())
					emitClosed = true
					data, err := io.ReadAll(output)
					require.NoError(t, err)
					t.Logf("drained %d bytes to release shim; stderr tail: %q", len(data), string(data[max(0, len(data)-120):]))
					waitErr = <-done
					waited = true
				}
				require.Error(t, host.Signal(syscall.Signal(0)), "pass-through host survived cancellation")
				if blocked {
					require.NoFileExists(t, ready+".finished", "cancellation must interrupt the blocked host output")
				}
				if !emitClosed {
					require.NoError(t, emit.Close())
					emitClosed = true
					data, err := io.ReadAll(output)
					require.NoError(t, err)
					require.LessOrEqual(t, len(prefix)+len(data), len(payload))
					require.Equal(t, payload[len(prefix):len(prefix)+len(data)], string(data),
						"cancellation must not write additional diagnostics after host output")
				}
			}
			require.NoError(t, ctx.Err(), "shim required the safety timeout")
			require.Equal(t, "host stdout\n", stdout.String())
			if test.signal != nil || test.code != 0 {
				var exit *exec.ExitError
				require.ErrorAs(t, waitErr, &exit)
				expectedCode := test.code
				if test.signal != nil {
					expectedCode = 130
				}
				require.Equal(t, expectedCode, exit.ExitCode(), "cancellation must not require a second signal or drainage")
			} else {
				require.NoError(t, waitErr)
			}
		})
	}
}

func TestRecoveryPassThroughOutputHostProcess(t *testing.T) {
	ready := os.Getenv("WAZA_TEST_RECOVERY_OUTPUT_HOST")
	if ready == "" {
		return
	}
	size, err := strconv.Atoi(os.Args[len(os.Args)-2])
	require.NoError(t, err)
	code, err := strconv.Atoi(os.Args[len(os.Args)-1])
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0600))
	_, err = io.WriteString(os.Stdout, "host stdout\n")
	require.NoError(t, err)
	n, err := io.WriteString(os.Stderr, strings.Repeat("x", size))
	require.NoError(t, err)
	require.Equal(t, size, n)
	require.NoError(t, os.WriteFile(ready+".finished", []byte("complete"), 0600))
	if os.Getenv("WAZA_TEST_RECOVERY_OUTPUT_WAIT") == "true" {
		time.Sleep(time.Hour)
	}
	os.Exit(code)
}

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
			{name: "configured exit 124", size: 32, code: 124},
			{name: "configured exit 130", size: 32, code: 130},
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
