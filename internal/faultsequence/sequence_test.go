package faultsequence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestReserveFiniteSequenceAndPrivateReset(t *testing.T) {
	for range 2 {
		dir := t.TempDir()
		for want := range 3 {
			index, err := Reserve(context.Background(), dir, 3)
			if err != nil || index != want {
				t.Fatalf("Reserve() = (%d, %v), want (%d, nil)", index, err, want)
			}
		}
		index, err := Reserve(context.Background(), dir, 3)
		if index != -1 || !errors.Is(err, ErrExhausted) {
			t.Fatalf("exhaustion = (%d, %v)", index, err)
		}
	}
}

func TestReserveCancellationDoesNotConsume(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	index, err := Reserve(ctx, dir, 1)
	if index != -1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reservation = (%d, %v)", index, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled reservation changed state: %v, %v", entries, err)
	}
	if index, err := Reserve(context.Background(), dir, 1); err != nil || index != 0 {
		t.Fatalf("first active reservation = (%d, %v)", index, err)
	}
	if _, err := Reserve(ctx, dir, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation should take precedence over exhaustion: %v", err)
	}
}

func TestReserveRejectsInvalidCountsAndStorageFailures(t *testing.T) {
	for _, count := range []int{-1, 0} {
		if _, err := Reserve(context.Background(), t.TempDir(), count); err == nil || errors.Is(err, ErrExhausted) {
			t.Fatalf("invalid count %d: %v", count, err)
		}
		for _, dir := range []string{"", ".", "relative"} {
			if _, err := Reserve(context.Background(), dir, 1); err == nil {
				t.Fatalf("non-absolute state directory %q accepted", dir)
			}
		}
	}
	dir := t.TempDir()
	for _, invalidDir := range []string{filepath.Join(dir, "missing"), filepath.Join(dir, "file")} {
		if invalidDir == filepath.Join(dir, "file") {
			if err := os.WriteFile(invalidDir, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Reserve(context.Background(), invalidDir, 1); err == nil || errors.Is(err, ErrExhausted) {
			t.Fatalf("invalid storage %q: %v", invalidDir, err)
		}
	}
}

func TestReservePreservesExistingMarkers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "step-00000000")
	want := []byte("existing reservation")
	if err := os.WriteFile(path, want, 0600); err != nil {
		t.Fatal(err)
	}
	if index, err := Reserve(context.Background(), dir, 2); err != nil || index != 1 {
		t.Fatalf("reservation = (%d, %v), want step 1", index, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("existing marker changed: %q, %v", got, err)
	}
}

func TestReserveConcurrentCallers(t *testing.T) {
	const count = 24
	dir := t.TempDir()
	indices := make(chan int, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			index, err := Reserve(context.Background(), dir, count)
			if err != nil {
				t.Errorf("Reserve(): %v", err)
				return
			}
			indices <- index
		})
	}
	wg.Wait()
	close(indices)
	assertAllocatedSteps(t, indices, count)
}

func TestReservationProcess(t *testing.T) {
	dir := os.Getenv("WAZA_FAULT_SEQUENCE_TEST_DIR")
	if dir == "" {
		return
	}
	var gate [1]byte
	if _, err := io.ReadFull(os.Stdin, gate[:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	index, err := Reserve(context.Background(), dir, 8)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := fmt.Fprint(os.Stdout, index); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if os.Getenv("WAZA_FAULT_SEQUENCE_TEST_INTERRUPT") == "1" {
		if err := Delay(context.Background(), MaxDelayMilliseconds); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
	os.Exit(0)
}

func registerReservationProcessCleanup(t *testing.T, command *exec.Cmd, gate io.Closer) {
	t.Helper()
	t.Cleanup(func() {
		if command.ProcessState == nil {
			if err := gate.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				t.Errorf("closing reservation subprocess gate: %v", err)
			}
			if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("stopping reservation subprocess: %v", err)
			}
			if err := command.Wait(); err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Errorf("waiting for reservation subprocess: %v", err)
				}
			}
		}
	})
}

