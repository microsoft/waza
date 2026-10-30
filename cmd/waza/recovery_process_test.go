package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/jsonrpc"
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

// Keep observation on the caller goroutine: an asynchronous predicate may
// outlive its timeout and race with session/child cleanup.
func waitRecoveryCondition(ctx context.Context, condition func() bool) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if condition() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func startRecoveryMCPProcess(parent context.Context, start func() error, cancelProcess context.CancelFunc) (context.Context, func(), error) {
	watch := func(ctx context.Context) func() {
		done := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(done)
			cancelProcess()
		})
		return sync.OnceFunc(func() {
			if !stop() {
				<-done
			}
		})
	}
	startup, cancelStartup := context.WithTimeout(parent, 30*time.Second)
	stopStartup := watch(startup)
	startErr := start()
	stopStartup()
	startupErr := startup.Err()
	cancelStartup()
	if err := errors.Join(startErr, startupErr); err != nil {
		cancelProcess()
		return nil, nil, fmt.Errorf("starting MCP subprocess: %w", err)
	}
	// OS process creation is setup, not part of the MCP exchange/EOF budget.
	exchange, cancelExchange := context.WithTimeout(parent, 10*time.Second)
	stopExchange := watch(exchange)
	return exchange, sync.OnceFunc(func() {
		stopExchange()
		cancelExchange()
		cancelProcess()
	}), nil
}

func TestRecoveryCommandMockContextCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	for _, test := range []struct {
		name string
		ctx  context.Context
		code int
	}{
		{"canceled", canceled, 130},
		{"deadline", expired, 124},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := newRootCommand()
			cmd.SetContext(test.ctx)
			cmd.SetArgs([]string{"__command-mock", "--root", t.TempDir(), "--name", "unused"})
			var diagnostic bytes.Buffer
			cmd.SetErr(&diagnostic)
			err := cmd.Execute()
			var exit *ExitCodeError
			require.ErrorAs(t, err, &exit)
			require.Equal(t, test.code, exit.Code)
			require.Nil(t, exit.Err, "main must not emit a second diagnostic")
			require.Empty(t, diagnostic.String(), "Cobra must not write after cancellation")
		})
	}
}

func TestRecoveryCommandMockDelayedContextCancellation(t *testing.T) {
	workspace := t.TempDir()
	session, err := commandmock.NewSession(workspace, []models.CommandMockConfig{{
		Name: "az", ExpectCalls: new(1), Responses: []models.CommandMockResponse{{
			Args: []string{"account"}, Sequence: []models.CommandMockStep{{DelayMS: 300000, Stderr: "not delivered", ExitCode: 7}},
		}},
	}}, workspace)
	require.NoError(t, err)
	env, err := commandmock.RuntimeEnvironment()
	require.NoError(t, err)
	var root string
	for _, item := range env {
		if value, ok := strings.CutPrefix(item, "WAZA_COMMAND_MOCK_ROOT="); ok {
			root = value
		}
	}
	require.NotEmpty(t, root)
	t.Chdir(workspace)
	t.Setenv(commandmock.SessionEnvironmentVariable, session.ID())
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	cmd := newRootCommand()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"__command-mock", "--root", root, "--name", "az", "--", "account"})
	var diagnostic bytes.Buffer
	cmd.SetErr(&diagnostic)
	executed, err := cmd.ExecuteC()
	var exit *ExitCodeError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 124, exit.Code)
	require.Nil(t, exit.Err, "main must not emit a second diagnostic")
	require.True(t, executed.SilenceErrors, "the delay cancellation cause must reach Cobra")
	require.Empty(t, diagnostic.String(), "Cobra must not write after a delayed cancellation")
	records, err := session.Close()
	require.NoError(t, err)
	require.Len(t, records, 1, "the deadline must occur after selecting the delayed step")
	require.Equal(t, new(124), records[0].ExitCode)
}

