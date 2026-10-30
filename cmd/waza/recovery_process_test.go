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

type recoveryDiagnosticStream struct {
	bytes.Buffer
	reads int
}

func (s *recoveryDiagnosticStream) String() string {
	s.reads++
	return s.Buffer.String()
}

type recoveryCommandMockProcess struct {
	child       *exec.Cmd
	ctx         context.Context
	cancel      context.CancelFunc
	stdout      recoveryDiagnosticStream
	stderr      recoveryDiagnosticStream
	wait        func() error
	start       time.Time
	startTime   time.Duration
	startErr    error
	startCtxErr error
	startBudget time.Duration
	readyTime   time.Duration
	readyErr    error
	observation string
	markerErr   error
	calls       int
	readyCtxErr error
	readyBudget time.Duration
	waitTime    time.Duration
}

func startRecoveryCommandMockProcess(t *testing.T, ctx context.Context, child *exec.Cmd, cancel context.CancelFunc) *recoveryCommandMockProcess {
	t.Helper()
	process := &recoveryCommandMockProcess{child: child, ctx: ctx, cancel: cancel, start: time.Now()}
	// Bound inherited output-pipe completion after exit/cancel, not startup.
	child.WaitDelay = time.Second
	child.Stdout, child.Stderr = &process.stdout, &process.stderr
	process.startErr = child.Start()
	process.startTime = time.Since(process.start)
	process.startCtxErr = ctx.Err()
	if deadline, ok := ctx.Deadline(); ok {
		process.startBudget = time.Until(deadline)
	}
	if process.startErr != nil {
		t.Logf("command-mock Start failed: elapsed=%s error=%v context=%v",
			process.startTime, process.startErr, process.startCtxErr)
		return process
	}
	// Both the normal path and failure cleanup share this sole Wait call.
	process.wait = sync.OnceValue(func() error {
		start := time.Now()
		err := child.Wait()
		process.waitTime = time.Since(start)
		return err
	})
	t.Cleanup(func() {
		cancel()
		waitErr := process.wait()
		if t.Failed() {
			t.Logf("command-mock subprocess: %s", process.diagnostic(true))
		}
		require.Error(t, waitErr, "interrupted or deliberately invalid command-mock child must not exit successfully")
	})
	return process
}

func (p *recoveryCommandMockProcess) awaitReady(ctx context.Context, marker string, session *commandmock.Session) error {
	observe := func() bool {
		_, p.markerErr = os.Stat(marker)
		p.calls = len(session.Invocations())
		return p.markerErr == nil && p.calls == 1
	}
	p.observation = "readiness poll"
	err := waitRecoveryCondition(ctx, observe)
	if err != nil {
		p.observation = "post-readiness-expiry snapshot (not readiness evidence)"
		observe()
	}
	p.readyTime = time.Since(p.start)
	p.readyErr = err
	p.readyCtxErr = p.ctx.Err()
	if deadline, ok := p.ctx.Deadline(); ok {
		p.readyBudget = time.Until(deadline)
	}
	return err
}

// Expected observation failures in controls use the same capture as test failures.
func (p *recoveryCommandMockProcess) diagnostic(failed bool) string {
	if !failed {
		return ""
	}
	p.cancel()
	waitErr := p.wait()
	return fmt.Sprintf("Start elapsed=%s error=%v context after Start=%v remaining=%s; readiness elapsed=%s error=%v; %s: marker error=%v calls=%d; child context before cleanup=%v remaining=%s; joined Wait=%v Wait elapsed=%s process state=%v stdout=%q stderr=%q",
		p.startTime, p.startErr, p.startCtxErr, p.startBudget, p.readyTime, p.readyErr, p.observation, p.markerErr, p.calls,
		p.readyCtxErr, p.readyBudget, waitErr, p.waitTime, p.child.ProcessState, p.stdout.String(), p.stderr.String())
}

