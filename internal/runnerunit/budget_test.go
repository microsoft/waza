package runnerunit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionbudget"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

func budgeted(t *testing.T, budget executionbudget.Request) []byte {
	t.Helper()
	request := fixture(t)
	request.Budgets = &budget
	return wire(t, request)
}

func limit(v int64) *int64 { return &v }

func encodeTerminal(t *testing.T, envelope runnerterminal.Envelope, binding *runnerterminal.Binding) {
	t.Helper()
	data, err := runnerterminal.Encode(envelope, binding)
	require.NoError(t, err, "%+v", envelope)
	decoded, err := runnerterminal.Validate(data, binding)
	require.NoError(t, err)
	require.Equal(t, envelope, *decoded)
}

func budgetRow(t *testing.T, report *executionbudget.Report, resource executionbudget.Resource) executionbudget.Usage {
	t.Helper()
	require.NotNil(t, report)
	for _, row := range report.Resources {
		if row.Resource == resource {
			return row
		}
	}
	t.Fatalf("missing budget row %s", resource)
	return executionbudget.Usage{}
}

func TestUnsupportedBudgetIsRejectedBeforeExecution(t *testing.T) {
	limits := map[executionbudget.Resource]executionbudget.Limits{
		executionbudget.Disk:         {DiskBytes: limit(1 << 20)},
		executionbudget.ProcessCount: {ProcessCount: limit(8)},
		executionbudget.CPUTime:      {CPUTimeMS: limit(1000)},
		executionbudget.Memory:       {MemoryBytes: limit(1 << 30)},
	}
	for resource, limits := range limits {
		t.Run(string(resource), func(t *testing.T) {
			data := budgeted(t, executionbudget.Request{Limits: limits})
			validation := Validate(data)
			require.Equal(t, "rejected", validation.Status)
			require.Equal(t, 5, validation.ExitCode(), "validate and run report the same unsupported capability")
			require.Nil(t, validation.Identity)
			require.Equal(t, "unsupported_budget", validation.Diagnostics[0].Code)

			constructed := 0
			service := NewService(func(cfg models.Config) (execution.AgentEngine, error) {
				constructed++
				return execution.NewMockEngine(cfg.ModelID), nil
			})
			envelope, binding := service.RunTerminal(t.Context(), data)
			require.Zero(t, constructed, "unsupported budgets must be rejected before a runtime exists")
			require.Nil(t, binding)
			require.Equal(t, runnerterminal.UnsupportedCapability, envelope.Outcome)
			require.Equal(t, "validation", envelope.FailureBoundary)
			require.Equal(t, 5, envelope.ExitCode)
			require.Zero(t, envelope.EngineCalls)
			require.Nil(t, envelope.Identity)
			require.Equal(t, "unsupported_budget", envelope.Diagnostics[0].Code)
			require.Contains(t, envelope.Diagnostics[0].Message, string(resource))
			require.Equal(t, executionbudget.StateRejectedUnsupported, envelope.Budget.State)
			row := budgetRow(t, envelope.Budget, resource)
			require.Equal(t, executionbudget.Unsupported, row.Enforcement)
			require.NotNil(t, row.Limit)
			require.Nil(t, row.Used)
			encodeTerminal(t, envelope, binding)
		})
	}
}

func TestReportPolicyRunsAndReportsUnsupportedLimits(t *testing.T) {
	data := budgeted(t, executionbudget.Request{Limits: executionbudget.Limits{
		DiskBytes: limit(1 << 20), WallTimeMS: limit(60_000), ModelTurns: limit(1),
	}, Unsupported: executionbudget.PolicyReport})
	envelope, binding := NewService(nil).RunTerminal(t.Context(), data)
	require.Equal(t, runnerterminal.Success, envelope.Outcome, "%+v", envelope.Diagnostics)
	require.Equal(t, executionbudget.StateWithinBudget, envelope.Budget.State)
	disk := budgetRow(t, envelope.Budget, executionbudget.Disk)
	require.Equal(t, executionbudget.Unsupported, disk.Enforcement)
	require.Equal(t, executionbudget.Unsupported, disk.Measurement)
	require.Nil(t, disk.Used)
	turns := budgetRow(t, envelope.Budget, executionbudget.ModelTurns)
	require.Equal(t, int64(1), *turns.Used)
	require.Equal(t, int64(0), *turns.Remaining)
	require.False(t, *turns.Exceeded)
	wall := budgetRow(t, envelope.Budget, executionbudget.WallTime)
	require.NotNil(t, wall.Used)
	encodeTerminal(t, envelope, binding)
}

