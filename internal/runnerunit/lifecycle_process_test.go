//go:build unix

package runnerunit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
)

const lifecycleHelperEnv = "WAZA_LIFECYCLE_HELPER_STORE"

// TestLifecycleHelperProcess is the owner process for the signal tests. It
// blocks in a dispatched execution until SIGINT or SIGKILL arrives.
func TestLifecycleHelperProcess(t *testing.T) {
	directory := os.Getenv(lifecycleHelperEnv)
	if directory == "" {
		t.Skip("helper process only")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	backend, err := artifactmanifest.OpenLocal(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Close() }()
	service := NewService(nil)
	service.heartbeat = 20 * time.Millisecond
	service.dispatch = func(ctx context.Context, report Report, input *admitted) Report {
		report.Mode = "mock"
		_, _ = os.Stdout.WriteString("dispatched\n")
		return execute(ctx, report, input, &instrumented{AgentEngine: execution.NewMockEngine(input.spec.Config.ModelID), mode: "canceled"})
	}
	hooks := SubmissionHooks{Reserved: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, reservation executionledger.Reservation) error {
		data, err := json.Marshal(reservation.Attempt)
		if err == nil {
			_, err = os.Stdout.WriteString("reserved " + string(data) + "\n")
		}
		return err
	}}
	if _, err := service.Submit(ctx, submissionWire(t, fixture(t), nil), backend, hooks); err != nil {
		t.Fatal(err)
	}
}

func startLifecycleOwner(t *testing.T, directory string) (*exec.Cmd, executionattempt.Attempt) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), lifecycleHelperEnv+"="+directory)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	var attempt executionattempt.Attempt
	timeout := time.After(30 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("owner process exited before dispatch")
			}
			if data, found := strings.CutPrefix(line, "reserved "); found {
				if err := json.Unmarshal([]byte(data), &attempt); err != nil {
					t.Fatal(err)
				}
			}
			if line == "dispatched" {
				go func() {
					for range lines {
					}
				}()
				return cmd, attempt
			}
		case <-timeout:
			t.Fatal("owner process did not dispatch")
		}
	}
}

func readLineage(t *testing.T, directory string, logical executionattempt.Logical, staleAfter time.Duration) *executionledger.Lineage {
	t.Helper()
	backend, err := artifactmanifest.OpenLocal(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Close() }()
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := ledger.Lineage(context.Background(), logical, executionledger.LineageOptions{StaleAfter: staleAfter})
	if err != nil {
		t.Fatal(err)
	}
	return lineage
}

func snapshotFiles(t *testing.T, directory string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		files[path] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestOwnerProcessInterruptIsAcknowledged(t *testing.T) {
	directory := t.TempDir()
	cmd, attempt := startLifecycleOwner(t, directory)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("interrupted owner did not exit cleanly: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("interrupted owner did not exit within bound")
	}
	lineage := readLineage(t, directory, attempt.Logical, 0)
	if len(lineage.Attempts) != 1 {
		t.Fatalf("lineage %+v", lineage)
	}
	lifecycle := lineage.Attempts[0].Lifecycle
	if lineage.Attempts[0].Status != executionledger.AttemptCompleted || lifecycle.State != "cancelled" || //nolint:misspell // wire value
		lifecycle.Cancellation != "acknowledged" || lifecycle.CancellationReason != "caller" || !lifecycle.Retry.Eligible {
		t.Fatalf("interrupted owner lifecycle %+v", lifecycle)
	}
}

func TestKilledOwnerIsOrphanedAndRetryPreservesRecords(t *testing.T) {
	directory := t.TempDir()
	cmd, attempt := startLifecycleOwner(t, directory)
	time.Sleep(100 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if lifecycle := readLineage(t, directory, attempt.Logical, time.Hour).Attempts[0].Lifecycle; lifecycle.State != "running" {
		t.Fatalf("fresh journal must not be orphaned under a long threshold: %+v", lifecycle)
	}
	time.Sleep(150 * time.Millisecond)
	lineage := readLineage(t, directory, attempt.Logical, 100*time.Millisecond)
	orphan := lineage.Attempts[0]
	if orphan.Status != executionledger.AttemptDispatched || orphan.Lifecycle.State != "orphaned" ||
		!orphan.Lifecycle.Retry.Eligible || !orphan.Lifecycle.Partial {
		t.Fatalf("killed owner lifecycle %+v", orphan.Lifecycle)
	}
	before := snapshotFiles(t, directory)

	backend, err := artifactmanifest.OpenLocal(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	request := fixture(t)
	request.Selection.Attempt++
	retry, err := NewService(nil).Submit(context.Background(), submissionWire(t, request, &SubmissionPolicy{Mode: "distinct",
		Parent: &executionattempt.Parent{ExecutionID: orphan.ExecutionID, Relationship: "retry"}}), backend, SubmissionHooks{})
	if err != nil || retry.Status != "completed" || retry.Reservation.ExecutionID == orphan.ExecutionID {
		t.Fatalf("retry did not create a new attempt: %+v %v", retry, err)
	}
	after := snapshotFiles(t, directory)
	for path, data := range before {
		if !bytes.Equal(after[path], data) {
			t.Fatalf("retry modified orphan record %s", path)
		}
	}
	if lineage := readLineage(t, directory, attempt.Logical, 100*time.Millisecond); len(lineage.Attempts) != 2 ||
		lineage.Attempts[1].Parent == nil || lineage.Attempts[1].Lifecycle.State != "succeeded" {
		t.Fatalf("retry lineage %+v", lineage.Attempts)
	}
}
