package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

const runnerConsoleInventoryLimit = 64
const runnerConsoleSampleLimit = 64

var errRunnerConsoleExited = errors.New("target exited before shared-console readiness")

type runnerConsoleSample struct {
	At          time.Time `json:"at"`
	Alive       bool      `json:"alive"`
	StateBefore uint32    `json:"state_before"`
	StateAfter  uint32    `json:"state_after"`
	Count       uint32    `json:"count"`
	PIDs        []uint32  `json:"pids"`
}

type runnerConsoleReadiness struct {
	TargetPID uint32                `json:"target_pid"`
	Started   time.Time             `json:"started"`
	Finished  time.Time             `json:"finished"`
	Attempts  int                   `json:"attempts"`
	Samples   []runnerConsoleSample `json:"last_samples"`
}

func (r *runnerConsoleReadiness) record(sample runnerConsoleSample) {
	sample.At = time.Now()
	sample.PIDs = slices.Clone(sample.PIDs)
	r.Attempts++
	if len(r.Samples) == runnerConsoleSampleLimit {
		r.Samples = r.Samples[1:]
	}
	r.Samples = append(r.Samples, sample)
	r.Finished = sample.At
}

func runnerConsoleContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// A timer becoming runnable does not mean its cancellation callback has run.
	if deadline, bounded := ctx.Deadline(); bounded && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func waitRunnerConsoleReady(ctx context.Context, pid uint32, inspect func() (runnerConsoleSample, error)) (report runnerConsoleReadiness, resultErr error) {
	report.TargetPID, report.Started = pid, time.Now()
	defer func() { report.Finished = time.Now() }()
	if pid == 0 {
		return report, errors.New("console readiness requires an exact nonzero target PID")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return report, errors.New("console readiness requires a bounded startup context")
	}
	var inspectionErr error
	ready := func() bool {
		if inspectionErr = runnerConsoleContextError(ctx); inspectionErr != nil {
			return true
		}
		sample, err := inspect()
		report.record(sample)
		if inspectionErr = errors.Join(err, runnerConsoleContextError(ctx)); inspectionErr != nil {
			return true
		}
		if sample.Count > runnerConsoleInventoryLimit {
			inspectionErr = errors.New("isolated Windows console process inventory exceeded its bound")
			return true
		}
		if !sample.Alive {
			inspectionErr = errRunnerConsoleExited
			return true
		}
		if sample.Count == 0 || int(sample.Count) != len(sample.PIDs) {
			inspectionErr = errors.New("console readiness received an incomplete process inventory")
			return true
		}
		return slices.Contains(sample.PIDs, pid)
	}
	if err := waitRecoveryCondition(ctx, ready); err != nil {
		return report, fmt.Errorf("waiting for Windows target %d shared console: %w", pid, err)
	}
	return report, errors.Join(inspectionErr, runnerConsoleContextError(ctx))
}

