package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/stretchr/testify/require"
)

const runSignalHelperEnv = "WAZA_RUN_SIGNAL_HELPER"
const runSignalSpecEnv = "WAZA_RUN_SIGNAL_SPEC"

type blockingBenchmarkRunner struct {
	started chan struct{}
}

func (r *blockingBenchmarkRunner) OnProgress(orchestration.ProgressListener) {}

func (r *blockingBenchmarkRunner) RunBenchmark(ctx context.Context) (*models.EvaluationOutcome, error) {
	if r.started != nil {
		close(r.started)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type successfulBenchmarkRunner struct{}

func (*successfulBenchmarkRunner) OnProgress(orchestration.ProgressListener) {}

func (*successfulBenchmarkRunner) RunBenchmark(context.Context) (*models.EvaluationOutcome, error) {
	return &models.EvaluationOutcome{}, nil
}

type persistentCancellationEngine struct {
	execution.AgentEngine
	started chan struct{}
	forced  chan struct{}
}

func (e *persistentCancellationEngine) Shutdown(ctx context.Context) error {
	if e.started != nil {
		close(e.started)
	}
	<-e.forced
	return ctx.Err()
}

func (e *persistentCancellationEngine) ForceStop() {
	select {
	case <-e.forced:
	default:
		close(e.forced)
	}
}

func TestRunCommand_CancelsOnSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM is not supported on Windows")
	}
	for _, tt := range []struct {
		name, identity string
	}{
		{"legacy", "skill: test-skill\n"},
		{"scenario", "scenario: inventory\nschemaVersion: \"2.0\"\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runCommandCancelsOnSIGTERM(t, tt.identity)
		})
	}
}

func TestRunCommandCancellationBoundsPersistentEngineShutdown(t *testing.T) {
	resetRunGlobals()
	oldEngineFactory := newRunEngine
	oldRunnerFactory := newBenchmarkRunner
	oldGrace := runEngineShutdownGrace
	t.Cleanup(func() {
		newRunEngine = oldEngineFactory
		newBenchmarkRunner = oldRunnerFactory
		runEngineShutdownGrace = oldGrace
		resetRunGlobals()
	})
	runEngineShutdownGrace = 5 * time.Millisecond

	engine := &persistentCancellationEngine{
		AgentEngine: execution.NewMockEngine("test-model"),
		forced:      make(chan struct{}),
	}
	started := make(chan struct{})
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		return engine, nil
	}
	newBenchmarkRunner = func(*config.EvalConfig, execution.AgentEngine, ...orchestration.RunnerOption) benchmarkRunner {
		return &blockingBenchmarkRunner{started: started}
	}

	ctx, cancel := context.WithCancel(t.Context())
	cmd := newRunCommand()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{createTestSpec(t, "mock")})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	<-started
	start := time.Now()
	cancel()

	err := <-done
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 250*time.Millisecond)
	select {
	case <-engine.forced:
	default:
		t.Fatal("cancellation-time shutdown did not force-stop the persistent engine")
	}
}

func TestShutdownRunEngineBoundsCancellationAfterShutdownStarts(t *testing.T) {
	oldGrace := runEngineShutdownGrace
	t.Cleanup(func() { runEngineShutdownGrace = oldGrace })
	runEngineShutdownGrace = 5 * time.Millisecond

	engine := &persistentCancellationEngine{
		AgentEngine: execution.NewMockEngine("test-model"),
		started:     make(chan struct{}),
		forced:      make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- shutdownRunEngine(ctx, engine) }()

	<-engine.started
	start := time.Now()
	cancel()

	require.ErrorIs(t, <-done, context.Canceled)
	require.Less(t, time.Since(start), 250*time.Millisecond)
	select {
	case <-engine.forced:
	default:
		t.Fatal("cancellation during shutdown did not force-stop the persistent engine")
	}
}

