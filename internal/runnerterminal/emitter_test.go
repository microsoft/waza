package runnerterminal

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(data []byte) (int, error) { return f(data) }

func TestEmitterExactlyOneAttempt(t *testing.T) {
	e, b := fixture(Success)
	want := encodeFixture(t, e, b)
	var output bytes.Buffer
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	emitter := NewEmitter(writerFunc(func(data []byte) (int, error) {
		calls.Add(1)
		close(entered)
		<-release
		return output.Write(data)
	}))
	done := make(chan error, 1)
	go func() {
		code, err := emitter.Emit(e, b)
		if code != 0 && err == nil {
			err = errors.New("successful emission returned wrong exit")
		}
		done <- err
	}()
	<-entered
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() {
			// A conflicting state cannot silently replace or reuse success.
			conflict, _ := fixture(InvalidInput)
			code, err := emitter.Emit(conflict, nil)
			if code != 2 || !errors.Is(err, ErrAlreadyAttempted) {
				t.Errorf("conflicting concurrent emission: code=%d err=%v", code, err)
			}
		})
	}
	group.Wait()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("calls=%d output=%s", calls.Load(), output.Bytes())
	}
	if _, err := Validate(output.Bytes(), b); err != nil {
		t.Fatalf("actual emitted bytes invalid: %v", err)
	}
}

func TestEmitterEveryOutcome(t *testing.T) {
	for _, outcome := range []Outcome{Success, EvaluationFailure, InfrastructureFailure, Canceled, Timeout, UnsupportedCapability, InvalidInput} {
		t.Run(string(outcome), func(t *testing.T) {
			e, b := fixture(outcome)
			var output bytes.Buffer
			emitter := NewEmitter(&output)
			code, err := emitter.Emit(e, b)
			if err != nil || code != e.ExitCode {
				t.Fatalf("emit code=%d err=%v", code, err)
			}
			got, err := Validate(output.Bytes(), b)
			if err != nil || got.Outcome != outcome || got.ExitCode != code {
				t.Fatalf("emitted state mismatch: %+v %v", got, err)
			}
			before := bytes.Clone(output.Bytes())
			if _, err := emitter.Emit(e, b); !errors.Is(err, ErrAlreadyAttempted) || !bytes.Equal(before, output.Bytes()) {
				t.Fatal("second envelope was emitted")
			}
		})
	}
}

func TestEmitterFaultsAreNotRetried(t *testing.T) {
	sentinel := errors.New("deterministic write failure")
	for _, tt := range []struct {
		name string
		n    int
		err  error
		want error
	}{
		{"zero write", 0, nil, io.ErrShortWrite},
		{"short write", 7, nil, io.ErrShortWrite},
		{"broken channel", 0, sentinel, sentinel},
		{"partial with error", 7, sentinel, sentinel},
		{"full with error", -1, sentinel, sentinel},
		{"invalid writer count", -2, nil, io.ErrShortWrite},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, b := fixture(Success)
			calls := 0
			emitter := NewEmitter(writerFunc(func(data []byte) (int, error) {
				calls++
				if tt.n == -1 {
					return len(data), tt.err
				}
				return tt.n, tt.err
			}))
			code, err := emitter.Emit(e, b)
			if code != 2 || !errors.Is(err, tt.want) || calls != 1 {
				t.Fatalf("fault: code=%d err=%v calls=%d", code, err, calls)
			}
			if _, err := emitter.Emit(e, b); !errors.Is(err, ErrAlreadyAttempted) || calls != 1 {
				t.Fatalf("retried failed transport: %v calls=%d", err, calls)
			}
		})
	}
}

func TestEmitterPreparationErrorsWriteNothing(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Envelope)
		want   error
	}{
		{"validation", func(e *Envelope) { e.ExitCode = 6 }, ErrInvalid},
		{"encoding", func(e *Envelope) { e.Identity = nil }, ErrInvalid},
		{"bounds", func(e *Envelope) { e.Diagnostics = make([]Diagnostic, MaxDiagnostics+1) }, ErrTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, b := fixture(Success)
			tt.mutate(&e)
			calls := 0
			emitter := NewEmitter(writerFunc(func(data []byte) (int, error) {
				calls++
				return len(data), nil
			}))
			code, err := emitter.Emit(e, b)
			if code != 2 || !errors.Is(err, tt.want) || calls != 0 {
				t.Fatalf("preparation fault: code=%d err=%v writes=%d", code, err, calls)
			}
			e, b = fixture(Success)
			if _, err := emitter.Emit(e, b); !errors.Is(err, ErrAlreadyAttempted) || calls != 0 {
				t.Fatalf("preparation failure allowed another attempt: %v", err)
			}
		})
	}
	e, b := fixture(Success)
	if code, err := NewEmitter(nil).Emit(e, b); code != 2 || err == nil {
		t.Fatalf("nil writer: code=%d err=%v", code, err)
	}
}
