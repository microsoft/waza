package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestRunnerCheckpointLocatorExactFramingAndInputAdmission(t *testing.T) {
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&output)
	emit := newRunnerCheckpointLocatorEmitter(cmd)
	digest := strings.Repeat("a", 64)
	for _, boundary := range []string{executioncheckpoint.BeforeUnit, executioncheckpoint.FinalizedUnit} {
		output.Reset()
		require.NoError(t, emit(boundary, digest))
		require.Equal(t, "checkpoint "+boundary+" "+digest+"\n", output.String())
	}
	for _, control := range []struct{ boundary, digest string }{
		{"before-unit\nprivate-report", digest},
		{"after-unit", digest},
		{"", digest},
		{executioncheckpoint.BeforeUnit, strings.ToUpper(digest)},
		{executioncheckpoint.BeforeUnit, strings.Repeat("g", 64)},
		{executioncheckpoint.BeforeUnit, digest + "\n"},
		{executioncheckpoint.BeforeUnit, digest[:63]},
	} {
		output.Reset()
		require.Error(t, emit(control.boundary, control.digest))
		require.Empty(t, output.Bytes(), "invalid locator must not disclose arbitrary progress/report bytes")
	}
}

func TestRunnerCheckpointLocatorRetainsActualProcessBudgetAndStickyFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		calls := 0
		writer := &processDiagnosticWriter{output: processDiagnosticWriteFunc(func(data []byte) (int, error) {
			calls++
			if calls == 1 {
				time.Sleep(processDiagnosticBudget / 2)
				return len(data), nil
			}
			<-release
			return len(data), nil
		})}
		cmd := &cobra.Command{}
		cmd.SetErr(writer)
		emit := newRunnerCheckpointLocatorEmitter(cmd)
		start := time.Now()
		_, err := writer.Write([]byte("ordinary diagnostic\n"))
		require.NoError(t, err)
		require.ErrorIs(t, emit(executioncheckpoint.BeforeUnit, strings.Repeat("b", 64)), os.ErrDeadlineExceeded)
		require.Equal(t, processDiagnosticBudget, time.Since(start), "locator must not get a fresh process budget")
		require.ErrorIs(t, emit(executioncheckpoint.FinalizedUnit, strings.Repeat("b", 64)), os.ErrDeadlineExceeded)
		close(release)
		synctest.Wait()
		require.Equal(t, 2, calls, "failed locator cannot start a second write or fallback")
	})
}

func TestRunnerCheckpointLocatorBoundsSuppliedCLIWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		cmd := &cobra.Command{}
		calls := 0
		cmd.SetErr(processDiagnosticWriteFunc(func(data []byte) (int, error) {
			calls++
			<-release
			return len(data), nil
		}))
		emit := newRunnerCheckpointLocatorEmitter(cmd)
		require.ErrorIs(t, emit(executioncheckpoint.BeforeUnit, strings.Repeat("c", 64)), os.ErrDeadlineExceeded)
		require.ErrorIs(t, emit(executioncheckpoint.FinalizedUnit, strings.Repeat("c", 64)), os.ErrDeadlineExceeded)
		close(release)
		synctest.Wait()
		require.Equal(t, 1, calls)
	})
	for _, output := range []io.Writer{shortSubmissionWriter{}, failingSubmissionWriter{}} {
		cmd := &cobra.Command{}
		cmd.SetErr(output)
		emit := newRunnerCheckpointLocatorEmitter(cmd)
		err := emit(executioncheckpoint.BeforeUnit, strings.Repeat("d", 64))
		if _, short := output.(shortSubmissionWriter); short {
			require.ErrorIs(t, err, io.ErrShortWrite)
		} else {
			require.ErrorIs(t, err, io.ErrClosedPipe)
		}

	}
}

func TestCheckpointActualCLIRejectsLocatorFailureBeforeEngineOrPreservesOriginalCompletion(t *testing.T) {
	for _, control := range []string{"blocked-before", "blocked-finalized", "canceled-finalized", "short", "broken"} {
		t.Run(control, func(t *testing.T) {
			wire, directory := checkpointCLIWire(t), t.TempDir()
			factories, calls := 0, 0
			service := runnerunit.NewService(func(cfg models.Config) (execution.AgentEngine, error) {
				factories++
				if control == "canceled-finalized" {
					return nil, context.Canceled
				}
				return execution.NewMockEngine(cfg.ModelID), nil
			})
			command := newRunnerCheckpointCommandWithRevision(service, func() (string, error) {
				return strings.Repeat("f", 40), nil
			})
			command.SetArgs([]string{"create", "--store", directory})
			command.SetIn(bytes.NewReader(wire))
			var output bytes.Buffer
			command.SetOut(&output)
			release := make(chan struct{})
			returned := make(chan struct{}, 2)
			command.SetErr(processDiagnosticWriteFunc(func(data []byte) (int, error) {
				defer func() { returned <- struct{}{} }()
				calls++
				if calls == 1 && (control == "blocked-finalized" || control == "canceled-finalized") {
					require.Regexp(t, `^checkpoint before-unit [a-f0-9]{64}\n$`, string(data))
					return len(data), nil
				}
				if control == "short" {
					return len(data) - 1, nil
				}
				if control == "broken" {
					return 0, io.ErrClosedPipe
				}
				<-release
				return len(data), nil
			}))
			err := command.ExecuteContext(t.Context())
			close(release)
			expectedCalls := 1
			if control == "blocked-finalized" || control == "canceled-finalized" {
				expectedCalls = 2
			}
			for range expectedCalls {
				select {
				case <-returned:
				case <-time.After(5 * time.Second):
					t.Fatal("caller-owned diagnostic writer must join after releasing backpressure")
				}
			}
			exit, ok := errors.AsType[*ExitCodeError](err)
			require.True(t, ok, "%v", err)
			require.Equal(t, 2, exit.Code)
			require.ErrorContains(t, err, "writing checkpoint locator")
			switch control {
			case "short":
				require.ErrorIs(t, err, io.ErrShortWrite)
			case "broken":
				require.ErrorIs(t, err, io.ErrClosedPipe)
			default:
				require.ErrorIs(t, err, os.ErrDeadlineExceeded)
			}
			result, err := runnercheckpoint.ParseResult(output.Bytes())
			require.NoError(t, err, "%s", output.Bytes())
			require.NotContains(t, output.String(), "writing checkpoint locator",
				"operator cause remains outside the protected result")
			require.Equal(t, 2, result.ExitCode)
			require.Equal(t, 1, bytes.Count(output.Bytes(), []byte{'\n'}))
			require.NotEmpty(t, result.BeforeDigest, "immutable published checkpoint remains locatable after delivery error")
			require.Empty(t, result.Submission.Report)
			if control == "blocked-finalized" || control == "canceled-finalized" {
				require.Equal(t, 1, factories)
				require.Equal(t, 2, calls)
				require.NotNil(t, result.Submission.Completion, "locator failure must not rewrite completed original execution")
				require.NotNil(t, result.Submission.Terminal)
				wantCode := 0
				if control == "canceled-finalized" {
					wantCode = 3
				}
				require.Equal(t, wantCode, result.Submission.Terminal.ExitCode)
				require.NotEmpty(t, result.FinalizedDigest)
			} else {
				require.Zero(t, factories, "before-unit publication delivery failure must stop engine admission")
				require.Equal(t, 1, calls)
				require.Nil(t, result.Submission.Completion)
				require.Nil(t, result.Submission.Terminal)
			}
		})
	}
}
