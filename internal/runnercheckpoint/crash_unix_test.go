//go:build unix

package runnercheckpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/models"
)

func TestCheckpointCrashProcess(t *testing.T) {
	if os.Getenv("WAZA_CHECKPOINT_CRASH_CHILD") != "1" {
		return
	}
	store, err := artifactmanifest.OpenLocal(os.Getenv("WAZA_CHECKPOINT_CRASH_STORE"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	boundary := os.Getenv("WAZA_CHECKPOINT_CRASH_BOUNDARY")
	_, err = service.Submit(context.Background(), fixture(t), store, Hooks{
		Published: func(ctx context.Context, ref Reference) error {
			if ref.Boundary != boundary {
				return nil
			}
			if err := json.NewEncoder(os.Stdout).Encode(ref); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		},
	})
	t.Fatalf("crash helper unexpectedly returned instead of being killed at %s: %v", boundary, err)
}

func TestActualProcessKillAndFreshOwnerRecoveryAtBothBoundaries(t *testing.T) {
	for _, boundary := range []string{executioncheckpoint.BeforeUnit, executioncheckpoint.FinalizedUnit} {
		t.Run(boundary, func(t *testing.T) {
			// This exercises real OS process death and actual Submit/adapter,
			// not the still-pending public CLI or a clean build's VCS metadata.
			directory := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCheckpointCrashProcess$", "-test.count=1")
			child.Env = append(os.Environ(), "WAZA_CHECKPOINT_CRASH_CHILD=1",
				"WAZA_CHECKPOINT_CRASH_STORE="+directory, "WAZA_CHECKPOINT_CRASH_BOUNDARY="+boundary)
			var stderr bytes.Buffer
			child.Stderr = &stderr
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					if err := child.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
						t.Error(err)
					}
					if err := child.Wait(); err == nil {
						t.Error("unfinished crash helper exited successfully")
					}
				}
			}()
			var ref Reference
			if err := json.NewDecoder(stdout).Decode(&ref); err != nil {
				if err := child.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					t.Error(err)
				}
				waitErr := child.Wait()
				waited = true
				t.Fatalf("actual checkpoint boundary was not observed: decode=%v wait=%v context=%v stderr=%s",
					err, waitErr, ctx.Err(), stderr.String())
			}
			if ref.Boundary != boundary || len(ref.Digest) != 64 {
				t.Fatalf("helper did not emit the requested real boundary: %+v", ref)
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			waitErr := child.Wait()
			waited = true
			exit, killed := errors.AsType[*exec.ExitError](waitErr)
			status, observed := syscall.WaitStatus(0), false
			if killed {
				status, observed = exit.Sys().(syscall.WaitStatus)
			}
			if !killed || !observed || !status.Signaled() || status.Signal() != syscall.SIGKILL || ctx.Err() != nil {
				t.Fatalf("test did not kill the actual helper before its deadline: wait=%v context=%v stderr=%s",
					waitErr, ctx.Err(), stderr.String())
			}
			store, err := artifactmanifest.OpenLocal(directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			checkpoint, err := executioncheckpoint.Load(context.Background(), store, ref.Digest)
			if err != nil || checkpoint.Boundary != boundary {
				t.Fatalf("checkpoint did not survive actual process death: %+v, %v", checkpoint, err)
			}
			factories := 0
			freshOwner := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
				factories++
				return execution.NewMockEngine(config.ModelID), nil
			})
			result, err := freshOwner.Resume(context.Background(), fixture(t), store, ref.Digest, Hooks{})
			if err != nil {
				t.Fatalf("actual fresh-owner recovery failed: %+v, %v", result, err)
			}
			if boundary == executioncheckpoint.BeforeUnit {
				if factories != 1 || result.Submission.Reservation.ExecutionID == checkpoint.ExecutionID ||
					result.Submission.Reservation.Parent == nil ||
					result.Submission.Reservation.Parent.ExecutionID != checkpoint.ExecutionID {
					t.Fatalf("recovery hydrated dead-process ownership instead of creating a fresh child: %+v", result)
				}
			} else if factories != 0 || result.Submission.EngineCalls != 0 ||
				result.Submission.Reservation.ExecutionID != checkpoint.ExecutionID ||
				result.Submission.Terminal.Selection.Attempt != 2 {
				t.Fatalf("finalized process recovery reran or retagged original work: %+v", result)
			}
			if boundary == executioncheckpoint.FinalizedUnit {
				marker := filepath.Join(directory, "final-"+checkpoint.ManifestDigest)
				if info, err := os.Stat(marker); err != nil || info.Size() != 64 {
					t.Fatalf("failed-restore control did not select the actual original final marker: %v", err)
				}
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
				failed, err := freshOwner.Resume(context.Background(), fixture(t), store, ref.Digest, Hooks{})
				if !errors.Is(err, artifactmanifest.ErrMissing) || failed.Submission != nil ||
					failed.RestoredUnits != nil || factories != 0 {
					t.Fatalf("missing crash-surviving final marker became successful restore or rerun: %+v, %v", failed, err)
				}
			}
			freshOwner.revision = strings.Repeat("d", 40)
			rejected, err := freshOwner.Resume(context.Background(), fixture(t), store, ref.Digest, Hooks{})
			if !errors.Is(err, executioncheckpoint.ErrIncompatible) || rejected.Submission != nil {
				t.Fatalf("crash-surviving blob bypassed current compatibility: %+v, %v", rejected, err)
			}
		})
	}
}