func TestModelTurnBudgetCancelsMockAttempt(t *testing.T) {
	data := budgeted(t, executionbudget.Request{Limits: executionbudget.Limits{ModelTurns: limit(0)}})
	envelope, binding := NewService(nil).RunTerminal(t.Context(), data)
	require.Equal(t, runnerterminal.Canceled, envelope.Outcome, "%+v", envelope)
	require.Equal(t, 1, envelope.EngineCalls)
	require.NotNil(t, envelope.Identity, "budget enforcement happens after admission")
	require.Equal(t, executionbudget.StateTerminated, envelope.Budget.State)
	require.Equal(t, executionbudget.Termination{Resource: executionbudget.ModelTurns, Limit: 0, Observed: 1}, *envelope.Budget.Termination)
	require.Equal(t, "budget_exceeded", envelope.Diagnostics[len(envelope.Diagnostics)-1].Code)
	encodeTerminal(t, envelope, binding)
}

func TestTerminalBudgetIsDetachedFromReport(t *testing.T) {
	report := NewService(nil).Run(t.Context(), budgeted(t, executionbudget.Request{Limits: executionbudget.Limits{ModelTurns: limit(0)}}))
	envelope, binding := report.Terminal()
	original, err := runnerterminal.Encode(envelope, binding)
	require.NoError(t, err)
	for _, row := range envelope.Budget.Resources {
		if row.Limit != nil {
			*row.Limit = 99
		}
		if row.Used != nil {
			*row.Used = 99
		}
	}
	envelope.Budget.Resources[0].Detail = "changed"
	envelope.Budget.Termination.Observed = 99
	again, againBinding := report.Terminal()
	unchanged, err := runnerterminal.Encode(again, againBinding)
	require.NoError(t, err)
	require.Equal(t, original, unchanged)
}

func TestUnstartedBudgetCannotClaimExecution(t *testing.T) {
	request := executionbudget.Request{Limits: executionbudget.Limits{ModelTurns: limit(3)}}
	envelope, binding := NewService(nil).RunTerminal(t.Context(), budgeted(t, request))
	require.Equal(t, runnerterminal.Success, envelope.Outcome, "%+v", envelope)
	unstarted, _ := executionbudget.Plan(request, executionbudget.HostCapabilities())
	envelope.Budget = unstarted
	_, err := runnerterminal.Encode(envelope, binding)
	require.ErrorContains(t, err, "completed outcome")
	envelope.Outcome, envelope.FailureBoundary, envelope.ExitCode = runnerterminal.InfrastructureFailure, "execution", 2
	envelope.Diagnostics = []runnerterminal.Diagnostic{{Code: "execution_failed", Message: "forged"}}
	_, err = runnerterminal.Encode(envelope, binding)
	require.ErrorContains(t, err, "unstarted attempt but the envelope reports execution")
}

type toolCallingEngine struct {
	*instrumented
	tools int
}

func (e *toolCallingEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if observer := execution.ActivityObserverFrom(ctx); observer != nil {
		for range e.tools {
			observer.ToolCallStarted()
		}
	}
	if err := ctx.Err(); err != nil {
		e.calls++
		return nil, err
	}
	return e.instrumented.Execute(ctx, req)
}

func TestCountedAndWallBudgetsTerminateFakeEngines(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limits   executionbudget.Limits
		mode     string
		tools    int
		outcome  runnerterminal.Outcome
		resource executionbudget.Resource
		state    string
	}{
		{"tool calls over budget", executionbudget.Limits{ToolCalls: limit(2)}, "", 3, runnerterminal.Canceled, executionbudget.ToolCalls, executionbudget.StateTerminated},
		{"wall time over budget", executionbudget.Limits{WallTimeMS: limit(20)}, "timeout", 0, runnerterminal.Timeout, executionbudget.WallTime, executionbudget.StateTerminated},
		{"within budget", executionbudget.Limits{ToolCalls: limit(2), ModelTurns: limit(1)}, "", 2, runnerterminal.Success, "", executionbudget.StateWithinBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, input := admit(budgeted(t, executionbudget.Request{Limits: tc.limits}))
			require.NotNil(t, input)
			require.NotNil(t, input.budget)
			report.Mode = "mock"
			engine := &toolCallingEngine{instrumented: &instrumented{AgentEngine: execution.NewMockEngine(input.spec.Config.ModelID), mode: tc.mode}, tools: tc.tools}
			report = metered(t.Context(), input, func(ctx context.Context) Report { return execute(ctx, report, input, engine) })
			envelope, binding := terminal(report)
			require.Equal(t, tc.outcome, envelope.Outcome, "%+v", envelope)
			require.Equal(t, tc.state, envelope.Budget.State)
			if tc.resource != "" {
				require.Equal(t, tc.resource, envelope.Budget.Termination.Resource)
				require.True(t, *budgetRow(t, envelope.Budget, tc.resource).Exceeded)
				require.Equal(t, "budget_exceeded", envelope.Diagnostics[len(envelope.Diagnostics)-1].Code)
			} else {
				require.Nil(t, envelope.Budget.Termination)
				require.Equal(t, int64(2), *budgetRow(t, envelope.Budget, executionbudget.ToolCalls).Used)
			}
			encodeTerminal(t, envelope, binding)
		})
	}
}

