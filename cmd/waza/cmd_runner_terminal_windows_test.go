package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/pe"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

const runnerWindowsHelperEnv = "WAZA_RUNNER_WINDOWS_CONSOLE_HELPER"

type runnerWindowsMessage struct {
	Kind            string                  `json:"kind"`
	Handle          uint64                  `json:"handle,omitempty"`
	Binary          string                  `json:"binary,omitempty"`
	Action          string                  `json:"action,omitempty"`
	PID             uint32                  `json:"pid,omitempty"`
	NoDeadline      bool                    `json:"no_deadline,omitempty"`
	GoVersion       string                  `json:"go_version,omitempty"`
	Code            int                     `json:"code,omitempty"`
	Stdout          []byte                  `json:"stdout,omitempty"`
	Stderr          []byte                  `json:"stderr,omitempty"`
	StartupDeadline time.Time               `json:"startup_deadline,omitempty"`
	Readiness       *runnerConsoleReadiness `json:"readiness,omitempty"`
}

type runnerWindowsOutput struct {
	mu      sync.Mutex
	data    []byte
	framing *runnerHelperFraming
	events  chan runnerWindowsMessage
}

func (w *runnerWindowsOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.data)+len(data) > 1<<20 {
		return 0, errors.New("Windows runner process output exceeded 1 MiB")
	}
	w.data = append(w.data, data...)
	if w.events != nil {
		records, err := w.framing.write(data)
		if err != nil {
			return 0, fmt.Errorf("framing Windows controller output: %w", err)
		}
		for _, line := range records {
			var event runnerWindowsMessage
			if err := json.Unmarshal(line, &event); err != nil {
				return 0, fmt.Errorf("decoding Windows controller event: %w", err)
			}
			select {
			case w.events <- event:
			default:
				return 0, errors.New("Windows controller event queue overflow")
			}
		}
	}
	return len(data), nil
}

func (w *runnerWindowsOutput) finish() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.framing != nil {
		return w.framing.finish()
	}
	return nil
}

func (w *runnerWindowsOutput) snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.data)
}

type runnerWindowsProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	err    error
	stdout *runnerWindowsOutput
	stderr *runnerWindowsOutput
}

func startRunnerWindowsProcess(cmd *exec.Cmd, protocol bool) (*runnerWindowsProcess, error) {
	p := &runnerWindowsProcess{
		cmd: cmd, done: make(chan struct{}),
		stdout: &runnerWindowsOutput{}, stderr: &runnerWindowsOutput{},
	}
	if protocol {
		p.stdout.events = make(chan runnerWindowsMessage, 16)
		p.stdout.framing = newRunnerHelperFraming("ready", "armed", "capability", "started", "result", "finished")
	}
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		defer close(p.done)
		waitErr := cmd.Wait()
		p.err = errors.Join(waitErr, p.stdout.finish())
	}()
	return p, nil
}

func (p *runnerWindowsProcess) wait(ctx context.Context) error {
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return fmt.Errorf("waiting for Windows process %d: %w", p.cmd.Process.Pid, ctx.Err())
	}
}

func (p *runnerWindowsProcess) stop() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	killErr := p.cmd.Process.Kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitErr := p.wait(ctx)
	if _, exited := errors.AsType[*exec.ExitError](waitErr); exited {
		waitErr = nil
	}
	return errors.Join(killErr, waitErr)
}

func (p *runnerWindowsProcess) event(ctx context.Context, kind string) (runnerWindowsMessage, error) {
	accept := func(event runnerWindowsMessage) (runnerWindowsMessage, error) {
		if event.Kind != kind {
			return event, fmt.Errorf("Windows controller returned %q, want %q", event.Kind, kind)
		}
		return event, nil
	}
	select {
	case event := <-p.stdout.events:
		return accept(event)
	case <-p.done:
		select {
		case event := <-p.stdout.events:
			return accept(event)
		default:
		}
		return runnerWindowsMessage{}, fmt.Errorf("Windows controller exited before %s: %v; stdout=%s; stderr=%s",
			kind, p.err, p.stdout.snapshot(), p.stderr.snapshot())
	case <-ctx.Done():
		return runnerWindowsMessage{}, fmt.Errorf("waiting for Windows controller %s: %w", kind, ctx.Err())
	}
}

