package executionbudget

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ExceededError is the cancellation cause for an enforced limit. It unwraps to
// the context error the terminal classifier maps: DeadlineExceeded for wall
// time (timeout) and Canceled for counted model/tool budgets (cancelled).
type ExceededError struct {
	Termination Termination
}

func (e *ExceededError) Error() string {
	return fmt.Sprintf("execution budget exceeded: %s observed %d beyond limit %d",
		e.Termination.Resource, e.Termination.Observed, e.Termination.Limit)
}

func (e *ExceededError) Unwrap() error {
	if e.Termination.Resource == WallTime {
		return context.DeadlineExceeded
	}
	return context.Canceled
}

// Meter enforces wall-time and counted budgets for one attempt and collects
// measured usage. Its observer methods are safe for concurrent event streams.
type Meter struct {
	plan     Report
	cancel   context.CancelCauseFunc
	stopWall context.CancelFunc
	ctx      context.Context
	start    time.Time
	before   sample
	sampler  func() sample
	now      func() time.Time

	mu          sync.Mutex
	finished    bool
	turns       int64
	tools       int64
	termination *Termination
}

type sample struct {
	cpuMS, peakRSS int64
	cpu, memory    bool
}

// Start begins metering. The returned context carries the wall-time deadline
// and is canceled with an *ExceededError when a counted limit is exceeded.
func Start(ctx context.Context, plan Report) (context.Context, *Meter) {
	return start(ctx, plan, hostSample, time.Now)
}

func start(ctx context.Context, plan Report, sampler func() sample, now func() time.Time) (context.Context, *Meter) {
	m := &Meter{plan: plan, sampler: sampler, now: now, stopWall: func() {}}
	ctx, m.cancel = context.WithCancelCause(ctx)
	if limit, ok := m.limit(WallTime); ok {
		ctx, m.stopWall = context.WithTimeoutCause(ctx, time.Duration(limit)*time.Millisecond,
			&ExceededError{Termination: Termination{Resource: WallTime, Limit: limit, Observed: limit}})
	}
	m.ctx, m.start, m.before = ctx, now(), sampler()
	return ctx, m
}

func (m *Meter) limit(resource Resource) (int64, bool) {
	for _, row := range m.plan.Resources {
		if row.Resource == resource && row.Limit != nil && row.Enforcement == Enforced {
			return *row.Limit, true
		}
	}
	return 0, false
}

// ModelTurnStarted counts one observed model turn.
func (m *Meter) ModelTurnStarted() { m.count(ModelTurns, &m.turns) }

// ToolCallStarted counts one observed tool execution.
func (m *Meter) ToolCallStarted() { m.count(ToolCalls, &m.tools) }

func (m *Meter) count(resource Resource, counter *int64) {
	m.mu.Lock()
	if m.finished {
		m.mu.Unlock()
		return
	}
	*counter++
	observed := *counter
	limit, limited := m.limit(resource)
	var cause error
	if limited && observed > limit && m.termination == nil {
		m.termination = &Termination{Resource: resource, Limit: limit, Observed: observed}
		cause = &ExceededError{Termination: *m.termination}
	}
	m.mu.Unlock()
	if cause != nil {
		m.cancel(cause)
	}
}

// Err returns the enforced-budget error that ended the attempt, if any.
func (m *Meter) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.errLocked()
}

func (m *Meter) errLocked() error {
	if m.termination != nil {
		return &ExceededError{Termination: *m.termination}
	}
	var exceeded *ExceededError
	if errors.As(context.Cause(m.ctx), &exceeded) && exceeded.Termination.Resource == WallTime {
		return exceeded
	}
	return nil
}

// Finish stops enforcement and returns the measured terminal report. Later
// observer calls are ignored. The wall timer is stopped before the terminal
// cause is read, so a deadline cannot fire unrecorded after the report is built.
func (m *Meter) Finish() *Report {
	m.mu.Lock()
	m.finished = true
	m.stopWall()
	wallErr := m.errLocked()
	elapsed := m.now().Sub(m.start).Milliseconds()
	after := m.sampler()
	report := m.plan
	report.Resources = make([]Usage, len(m.plan.Resources))
	termination := m.termination
	var exceeded *ExceededError
	if termination == nil && errors.As(wallErr, &exceeded) {
		termination = &exceeded.Termination
		termination.Observed = max(elapsed, termination.Limit)
	}
	turns, tools := m.turns, m.tools
	m.mu.Unlock()
	m.cancel(nil)

	report.State = StateWithinBudget
	for i, row := range m.plan.Resources {
		var used int64
		measured := row.Measurement == Measured
		switch row.Resource {
		case WallTime:
			used = max(elapsed, 0)
		case ModelTurns:
			used = turns
		case ToolCalls:
			used = tools
		case CPUTime:
			measured = measured && m.before.cpu && after.cpu
			used = max(after.cpuMS-m.before.cpuMS, 0)
		case Memory:
			measured = measured && after.memory
			used = after.peakRSS
		default:
			measured = false
		}
		if !measured {
			row.Measurement, row.Scope = Unsupported, ""
		} else {
			row.Used = &used
			if row.Limit != nil {
				// The enforced limit that ended the attempt is exceeded even
				// when millisecond rounding reports used == limit.
				over := used > *row.Limit || (termination != nil && termination.Resource == row.Resource)
				remaining := max(*row.Limit-used, 0)
				row.Remaining, row.Exceeded = &remaining, &over
				if over {
					report.State = StateExceeded
				}
			}
		}
		report.Resources[i] = row
	}
	if termination != nil {
		copied := *termination
		report.Termination, report.State = &copied, StateTerminated
	}
	return &report
}