func TestRunnerTerminalConsoleReadiness(t *testing.T) {
	live := func(pids ...uint32) runnerConsoleSample {
		return runnerConsoleSample{Alive: true, StateBefore: 258, StateAfter: 258, Count: uint32(len(pids)), PIDs: pids}
	}
	apiError := errors.New("injected console API error")
	tests := []struct {
		name       string
		samples    []runnerConsoleSample
		err        error
		wantErr    error
		wantText   string
		cancelAt   int
		preCancel  bool
		wantCalls  int
		wantPeriod time.Duration
	}{
		{name: "immediate-positive", samples: []runnerConsoleSample{live(7, 42)}, wantCalls: 1},
		{name: "delayed-positive", samples: []runnerConsoleSample{live(7), live(7, 19), live(42, 7)}, wantCalls: 3, wantPeriod: 20 * time.Millisecond},
		{name: "never-present", samples: []runnerConsoleSample{live(7)}, wantErr: context.DeadlineExceeded, wantCalls: 3, wantPeriod: 30 * time.Millisecond},
		{name: "wrong-pid", samples: []runnerConsoleSample{live(420)}, wantErr: context.DeadlineExceeded, wantCalls: 3, wantPeriod: 30 * time.Millisecond},
		{name: "pre-cancel", preCancel: true, samples: []runnerConsoleSample{live(42)}, wantErr: context.Canceled},
		{name: "mid-cancel-positive", samples: []runnerConsoleSample{live(42)}, cancelAt: 1, wantErr: context.Canceled, wantCalls: 1},
		{name: "mid-cancel-absent", samples: []runnerConsoleSample{live(7)}, cancelAt: 2, wantErr: context.Canceled, wantCalls: 2, wantPeriod: 10 * time.Millisecond},
		{name: "pre-exit", samples: []runnerConsoleSample{{}}, wantErr: errRunnerConsoleExited, wantCalls: 1},
		{name: "mid-exit", samples: []runnerConsoleSample{live(7), {}}, wantErr: errRunnerConsoleExited, wantCalls: 2, wantPeriod: 10 * time.Millisecond},
		{name: "present-but-exited", samples: []runnerConsoleSample{{Count: 1, PIDs: []uint32{42}}}, wantErr: errRunnerConsoleExited, wantCalls: 1},
		{name: "api-error", samples: []runnerConsoleSample{live(42)}, err: apiError, wantErr: apiError, wantCalls: 1},
		{name: "overflow", samples: []runnerConsoleSample{{Alive: true, Count: 65}}, wantText: "inventory exceeded its bound", wantCalls: 1},
		{name: "incomplete-inventory", samples: []runnerConsoleSample{{Alive: true, Count: 2, PIDs: []uint32{42}}}, wantText: "incomplete process inventory", wantCalls: 1},
		{name: "empty-inventory", samples: []runnerConsoleSample{{Alive: true}}, wantText: "incomplete process inventory", wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
				defer cancel()
				if test.preCancel {
					cancel()
				}
				calls := 0
				report, err := waitRunnerConsoleReady(ctx, 42, func() (runnerConsoleSample, error) {
					calls++
					if calls == test.cancelAt {
						cancel()
					}
					return test.samples[min(calls-1, len(test.samples)-1)], test.err
				})
				if test.wantErr != nil {
					require.ErrorIs(t, err, test.wantErr)
				} else if test.wantText != "" {
					require.ErrorContains(t, err, test.wantText)
				} else {
					require.NoError(t, err)
					last := report.Samples[len(report.Samples)-1]
					require.True(t, last.Alive)
					require.Contains(t, last.PIDs, uint32(42))
					require.NoError(t, ctx.Err())
				}
				require.Equal(t, test.wantCalls, calls)
				require.Equal(t, calls, report.Attempts)
				require.Len(t, report.Samples, calls)
				require.Equal(t, uint32(42), report.TargetPID)
				require.Equal(t, test.wantPeriod, report.Finished.Sub(report.Started))
				for _, sample := range report.Samples {
					require.False(t, sample.At.Before(report.Started))
					require.False(t, sample.At.After(report.Finished))
				}
			})
		})
	}
}

func TestRunnerTerminalConsoleReadinessJoinsInspection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		joined := false
		report, err := waitRunnerConsoleReady(ctx, 42, func() (runnerConsoleSample, error) {
			time.Sleep(50 * time.Millisecond)
			joined = true
			return runnerConsoleSample{Alive: true, Count: 1, PIDs: []uint32{42}}, nil
		})
		require.True(t, joined, "inspection must not outlive timeout and race with cleanup")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Equal(t, 50*time.Millisecond, report.Finished.Sub(report.Started))
		require.Equal(t, 1, report.Attempts)
	})
}

func TestRunnerTerminalConsoleReadinessDeadlineAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		time.Sleep(time.Until(deadline))
		require.ErrorIs(t, runnerConsoleContextError(ctx), context.DeadlineExceeded)
		report, err := waitRunnerConsoleReady(ctx, 42, func() (runnerConsoleSample, error) {
			t.Fatal("no observation may start at or beyond the shared deadline")
			return runnerConsoleSample{}, nil
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Zero(t, report.Attempts)
	})
}

func TestRunnerTerminalConsoleReadinessCancellationWhileWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		go func() {
			time.Sleep(5 * time.Millisecond)
			cancel()
		}()
		report, err := waitRunnerConsoleReady(ctx, 42, func() (runnerConsoleSample, error) {
			return runnerConsoleSample{Alive: true, Count: 1, PIDs: []uint32{7}}, nil
		})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, report.Attempts, "cancellation must prevent another observation")
		require.Equal(t, 5*time.Millisecond, report.Finished.Sub(report.Started))
	})
}