func runnerWindowsCallError(operation string, err error) error {
	if errors.Is(err, syscall.Errno(0)) {
		return fmt.Errorf("%s returned zero without an error code", operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func runnerWindowsInJob(process, job windows.Handle) error {
	var member uint32
	result, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob").Call(
		uintptr(process), uintptr(job), uintptr(unsafe.Pointer(&member)))
	if result == 0 {
		return runnerWindowsCallError("checking owned Windows job membership", err)
	}
	if member == 0 {
		return errors.New("Windows process is not in the invocation-owned job")
	}
	return nil
}

func runnerWindowsConsoleInventory() (runnerConsoleSample, error) {
	var processes [runnerConsoleInventoryLimit]uint32
	count, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList").Call(
		uintptr(unsafe.Pointer(&processes[0])), uintptr(len(processes)))
	sample := runnerConsoleSample{At: time.Now(), Count: uint32(count)}
	if count == 0 {
		return sample, runnerWindowsCallError("enumerating isolated Windows console", err)
	}
	if count > uintptr(len(processes)) {
		return sample, errors.New("isolated Windows console process inventory exceeded its bound")
	}
	sample.PIDs = append([]uint32(nil), processes[:count]...)
	return sample, nil
}

func runnerWindowsConsoleContains(pid uint32) error {
	sample, err := runnerWindowsConsoleInventory()
	if err != nil {
		return err
	}
	for _, current := range sample.PIDs {
		if current == pid {
			return nil
		}
	}
	return fmt.Errorf("Windows target %d does not share the isolated controller console: %+v", pid, sample)
}

func runnerWindowsConsoleObservation(handle windows.Handle) (runnerConsoleSample, error) {
	before, err := windows.WaitForSingleObject(handle, 0)
	sample := runnerConsoleSample{StateBefore: ^uint32(0), StateAfter: ^uint32(0)}
	if err != nil {
		return sample, fmt.Errorf("checking target before console observation: %w", err)
	}
	sample.StateBefore, sample.StateAfter = before, before
	if before == windows.WAIT_OBJECT_0 {
		return sample, nil
	}
	if before != uint32(windows.WAIT_TIMEOUT) {
		return sample, fmt.Errorf("unexpected target handle state before console observation: %d", before)
	}
	sample, inventoryErr := runnerWindowsConsoleInventory()
	after, waitErr := windows.WaitForSingleObject(handle, 0)
	sample.StateBefore, sample.StateAfter = before, ^uint32(0)
	if waitErr == nil {
		sample.StateAfter = after
	}
	sample.Alive = waitErr == nil && after == uint32(windows.WAIT_TIMEOUT)
	if waitErr == nil && after != windows.WAIT_OBJECT_0 && after != uint32(windows.WAIT_TIMEOUT) {
		waitErr = fmt.Errorf("unexpected target handle state after console observation: %d", after)
	}
	return sample, errors.Join(inventoryErr, waitErr)
}

func runnerWindowsStartupFailure(target *runnerWindowsProcess, report runnerConsoleReadiness, startupErr error) error {
	observedExit := len(report.Samples) != 0 &&
		report.Samples[len(report.Samples)-1].StateAfter == windows.WAIT_OBJECT_0
	var earlyWaitErr error
	originalExit := "not observed before cleanup"
	if observedExit {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		earlyWaitErr = target.wait(ctx)
		cancel()
		select {
		case <-target.done:
			if target.cmd.ProcessState != nil {
				originalExit = target.cmd.ProcessState.String()
			}
		default:
			originalExit = "exited handle observed; output join incomplete before cleanup"
		}
	}
	cleanupErr := target.stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitErr := target.wait(ctx)
	joined, exit := false, "not joined"
	select {
	case <-target.done:
		joined = true
		if target.cmd.ProcessState != nil {
			exit = target.cmd.ProcessState.String()
		} else {
			exit = "unavailable after joined Wait"
		}
	default:
	}
	// Reserve room under the existing controller output bound, even after JSON
	// escaping. Full-size captures remain bounded; truncation is explicit.
	output := func(data []byte) map[string]any {
		const limit = 64 << 10
		return map[string]any{
			"bytes": len(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data)),
			"truncated": len(data) > limit, "prefix": data[:min(len(data), limit)],
		}
	}
	diagnostic, encodeErr := json.Marshal(map[string]any{
		"controller_pid": os.Getpid(), "target_pid": target.cmd.Process.Pid,
		"controller_requested_flags": windows.CREATE_NEW_CONSOLE, "target_requested_flags": windows.CREATE_NEW_PROCESS_GROUP,
		"readiness": report, "elapsed": report.Finished.Sub(report.Started).String(),
		"wait_joined": joined, "original_exit": originalExit, "exit_after_cleanup": exit,
		"wait_error": fmt.Sprint(waitErr), "early_wait_error": fmt.Sprint(earlyWaitErr),
		"exit_observed_before_cleanup": observedExit, "cleanup_error": fmt.Sprint(cleanupErr),
		"stdout": output(target.stdout.snapshot()), "stderr": output(target.stderr.snapshot()),
	})
	var writeErr error
	if encodeErr == nil {
		_, writeErr = fmt.Fprintf(os.Stderr, "Windows startup diagnostic: %s\n", diagnostic)
	}
	return errors.Join(fmt.Errorf("Windows target startup admission: %w; diagnostic=%s", startupErr, diagnostic),
		cleanupErr, earlyWaitErr, waitErr, encodeErr, writeErr)
}