func TestRunCommandPropagatesCancellationDuringShutdown(t *testing.T) {
	resetRunGlobals()
	oldEngineFactory := newRunEngine
	oldRunnerFactory := newBenchmarkRunner
	oldGrace := runEngineShutdownGrace
	t.Cleanup(func() {
		newRunEngine = oldEngineFactory
		newBenchmarkRunner = oldRunnerFactory
		runEngineShutdownGrace = oldGrace
		resetRunGlobals()
	})
	runEngineShutdownGrace = 5 * time.Millisecond

	engine := &persistentCancellationEngine{
		AgentEngine: execution.NewMockEngine("test-model"),
		started:     make(chan struct{}),
		forced:      make(chan struct{}),
	}
	newRunEngine = func(models.Config) (execution.AgentEngine, error) {
		return engine, nil
	}
	newBenchmarkRunner = func(*config.EvalConfig, execution.AgentEngine, ...orchestration.RunnerOption) benchmarkRunner {
		return &successfulBenchmarkRunner{}
	}

	ctx, cancel := context.WithCancel(t.Context())
	resultPath := filepath.Join(t.TempDir(), "result.json")
	cmd := newRunCommand()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{createTestSpec(t, "mock"), "--output", resultPath})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	<-engine.started
	cancel()

	require.ErrorIs(t, <-done, context.Canceled)
	_, err := os.Stat(resultPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func runCommandCancelsOnSIGTERM(t *testing.T, identity string) {
	t.Helper()
	resetRunGlobals()

	dir := t.TempDir()
	taskDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(taskDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(taskDir, "task.yaml"), []byte(`
id: signal-task
name: Signal Task
inputs:
  prompt: "test"
`), 0o644))

	specPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(`
name: signal-cancel-test
`+identity+`
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test-model
tasks:
  - "tasks/*.yaml"
`), 0o644))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestRunCommand_SignalCancelsBenchmarkHelperProcess")
	cmd.Env = append(os.Environ(),
		runSignalHelperEnv+"=1",
		runSignalSpecEnv+"="+specPath,
	)
	var stdout, stderr bytes.Buffer
	stdoutPipe, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stderrPipe, err := cmd.StderrPipe()
	require.NoError(t, err)

	ready := make(chan struct{})
	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go func() {
		defer close(stdoutDone)
		scanner := bufio.NewScanner(stdoutPipe)
		signaledReady := false
		for scanner.Scan() {
			line := scanner.Text()
			stdout.WriteString(line)
			stdout.WriteByte('\n')
			if !signaledReady && strings.Contains(line, "Running benchmark:") {
				close(ready)
				signaledReady = true
			}
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(&stdout, "stdout read error: %v\n", err)
		}
	}()
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(&stderr, stderrPipe)
	}()

	require.NoError(t, cmd.Start())
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		<-stdoutDone
		<-stderrDone
		require.FailNow(t, "helper did not reach signal-aware benchmark run", "stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	waitErr := cmd.Wait()
	<-stdoutDone
	<-stderrDone
	require.NoError(t, waitErr, "run should exit cleanly after SIGTERM; stdout=%s stderr=%s", stdout.String(), stderr.String())
}

func TestRunCommand_SignalCancelsBenchmarkHelperProcess(t *testing.T) {
	if os.Getenv(runSignalHelperEnv) != "1" {
		return
	}

	resetRunGlobals()
	newBenchmarkRunner = func(cfg *config.EvalConfig, engine execution.AgentEngine, opts ...orchestration.RunnerOption) benchmarkRunner {
		return &blockingBenchmarkRunner{}
	}

	specPath := os.Getenv(runSignalSpecEnv)
	if specPath == "" {
		fmt.Fprintln(os.Stderr, "missing signal test spec path")
		os.Exit(2)
	}

	cmd := newRunCommand()
	cmd.SetArgs([]string{specPath})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.Execute()
	if err == nil {
		fmt.Fprintln(os.Stderr, "expected interrupt to cancel the run")
		os.Exit(3)
	}
	if !strings.Contains(err.Error(), "context canceled") {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	os.Exit(0)
}
