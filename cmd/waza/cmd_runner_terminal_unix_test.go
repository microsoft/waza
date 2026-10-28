//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRunnerTerminalRealProcessSignalsWithOpenStdin(t *testing.T) {
	binary := buildRecoveryCLI(t)
	for _, signal := range []os.Signal{syscall.SIGTERM, os.Interrupt, syscall.SIGKILL} {
		t.Run(signal.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "runner", "run")
			child.Dir = t.TempDir()
			input, err := child.StdinPipe()
			require.NoError(t, err)
			defer input.Close() //nolint:errcheck
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			require.NoError(t, child.Start())
			t.Cleanup(func() {
				if child.ProcessState == nil {
					require.NoError(t, child.Process.Kill())
					_ = child.Wait()
				}
			})
			// More than the native pipe capacity, below request size bounds:
			// completed write proves the real command has entered its read.
			_, err = input.Write(append(bytes.Repeat([]byte(" "), 2<<20), '{'))
			require.NoError(t, err)
			require.NoError(t, child.Process.Signal(signal))
			err = child.Wait()
			exit, ok := errors.AsType[*exec.ExitError](err)
			require.True(t, ok, "%v: %s", err, stderr.String())
			if signal == syscall.SIGKILL {
				require.Equal(t, -1, exit.ExitCode())
				require.Empty(t, stdout.String(), "uncatchable termination cannot produce a terminal result")
				return
			}
			require.Equal(t, 3, exit.ExitCode(), "%s", stderr.String())
			require.Equal(t, 1, bytes.Count(stdout.Bytes(), []byte{'\n'}))
			envelope, err := runnerterminal.Validate(stdout.Bytes(), nil)
			require.NoError(t, err)
			require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
			require.Contains(t, stdout.String(), `"failure_boundary":"input"`)
			require.NotContains(t, stdout.String(), `"identity"`)
			require.Contains(t, stderr.String(), "runner run: "+string(runnerterminal.Canceled))
		})
	}
}

func TestRunnerTerminalRealProcessOutputFailure(t *testing.T) {
	binary := buildRecoveryCLI(t)
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	for _, control := range []string{"broken", "blocked"} {
		t.Run(control, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			defer reader.Close() //nolint:errcheck
			defer writer.Close() //nolint:errcheck
			var filled []byte
			if control == "broken" {
				require.NoError(t, reader.Close())
			} else {
				fd := int(writer.Fd())
				require.NoError(t, unix.SetNonblock(fd, true))
				for {
					n, writeErr := unix.Write(fd, bytes.Repeat([]byte("x"), 4096))
					if writeErr != nil {
						require.ErrorIs(t, writeErr, unix.EAGAIN)
						break
					}
					filled = append(filled, bytes.Repeat([]byte("x"), n)...)
				}
				require.NotEmpty(t, filled, "the native output pipe must really be full")
				require.NoError(t, unix.SetNonblock(fd, false))
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "runner", "run")
			child.Dir = t.TempDir()
			child.Stdin, child.Stdout = bytes.NewReader(request), writer
			var stderr bytes.Buffer
			child.Stderr = &stderr
			require.NoError(t, child.Start())
			require.NoError(t, writer.Close())
			exit, ok := errors.AsType[*exec.ExitError](child.Wait())
			require.True(t, ok, "%s", stderr.String())
			require.Equal(t, 2, exit.ExitCode(), "%s", stderr.String())
			require.NoError(t, ctx.Err(), "output failure must return before forced termination")
			if control == "blocked" {
				output, err := io.ReadAll(reader)
				require.NoError(t, err)
				require.Equal(t, filled, output, "no terminal fallback may be appended after failed delivery")
				require.Contains(t, stderr.String(), "i/o timeout")
			} else {
				require.Contains(t, stderr.String(), "broken pipe")
			}
		})
	}
}
