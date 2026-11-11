package main

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestCheckpointProductionProcessRecovery(t *testing.T) {
	binary := buildCheckpointRecoveryCLI(t)
	info, err := buildinfo.ReadFile(binary)
	require.NoError(t, err)
	require.Equal(t, "github.com/microsoft/waza/cmd/waza", info.Path)
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	require.Equal(t, "false", settings["vcs.modified"], "the test-owned source snapshot must have genuine clean VCS provenance")
	require.Len(t, settings["vcs.revision"], 40)

	for _, action := range []string{"create", "resume"} {
		t.Run(action+"-flag-diagnostics", func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "must-not-open")
			args := []string{"runner", "checkpoint", action}
			if action == "resume" {
				args = append(args, strings.Repeat("e", 64))
			}
			const flag = "private-unknown-flag-control"
			args = append(args, "--store", directory, "--"+flag)
			command := exec.CommandContext(t.Context(), binary, args...)
			command.Stdin = bytes.NewReader(checkpointCLIWire(t))
			var output, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &output, &diagnostic
			err := command.Run()
			exit, ok := errors.AsType[*exec.ExitError](err)
			require.True(t, ok, "%v", err)
			require.Equal(t, 6, exit.ExitCode())
			result, err := runnercheckpoint.ParseResult(output.Bytes())
			require.NoError(t, err, "%s", output.Bytes())
			require.Equal(t, exit.ExitCode(), result.ExitCode)
			require.Equal(t, "checkpoint_flags_invalid", result.Diagnostics[0].Code)
			require.Equal(t, 1, bytes.Count(output.Bytes(), []byte{'\n'}))
			require.NotContains(t, output.String(), flag)
			require.Contains(t, diagnostic.String(), "checkpoint flags: unknown flag: --"+flag)
			require.NoDirExists(t, directory)
		})
	}

	for _, stage := range []string{"read", "admission"} {
		t.Run("stdin-"+stage+"-diagnostics", func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "must-not-open")
			command := exec.CommandContext(t.Context(), binary, "runner", "checkpoint", "create", "--store", directory)
			code, cause := "checkpoint_input_invalid", "parsing execution submission: invalid JSON object\n"
			command.Stdin = bytes.NewReader([]byte("{"))
			if stage == "read" {
				input, err := os.Open(t.TempDir())
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, input.Close()) })
				command.Stdin = input
				code, cause = "checkpoint_input_unavailable", "is a directory"
			}
			var output, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &output, &diagnostic
			err := command.Run()
			exit, ok := errors.AsType[*exec.ExitError](err)
			require.True(t, ok, "%v", err)
			result, err := runnercheckpoint.ParseResult(output.Bytes())
			require.NoError(t, err, "%s", output.Bytes())
			require.Equal(t, 6, exit.ExitCode())
			require.Equal(t, exit.ExitCode(), result.ExitCode)
			require.Equal(t, code, result.Diagnostics[0].Code)
			require.Equal(t, 1, bytes.Count(output.Bytes(), []byte{'\n'}))
			require.Contains(t, diagnostic.String(), "checkpoint input "+stage+":")
			require.Contains(t, diagnostic.String(), cause)
			require.NotContains(t, output.String(), cause)
			require.NoDirExists(t, directory)
		})
	}

	t.Run("unblocked-create", func(t *testing.T) {
		directory := t.TempDir()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, binary, "runner", "checkpoint", "create", "--store", directory)
		child.Stdin = bytes.NewReader(checkpointCLIWire(t))
		var output, diagnostic bytes.Buffer
		child.Stdout, child.Stderr = &output, &diagnostic
		require.NoError(t, child.Run(), "%s", diagnostic.String())
		result, err := runnercheckpoint.ParseResult(output.Bytes())
		require.NoError(t, err)
		require.Equal(t, "completed", result.Status)
		require.Zero(t, result.ExitCode)
		require.Equal(t, 1, result.Submission.EngineCalls)
		require.NotNil(t, result.Submission.Completion)
		require.NotEmpty(t, result.BeforeDigest)
		require.NotEmpty(t, result.FinalizedDigest)
	})

	for _, control := range []struct {
		name, boundary string
		failedChecks   bool
	}{
		{"before-dispatch", executioncheckpoint.BeforeUnit, false},
		{"finalized-success", executioncheckpoint.FinalizedUnit, false},
		{"finalized-failed-checks", executioncheckpoint.FinalizedUnit, true},
	} {
		t.Run(control.name, func(t *testing.T) {
			directory := t.TempDir()
			wire := checkpointCLIWire(t)
			if control.failedChecks {
				changed := bytes.Replace(wire, []byte("- hello"), []byte("- missing-process-control"), 1)
				require.NotEqual(t, wire, changed)
				wire = changed
			}
			digest, source := killCheckpointProcessAtBoundary(t, binary, directory, wire, control.boundary)
			require.Equal(t, settings["vcs.revision"], source.Compatibility.RunnerRevision)
			original, err := os.ReadFile(filepath.Join(directory, "blob-"+digest))
			require.NoError(t, err)
			entries, err := os.ReadDir(directory)
			require.NoError(t, err)
			retained := make(map[string][]byte, len(entries))
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
				require.NoError(t, err)
				retained[entry.Name()] = data
			}
			require.NotEmpty(t, retained)
			backend, err := artifactmanifest.OpenLocal(directory)
			require.NoError(t, err)
			defer backend.Close() //nolint:errcheck
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			var terminal []byte
			if control.boundary == executioncheckpoint.BeforeUnit {
				_, err := executioncheckpoint.ReadBefore(t.Context(), ledger, *source, source.Compatibility)
				require.NoError(t, err, "joined original must have no dispatch or completion")
			} else {
				completed, err := executioncheckpoint.ReadStoredFinalized(t.Context(), ledger, *source, source.Compatibility)
				require.NoError(t, err)
				terminal = completed.Publication.ArtifactBytes[completed.Publication.Manifest.TerminalArtifact]
				require.NotEmpty(t, terminal)
			}

			destination := bytes.Replace(wire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
			require.NotEqual(t, wire, destination)
			request, err := runnerunit.ParseSubmission(destination)
			require.NoError(t, err)
			caller, err := runneridentity.DeriveJSON(request.Request)
			require.NoError(t, err)
			code := 0
			if control.failedChecks {
				code = 1
			}
			result := resumeCheckpointProcess(t, binary, directory, destination, digest, code)
			require.Equal(t, "compatible", result.CompatibilityResult)
			require.NotNil(t, result.Source)
			require.Equal(t, source.ExecutionID, result.Source.ExecutionID)
			require.NotNil(t, result.Submission)
			require.Equal(t, caller, result.Submission.Caller)
			require.Empty(t, result.Submission.Report)
			require.NotNil(t, result.Submission.Terminal)
			require.NotNil(t, result.RemainingWork)
			require.Zero(t, *result.RemainingWork)
			if control.boundary == executioncheckpoint.BeforeUnit {
				require.Equal(t, "completed", result.Status)
				require.Equal(t, 1, result.Submission.EngineCalls)
				require.NotEqual(t, source.ExecutionID, result.Submission.Reservation.ExecutionID)
				require.NotNil(t, result.Submission.Reservation.Parent)
				require.Equal(t, source.ExecutionID, result.Submission.Reservation.Parent.ExecutionID)
				require.Equal(t, "retry", result.Submission.Reservation.Parent.Relationship)
				require.NotNil(t, result.RestoredUnits)
				require.Zero(t, *result.RestoredUnits)
			} else {
				require.Equal(t, "reused", result.Status)
				require.Zero(t, result.Submission.EngineCalls, "fresh compiled child must not execute finalized work")
				require.Equal(t, source.ExecutionID, result.Submission.Reservation.ExecutionID)
				require.Equal(t, 2, result.Submission.Terminal.Selection.Attempt)
				require.NotNil(t, result.RestoredUnits)
				require.Equal(t, 1, *result.RestoredUnits)
				actual, err := runnerterminal.Encode(*result.Submission.Terminal, result.Submission.Reservation.Binding.TerminalBinding())
				require.NoError(t, err)
				require.Equal(t, terminal, actual, "restore must preserve exact original terminal bytes and failure")
			}
			preserved, err := os.ReadFile(filepath.Join(directory, "blob-"+digest))
			require.NoError(t, err)
			require.Equal(t, original, preserved)
			for name, expected := range retained {
				actual, err := os.ReadFile(filepath.Join(directory, name))
				require.NoError(t, err)
				require.Equal(t, expected, actual, "fresh child must preserve original store record %s", name)
			}
		})
	}

	t.Run("dispatched-without-completion", func(t *testing.T) {
		directory := t.TempDir()
		wire := checkpointCLIWire(t)
		backend, err := artifactmanifest.OpenLocal(directory)
		require.NoError(t, err)
		defer backend.Close() //nolint:errcheck
		checkpoints, err := runnercheckpoint.NewService(runnerunit.NewService(nil), settings["vcs.revision"])
		require.NoError(t, err)
		failure := errors.New("stop publication after actual dispatch and terminal observation")
		created, err := checkpoints.SubmitCaptured(t.Context(), wire, backend, runnercheckpoint.Hooks{
			Submission: runnerunit.SubmissionHooks{Terminal: func(context.Context, *executionledger.Ledger,
				executionledger.Creator, executionledger.Reservation, runnerterminal.Envelope,
			) error {
				return failure
			}},
		})
		require.ErrorIs(t, err, failure)
		require.NotEmpty(t, created.BeforeDigest)
		require.Equal(t, 1, created.Submission.EngineCalls)
		require.Nil(t, created.Submission.Completion)
		ledger, err := executionledger.New(backend)
		require.NoError(t, err)
		dispatched, err := ledger.HasDistinctDecision(t.Context(), created.Submission.Reservation.ExecutionID)
		require.NoError(t, err)
		require.True(t, dispatched)
		before, err := os.ReadDir(directory)
		require.NoError(t, err)
		result := resumeCheckpointProcess(t, binary, directory, wire, created.BeforeDigest, 6)
		require.Equal(t, "rejected", result.Status)
		require.Nil(t, result.Submission)
		require.True(t, result.SourceWithheld)
		require.Nil(t, result.Source)
		require.Nil(t, result.RestoredUnits)
		require.Nil(t, result.RemainingWork)
		after, err := os.ReadDir(directory)
		require.NoError(t, err)
		require.Len(t, after, len(before), "stale restore must not create another reservation or decision")
	})
}