func TestRecoveryCommandMockStartupDiagnostics(t *testing.T) {
	binary := buildRecoveryCLI(t)
	t.Run("start_failure", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing-cli"), "__command-mock")
		process := startRecoveryCommandMockProcess(t, ctx, child, cancel)
		require.ErrorIs(t, process.startErr, os.ErrNotExist)
		require.Nil(t, process.startCtxErr)
		require.Nil(t, process.wait, "failed Start has no process to Wait")
		require.Zero(t, process.stdout.reads)
		require.Zero(t, process.stderr.reads)
	})
	for _, name := range []string{"early_exit", "live_missing_marker", "live_post_deadline_marker"} {
		t.Run(name, func(t *testing.T) {
			earlyExit := name == "early_exit"
			workspace := t.TempDir()
			session, err := commandmock.NewSession(workspace, []models.CommandMockConfig{{
				Name: "az", ExpectCalls: new(2), Responses: []models.CommandMockResponse{{
					Args: []string{"account", "show"}, Sequence: []models.CommandMockStep{
						{DelayMS: 300000}, {Stdout: "recovered"},
					},
				}},
			}}, workspace)
			require.NoError(t, err)
			t.Cleanup(func() {
				records, closeErr := session.Close()
				expected := 1
				if earlyExit {
					expected = 0
				}
				require.Len(t, records, expected)
				require.ErrorContains(t, closeErr, fmt.Sprintf("expected 2 call(s), got %d", expected))
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
			effectiveRoot := root
			if earlyExit {
				effectiveRoot = filepath.Join(workspace, "missing-runtime")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			t.Cleanup(cancel)
			child := exec.CommandContext(ctx, binary, "__command-mock", "--root", effectiveRoot, "--name", "az", "--", "account", "show")
			child.Dir = workspace
			child.Env = append(env, commandmock.SessionEnvironmentVariable+"="+session.ID(), "WAZA_NO_UPDATE_CHECK=1")
			process := startRecoveryCommandMockProcess(t, ctx, child, cancel)
			require.NoError(t, process.startErr)
			marker := filepath.Join(root, "sessions", session.ID(), "sequences", "mock-0000-response-0000", "step-00000000")
			if earlyExit {
				var exit *exec.ExitError
				require.ErrorAs(t, process.wait(), &exit)
				require.Equal(t, 127, exit.ExitCode())
			} else {
				ready, stop := context.WithTimeout(t.Context(), 5*time.Second)
				err := process.awaitReady(ready, marker, session)
				stop()
				require.NoError(t, err, "control must reach the actual delayed step")
				require.Nil(t, session.Invocations()[0].ExitCode)
				require.Empty(t, process.diagnostic(err != nil), "successful readiness must not capture streams or cancel the live child")
				require.Zero(t, process.stdout.reads)
				require.Zero(t, process.stderr.reads)
				require.Nil(t, ctx.Err())
				require.Nil(t, child.ProcessState, "successful observation must not Wait")
			}
			// Expired before observation: even a matching marker is only late
			// evidence, never proof that this observation met its deadline.
			observationMarker := filepath.Join(workspace, "absent-readiness-marker")
			if name == "live_post_deadline_marker" {
				observationMarker = marker
			}
			ready, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			err = process.awaitReady(ready, observationMarker, session)
			stop()
			require.ErrorIs(t, err, context.DeadlineExceeded)
			if name == "live_post_deadline_marker" {
				require.NoError(t, process.markerErr)
			} else {
				require.ErrorIs(t, process.markerErr, os.ErrNotExist)
			}
			require.Nil(t, process.readyCtxErr, "readiness expiry must not cancel the owned child")
			if !earlyExit {
				require.Equal(t, 1, process.calls)
				require.NoError(t, child.Process.Kill(), "the unready control must still be a live process")
			} else {
				require.Zero(t, process.calls)
			}
			require.Zero(t, process.stdout.reads, "the failed observation alone must not read streams")
			require.Zero(t, process.stderr.reads)
			diagnostic := process.diagnostic(err != nil)
			t.Log(diagnostic)
			require.ErrorIs(t, ctx.Err(), context.Canceled, "failure capture must cancel before joining and reading streams")
			require.NotNil(t, child.ProcessState, "failure capture must join before reading streams")
			require.Equal(t, 1, process.stdout.reads)
			require.Equal(t, 1, process.stderr.reads)
			require.Contains(t, diagnostic, "post-readiness-expiry snapshot (not readiness evidence): marker error=")
			require.NotContains(t, diagnostic, "; readiness poll:")
			require.Contains(t, diagnostic, "child context before cleanup=<nil>")
			require.Contains(t, diagnostic, "error=context deadline exceeded")
			require.Contains(t, diagnostic, "stdout=\"\"")
			waitErr := process.wait()
			if earlyExit {
				require.Contains(t, diagnostic, "joined Wait=exit status 127")
				require.Contains(t, diagnostic, "failed to load task configuration")
			} else {
				require.Error(t, waitErr)
				require.Contains(t, diagnostic, "stderr=\"\"")
				require.Contains(t, diagnostic, "calls=1")
				if name == "live_post_deadline_marker" {
					require.Contains(t, diagnostic, "post-readiness-expiry snapshot (not readiness evidence): marker error=<nil> calls=1")
					require.ErrorIs(t, process.readyErr, context.DeadlineExceeded)
				}
			}
			require.Equal(t, waitErr, process.wait(), "repeated inspection must reuse the sole Wait result")
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
			process := startRecoveryCommandMockProcess(t, ctx, child, cancel)
			require.NoError(t, process.startErr)
			marker := filepath.Join(root, "sessions", session.ID(), "sequences", "mock-0000-response-0000", "step-00000000")
			readiness, stopReadiness := context.WithTimeout(t.Context(), 5*time.Second)
			err = process.awaitReady(readiness, marker, session)
			stopReadiness()
			require.NoError(t, err, "child never started and reserved the delayed step")
			require.Nil(t, session.Invocations()[0].ExitCode)
			if abrupt {
				require.NoError(t, child.Process.Kill())
			} else {
				require.NoError(t, child.Process.Signal(syscall.SIGTERM))
			}
			err = process.wait()
			var exit *exec.ExitError
			require.True(t, errors.As(err, &exit), "%v", err)
			if !abrupt {
				require.Equal(t, 130, exit.ExitCode())
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
			require.Zero(t, process.stdout.reads, "ordinary interruption success must not capture diagnostic streams")
			require.Zero(t, process.stderr.reads)
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