func TestRecoveryMCPMockProcessStdinLifecycle(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, test := range []struct {
		name     string
		signal   os.Signal
		deadline bool
	}{
		{"EOF", nil, false},
		{"SIGTERM with open stdin", syscall.SIGTERM, false},
		{"SIGINT with open stdin", os.Interrupt, false},
		{"I/O deadline with open stdin", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.signal != nil && runtime.GOOS == "windows" {
				t.Skip("Windows cannot send these signals to an independent process")
			}
			input, send, err := os.Pipe()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, input.Close()) })
			sendClosed := false
			t.Cleanup(func() {
				if !sendClosed {
					require.NoError(t, send.Close())
				}
			})
			// A real inherited file descriptor exercises blocking os.Stdin,
			// unlike the interruptible io.Pipe endpoints in serving tests.
			config := base64.StdEncoding.EncodeToString([]byte(`{"name":"dependency"}`))
			childCtx, cancelChild := context.WithCancel(t.Context())
			t.Cleanup(cancelChild)
			child := exec.CommandContext(childCtx, binary, "__mcp-mock", "--config-base64", config)
			child.Stdin = input
			child.Env = append(os.Environ(), "WAZA_NO_UPDATE_CHECK=1")
			child.WaitDelay = time.Second
			var stderr bytes.Buffer
			child.Stderr = &stderr
			output, err := child.StdoutPipe()
			require.NoError(t, err)
			waited := false
			waitStarted := false
			done := make(chan error, 1)
			waitChild := func() (error, bool) {
				if !waitStarted {
					waitStarted = true
					go func() { done <- child.Wait() }()
				}
				select {
				case err := <-done:
					return err, true
				case <-time.After(3 * time.Second):
					return errors.New("MCP child Wait did not join within its bound"), false
				}
			}
			var startElapsed, writeElapsed, decodeElapsed time.Duration
			// t.Context is canceled before cleanup, so snapshot before assertions.
			var initializeContextErr error
			t.Cleanup(func() {
				if child.Process != nil && !waited {
					cancelChild()
					waitErr, joined := waitChild()
					require.True(t, joined, "%v; child context: %v", waitErr, childCtx.Err())
					t.Logf("MCP subprocess failure: start elapsed: %s; write elapsed: %s; decode elapsed: %s; context after initialize I/O before cleanup cancel: %v; joined child Wait: %v; process state: %v; stderr: %s",
						startElapsed, writeElapsed, decodeElapsed, initializeContextErr, waitErr, child.ProcessState, stderr.String())
					require.Error(t, waitErr)
				}
			})
			ctx, stopExchange, err := startRecoveryMCPProcess(t.Context(), func() error {
				started := time.Now()
				err := child.Start()
				startElapsed = time.Since(started)
				return err
			}, cancelChild)
			require.NoError(t, err, "MCP start elapsed: %s; child context: %v", startElapsed, childCtx.Err())
			t.Cleanup(stopExchange)
			writeStarted := time.Now()
			_, err = io.WriteString(send, "{\"jsonrpc\":\"2.0\",\"method\":\"initialize\",\"id\":1}\n")
			writeElapsed = time.Since(writeStarted)
			initializeContextErr = ctx.Err()
			require.NoError(t, err)
			var response jsonrpc.Response
			decodeStarted := time.Now()
			err = json.NewDecoder(output).Decode(&response)
			decodeElapsed = time.Since(decodeStarted)
			initializeContextErr = ctx.Err()
			require.NoError(t, err)
			require.JSONEq(t, "1", string(response.ID))
			require.Nil(t, response.Error)
			require.NotNil(t, response.Result, "the actual subprocess must be serving before shutdown")
			if test.deadline {
				_, err = io.WriteString(send, `{"jsonrpc":"2.0",`)
				require.NoError(t, err)
				err = json.NewDecoder(output).Decode(&response)
				require.ErrorIs(t, err, io.EOF, "an incomplete request must not receive a fabricated response")
				require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
				err, waited = waitChild()
				require.True(t, waited, "%v", err)
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				require.Error(t, childCtx.Err(), "the exchange deadline must cancel the owned child")
				_, statErr := send.Stat()
				require.NoError(t, statErr, "stdin must remain open through deadline cleanup")
				return
			}
			if test.signal == nil {
				require.NoError(t, send.Close())
				sendClosed = true
			} else {
				_, err = io.WriteString(send, `{"jsonrpc":"2.0",`)
				require.NoError(t, err)
				require.NoError(t, child.Process.Signal(test.signal))
			}
			err, waited = waitChild()
			require.True(t, waited, "%v", err)
			require.NoError(t, ctx.Err(), "MCP shutdown required the safety timeout; stderr: %s", stderr.String())
			if test.signal == nil {
				require.NoError(t, err, "%s", stderr.String())
				return
			}
			_, statErr := send.Stat()
			require.NoError(t, statErr, "stdin must remain open until after the child exits")
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "%s", stderr.String())
			status, ok := exit.Sys().(syscall.WaitStatus)
			require.True(t, ok)
			require.True(t, status.Signaled(), "termination must not fabricate a successful response")
			require.Equal(t, test.signal, status.Signal())
		})
	}
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
	started := time.Now()
	require.NoError(t, child.Start())
	startElapsed := time.Since(started)
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	waited := false
	t.Cleanup(func() {
		if !waited {
			cancel()
			select {
			case err := <-done:
				require.Error(t, err)
			case <-time.After(3 * time.Second):
				t.Error("pass-through child cleanup did not join within its bound")
			}
		}
	})
	var pid int
	var readErr, parseErr error
	inspections := 0
	readyCtx, stopReady := context.WithTimeout(ctx, 5*time.Second)
	defer stopReady()
	readyErr := waitRecoveryCondition(readyCtx, func() bool {
		inspections++
		var data []byte
		data, readErr = os.ReadFile(ready)
		if readErr != nil {
			return false
		}
		pid, parseErr = strconv.Atoi(string(data))
		return parseErr == nil && pid > 0
	})
	t.Logf("pass-through readiness: start=%s; elapsed=%s; inspections=%d; pid=%d; read=%v; parse=%v; readiness=%v; child context=%v",
		startElapsed, time.Since(started), inspections, pid, readErr, parseErr, readyErr, ctx.Err())
	if readyErr != nil {
		contextErr := ctx.Err()
		aliveErr := child.Process.Signal(syscall.Signal(0))
		earlyExit := false
		var waitErr, signalErr error
		select {
		case waitErr = <-done:
			earlyExit, waited = true, true
		default:
			signalErr = child.Process.Signal(syscall.SIGTERM)
			select {
			case waitErr = <-done:
				waited = true
			case <-time.After(2 * time.Second):
				cancel()
				select {
				case waitErr = <-done:
					waited = true
				case <-time.After(time.Second):
					t.Fatal("pass-through readiness failure cleanup did not join within its bound")
				}
			}
		}
		t.Fatalf("pass-through host command never started: readiness=%v; context before cleanup=%v; child pid=%d; alive=%v; early exit=%t; cleanup signal=%v; joined wait=%v; state=%v; output=%q",
			readyErr, contextErr, child.Process.Pid, aliveErr, earlyExit, signalErr, waitErr, child.ProcessState, output.String())
	}
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
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("pass-through child did not join after termination within its bound")
	}
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
