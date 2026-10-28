package runnerunit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

type inlineCauseEngine struct {
	execution.AgentEngine
	execute func(context.Context) (*execution.ExecutionResponse, error)
}

func (e *inlineCauseEngine) Execute(ctx context.Context, _ *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	return e.execute(ctx)
}

func TestTerminalInlineExecutionCauses(t *testing.T) {
	for _, control := range []string{"eval-deadline", "task-deadline", "task-override", "canceled-cause", "ordinary-error", "deadline-text-only"} {
		t.Run(control, func(t *testing.T) {
			request := fixture(t)
			if control == "eval-deadline" || control == "task-override" {
				request.Input.Eval = strings.Replace(request.Input.Eval, "timeout_seconds: 30", "timeout_seconds: 1", 1)
			}
			if control == "task-deadline" {
				request.Input.Task += "timeout_seconds: 1\n"
			}
			if control == "task-override" {
				request.Input.Task += "timeout_seconds: 3\n"
			}
			data := wire(t, request)
			expectedIdentity, err := runneridentity.Derive(&request)
			require.NoError(t, err)
			report, input := admit(data)
			require.NotNil(t, input)
			report.Mode = "mock"
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				start := time.Now()
				want, code := runnerterminal.Timeout, 4
				cause := context.DeadlineExceeded
				engine := &inlineCauseEngine{
					AgentEngine: execution.NewMockEngine("mock"),
					execute: func(execCtx context.Context) (*execution.ExecutionResponse, error) {
						if strings.HasSuffix(control, "deadline") || control == "task-override" {
							<-execCtx.Done()
							return nil, fmt.Errorf("inline execution interrupted: %w", execCtx.Err())
						}
						return nil, fmt.Errorf("inline execution interrupted: %w", cause)
					},
				}
				switch control {
				case "canceled-cause":
					want, code, cause = runnerterminal.Canceled, 3, context.Canceled
				case "ordinary-error":
					want, code, cause = runnerterminal.InfrastructureFailure, 2, errors.New("runtime unavailable")
				case "deadline-text-only":
					want, code, cause = runnerterminal.InfrastructureFailure, 2, errors.New(context.DeadlineExceeded.Error())
				}
				// This is the lifecycle/attempt caller used by Service.Run, not
				// a hand-built observation or a terminal classifier fixture.
				result := execute(ctx, report, input, engine)
				require.NoError(t, ctx.Err(), "the outer request deadline must still be active")
				require.Equal(t, "execution_error", result.Status)
				require.ErrorIs(t, result.failure, cause, "the typed cause must survive the inline attempt boundary")
				require.Equal(t, 2, result.ExitCode(), "legacy unit receipt exit policy is unchanged")
				require.NotNil(t, result.Observation)
				require.Equal(t, models.StatusError, result.Observation.Status)
				require.Equal(t, "inline execution interrupted: "+cause.Error(), result.Observation.ErrorMsg)
				if strings.HasSuffix(control, "deadline") {
					require.Equal(t, time.Second, time.Since(start), "the eval/task budget, not the outer budget, must expire")
				}
				if control == "task-override" {
					require.Equal(t, 3*time.Second, time.Since(start), "the task budget must override the shorter eval budget")
				}
				envelope, binding := terminal(result)
				var output bytes.Buffer
				exit, err := runnerterminal.NewEmitter(&output).Emit(envelope, binding)
				require.NoError(t, err)
				require.Equal(t, code, exit)
				decoded, err := runnerterminal.Validate(output.Bytes(), binding)
				require.NoError(t, err, "%s", output.Bytes())
				require.Equal(t, want, decoded.Outcome)
				require.Equal(t, code, decoded.ExitCode)
				require.Equal(t, "execution", decoded.FailureBoundary)
				require.Equal(t, expectedIdentity, decoded.Identity)
				require.Equal(t, request.Selection, *decoded.Selection)
				require.Equal(t, request.Selection, decoded.Observation.Selection)
				require.Equal(t, request.Selection.Trial, decoded.Observation.RunNumber)
				require.Equal(t, request.Selection.Attempt, decoded.Observation.Attempts)
				require.Equal(t, result.Observation.ErrorMsg, decoded.Observation.ErrorMsg)
				require.Equal(t, "error", decoded.Observation.Status)
				require.Equal(t, 1, decoded.EngineCalls)
				require.Equal(t, 1, decoded.Observation.EngineCalls)
				require.Empty(t, decoded.Observation.Validations, "execution failed before any grader ran")
			})
		})
	}
}

