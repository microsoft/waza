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

	mu     sync.Mutex
	err    error
	afters map[*func()]struct{}
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
	run := a.inheritLocked()
	err := a.err
	a.mu.Unlock()
	run()
	return err
}

// AfterFunc lets context.AfterFunc and derived contexts observe a stop as
// synchronously as with a stdlib cancel context: the callback is claimed when
// the attempt stops, so a stop function racing a fast return cannot drop it.
func (a *attemptContext) AfterFunc(f func()) func() bool {
	a.mu.Lock()
	run := a.inheritLocked()
	if a.err != nil {
		a.mu.Unlock()
		run()
		go f()
		return func() bool { return false }
	}
	if a.afters == nil {
		a.afters = map[*func()]struct{}{}
	}
	key := &f
	a.afters[key] = struct{}{}
	a.mu.Unlock()
	run()
	return func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		_, pending := a.afters[key]
		delete(a.afters, key)
		return pending
	}
}

// inheritLocked observes an enclosing cancellation synchronously, so a later
// budget event cannot claim an attempt the caller already canceled. The
// returned function runs claimed callbacks and must be called after unlocking.
func (a *attemptContext) inheritLocked() func() {
	if a.err == nil {
		if err := a.parent.Err(); err != nil {
			return a.stopLocked(err)
		}
	}
	return func() {}
}

func (a *attemptContext) stopLocked(err error) func() {
	a.err = err
	close(a.done)
	claimed := make([]func(), 0, len(a.afters))
	for key := range a.afters {
		claimed = append(claimed, *key)
	}
	a.afters = nil
	return func() {
		for _, f := range claimed {
			f()
		}
	}
}

func (a *attemptContext) Value(key any) any { return a.parent.Value(key) }

// cancel records the first cancellation and reports whether it was this one.
func (a *attemptContext) cancel(err error) bool {
	a.mu.Lock()
	run := a.inheritLocked()
	first := a.err == nil
	if first {
		run = a.stopLocked(err)
	}
	a.mu.Unlock()
	run()
	return first
}