func TestWallBudgetReachedAfterEngineReturnsEndsAttempt(t *testing.T) {
	report, input := admit(budgeted(t, executionbudget.Request{Limits: executionbudget.Limits{WallTimeMS: limit(20)}}))
	require.NotNil(t, input)
	report.Mode = "mock"
	engine := &instrumented{AgentEngine: execution.NewMockEngine(input.spec.Config.ModelID)}
	report = metered(t.Context(), input, func(ctx context.Context) Report {
		result := execute(ctx, report, input, engine)
		<-ctx.Done()
		return result
	})
	envelope, binding := terminal(report)
	require.Equal(t, runnerterminal.Timeout, envelope.Outcome, "%+v", envelope)
	require.Equal(t, executionbudget.StateTerminated, envelope.Budget.State)
	require.Equal(t, executionbudget.WallTime, envelope.Budget.Termination.Resource)
	encodeTerminal(t, envelope, binding)
}

func TestRequestsWithoutBudgetsCarryNoBudgetReport(t *testing.T) {
	envelope, _ := NewService(nil).RunTerminal(t.Context(), wire(t, fixture(t)))
	require.Equal(t, runnerterminal.Success, envelope.Outcome)
	require.Nil(t, envelope.Budget)
}

func TestBudgetsDoNotChangeLogicalIdentity(t *testing.T) {
	plain := Validate(wire(t, fixture(t)))
	limited := Validate(budgeted(t, executionbudget.Request{Limits: executionbudget.Limits{ModelTurns: limit(3)}}))
	require.Equal(t, "ready", limited.Status)
	require.Equal(t, *plain.Identity, *limited.Identity)
}

func TestBudgetExampleRunsWithinBudget(t *testing.T) {
	data, err := os.ReadFile("../../examples/runner/budget-unit.json")
	require.NoError(t, err)
	envelope, binding := NewService(nil).RunTerminal(t.Context(), data)
	require.Equal(t, runnerterminal.Success, envelope.Outcome, "%+v", envelope.Diagnostics)
	require.Equal(t, executionbudget.StateWithinBudget, envelope.Budget.State)
	encodeTerminal(t, envelope, binding)
}

func TestPreExecutionFailureKeepsConfiguredBudget(t *testing.T) {
	request := fixture(t)
	request.Selection.Task = "not-the-declared-task"
	request.Budgets = &executionbudget.Request{Limits: executionbudget.Limits{ModelTurns: limit(3)}}
	envelope, binding := NewService(nil).RunTerminal(t.Context(), wire(t, request))
	require.Equal(t, runnerterminal.InvalidInput, envelope.Outcome, "%+v", envelope)
	require.NotNil(t, envelope.Budget, "admission failures after planning keep the declared limits")
	require.Equal(t, executionbudget.StateNotStarted, envelope.Budget.State)
	require.Equal(t, int64(3), *budgetRow(t, envelope.Budget, executionbudget.ModelTurns).Limit)
	encodeTerminal(t, envelope, binding)
}

// lateActivityEngine waits for cancellation, then reports activity and delays
// during shutdown, after any request deadline has already ended the attempt.
type lateActivityEngine struct {
	*instrumented
	observer execution.ActivityObserver
	delay    time.Duration
}

func (e *lateActivityEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.observer = execution.ActivityObserverFrom(ctx)
	return e.instrumented.Execute(ctx, req)
}

func (e *lateActivityEngine) Shutdown(ctx context.Context) error {
	if e.observer != nil {
		e.observer.ToolCallStarted()
	}
	time.Sleep(e.delay)
	return e.instrumented.Shutdown(ctx)
}

