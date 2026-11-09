package main

import (
	"bytes"
	"io"
	"os"
	"sync"
	"time"
)

const processDiagnosticBudget = 100 * time.Millisecond

// processDiagnosticWriter is only for the exiting CLI process, not embedding.
// It never closes output or changes shared descriptor flags. A blocked write
// may finish late, but os.Exit terminates it; callers must not join it on exit.
type processDiagnosticWriter struct {
	output io.Writer
	mu     sync.Mutex
	spent  time.Duration
	err    error
}

func (w *processDiagnosticWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	remaining := processDiagnosticBudget - w.spent
	if remaining <= 0 {
		w.err = os.ErrDeadlineExceeded
		return 0, w.err
	}
	// On timeout the caller may reuse its buffer before the write returns.
	owned := bytes.Clone(data)
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	go func() {
		n, err := w.output.Write(owned)
		if err == nil && n != len(owned) {
			err = io.ErrShortWrite
		}
		done <- result{n: n, err: err}
	}()
	select {
	case written := <-done:
		w.spent += time.Since(start)
		w.err = written.err
		return written.n, written.err
	case <-timer.C:
		// Delivery may be partial, but its byte count is unavailable. Do not
		// retry, start another write, or report a successful delivery.
		w.err = os.ErrDeadlineExceeded
		return 0, w.err
	}
}