func buildCheckpointRecoveryCLI(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	snapshot := checkpointSourceSnapshot(t, root)
	return buildRecoveryCLIFromDirectory(t, filepath.Join(snapshot, "cmd", "waza"))
}

func killCheckpointProcessAtBoundary(t *testing.T, binary, directory string, wire []byte, boundary string) (string, *executioncheckpoint.Checkpoint) {
	t.Helper()
	// Trace the unchanged binary's syscalls, not its bounded diagnostic writer.
	// The publishing thread cannot return from the immutable link syscall until
	// this tracer resumes it, so no wall-clock diagnostic timeout is a barrier.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "runner", "checkpoint", "create", "--store", directory)
	child.SysProcAttr = &syscall.SysProcAttr{Ptrace: true, Setpgid: true}
	child.Stdin = bytes.NewReader(wire)
	var output, diagnostic bytes.Buffer
	child.Stdout, child.Stderr = &output, &diagnostic
	require.NoError(t, child.Start())
	reap := func() (syscall.WaitStatus, error) {
		var final syscall.WaitStatus
		for {
			var status syscall.WaitStatus
			pid, err := syscall.Wait4(-child.Process.Pid, &status, unix.WALL, nil)
			if errors.Is(err, syscall.ECHILD) {
				return final, nil
			}
			if err != nil {
				return final, err
			}
			if pid == child.Process.Pid && (status.Exited() || status.Signaled()) {
				final = status
			}
		}
	}
	joined := false
	defer func() {
		if !joined {
			_ = child.Process.Kill()
			_, _ = reap()
			_ = child.Wait()
		}
	}()
	var initial syscall.WaitStatus
	_, err := syscall.Wait4(child.Process.Pid, &initial, 0, nil)
	require.NoError(t, err)
	require.True(t, initial.Stopped())
	require.NoError(t, syscall.PtraceSetOptions(child.Process.Pid, unix.PTRACE_O_TRACESYSGOOD|unix.PTRACE_O_TRACECLONE))
	require.NoError(t, syscall.PtraceSyscall(child.Process.Pid, 0))
	var digest string
	var source *executioncheckpoint.Checkpoint
	for source == nil {
		var status syscall.WaitStatus
		tid, err := syscall.Wait4(-child.Process.Pid, &status, unix.WALL, nil)
		require.NoError(t, err)
		require.NoError(t, ctx.Err())
		if !status.Stopped() {
			require.NotEqual(t, child.Process.Pid, tid, "CLI exited before the requested boundary")
			continue
		}
		entries, err := os.ReadDir(directory)
		require.NoError(t, err)
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "blob-") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
			if err != nil || !bytes.Contains(data, []byte(`"kind":"waza.execution-checkpoint"`)) {
				continue
			}
			checkpoint, err := executioncheckpoint.Parse(data)
			if err == nil && checkpoint.Boundary == boundary {
				digest, source = artifactmanifest.Hash(data), checkpoint
			}
		}
		if source != nil {
			break
		}
		signal := int(status.StopSignal())
		if signal == int(syscall.SIGTRAP) || signal == int(syscall.SIGTRAP)|0x80 || signal == int(syscall.SIGSTOP) {
			signal = 0
		}
		require.NoError(t, syscall.PtraceSyscall(tid, signal))
	}
	backend, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	if boundary == executioncheckpoint.BeforeUnit {
		_, err = executioncheckpoint.ReadBefore(ctx, ledger, *source, source.Compatibility)
		require.NoError(t, err, "live stopped publisher must retain reserved/undispatched/incomplete source")
	} else {
		completed, readErr := executioncheckpoint.ReadStoredFinalized(ctx, ledger, *source, source.Compatibility)
		require.NoError(t, readErr, "live stopped publisher must already retain verified finalized evidence")
		require.NotEmpty(t, completed.Publication.ArtifactBytes[completed.Publication.Manifest.TerminalArtifact])
	}
	require.NoError(t, backend.Close())
	require.NoError(t, child.Process.Kill())
	status, err := reap()
	require.NoError(t, err)
	waitErr := child.Wait() // Wait4 reaped the process; Wait still joins owned output-copy goroutines.
	joined = true
	require.ErrorIs(t, waitErr, syscall.ECHILD)
	require.Equal(t, syscall.SIGKILL, status.Signal())
	require.Empty(t, output.Bytes(), "killed operation must not have delivered a terminal record")
	if boundary == executioncheckpoint.BeforeUnit {
		require.Empty(t, diagnostic.Bytes())
	} else {
		checkpointDigest := checkpointDigestAtBoundary(t, directory, executioncheckpoint.BeforeUnit)
		require.Equal(t, fmt.Sprintf("checkpoint %s %s\n", executioncheckpoint.BeforeUnit, checkpointDigest), diagnostic.String())
	}
	return digest, source
}

func checkpointDigestAtBoundary(t *testing.T, directory, boundary string) string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "blob-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		require.NoError(t, err)
		checkpoint, err := executioncheckpoint.Parse(data)
		if err == nil && checkpoint.Boundary == boundary {
			return artifactmanifest.Hash(data)
		}
	}
	t.Fatalf("no immutable %s checkpoint", boundary)
	return ""
}

func resumeCheckpointProcess(t *testing.T, binary, directory string, wire []byte, digest string, code int) runnercheckpoint.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "runner", "checkpoint", "resume", digest, "--store", directory)
	child.Stdin = bytes.NewReader(wire)
	var output, diagnostic bytes.Buffer
	child.Stdout, child.Stderr = &output, &diagnostic
	err := child.Run()
	if code == 0 {
		require.NoError(t, err, "%s", diagnostic.String())
	} else {
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit, "%s", diagnostic.String())
		require.Equal(t, code, exit.ExitCode(), "%s", diagnostic.String())
	}
	result, err := runnercheckpoint.ParseResult(output.Bytes())
	require.NoError(t, err, "%s", output.Bytes())
	require.Equal(t, code, result.ExitCode)
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte{'\n'}))
	return *result
}
