package executionbudget

import (
	"context"
	"sync"
	"time"
)

// attemptContext is the metered attempt context. Its wall deadline is owned by
// the meter rather than a stdlib timer context, so a preempted attempt can
// disarm the deadline without canceling the attempt. Enforced stops report the
// stdlib errors: DeadlineExceeded for wall time and Canceled for counted
// budgets; the typed *ExceededError is kept by the meter.
type attemptContext struct {
	parent   context.Context
	deadline time.Time
	done     chan struct{}
	release  func() bool

	mu  sync.Mutex
	err error
}

func newAttemptContext(parent context.Context, deadline time.Time) *attemptContext {
	a := &attemptContext{parent: parent, deadline: deadline, done: make(chan struct{})}
	a.release = context.AfterFunc(parent, func() { a.cancel(parent.Err()) })
	return a
}

func (a *attemptContext) Deadline() (time.Time, bool) {
	enclosing, has := a.parent.Deadline()
	switch {
	case a.deadline.IsZero():
		return enclosing, has
	case has && enclosing.Before(a.deadline):
		return enclosing, true
	default:
		return a.deadline, true
	}
}

func (a *attemptContext) Done() <-chan struct{} { return a.done }

func (a *attemptContext) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inheritLocked()
	return a.err
}

// inheritLocked observes an enclosing cancellation synchronously, so a later
// budget event cannot claim an attempt the caller already canceled.
func (a *attemptContext) inheritLocked() {
	if a.err == nil {
		if err := a.parent.Err(); err != nil {
			a.err = err
			close(a.done)
		}
	}
}

func (a *attemptContext) Value(key any) any { return a.parent.Value(key) }

// cancel records the first cancellation and reports whether it was this one.
func (a *attemptContext) cancel(err error) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inheritLocked()
	if a.err != nil {
		return false
	}
	a.err = err
	close(a.done)
	return true
}