func runnerWindowsRemaining(input *os.File) (uint32, error) {
	var remaining uint32
	result, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe").Call(
		input.Fd(), 0, 0, 0, uintptr(unsafe.Pointer(&remaining)), 0)
	if result == 0 {
		return 0, runnerWindowsCallError("observing inherited Windows pipe consumption", err)
	}
	return remaining, nil
}

func runnerWindowsCloseFile(file *os.File) error {
	done := make(chan error, 1)
	go func() { done <- file.Close() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("closing owned Windows pipe %s did not complete", file.Name())
	}
}

type runnerWindowsOperation struct {
	done chan struct{}
	err  error
}

func TestRunnerTerminalWindowsInheritedStdinCancellation(t *testing.T) {
	binary := buildRecoveryCLI(t)
	file, err := os.Open(binary)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	image, err := pe.NewFile(file)
	require.NoError(t, err)
	var subsystem uint16
	switch header := image.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		subsystem = header.Subsystem
	case *pe.OptionalHeader64:
		subsystem = header.Subsystem
	default:
		t.Fatal("actual Windows target lacks a supported PE optional header")
	}
	digest := sha256.New()
	_, err = io.Copy(digest, file)
	require.NoError(t, err)
	info, err := buildinfo.Read(file)
	require.NoError(t, err)
	settings := make(map[string]string)
	for _, setting := range info.Settings {
		switch setting.Key {
		case "GOOS", "GOARCH", "GOAMD64", "CGO_ENABLED", "-buildmode", "-compiler", "-race", "-cover", "-trimpath":
			settings[setting.Key] = setting.Value
		case "-ldflags":
			settings["-ldflags.sha256"] = fmt.Sprintf("%x", sha256.Sum256([]byte(setting.Value)))
		}
	}
	diagnostic, err := json.Marshal(map[string]any{
		"machine": image.Machine, "subsystem": subsystem, "sha256": fmt.Sprintf("%x", digest.Sum(nil)),
		"go_version": info.GoVersion, "build_settings_allowlist": settings,
		"controller_requested_flags": windows.CREATE_NEW_CONSOLE, "target_requested_flags": windows.CREATE_NEW_PROCESS_GROUP,
	})
	require.NoError(t, err)
	t.Logf("actual built Windows target (not a crossbuild receipt): %s", diagnostic)
	require.Equal(t, uint16(pe.IMAGE_SUBSYSTEM_WINDOWS_CUI), subsystem)
	require.Equal(t, runtime.Version(), info.GoVersion, "capability probe and actual CLI must use the same Go runtime")
	for _, action := range []string{"validate", "run"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			job, err := windows.CreateJobObject(nil, nil)
			require.NoError(t, err)
			var controller *runnerWindowsProcess
			var control io.WriteCloser
			var controllerHandle, targetHandle windows.Handle
			var files []*os.File
			var operations []*runnerWindowsOperation
			t.Cleanup(func() {
				var cleanupErr error
				cleanupErr = errors.Join(cleanupErr, windows.CloseHandle(job))
				if targetHandle != 0 {
					state, waitErr := windows.WaitForSingleObject(targetHandle, 5000)
					if waitErr == nil && state != windows.WAIT_OBJECT_0 {
						waitErr = errors.New("owned Windows CLI survived job cleanup")
					}
					cleanupErr = errors.Join(cleanupErr, waitErr, windows.CloseHandle(targetHandle))
				}
				if control != nil {
					if err := control.Close(); !errors.Is(err, os.ErrClosed) {
						cleanupErr = errors.Join(cleanupErr, err)
					}
				}
				if controller != nil {
					cleanupErr = errors.Join(cleanupErr, controller.stop())
				}
				if controllerHandle != 0 {
					cleanupErr = errors.Join(cleanupErr, windows.CloseHandle(controllerHandle))
				}
				for _, file := range files {
					cleanupErr = errors.Join(cleanupErr, runnerWindowsCloseFile(file))
				}
				for _, operation := range operations {
					select {
					case <-operation.done:
						cleanupErr = errors.Join(cleanupErr, operation.err)
					case <-time.After(5 * time.Second):
						cleanupErr = errors.Join(cleanupErr, errors.New("owned Windows pipe operation survived cleanup"))
					}
				}
				require.NoError(t, cleanupErr, "Windows process/pipe cleanup")
			})
			limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
			limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
			_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
				uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
			require.NoError(t, err)

			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerTerminalWindowsConsoleHelper$")
			command.Dir = t.TempDir()
			command.Env = append(os.Environ(), runnerWindowsHelperEnv+"=controller")
			command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
			control, err = command.StdinPipe()
			require.NoError(t, err)
			controller, err = startRunnerWindowsProcess(command, true)
			require.NoError(t, err)
			ready, err := controller.event(ctx, "ready")
			require.NoError(t, err)
			require.Equal(t, uint32(command.Process.Pid), ready.PID)
			controllerHandle, err = windows.OpenProcess(
				windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_DUP_HANDLE|windows.PROCESS_QUERY_INFORMATION,
				false, ready.PID)
			require.NoError(t, err)
			require.NoError(t, windows.AssignProcessToJobObject(job, controllerHandle))
			require.NoError(t, runnerWindowsInJob(controllerHandle, job))
			requests := json.NewEncoder(control)
			require.NoError(t, requests.Encode(runnerWindowsMessage{Kind: "arm"}))
			_, err = controller.event(ctx, "armed")
			require.NoError(t, err)

			pipe := func() (*os.File, *os.File, uint64) {
				var read, write windows.Handle
				require.NoError(t, windows.CreatePipe(&read, &write, nil, 4096))
				input := os.NewFile(uintptr(read), "runner-windows-retained-input")
				producer := os.NewFile(uintptr(write), "runner-windows-retained-producer")
				files = append(files, input, producer)
				var remote windows.Handle
				// Transfer after ARMED, not through controller handle inheritance.
				require.NoError(t, windows.DuplicateHandle(windows.CurrentProcess(), read,
					controllerHandle, &remote, 0, false, windows.DUPLICATE_SAME_ACCESS))
				return input, producer, uint64(remote)
			}
			_, _, probe := pipe()
			require.NoError(t, requests.Encode(runnerWindowsMessage{Kind: "probe", Handle: probe}))
			capability, err := controller.event(ctx, "capability")
			require.NoError(t, err)
			require.True(t, capability.NoDeadline, "child-compatible inherited pipe must report os.ErrNoDeadline")
			require.Equal(t, runtime.Version(), capability.GoVersion)
			t.Logf("inherited anonymous-pipe environment/handle capability: os.ErrNoDeadline; %s", capability.GoVersion)

			input, producer, remote := pipe()
			require.NoError(t, runnerConsoleContextError(ctx))
			deadline, bounded := ctx.Deadline()
			require.True(t, bounded)
			if startupLimit := time.Now().Add(5 * time.Second); startupLimit.Before(deadline) {
				deadline = startupLimit
			}
			require.NoError(t, requests.Encode(runnerWindowsMessage{
				Kind: "start", Handle: remote, Binary: binary, Action: action, StartupDeadline: deadline,
			}))
			started, err := controller.event(ctx, "started")
			require.NoError(t, err)
			require.NoError(t, runnerConsoleContextError(ctx))
			require.NotZero(t, started.PID)
			require.NotNil(t, started.Readiness)
			require.Equal(t, started.PID, started.Readiness.TargetPID)
			require.NotEmpty(t, started.Readiness.Samples)
			last := started.Readiness.Samples[len(started.Readiness.Samples)-1]
			require.True(t, last.Alive)
			require.Contains(t, last.PIDs, started.PID)
			readiness, err := json.Marshal(started.Readiness)
			require.NoError(t, err)
			t.Logf("controller PID=%d; positive live target shared-console admission: %s", ready.PID, readiness)
			targetHandle, err = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_INFORMATION, false, started.PID)
			require.NoError(t, err)
			require.NoError(t, runnerWindowsInJob(targetHandle, job))
			startOperation := func(operation func() error) *runnerWindowsOperation {
				owned := &runnerWindowsOperation{done: make(chan struct{})}
				go func() {
					defer close(owned.done)
					owned.err = operation()
				}()
				operations = append(operations, owned)
				return owned
			}
			await := func(operation *runnerWindowsOperation) {
				select {
				case <-operation.done:
					require.NoError(t, operation.err)
				case <-time.After(5 * time.Second):
					t.Fatal("Windows pipe operation did not complete")
				}
			}
			padding := append(bytes.Repeat([]byte(" "), 2<<20), '{')
			require.Less(t, len(padding), runnercontract.MaxRequestBytes)
			await(startOperation(func() error {
				n, writeErr := producer.Write(padding)
				if writeErr == nil && n != len(padding) {
					writeErr = io.ErrShortWrite
				}
				return writeErr
			}))
			var remaining uint32
			var peekErr error
			readEntry, stopReadEntry := context.WithTimeout(ctx, 5*time.Second)
			err = waitRecoveryCondition(readEntry, func() bool {
				remaining, peekErr = runnerWindowsRemaining(input)
				return peekErr != nil || uint64(remaining) < uint64(len(padding))
			})
			stopReadEntry()
			require.NoError(t, err)
			require.NoError(t, peekErr)
			require.Less(t, uint64(remaining), uint64(len(padding)), "only the actual CLI may consume target input")
			state, err := windows.WaitForSingleObject(targetHandle, 0)
			require.NoError(t, err)
			require.Equal(t, uint32(windows.WAIT_TIMEOUT), state, "target must remain alive with producer open")
			t.Logf("target/group PID=%d; written=%d remaining=%d; producer remains open", started.PID, len(padding), remaining)
			require.NoError(t, requests.Encode(runnerWindowsMessage{Kind: "cancel", PID: started.PID}))
			result, err := controller.event(ctx, "result")
			require.NoError(t, err)
			require.Equal(t, started.PID, result.PID)
			require.NotContains(t, string(result.Stderr), "Usage:")
			if action == "validate" {
				require.Equal(t, 2, result.Code)
				require.Empty(t, result.Stdout)
				require.Contains(t, string(result.Stderr), "runner input interrupted: context canceled")
			} else {
				require.Equal(t, 3, result.Code)
				envelope, decodeErr := runnerterminal.Validate(result.Stdout, nil)
				require.NoError(t, decodeErr)
				require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
				require.Equal(t, "input", envelope.FailureBoundary)
				require.Equal(t, "not_checked", envelope.Validation)
				require.Nil(t, envelope.Identity)
				require.Nil(t, envelope.Selection)
				require.Nil(t, envelope.Observation)
				require.Zero(t, envelope.EngineCalls)
				require.Len(t, envelope.Diagnostics, 1)
				require.Contains(t, envelope.Diagnostics[0].Message, "context canceled")
				require.Equal(t, 1, bytes.Count(result.Stdout, []byte{'\n'}))
				require.Equal(t, "runner run: "+string(runnerterminal.Canceled)+"\n", string(result.Stderr))
			}
			state, err = windows.WaitForSingleObject(targetHandle, 0)
			require.NoError(t, err)
			require.Equal(t, uint32(windows.WAIT_OBJECT_0), state, "terminal receipt must follow process completion")
			awaitRead := startOperation(func() error {
				value, readErr := readRunnerOwnershipMarker(input, padding)
				if readErr == nil && !bytes.HasSuffix(value, []byte("still-owned")) {
					readErr = errors.New("retained parent input lost the exact ownership marker")
				}
				return readErr
			})
			await(startOperation(func() error {
				n, writeErr := producer.Write([]byte("still-owned"))
				if writeErr == nil && n != len("still-owned") {
					writeErr = io.ErrShortWrite
				}
				return writeErr
			}))
			await(awaitRead)
			require.NoError(t, requests.Encode(runnerWindowsMessage{Kind: "finish"}))
			_, err = controller.event(ctx, "finished")
			require.NoError(t, err)
			require.NoError(t, controller.wait(ctx), "controller stderr: %s", controller.stderr.snapshot())
		})
	}
}

