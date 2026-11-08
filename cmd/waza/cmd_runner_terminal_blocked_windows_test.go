package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

const (
	runnerTerminalTimingPrefix = "waza-runner-terminal-timing "
)

type runnerTerminalTimingEvent struct {
	phase       string
	at          time.Time
	emitElapsed time.Duration
}

func runnerTerminalTimingOverlay(source []byte, nonce string) ([]byte, error) {
	canonical := bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))
	if fmt.Sprintf("%x", sha256.Sum256(canonical)) != runnerReadProgressOriginalSHA256 ||
		(!bytes.Equal(source, canonical) && !bytes.Equal(source, bytes.ReplaceAll(canonical, []byte("\n"), []byte("\r\n")))) {
		return nil, fmt.Errorf("terminal timing source changed; review and rebind the test-only overlay (raw_sha256=%x canonical_lf_sha256=%x)",
			sha256.Sum256(source), sha256.Sum256(canonical))
	}
	decoded, err := hex.DecodeString(nonce)
	if err != nil || len(decoded) != 16 || len(nonce) != 32 || nonce != strings.ToLower(nonce) {
		return nil, errors.New("terminal timing overlay requires a 32-character lowercase hex nonce")
	}

	signature := "func emitInheritedRunnerTerminal(envelope runnerterminal.Envelope, binding *runnerterminal.Binding) (int, error) {"
	start := signature + fmt.Sprintf(`
	emitStarted := time.Now()
	fmt.Fprintf(os.Stderr, %q, time.Now().UnixNano())`,
		runnerTerminalTimingPrefix+"nonce="+nonce+" phase=emit-start at_unix_nano=%d\n")
	timeout := `	case <-timer.C:
		return 2, fmt.Errorf("writing terminal output: %w", os.ErrDeadlineExceeded)`
	timedTimeout := fmt.Sprintf(`	case <-timer.C:
		now := time.Now()
		fmt.Fprintf(os.Stderr, %q, now.UnixNano(), now.Sub(emitStarted).Nanoseconds())
		return 2, fmt.Errorf("writing terminal output: %%w", os.ErrDeadlineExceeded)`,
		runnerTerminalTimingPrefix+"nonce="+nonce+" phase=emit-timeout at_unix_nano=%d emit_elapsed_ns=%d\n")
	if bytes.Count(canonical, []byte(signature)) != 1 || bytes.Count(canonical, []byte(timeout)) != 1 {
		return nil, errors.New("unexpected inherited terminal emitter shape")
	}
	overlay := bytes.Replace(canonical, []byte(signature), []byte(start), 1)
	overlay = bytes.Replace(overlay, []byte(timeout), []byte(timedTimeout), 1)

	restored := bytes.Replace(overlay, []byte(timedTimeout), []byte(timeout), 1)
	restored = bytes.Replace(restored, []byte(start), []byte(signature), 1)
	if !bytes.Equal(restored, canonical) {
		return nil, errors.New("terminal timing overlay cannot restore the complete original source")
	}
	return overlay, nil
}

func buildRunnerTerminalTimingCLI(t *testing.T) (string, string) {
	t.Helper()
	source, err := os.ReadFile("runner_stdin_windows.go")
	require.NoError(t, err)
	var random [16]byte
	_, err = rand.Read(random[:])
	require.NoError(t, err)
	nonce := hex.EncodeToString(random[:])
	overlay, err := runnerTerminalTimingOverlay(source, nonce)
	require.NoError(t, err)

	directory := t.TempDir()
	replacement := filepath.Join(directory, "runner_stdin_windows.go")
	require.NoError(t, os.WriteFile(replacement, overlay, 0o600))
	original, err := filepath.Abs("runner_stdin_windows.go")
	require.NoError(t, err)
	manifest, err := json.Marshal(struct {
		Replace map[string]string
	}{Replace: map[string]string{original: replacement}})
	require.NoError(t, err)
	manifestPath := filepath.Join(directory, "overlay.json")
	require.NoError(t, os.WriteFile(manifestPath, manifest, 0o600))
	binary := filepath.Join(directory, "waza.exe")
	args := []string{"build", "-overlay=" + manifestPath, "-o", binary, "."}
	started := time.Now()
	output, err := exec.CommandContext(t.Context(), "go", args...).CombinedOutput()
	require.NoError(t, err, "%s", output)
	t.Logf("source-bound production CLI timing build: elapsed=%s original_sha256=%x overlay_sha256=%x",
		time.Since(started), sha256.Sum256(source), sha256.Sum256(overlay))
	return binary, nonce
}

func parseRunnerTerminalTiming(stderr, nonce string) ([]runnerTerminalTimingEvent, error) {
	var events []runnerTerminalTimingEvent
	for line := range strings.SplitSeq(stderr, "\n") {
		if !strings.HasPrefix(line, runnerTerminalTimingPrefix) {
			continue
		}
		values := make(map[string]string)
		for _, field := range strings.Fields(strings.TrimPrefix(line, runnerTerminalTimingPrefix)) {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				return nil, fmt.Errorf("malformed terminal timing field %q", field)
			}
			values[key] = value
		}
		if values["nonce"] != nonce {
			return nil, fmt.Errorf("terminal timing nonce %q does not match %q", values["nonce"], nonce)
		}
		at, err := strconv.ParseInt(values["at_unix_nano"], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parsing terminal timing clock: %w", err)
		}
		event := runnerTerminalTimingEvent{phase: values["phase"], at: time.Unix(0, at)}
		if elapsed := values["emit_elapsed_ns"]; elapsed != "" {
			nanos, err := strconv.ParseInt(elapsed, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parsing terminal emission duration: %w", err)
			}
			event.emitElapsed = time.Duration(nanos)
		}
		events = append(events, event)
	}
	return events, nil
}