func TestRequestTimeoutBeforeBudgetKeepsTimeout(t *testing.T) {
	request := fixture(t)
	request.TimeoutMS = 20
	request.Budgets = &executionbudget.Request{Limits: executionbudget.Limits{WallTimeMS: limit(60), ToolCalls: limit(0)}}
	report, input := admit(wire(t, request))
	require.NotNil(t, input)
	report.Mode = "mock"
	engine := &lateActivityEngine{instrumented: &instrumented{AgentEngine: execution.NewMockEngine(input.spec.Config.ModelID), mode: "timeout"}, delay: 80 * time.Millisecond}
	report = admittedAttempt(t.Context(), input, false, func(ctx context.Context) Report { return execute(ctx, report, input, engine) })
	envelope, binding := terminal(report)
	require.Equal(t, runnerterminal.Timeout, envelope.Outcome, "%+v", envelope)
	require.Nil(t, envelope.Budget.Termination, "the request deadline ended the attempt first")
	require.Equal(t, executionbudget.StateExceeded, envelope.Budget.State)
	require.True(t, *budgetRow(t, envelope.Budget, executionbudget.ToolCalls).Exceeded)
	encodeTerminal(t, envelope, binding)
}

func TestSubmittedBudgetStopsAreJournaled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limits   executionbudget.Limits
		mode     string
		tools    int
		state    string
		reason   string
		deadline bool
	}{
		{"wall time", executionbudget.Limits{WallTimeMS: limit(30)}, "timeout", 0, "timed_out", "deadline", true},
		{"tool calls", executionbudget.Limits{ToolCalls: limit(1)}, "canceled", 2, "cancelled", "caller", false}, //nolint:misspell // wire value
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := submissionBackend(t)
			request := fixture(t)
			request.Budgets = &executionbudget.Request{Limits: tc.limits}
			service := NewService(nil)
			service.heartbeat = 2 * time.Millisecond
			service.dispatch = func(ctx context.Context, report Report, input *admitted) Report {
				report.Mode = "mock"
				engine := &toolCallingEngine{instrumented: &instrumented{AgentEngine: execution.NewMockEngine(input.spec.Config.ModelID), mode: tc.mode}, tools: tc.tools}
				return execute(ctx, report, input, engine)
			}
			started := time.Now()
			result, err := service.Submit(t.Context(), submissionWire(t, request, nil), backend, SubmissionHooks{})
			require.NoError(t, err)
			require.Equal(t, "completed", result.Status, "%+v", result)
			require.NoError(t, result.LifecycleError())
			lifecycle := lineageAttempt(t, backend, result).Lifecycle
			require.Equal(t, tc.state, lifecycle.State, "%+v", lifecycle)
			require.Equal(t, "acknowledged", lifecycle.Cancellation)
			require.Equal(t, tc.reason, lifecycle.CancellationReason)
			require.Equal(t, tc.deadline, lifecycle.DeadlineExceeded)
			require.NotNil(t, lifecycle.DeadlineAt)
			if tc.limits.WallTimeMS != nil {
				require.WithinDuration(t, started.Add(30*time.Millisecond), *lifecycle.DeadlineAt, time.Second,
					"the journal records the effective budget deadline, not the request timeout")
			}
		})
	}
}

func TestUnrepresentableEvidenceKeepsBudgetTerminationConsistent(t *testing.T) {
	report, input := admit(budgeted(t, executionbudget.Request{Limits: executionbudget.Limits{ToolCalls: limit(0)}}))
	require.NotNil(t, input)
	report = metered(t.Context(), input, func(ctx context.Context) Report {
		execution.ActivityObserverFrom(ctx).ToolCallStarted()
		report.Mode, report.Calls = "mock", 1
		// An unadmitted grader verdict cannot be encoded against the binding.
		report.Observation = &models.RunResult{RunNumber: input.request.Selection.Trial, Attempts: input.request.Selection.Attempt,
			Status: models.StatusError, Validations: map[string]models.GraderResults{"unadmitted": {Name: "unadmitted"}}}
		return report
	})
	envelope, binding := terminal(report)
	require.Equal(t, "unrepresentable_evidence", envelope.Diagnostics[0].Code)
	require.Equal(t, runnerterminal.Canceled, envelope.Outcome, "%+v", envelope)
	require.Equal(t, "execution", envelope.FailureBoundary)
	require.Equal(t, executionbudget.StateTerminated, envelope.Budget.State)
	encodeTerminal(t, envelope, binding)
}