func TestRunnerTerminalWindowsConsoleHelper(t *testing.T) {
	switch os.Getenv(runnerWindowsHelperEnv) {
	case "":
		return
	case "probe":
		require.ErrorIs(t, os.Stdin.SetReadDeadline(time.Time{}), os.ErrNoDeadline)
		kind, err := windows.GetFileType(windows.Handle(os.Stdin.Fd()))
		require.NoError(t, err)
		require.Equal(t, uint32(windows.FILE_TYPE_PIPE), kind)
		require.NoError(t, json.NewEncoder(os.Stdout).Encode(runnerWindowsMessage{
			Kind: "capability", NoDeadline: true, GoVersion: runtime.Version(),
		}))
	case "controller":
		require.NoError(t, runRunnerWindowsController())
	default:
		t.Fatal("unknown Windows runner helper mode")
	}
}

func spawnRunnerWindowsTarget(request runnerWindowsMessage, probe bool) (*runnerWindowsProcess, error) {
	input := os.NewFile(uintptr(request.Handle), "runner-windows-transferred-input")
	if input == nil {
		return nil, errors.New("invalid transferred Windows stdin handle")
	}
	var cmd *exec.Cmd
	if probe {
		cmd = exec.Command(os.Args[0], "-test.run=^TestRunnerTerminalWindowsConsoleHelper$")
		cmd.Env = append(os.Environ(), runnerWindowsHelperEnv+"=probe")
	} else {
		cmd = exec.Command(request.Binary, "runner", request.Action)
		cmd.Env = append(os.Environ(), runnerWindowsHelperEnv+"=")
	}
	cmd.Stdin = input
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	process, startErr := startRunnerWindowsProcess(cmd, false)
	closeErr := input.Close()
	if err := errors.Join(startErr, closeErr); err != nil {
		if process != nil {
			err = errors.Join(err, process.stop())
		}
		return nil, err
	}
	return process, nil
}