func TestRunnerTerminalConsoleReadinessBounds(t *testing.T) {
	t.Run("requires-budget-and-target", func(t *testing.T) {
		for _, pid := range []uint32{0, 42} {
			_, err := waitRunnerConsoleReady(t.Context(), pid, func() (runnerConsoleSample, error) {
				t.Fatal("invalid admission must not inspect")
				return runnerConsoleSample{}, nil
			})
			require.Error(t, err)
		}
	})
	t.Run("owned-bounded-diagnostics", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			pids := []uint32{7}
			report, err := waitRunnerConsoleReady(ctx, 42, func() (runnerConsoleSample, error) {
				return runnerConsoleSample{Alive: true, Count: 1, PIDs: pids}, nil
			})
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Equal(t, 100, report.Attempts)
			require.Len(t, report.Samples, runnerConsoleSampleLimit)
			pids[0] = 42
			for _, sample := range report.Samples {
				require.Equal(t, []uint32{7}, sample.PIDs, "diagnostics must own each observed inventory")
			}
		})
	})
}

// Execute the controller itself, not a second implementation of its admission
// policy. Only OS/process boundaries and the budget-check return boundary are
// faked; the extracted source remains byte-for-byte identical.
func TestRunnerTerminalConsoleReadinessController(t *testing.T) {
	declarations := func(file string, names ...string) string {
		t.Helper()
		source, err := os.ReadFile(file)
		require.NoError(t, err)
		positions := token.NewFileSet()
		parsed, err := parser.ParseFile(positions, file, source, 0)
		require.NoError(t, err)
		var output strings.Builder
		for _, name := range names {
			found := false
			for _, declaration := range parsed.Decls {
				matches := false
				switch declaration := declaration.(type) {
				case *ast.FuncDecl:
					matches = declaration.Name.Name == name
				case *ast.GenDecl:
					for _, spec := range declaration.Specs {
						if spec, ok := spec.(*ast.TypeSpec); ok && spec.Name.Name == name {
							matches = true
						}
					}
				}
				if matches {
					require.False(t, found, "duplicate source declaration %s", name)
					found = true
					output.Write(source[positions.Position(declaration.Pos()).Offset:positions.Position(declaration.End()).Offset])
					output.WriteString("\n\n")
				}
			}
			require.True(t, found, "missing actual source declaration %s", name)
		}
		return output.String()
	}
	controller := declarations("cmd_runner_terminal_windows_test.go", "runnerWindowsMessage", "runRunnerWindowsController")
	t.Logf("exact native controller declarations SHA256=%x", sha256.Sum256([]byte(controller)))
	policy := declarations("cmd_runner_console_readiness_test.go", "runnerConsoleSample", "runnerConsoleReadiness",
		"record", "runnerConsoleContextError", "waitRunnerConsoleReady")
	// Wrappers inject cancellation/expiry just after the real checks return,
	// modeling the two rejected intervals without sleeps or scheduler races.
	policy = strings.Replace(policy, "func runnerConsoleContextError(", "func fixtureConsoleContextError(", 1)
	policy = strings.Replace(policy, "func waitRunnerConsoleReady(", "func fixtureWaitConsoleReady(", 1)
	poll := declarations("recovery_process_test.go", "waitRecoveryCondition")
	dir := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":             "module controllerfixture\n\ngo 1.26\n",
		"controller.go":      runnerConsoleControllerFixture + controller + policy + poll,
		"controller_test.go": runnerConsoleControllerTests,
		"context/context.go": runnerConsoleContextFixture,
		"windows/windows.go": runnerConsoleWindowsFixture,
	} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	args := []string{"test", "-count=1", "-json", "-timeout=30s"}
	info, ok := debug.ReadBuildInfo()
	require.True(t, ok, "fixture must preserve the parent test binary's race setting")
	for _, setting := range info.Settings {
		if setting.Key == "-race" && setting.Value == "true" {
			args = append(args, "-race")
		}
	}
	if testing.CoverMode() != "" {
		args = append(args, "-covermode=atomic", "-coverprofile="+filepath.Join(dir, "controller.cover"))
	}
	args = append(args, ".")
	command := exec.CommandContext(t.Context(), "go", args...)
	command.Dir = dir
	t.Logf("nonnative exact-source controller fixture command: go %q", args)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "exact-source controller fixture (nonnative OS boundaries):\n%s", output)
	for _, name := range []string{
		"before-final/canceled", "before-final/deadline", "before-final/wall-clock",
		"before-started/canceled", "before-started/deadline", "before-started/wall-clock",
		"positive", "final-api-error", "final-exited", "final-wrong-pid", "final-overflow",
	} {
		require.Contains(t, string(output), `"Action":"pass","Package":"controllerfixture","Test":"TestActualController/`+name+`"`)
		t.Logf("actual controller regression/control passed: %s", name)
	}
	require.NotContains(t, string(output), `"Action":"skip"`)
	if testing.CoverMode() != "" {
		profile, err := os.ReadFile(filepath.Join(dir, "controller.cover"))
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(string(profile), "mode: atomic\n"))
		require.Contains(t, string(profile), "controllerfixture/controller.go:")
		start := strings.Count(runnerConsoleControllerFixture, "\n") +
			strings.Count(controller[:strings.Index(controller, "func runRunnerWindowsController(")], "\n") + 1
		end := strings.Count(runnerConsoleControllerFixture+controller, "\n")
		covered := 0
		for _, line := range strings.Split(string(profile), "\n")[1:] {
			if line == "" {
				continue
			}
			var first, firstColumn, last, lastColumn, statements, count int
			n, err := fmt.Sscanf(line, "controllerfixture/controller.go:%d.%d,%d.%d %d %d",
				&first, &firstColumn, &last, &lastColumn, &statements, &count)
			require.NoError(t, err)
			require.Equal(t, 6, n)
			if first >= start && last <= end && count > 0 {
				covered += statements
			}
		}
		require.Positive(t, covered, "actual controller statements, not only fixture fakes, must execute under atomic coverage")
		t.Logf("actual controller covered statements=%d; nonnative OS boundaries", covered)
	}
}