func TestTerminalProductionOutcomes(t *testing.T) {
	for _, control := range []string{"success", "failed", "invalid", "unsupported-version", "unsupported-capability", "factory", "cancel", "timeout", "oversize-diagnostic", "oversize-graders"} {
		t.Run(control, func(t *testing.T) {
			request := fixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := runnerterminal.Success
			service := NewService(nil)
			switch control {
			case "failed":
				want = runnerterminal.EvaluationFailure
				request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- never-returned", 1)
			case "invalid":
				want = runnerterminal.InvalidInput
				request.Selection.Task = "neighbor"
			case "unsupported-version":
				want = runnerterminal.UnsupportedCapability
				request.Versions = []string{"9.0"}
			case "unsupported-capability":
				want = runnerterminal.UnsupportedCapability
				request.RequiredCapabilities = append(request.RequiredCapabilities, "inline.native-text")
			case "factory", "oversize-diagnostic":
				want = runnerterminal.InfrastructureFailure
				service = NewService(func(models.Config) (execution.AgentEngine, error) {
					return nil, errors.New(strings.Repeat("runtime failed ", map[bool]int{true: 1000, false: 1}[control == "oversize-diagnostic"]))
				})
			case "cancel":
				want = runnerterminal.Canceled
				service = NewService(func(cfg models.Config) (execution.AgentEngine, error) {
					cancel()
					return execution.NewMockEngine(cfg.ModelID), nil
				})
			case "timeout":
				want = runnerterminal.Timeout
				service = NewService(func(cfg models.Config) (execution.AgentEngine, error) {
					// Virtual time is advanced below; the real production
					// constructor returns only after the parent deadline.
					<-ctx.Done()
					return execution.NewMockEngine(cfg.ModelID), nil
				})
			case "oversize-graders":
				want = runnerterminal.InfrastructureFailure
				request.Input.Task += "graders:\n"
				for i := range runnerterminal.MaxGraders + 1 {
					request.Input.Task += fmt.Sprintf("  - name: check%d\n    type: text\n    config:\n      contains: [hello]\n", i)
				}
			}
			run := func(ctx context.Context) {
				envelope, binding := service.RunTerminal(ctx, wire(t, request))
				require.Equal(t, want, envelope.Outcome, "%+v", envelope)
				data, err := runnerterminal.Encode(envelope, binding)
				require.NoError(t, err, "%+v", envelope)
				decoded, err := runnerterminal.Validate(data, binding)
				require.NoError(t, err)
				require.Equal(t, envelope, *decoded)
				code, ok := want.ExitCode()
				require.True(t, ok)
				require.Equal(t, code, decoded.ExitCode)
				if control == "invalid" || strings.HasPrefix(control, "unsupported-") {
					require.Nil(t, binding)
					require.Nil(t, decoded.Identity)
					require.Nil(t, decoded.Selection)
					require.Nil(t, decoded.Observation)
					require.Zero(t, decoded.EngineCalls)
				} else {
					require.NotNil(t, binding)
					require.Equal(t, binding.Identity, decoded.Identity)
					require.Equal(t, binding.Selection, *decoded.Selection)
				}
				if control == "oversize-graders" {
					require.Equal(t, "terminalization", envelope.FailureBoundary)
					require.Equal(t, "unrepresentable_evidence", envelope.Diagnostics[0].Code)
				}
				if control == "oversize-diagnostic" {
					require.Equal(t, "startup", envelope.FailureBoundary)
					require.Contains(t, envelope.Diagnostics[0].Message, "diagnostic truncated")
				}
				if control == "unsupported-version" {
					require.Equal(t, "unsupported_version", envelope.Diagnostics[0].Code)
				}
				if decoded.Observation != nil {
					require.Equal(t, request.Selection.Attempt, decoded.Observation.Attempts)
					require.Equal(t, request.Selection.Trial, decoded.Observation.RunNumber)
					require.NotEmpty(t, decoded.Observation.Validations)
					require.NotContains(t, string(data), `"usage"`)
				}
			}
			if control == "timeout" {
				synctest.Test(t, func(t *testing.T) {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(t.Context(), time.Second)
					defer stop()
					run(ctx)
				})
			} else {
				run(ctx)
			}
		})
	}
}

