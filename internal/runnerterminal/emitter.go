package runnerterminal

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
)

var ErrAlreadyAttempted = errors.New("terminal emission already attempted")

// Emitter owns one invocation's sole output attempt. Do not copy it or construct
// another emitter for the same channel after failure. The writer owns deadlines,
// flush/close, and cancellation; successful Write does not prove consumer receipt.
type Emitter struct {
	attempted atomic.Bool
	writer    io.Writer
}

func NewEmitter(writer io.Writer) *Emitter {
	return &Emitter{writer: writer}
}

// Emit validates before writing and never retries a short/failed write. A
// conflicting call fails immediately, even while the first writer is blocked.
func (e *Emitter) Emit(envelope Envelope, expected *Binding) (int, error) {
	if !e.attempted.CompareAndSwap(false, true) {
		return 2, ErrAlreadyAttempted
	}
	data, err := Encode(envelope, expected)
	if err != nil {
		return 2, fmt.Errorf("preparing terminal output: %w", err)
	}
	if e.writer == nil {
		return 2, errors.New("terminal output writer is nil")
	}
	n, err := e.writer.Write(data)
	if err != nil {
		return 2, fmt.Errorf("writing terminal output: %w", err)
	}
	if n != len(data) {
		return 2, fmt.Errorf("writing terminal output: %w", io.ErrShortWrite)
	}
	return envelope.ExitCode, nil
}
