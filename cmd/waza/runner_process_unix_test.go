//go:build !windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/stretchr/testify/require"
)

type runnerSignalReader struct {
	ctx   context.Context
	ready bool
}

func (*runnerSignalReader) Close() error { return os.Stdin.Close() }

func (r *runnerSignalReader) Read(p []byte) (int, error) {
	if !r.ready {
		r.ready = true
		fmt.Fprintln(os.Stderr, "runner-reader-ready")
		<-r.ctx.Done()
	}
	return os.Stdin.Read(p)
}

func TestRunnerSignalProcessHelper(t *testing.T) {
	if os.Getenv("WAZA_RUNNER_SIGNAL_HELPER") != "1" {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	cmd := newRootCommand()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"runner", "run"})
	cmd.SetIn(&runnerSignalReader{ctx: ctx})
	err := cmd.Execute()
	if exit, ok := err.(*ExitCodeError); ok {
		os.Exit(exit.Code)
	}
	os.Exit(2)
}

func TestRunnerGracefulAndAbruptProcessTermination(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, mode := range []string{"graceful", "abrupt"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestRunnerSignalProcessHelper$")
			cmd.Dir = t.TempDir()
			cmd.Env = append(runnerProcessEnvironment(), "WAZA_RUNNER_SIGNAL_HELPER=1")
			stdin, err := cmd.StdinPipe()
			require.NoError(t, err)
			stdout, err := cmd.StdoutPipe()
			require.NoError(t, err)
			stderr, err := cmd.StderrPipe()
			require.NoError(t, err)
			require.NoError(t, cmd.Start())
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			barrier, err := bufio.NewReader(stderr).ReadString('\n')
			require.NoError(t, err)
			require.Equal(t, "runner-reader-ready\n", barrier)
			if mode == "graceful" {
				require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
			} else {
				require.NoError(t, cmd.Process.Kill())
			}
			require.NoError(t, stdin.Close())
			data, err := io.ReadAll(stdout)
			require.NoError(t, err)
			err = cmd.Wait()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			if mode == "graceful" {
				require.Equal(t, 3, exit.ExitCode())
				result, err := runnercontract.ParseResult(data)
				require.NoError(t, err)
				require.Equal(t, runnercontract.Canceled, result.Outcome)
				require.Nil(t, result.Observation)
			} else {
				require.Empty(t, data, "SIGKILL cannot produce a terminal result; do not fabricate one")
			}
		})
	}
}