const runnerConsoleContextFixture = `package context

import (
	"context"
	"time"
)

type Context = context.Context

var Background = context.Background
var WithTimeout = context.WithTimeout
var Canceled = context.Canceled
var DeadlineExceeded = context.DeadlineExceeded
var CancelStartup context.CancelFunc
var HideTimerError bool

type delayedTimer struct{ context.Context }

func (c delayedTimer) Err() error { return nil }
func WithDeadline(parent Context, deadline time.Time) (Context, context.CancelFunc) {
	ctx, cancel := context.WithDeadline(parent, deadline)
	CancelStartup = cancel
	if HideTimerError {
		return delayedTimer{ctx}, cancel
	}
	return ctx, cancel
}
`

const runnerConsoleWindowsFixture = `package windows

type Handle uintptr

const SYNCHRONIZE = 1
const WAIT_TIMEOUT = 258
const CTRL_BREAK_EVENT = 1

var Closed, Signals int

func OpenProcess(_ uint32, _ bool, _ uint32) (Handle, error) { return 7, nil }
func CloseHandle(_ Handle) error                             { Closed++; return nil }
func WaitForSingleObject(_ Handle, _ uint32) (uint32, error) { return WAIT_TIMEOUT, nil }
func GenerateConsoleCtrlEvent(_ uint32, _ uint32) error      { Signals++; return nil }
`