func TestRunnerTerminalTimingOverlay(t *testing.T) {
	source, err := os.ReadFile("runner_stdin_windows.go")
	require.NoError(t, err)
	nonce := strings.Repeat("a", 32)
	overlay, err := runnerTerminalTimingOverlay(source, nonce)
	require.NoError(t, err)
	require.Contains(t, string(overlay), runnerTerminalTimingPrefix+"nonce="+nonce+" phase=emit-start")
	require.Contains(t, string(overlay), runnerTerminalTimingPrefix+"nonce="+nonce+" phase=emit-timeout")
	require.NotEqual(t, source, overlay)

	crlf := bytes.ReplaceAll(bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
	_, err = runnerTerminalTimingOverlay(crlf, nonce)
	require.NoError(t, err, "uniform CRLF is an alternative valid checkout")

	changed := bytes.Replace(source, []byte("5 * time.Second"), []byte("6 * time.Second"), 1)
	_, err = runnerTerminalTimingOverlay(changed, nonce)
	require.ErrorContains(t, err, "source changed")
	_, err = runnerTerminalTimingOverlay(source, strings.Repeat("g", 32))
	require.ErrorContains(t, err, "lowercase hex nonce")
}

func TestRunnerTerminalWindowsFullUndrainedStdoutPipe(t *testing.T) {
	binary, nonce := buildRunnerTerminalTimingCLI(t)
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	var read, write windows.Handle
	require.NoError(t, windows.CreatePipe(&read, &write, nil, 4096))
	reader := os.NewFile(uintptr(read), "runner-terminal-blocked-reader")
	childOutput := os.NewFile(uintptr(write), "runner-terminal-blocked-stdout")
	require.NotNil(t, reader)
	require.NotNil(t, childOutput)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	t.Cleanup(func() { _ = childOutput.Close() })
	filled := fillRunnerWindowsStdout(t, read, write)
	mode := uint32(windows.PIPE_WAIT)
	require.NoError(t, windows.SetNamedPipeHandleState(write, &mode, nil, nil))
	require.Equal(t, mode, runnerWindowsPipeState(t, write)&windows.PIPE_NOWAIT)

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "runner", "run")
	command.Dir = t.TempDir()
	command.Stdin = bytes.NewReader(request)
	command.Stdout = childOutput
	var stderr bytes.Buffer
	command.Stderr = &stderr
	started := time.Now()
	require.NoError(t, command.Start())
	startReturned := time.Now()
	waitErr := command.Wait()
	finished := time.Now()
	require.NoError(t, ctx.Err(), "watchdog cleanup is not a natural exit: %v; %s", waitErr, stderr.String())

	t.Run("exit", func(t *testing.T) {
		exit, ok := errors.AsType[*exec.ExitError](waitErr)
		require.True(t, ok, "%v: %s", waitErr, stderr.String())
		require.Equal(t, 2, exit.ExitCode(), "stderr: %s", stderr.String())
	})
	t.Run("phase timing", func(t *testing.T) {
		events, err := parseRunnerTerminalTiming(stderr.String(), nonce)
		require.NoError(t, err)
		require.Len(t, events, 2)
		require.Equal(t, "emit-start", events[0].phase)
		require.Equal(t, "emit-timeout", events[1].phase)
		require.False(t, events[0].at.Before(started))
		require.False(t, events[1].at.Before(events[0].at))
		require.False(t, finished.Before(events[1].at))
		require.GreaterOrEqual(t, events[1].emitElapsed, 5*time.Second,
			"the production emission allowance must remain five seconds")
		require.GreaterOrEqual(t, events[1].at.Sub(events[0].at), 5*time.Second)
		t.Logf("native production-caller phases: start_call=%s pre_emission=%s emission=%s post_emission_exit_wait=%s total=%s",
			startReturned.Sub(started), events[0].at.Sub(started), events[1].emitElapsed,
			finished.Sub(events[1].at), finished.Sub(started))
	})
	t.Run("stderr", func(t *testing.T) {
		require.Contains(t, stderr.String(), "i/o timeout")
	})
	t.Run("pipe occupancy", func(t *testing.T) {
		require.Equal(t, uint32(len(filled)), runnerWindowsPipeOccupancy(t, read),
			"the full pipe must remain undrained through natural process exit")
	})
	t.Run("pipe mode", func(t *testing.T) {
		require.Equal(t, mode, runnerWindowsPipeState(t, write)&windows.PIPE_NOWAIT)
	})
	t.Run("no fallback", func(t *testing.T) {
		require.NoError(t, childOutput.Close())
		output, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.Equal(t, filled, output, "failed delivery must not append a fallback")
	})
}