func TestTerminalProductionRejectionDiagnosticBounds(t *testing.T) {
	for _, count := range []int{1, 16, 17, 80} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			request := fixture(t)
			for i := range count {
				request.RequiredCapabilities = append(request.RequiredCapabilities, fmt.Sprintf("unsupported.%d", i))
			}
			envelope, binding := NewService(nil).RunTerminal(t.Context(), wire(t, request))
			require.Equal(t, runnerterminal.UnsupportedCapability, envelope.Outcome)
			require.Equal(t, 5, envelope.ExitCode)
			require.Nil(t, binding)
			require.Nil(t, envelope.Identity)
			require.Nil(t, envelope.Observation)
			require.Zero(t, envelope.EngineCalls)
			require.Len(t, envelope.Diagnostics, min(count, runnerterminal.MaxDiagnostics))
			if count > runnerterminal.MaxDiagnostics {
				require.Equal(t, "additional_diagnostics", envelope.Diagnostics[runnerterminal.MaxDiagnostics-1].Code)
				require.Contains(t, envelope.Diagnostics[runnerterminal.MaxDiagnostics-1].Message, "additional rejection diagnostics")
			}
			_, err := runnerterminal.Encode(envelope, binding)
			require.NoError(t, err)
		})
	}
}

func TestTerminalRealRuntimeFilesystemFailure(t *testing.T) {
	// The production concrete mock, not a lifecycle fake, reaches MkdirTemp.
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory+"/missing")
	t.Setenv("TMP", directory+"/missing")
	t.Setenv("TEMP", directory+"/missing")
	envelope, binding := NewService(nil).RunTerminal(t.Context(), wire(t, fixture(t)))
	require.Equal(t, runnerterminal.InfrastructureFailure, envelope.Outcome)
	require.Equal(t, "execution", envelope.FailureBoundary)
	require.Equal(t, 1, envelope.EngineCalls)
	require.NotNil(t, envelope.Identity)
	require.NotNil(t, envelope.Observation)
	require.Equal(t, "error", envelope.Observation.Status)
	require.Contains(t, envelope.Observation.ErrorMsg, "mock workspace")
	_, err := runnerterminal.Encode(envelope, binding)
	require.NoError(t, err)
	_, err = os.Stat(directory + "/missing")
	require.ErrorIs(t, err, os.ErrNotExist)
}

type terminalShutdownCancellation struct {
	execution.AgentEngine
	cancel context.CancelFunc
}

func (e *terminalShutdownCancellation) Shutdown(context.Context) error {
	e.cancel()
	return errors.New("shutdown failed after cancellation")
}

func TestTerminalCancellationDuringFailingShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	report, input := admit(wire(t, fixture(t)))
	require.NotNil(t, input)
	report.Mode = "mock"
	engine := &terminalShutdownCancellation{AgentEngine: execution.NewMockEngine("mock"), cancel: cancel}
	envelope, binding := terminal(execute(ctx, report, input, engine))
	require.Equal(t, runnerterminal.Canceled, envelope.Outcome)
	require.Equal(t, "shutdown", envelope.FailureBoundary)
	require.Equal(t, "passed", envelope.Observation.Status)
	require.Contains(t, envelope.Diagnostics[0].Message, "shutdown failed")
	require.Contains(t, envelope.Diagnostics[0].Message, "context canceled")
	_, err := runnerterminal.Encode(envelope, binding)
	require.NoError(t, err)
}

func TestTerminalLifecycleErrorPrecedence(t *testing.T) {
	for _, mode := range []string{"initialize", "execution", "nil", "unsuccessful", "shutdown", "canceled", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			report, input := admit(wire(t, fixture(t)))
			require.NotNil(t, input)
			report.Mode = "mock"
			engine := &instrumented{AgentEngine: execution.NewMockEngine("mock"), mode: mode}
			if mode == "canceled" {
				cancel()
			}
			check := func(ctx context.Context) {
				envelope, binding := terminal(execute(ctx, report, input, engine))
				want := runnerterminal.InfrastructureFailure
				if mode == "canceled" {
					want = runnerterminal.Canceled
				}
				if mode == "timeout" {
					want = runnerterminal.Timeout
				}
				require.Equal(t, want, envelope.Outcome, "%+v", envelope)
				_, err := runnerterminal.Encode(envelope, binding)
				require.NoError(t, err)
				require.Equal(t, 1, engine.shutdowns)
				if mode == "shutdown" {
					require.Equal(t, "passed", envelope.Observation.Status)
					require.Equal(t, "shutdown", envelope.FailureBoundary)
				}
			}
			if mode == "timeout" {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					defer cancel()
					check(ctx)
				})
			} else {
				check(ctx)
			}
		})
	}
	envelope := runnerterminal.Failure("input", errors.Join(context.Canceled, context.DeadlineExceeded))
	require.Equal(t, runnerterminal.Timeout, envelope.Outcome)
}
