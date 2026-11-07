//go:build unix

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// The production checkpoint command requires an actually clean VCS-stamped
// binary. Execute this control after committing the tested source, not an overlay.
func TestCheckpointLocatorRealProcessRetainsUndrainedStderrAndNaturalExit(t *testing.T) {
	binary := buildRecoveryCLI(t)
	wire := checkpointCLIWire(t)
	for _, control := range []string{"full", "full-nonblocking", "broken", "writable"} {
		t.Run(control, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			defer reader.Close() //nolint:errcheck
			defer writer.Close() //nolint:errcheck
			peer := duplicateRunnerFD(t, writer)
			defer peer.Close() //nolint:errcheck
			var filled []byte
			if strings.HasPrefix(control, "full") {
				filled = fillProcessDiagnosticPipe(t, writer)
				if control == "full-nonblocking" {
					require.NoError(t, unix.SetNonblock(int(peer.Fd()), true))
				}
			}
			flags := runnerFDFlags(t, peer)
			if control == "broken" {
				require.NoError(t, reader.Close())
			}
			child := exec.Command(binary, "runner", "checkpoint", "create", "--store", t.TempDir())
			child.Dir = t.TempDir()
			child.Stdin = bytes.NewReader(wire)
			child.WaitDelay = time.Second
			var output, diagnostics bytes.Buffer
			child.Stdout, child.Stderr = &output, peer
			if control == "writable" {
				child.Stderr = &diagnostics
			}
			start := time.Now()
			require.NoError(t, child.Start())
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			joined := false
			t.Cleanup(func() {
				if !joined {
					if err := child.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
						t.Errorf("failure-only process cleanup: %v", err)
					}
					<-done
				}
			})
			var waitErr error
			select {
			case waitErr = <-done:
				joined = true
			case <-time.After(10 * time.Second):
				t.Fatal("checkpoint must exit naturally while inherited stderr remains full/undrained")
			}
			require.Less(t, time.Since(start), 10*time.Second)
			result, err := runnercheckpoint.ParseResult(output.Bytes())
			require.NoError(t, err, "%s; %s", output.Bytes(), diagnostics.String())
			require.NotEmpty(t, result.BeforeDigest, "actual owning revision/admission must reach published checkpoint")
			require.Equal(t, 1, bytes.Count(output.Bytes(), []byte{'\n'}))
			require.Equal(t, flags, runnerFDFlags(t, peer), "inherited peer flags must remain unchanged")
			require.Equal(t, flags, runnerFDFlags(t, writer), "caller handle must remain open and unchanged")
			if control == "writable" {
				require.NoError(t, waitErr)
				require.Zero(t, result.ExitCode)
				require.NotNil(t, result.Submission.Completion)
				require.Equal(t, "checkpoint before-unit "+result.BeforeDigest+"\ncheckpoint finalized-unit "+result.FinalizedDigest+"\n",
					diagnostics.String())
			} else {
				exit, ok := errors.AsType[*exec.ExitError](waitErr)
				require.True(t, ok, "%v", waitErr)
				require.Equal(t, 2, exit.ExitCode())
				require.Equal(t, 2, result.ExitCode)
				require.Zero(t, result.Submission.EngineCalls)
				require.Nil(t, result.Submission.Completion)
				require.Nil(t, result.Submission.Terminal)
			}
			if strings.HasPrefix(control, "full") {
				require.NoError(t, writer.Close())
				require.NoError(t, peer.Close())
				data, err := io.ReadAll(reader)
				require.NoError(t, err)
				require.Equal(t, filled, data, "exit must need no drain, fallback, retry or late locator")
			}
		})
	}
}