func runRunnerWindowsController() (resultErr error) {
	events := json.NewEncoder(os.Stdout)
	if err := events.Encode(runnerWindowsMessage{Kind: "ready", PID: uint32(os.Getpid())}); err != nil {
		return err
	}
	requests := json.NewDecoder(os.Stdin)
	var request runnerWindowsMessage
	if err := requests.Decode(&request); err != nil {
		return err
	}
	if request.Kind != "arm" {
		return errors.New("Windows controller received no post-job-assignment ARM gate")
	}
	if err := events.Encode(runnerWindowsMessage{Kind: "armed"}); err != nil {
		return err
	}
	var target *runnerWindowsProcess
	var targetHandle windows.Handle
	probed := false
	defer func() {
		if target != nil {
			resultErr = errors.Join(resultErr, target.stop())
		}
		if targetHandle != 0 {
			resultErr = errors.Join(resultErr, windows.CloseHandle(targetHandle))
		}
	}()
	for {
		if err := requests.Decode(&request); err != nil {
			return fmt.Errorf("reading Windows controller request: %w", err)
		}
		switch request.Kind {
		case "probe":
			if target != nil {
				return errors.New("capability probe arrived after target spawn")
			}
			probe, err := spawnRunnerWindowsTarget(request, true)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = probe.wait(ctx)
			cancel()
			err = errors.Join(err, probe.stop())
			if err != nil {
				return fmt.Errorf("inherited-pipe capability probe: %w; stdout=%s; stderr=%s",
					err, probe.stdout.snapshot(), probe.stderr.snapshot())
			}
			records, err := decodeRunnerHelperSnapshot(probe.stdout.snapshot(), "capability")
			if err != nil {
				return fmt.Errorf("invalid native capability probe output: %w; stdout=%s", err, probe.stdout.snapshot())
			}
			var capability runnerWindowsMessage
			if err := json.Unmarshal(records[0], &capability); err != nil {
				return err
			}
			if capability.Kind != "capability" || !capability.NoDeadline || capability.GoVersion != runtime.Version() {
				return errors.New("native inherited-pipe capability proof is missing or uses another Go runtime")
			}
			if err := events.Encode(capability); err != nil {
				return err
			}
			probed = true
		case "start":
			if !probed || target != nil || (request.Action != "validate" && request.Action != "run") ||
				request.Binary == "" || request.StartupDeadline.IsZero() {
				return errors.New("invalid or duplicate Windows CLI start")
			}
			startup, cancelStartup := context.WithDeadline(context.Background(), request.StartupDeadline)
			if err := runnerConsoleContextError(startup); err != nil {
				cancelStartup()
				return fmt.Errorf("Windows startup budget expired before target creation: %w", err)
			}
			var err error
			target, err = spawnRunnerWindowsTarget(request, false)
			if err != nil {
				cancelStartup()
				return err
			}
			pid := uint32(target.cmd.Process.Pid)
			targetHandle, err = windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
			if err != nil {
				cancelStartup()
				return runnerWindowsStartupFailure(target, runnerConsoleReadiness{TargetPID: pid}, err)
			}
			report, readyErr := waitRunnerConsoleReady(startup, pid, func() (runnerConsoleSample, error) {
				return runnerWindowsConsoleObservation(targetHandle)
			})
			if readyErr == nil {
				readyErr = runnerConsoleContextError(startup)
			}
			if readyErr == nil {
				if err := runnerConsoleContextError(startup); err != nil {
					cancelStartup()
					return runnerWindowsStartupFailure(target, report, err)
				}
				final, finalErr := runnerWindowsConsoleObservation(targetHandle)
				report.record(final)
				if finalErr != nil {
					readyErr = finalErr
				} else if !final.Alive {
					readyErr = errRunnerConsoleExited
				} else {
					found := false
					for _, member := range final.PIDs {
						found = found || member == pid
					}
					if !found {
						readyErr = errors.New("target lost shared-console membership before started")
					}
				}
				readyErr = errors.Join(readyErr, runnerConsoleContextError(startup))
			}
			if readyErr != nil {
				cancelStartup()
				return runnerWindowsStartupFailure(target, report, readyErr)
			}
			if err := runnerConsoleContextError(startup); err != nil {
				cancelStartup()
				return runnerWindowsStartupFailure(target, report, err)
			}
			err = events.Encode(runnerWindowsMessage{Kind: "started", PID: pid, Readiness: &report})
			cancelStartup()
			if err != nil {
				return err
			}
		case "cancel":
			if target == nil || request.PID == 0 || request.PID != uint32(target.cmd.Process.Pid) {
				return errors.New("Windows cancellation did not name the exact owned target group")
			}
			state, err := windows.WaitForSingleObject(targetHandle, 0)
			if err != nil {
				return err
			}
			if state != uint32(windows.WAIT_TIMEOUT) || len(target.stdout.snapshot()) != 0 {
				return errors.New("Windows target completed or emitted output before input cancellation")
			}
			if err := runnerWindowsConsoleContains(request.PID); err != nil {
				return err
			}
			if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, request.PID); err != nil {
				return fmt.Errorf("sending targeted CTRL_BREAK: %w", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			waitErr := target.wait(ctx)
			cancel()
			exit, ok := errors.AsType[*exec.ExitError](waitErr)
			if !ok {
				return fmt.Errorf("expected CLI cancellation exit after CTRL_BREAK, got %v", waitErr)
			}
			if err := events.Encode(runnerWindowsMessage{
				Kind: "result", PID: request.PID, Code: exit.ExitCode(),
				Stdout: target.stdout.snapshot(), Stderr: target.stderr.snapshot(),
			}); err != nil {
				return err
			}
		case "finish":
			if target == nil {
				return errors.New("Windows controller finished without a target")
			}
			select {
			case <-target.done:
			default:
				return errors.New("Windows controller finished with a live target")
			}
			return events.Encode(runnerWindowsMessage{Kind: "finished"})
		default:
			return fmt.Errorf("unknown Windows controller request %q", request.Kind)
		}
	}
}
