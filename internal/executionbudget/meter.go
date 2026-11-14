package executionbudget

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ExceededError is the meter's record of an enforced limit. It unwraps to the
// context error the attempt context reports and the terminal classifier maps:
// DeadlineExceeded for wall time (timeout) and Canceled for counted budgets.
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
	plan    Report
	parent  context.Context
	ctx     *attemptContext
	wall    *time.Timer
	start   time.Time
	before  sample
	sampler func() sample
	now     func() time.Time

	mu        sync.Mutex
	finished  bool
	preempted bool
	stopped   *ExceededError
	turns     int64
	tools     int64
}

type sample struct {
	cpuMS, peakRSS int64
	cpu, memory    bool
}

// Start begins metering. The returned context carries the wall-time deadline
// and is canceled when an enforced limit is exceeded; Err returns the typed
// *ExceededError. A non-nil observe installs the meter into the parent beneath
// the returned context, which must stay outermost so context.AfterFunc
// observes budget stops synchronously.
func Start(ctx context.Context, plan Report, observe func(context.Context, *Meter) context.Context) (context.Context, *Meter) {
	return startObserved(ctx, plan, hostSample, time.Now, observe)
}

func start(ctx context.Context, plan Report, sampler func() sample, now func() time.Time) (context.Context, *Meter) {
	return startObserved(ctx, plan, sampler, now, nil)
}

func startObserved(ctx context.Context, plan Report, sampler func() sample, now func() time.Time,
	observe func(context.Context, *Meter) context.Context,
) (context.Context, *Meter) {
	m := &Meter{plan: plan, sampler: sampler, now: now}
	if observe != nil {
		ctx = observe(ctx, m)
	}
	m.parent = ctx
	var deadline time.Time
	limit, walled := m.limit(WallTime)
	if walled {
		deadline = time.Now().Add(time.Duration(limit) * time.Millisecond)
	}
	m.ctx, m.start, m.before = newAttemptContext(ctx, deadline), now(), sampler()
	if walled {
		m.wall = time.AfterFunc(time.Until(deadline), m.wallExpired)
	}
	return m.ctx, m
}

func (m *Meter) wallExpired() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit, ok := m.limit(WallTime); ok && !m.finished && !m.preempted {
		m.stopLocked(&ExceededError{Termination: Termination{Resource: WallTime, Limit: limit, Observed: limit}})
	}
}

// stopLocked ends the attempt on an enforced limit unless it already ended.
func (m *Meter) stopLocked(exceeded *ExceededError) {
	if m.ctx.cancel(exceeded.Unwrap()) {
		m.stopped = exceeded
	}
}

func (m *Meter) disarmWall() {
	if m.wall != nil {
		m.wall.Stop()
	}
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
	// Usage is always counted, but a budget terminates the attempt only when
	// it is the first cancellation; a later event after a wall deadline or
	// caller cancellation must not claim the termination.
	if limited && observed > limit && m.ctx.Err() == nil && !m.preempted {
		m.stopLocked(&ExceededError{Termination: Termination{Resource: resource, Limit: limit, Observed: observed}})
	}
	m.mu.Unlock()
}

// Preempt records that the attempt already ended with its own cause, such as
// an eval or task timeout outside the metered context. Later crossings are
// still measured and reported as exceeded but no longer cancel, terminate or
// claim the attempt: the wall deadline is disarmed without canceling the
// context. It has no effect once a budget or the caller has canceled.
func (m *Meter) Preempt() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx.Err() == nil {
		m.preempted = true
		m.disarmWall()
	}
}

// Err returns the enforced-budget error that ended the attempt, if any.
func (m *Meter) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.errLocked()
}

// The first cancellation is authoritative: a budget claims the attempt only
// when it canceled before the wall deadline, another budget or the caller.
func (m *Meter) errLocked() error {
	if m.stopped == nil {
		return nil
	}
	return m.stopped
}

// expireWallLocked ends the attempt on a wall deadline that has passed but
// whose timer has not fired yet. An earlier cancellation, or an enclosing
// deadline no later than the wall deadline, keeps its own cause.
func (m *Meter) expireWallLocked(elapsed time.Duration) {
	limit, ok := m.limit(WallTime)
	if !ok || m.preempted || m.ctx.Err() != nil || elapsed < time.Duration(limit)*time.Millisecond {
		return
	}
	if enclosing, has := m.parent.Deadline(); has && !enclosing.After(m.ctx.deadline) {
		return
	}
	m.stopLocked(&ExceededError{Termination: Termination{Resource: WallTime, Limit: limit, Observed: limit}})
}

// Finish stops enforcement and returns the measured terminal report. Later
// observer calls are ignored. A passed wall deadline is enforced before the
// timer is stopped, and the timer is stopped before the terminal cause is
// read, so a deadline cannot be lost or fire unrecorded after the report is built.
func (m *Meter) Finish() *Report {
	m.mu.Lock()
	m.finished = true
	elapsedTime := m.now().Sub(m.start)
	m.expireWallLocked(elapsedTime)
	m.disarmWall()
	cause := m.errLocked()
	elapsed := elapsedTime.Milliseconds()
	after := m.sampler()
	report := m.plan
	report.Resources = make([]Usage, len(m.plan.Resources))
	var termination *Termination
	var exceeded *ExceededError
	if errors.As(cause, &exceeded) {
		copied := exceeded.Termination
		if copied.Resource == WallTime {
			copied.Observed = max(elapsed, copied.Limit)
		}
		termination = &copied
	}
	turns, tools := m.turns, m.tools
	m.mu.Unlock()
	m.ctx.cancel(context.Canceled)
	m.ctx.release()

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