func TestReserveAcrossProcesses(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	type child struct {
		command *exec.Cmd
		gate    io.WriteCloser
		output  *bytes.Buffer
	}
	var children []child
	for range 8 {
		command := exec.CommandContext(ctx, executable, "-test.run=^TestReservationProcess$")
		command.Env = append(os.Environ(), "WAZA_FAULT_SEQUENCE_TEST_DIR="+dir, "WAZA_FAULT_SEQUENCE_TEST_INTERRUPT=")
		output := new(bytes.Buffer)
		command.Stdout = output
		command.Stderr = output
		gate, err := command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, child{command, gate, output})
		registerReservationProcessCleanup(t, command, gate)
	}
	for _, child := range children {
		if _, err := child.gate.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		if err := child.gate.Close(); err != nil {
			t.Fatal(err)
		}
	}
	indices := make(chan int, len(children))
	for _, child := range children {
		if err := child.command.Wait(); err != nil {
			t.Fatalf("reservation subprocess: %v: %s", err, child.output)
		}
		index, err := strconv.Atoi(child.output.String())
		if err != nil {
			t.Fatalf("subprocess index %q: %v", child.output, err)
		}
		indices <- index
	}
	close(indices)
	assertAllocatedSteps(t, indices, len(children))
	if _, err := Reserve(context.Background(), dir, len(children)); !errors.Is(err, ErrExhausted) {
		t.Fatalf("subprocess exit must not release reservations: %v", err)
	}
}

func TestKilledReservationProcessDoesNotRollBackOrResetOtherAttempts(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestReservationProcess$")
	command.Env = append(os.Environ(), "WAZA_FAULT_SEQUENCE_TEST_DIR="+dir, "WAZA_FAULT_SEQUENCE_TEST_INTERRUPT=1")
	gate, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	registerReservationProcessCleanup(t, command, gate)
	if _, err := gate.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := gate.Close(); err != nil {
		t.Fatal(err)
	}
	var ready [1]byte
	if _, err := io.ReadFull(output, ready[:]); err != nil {
		t.Fatalf("reading confirmed reservation: %v", err)
	}
	if ready[0] != '0' {
		t.Fatalf("child reserved unexpected step %q", ready)
	}
	// Readiness confirms reservation, not entry into the child's long delay.
	if index, err := Reserve(ctx, dir, 8); err != nil || index != 1 {
		t.Fatalf("reservation beside pending child = (%d, %v), want (1, nil)", index, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("killing owned reservation child: %v", err)
	}
	var exitErr *exec.ExitError
	if err := command.Wait(); !errors.As(err, &exitErr) {
		t.Fatalf("killed child must exit unsuccessfully: %v", err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("watchdog expiry cannot stand in for explicit interruption: %v", err)
	}
	if index, err := Reserve(ctx, dir, 8); err != nil || index != 2 {
		t.Fatalf("post-kill reservation = (%d, %v), want (2, nil)", index, err)
	}
	if index, err := Reserve(ctx, t.TempDir(), 8); err != nil || index != 0 {
		t.Fatalf("isolated attempt reservation = (%d, %v), want (0, nil)", index, err)
	}
}

func assertAllocatedSteps(t *testing.T, indices <-chan int, count int) {
	t.Helper()
	seen := make(map[int]bool, count)
	for index := range indices {
		if index < 0 || index >= count || seen[index] {
			t.Errorf("invalid or repeated allocation: %d", index)
		}
		seen[index] = true
	}
	if len(seen) != count {
		t.Errorf("allocated %d steps, want %d", len(seen), count)
	}
}

func TestDelayValidationAndCancellation(t *testing.T) {
	requireValidDelay := []int64{0, 1, MaxDelayMilliseconds}
	for _, milliseconds := range requireValidDelay {
		if err := ValidateDelayMilliseconds(milliseconds); err != nil {
			t.Fatalf("valid source bound %d: %v", milliseconds, err)
		}
	}
	for _, milliseconds := range []int64{-1, MaxDelayMilliseconds + 1} {
		if err := Delay(context.Background(), milliseconds); err == nil {
			t.Fatalf("invalid delay %d accepted", milliseconds)
		}
	}
	for _, milliseconds := range []int64{0, 1} {
		if err := Delay(context.Background(), milliseconds); err != nil {
			t.Fatalf("valid delay %d: %v", milliseconds, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, milliseconds := range []int64{0, 1, MaxDelayMilliseconds} {
		if err := Delay(ctx, milliseconds); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled delay %d: %v", milliseconds, err)
		}
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	if err := Delay(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired delay: %v", err)
	}
	waitCtx, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if err := Delay(waitCtx, MaxDelayMilliseconds); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("time budget during delay: %v", err)
	}
}

func TestDelayDoesNotReturnBeforeRequestedDuration(t *testing.T) {
	started := time.Now()
	if err := Delay(context.Background(), 20); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("delay returned early after %s", elapsed)
	}
}

func TestCancellationAfterReservationDoesNotRollBack(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := Reserve(ctx, dir, 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := Delay(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Reserve(context.Background(), dir, 1); !errors.Is(err, ErrExhausted) {
		t.Fatalf("canceled delivery reset reservation: %v", err)
	}
}