const runnerConsoleControllerFixture = `package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"time"

	"controllerfixture/context"
	"controllerfixture/windows"
)

const runnerConsoleInventoryLimit = 64
const runnerConsoleSampleLimit = 64

var errRunnerConsoleExited = errors.New("target exited before shared-console readiness")
var apiFailure = errors.New("injected final observation API error")
var scenario, interval, interruption string
var returnedReady, injected, wallClockOnly bool
var observations, observationsAfterBudget, failures, targetStops int
var failureReport runnerConsoleReadiness

type fixtureOutput struct{ data []byte }

func (o *fixtureOutput) snapshot() []byte { return bytes.Clone(o.data) }

type runnerWindowsProcess struct {
	cmd            *exec.Cmd
	done           chan struct{}
	stdout, stderr *fixtureOutput
	stopped        bool
}

func (p *runnerWindowsProcess) wait(context.Context) error { return nil }
func (p *runnerWindowsProcess) stop() error {
	if p.cmd.Process.Pid == 42 && !p.stopped {
		targetStops++
		p.stopped = true
	}
	return nil
}
func spawnRunnerWindowsTarget(_ runnerWindowsMessage, probe bool) (*runnerWindowsProcess, error) {
	p := &runnerWindowsProcess{
		cmd: &exec.Cmd{Process: &os.Process{Pid: 42}}, done: make(chan struct{}),
		stdout: &fixtureOutput{}, stderr: &fixtureOutput{},
	}
	if probe {
		p.cmd.Process.Pid = 19
		p.stdout.data, _ = json.Marshal(runnerWindowsMessage{Kind: "capability", NoDeadline: true, GoVersion: runtime.Version()})
	}
	close(p.done)
	return p, nil
}
func decodeRunnerHelperSnapshot(data []byte, _ ...string) ([][]byte, error) {
	return [][]byte{data}, nil
}
func runnerWindowsStartupFailure(target *runnerWindowsProcess, report runnerConsoleReadiness, err error) error {
	failures++
	failureReport = report
	return errors.Join(fmt.Errorf("Windows target startup admission: %w", err), target.stop())
}
func runnerWindowsConsoleContains(uint32) error { return nil }
func runnerWindowsConsoleObservation(windows.Handle) (runnerConsoleSample, error) {
	observations++
	if injected {
		observationsAfterBudget++
	}
	sample := runnerConsoleSample{Alive: true, Count: 1, PIDs: []uint32{42}}
	if observations == 2 {
		switch scenario {
		case "final-api-error":
			return sample, apiFailure
		case "final-exited":
			sample.Alive = false
		case "final-wrong-pid":
			sample.PIDs[0] = 420
		case "final-overflow":
			return sample, errors.New("inventory exceeded its bound")
		}
	}
	return sample, nil
}
func waitRunnerConsoleReady(ctx context.Context, pid uint32, inspect func() (runnerConsoleSample, error)) (runnerConsoleReadiness, error) {
	report, err := fixtureWaitConsoleReady(ctx, pid, inspect)
	returnedReady = true
	return report, err
}
func runnerConsoleContextError(ctx context.Context) error {
	err := fixtureConsoleContextError(ctx)
	atBoundary := interval == "before-final" && returnedReady && observations == 1 ||
		interval == "before-started" && observations == 2
	if err == nil && !injected && atBoundary {
		injected = true
		switch interruption {
		case "canceled":
			context.CancelStartup()
		case "deadline", "wall-clock":
			deadline, _ := ctx.Deadline()
			time.Sleep(time.Until(deadline))
			wallClockOnly = ctx.Err() == nil && !time.Now().Before(deadline)
		}
	}
	return err
}

`

const runnerConsoleControllerTests = `package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"controllerfixture/context"
	"controllerfixture/windows"
)

func TestActualController(t *testing.T) {
	tests := []struct{ name, boundary, interrupt string }{
		{"before-final/canceled", "before-final", "canceled"},
		{"before-final/deadline", "before-final", "deadline"},
		{"before-final/wall-clock", "before-final", "wall-clock"},
		{"before-started/canceled", "before-started", "canceled"},
		{"before-started/deadline", "before-started", "deadline"},
		{"before-started/wall-clock", "before-started", "wall-clock"},
		{name: "positive"}, {name: "final-api-error"}, {name: "final-exited"},
		{name: "final-wrong-pid"}, {name: "final-overflow"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				scenario, interval, interruption = test.name, test.boundary, test.interrupt
				returnedReady, injected, wallClockOnly = false, false, false
				observations, observationsAfterBudget, failures, targetStops = 0, 0, 0, 0
				windows.Closed, windows.Signals = 0, 0
				context.HideTimerError = interruption == "wall-clock"
				failureReport = runnerConsoleReadiness{}
				input, producer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				reader, output, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					for _, file := range []*os.File{input, producer, reader, output} {
						if file != nil {
							if err := file.Close(); err != nil {
								t.Error(err)
							}
						}
					}
				}()
				originalIn, originalOut := os.Stdin, os.Stdout
				os.Stdin, os.Stdout = input, output
				defer func() { os.Stdin, os.Stdout = originalIn, originalOut }()
				var requests bytes.Buffer
				encoder := json.NewEncoder(&requests)
				for _, request := range []runnerWindowsMessage{
					{Kind: "arm"}, {Kind: "probe"},
					{Kind: "start", Binary: "fake-owned-target", Action: "validate", StartupDeadline: time.Now().Add(time.Second)},
					{Kind: "finish"},
				} {
					if err := encoder.Encode(request); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := producer.Write(requests.Bytes()); err != nil {
					t.Fatal(err)
				}
				result := runRunnerWindowsController()
				// No concurrent stdout reader is needed: the entire protocol is <4 KiB.
				var events []runnerWindowsMessage
				decoder := json.NewDecoder(reader)
				wantKinds := []string{"ready", "armed", "capability"}
				if scenario == "positive" {
					wantKinds = append(wantKinds, "started", "finished")
				}
				for range wantKinds {
					var event runnerWindowsMessage
					if err := decoder.Decode(&event); err != nil {
						t.Fatal(err)
					}
					events = append(events, event)
				}
				for i, kind := range wantKinds {
					if events[i].Kind != kind {
						t.Errorf("event %d = %q, want %q", i, events[i].Kind, kind)
					}
				}
				if scenario == "positive" {
					if result != nil {
						t.Fatal(result)
					}
					if observations != 2 || failures != 0 {
						t.Errorf("positive observations=%d failures=%d", observations, failures)
					}
					started := events[3]
					if started.PID != 42 || started.Readiness == nil || started.Readiness.Attempts != 2 {
						t.Errorf("missing actual positive admission report: %+v", started)
					}
				} else {
					if result == nil {
						t.Error("controller admitted started after a rejected boundary")
					}
					if failures != 1 {
						t.Errorf("startup failure diagnostics=%d, want 1", failures)
					}
					if test.boundary != "" {
						if !injected {
							t.Fatal("regression setup never reached the selected return boundary")
						}
						expected := context.Canceled
						if interruption != "canceled" {
							expected = context.DeadlineExceeded
						}
						if !errors.Is(result, expected) {
							t.Errorf("startup error=%v, want %v", result, expected)
						}
						wantObservations := 2
						if interval == "before-final" {
							wantObservations = 1
						}
						if observations != wantObservations || observationsAfterBudget != 0 {
							t.Errorf("post-budget observation: total=%d want=%d after-budget=%d", observations, wantObservations, observationsAfterBudget)
						}
						if context.HideTimerError && !wallClockOnly {
							t.Fatal("setup did not isolate wall-clock expiry from timer cancellation")
						}
					} else {
						if observations != 2 {
							t.Errorf("final recheck observations=%d, want 2", observations)
						}
						switch scenario {
						case "final-api-error":
							if !errors.Is(result, apiFailure) {
								t.Errorf("lost API cause: %v", result)
							}
						case "final-exited":
							if !errors.Is(result, errRunnerConsoleExited) {
								t.Errorf("lost exit cause: %v", result)
							}
						case "final-wrong-pid":
							if result == nil || !bytes.Contains([]byte(result.Error()), []byte("lost shared-console membership")) {
								t.Errorf("lost exact-PID failure: %v", result)
							}
						case "final-overflow":
							if result == nil || !bytes.Contains([]byte(result.Error()), []byte("inventory exceeded its bound")) {
								t.Errorf("lost overflow failure: %v", result)
							}
						}
					}
					if failureReport.Attempts != observations || failureReport.TargetPID != 42 {
						t.Errorf("incorrect failure observations: %+v", failureReport)
					}
				}
				if windows.Closed != 1 || targetStops != 1 || windows.Signals != 0 {
					t.Errorf("ownership/signal effects: close=%d stop=%d signal=%d", windows.Closed, targetStops, windows.Signals)
				}
				// Flush/EOF is obtained without closing a file twice in cleanup.
				os.Stdout = originalOut
				if err := output.Close(); err != nil {
					t.Fatal(err)
				}
				output = nil
				var extra runnerWindowsMessage
				if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
					t.Errorf("unexpected extra admission event: %+v; error=%v", extra, err)
				}
			})
		})
	}
}
`
