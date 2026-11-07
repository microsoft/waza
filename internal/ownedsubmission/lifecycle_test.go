package ownedsubmission

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func stageLifecycle(t *testing.T, selection runnerunit.LifecycleSelection) runnerunit.LifecycleCapture {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	started := executionlifecycle.Event{
		Kind: "waza.execution-lifecycle-observation", SchemaVersion: "1.0",
		ExecutionID: selection.Reservation.Attempt.ExecutionID, ReservationDigest: selection.Reservation.Digest,
		Sequence: 1, ObservedAt: now, Event: "started",
	}
	terminal := started
	terminal.Sequence, terminal.Event, terminal.Terminal = 2, "terminal_recorded", &selection.Terminal
	observations := []executionlifecycle.Event{started, terminal}
	digests := []string{}
	for _, event := range observations {
		data, err := executionlifecycle.Encode(event)
		require.NoError(t, err)
		digests = append(digests, artifactmanifest.Hash(data))
	}
	// This real non-I/O sink records no SDK actor for mock execution.
	runtime, err := execution.NewOwnedRuntimeObservations().Seal()
	require.NoError(t, err)
	return runnerunit.LifecycleCapture{
		Kind: "waza.execution-lifecycle-capture", SchemaVersion: "1.0",
		Reservation: selection.Reservation.Bytes, OriginalAdmission: selection.Reservation.Attempt.Binding,
		ReservationDigest: selection.Reservation.Digest, Deadline: selection.Deadline,
		Observations: observations, ObservationDigests: digests, Runtime: runtime,
	}
}

func assertNoPublication(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	for _, entry := range entries {
		require.False(t, strings.HasPrefix(entry.Name(), "blob-"), entry.Name())
		require.False(t, strings.HasPrefix(entry.Name(), "manifest-"), entry.Name())
		require.False(t, strings.HasPrefix(entry.Name(), "final-"), entry.Name())
	}
}

func TestOwnedLifecycleSelectionPrecedesOriginalValidationAndSinglePublication(t *testing.T) {
	directory := t.TempDir()
	backend, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	selected, validated, observed := false, false, false
	var staged runnerunit.LifecycleCapture
	hooks, err := CaptureHooks(runnerunit.SubmissionHooks{
		SelectLifecycle: func(ctx context.Context, _ *executionledger.Ledger, _ executionledger.Creator, selection runnerunit.LifecycleSelection) (runnerunit.LifecycleCapture, error) {
			selected = true
			assertNoPublication(t, directory)
			deadline, present := ctx.Deadline()
			require.True(t, present)
			require.NotEqual(t, deadline.UTC().Format(time.RFC3339Nano), selection.Deadline,
				"detached ten-second publication deadline must not replace original invocation")
			staged = stageLifecycle(t, selection)
			return staged, nil
		},
		Terminal: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator,
			reservation executionledger.Reservation, terminal runnerterminal.Envelope) error {
			require.True(t, selected)
			require.True(t, validated)
			observed = true
			assertNoPublication(t, directory)
			require.Equal(t, staged.ReservationDigest, reservation.Digest)
			require.Equal(t, *staged.Observations[1].Terminal, terminal)
			return nil
		},
	})
	require.NoError(t, err)
	validate := hooks.ValidatePublication
	hooks.ValidatePublication = func(ctx context.Context, original runnerunit.SubmissionSnapshot, publication runnerunit.SubmissionPublication) error {
		require.True(t, selected)
		require.False(t, observed)
		validated = true
		return validate(ctx, original, publication)
	}
	service := runnerunit.NewService(nil)
	request := captureRequest(t)
	result, err := service.Submit(t.Context(), captureWire(t, request, nil), backend, hooks)
	require.NoError(t, err)
	require.True(t, observed)
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
	complete, err := ledger.ReadCompleted(t.Context(), source, "terminal.json", "execution-attempt.json",
		"lifecycle.json", "lifecycle-observation-001.json", "lifecycle-observation-002.json")
	require.NoError(t, err)
	var capture runnerunit.LifecycleCapture
	require.NoError(t, json.Unmarshal(complete.Publication.ArtifactBytes["lifecycle.json"], &capture))
	require.Equal(t, staged, capture)
	for i, name := range []string{"lifecycle-observation-001.json", "lifecycle-observation-002.json"} {
		require.Equal(t, capture.ObservationDigests[i], artifactmanifest.Hash(complete.Publication.ArtifactBytes[name]))
	}
	terminal, err := runnerterminal.Encode(*capture.Observations[1].Terminal, complete.Reservation.Attempt.Binding.TerminalBinding())
	require.NoError(t, err)
	require.Equal(t, terminal, complete.Publication.ArtifactBytes["terminal.json"])
	request.Selection.Attempt++
	reuseHooks, err := CaptureHooks(runnerunit.SubmissionHooks{
		SelectLifecycle: func(context.Context, *executionledger.Ledger, executionledger.Creator, runnerunit.LifecycleSelection) (runnerunit.LifecycleCapture, error) {
			t.Fatal("reuse must not select or seal new lifecycle metadata")
			return runnerunit.LifecycleCapture{}, nil
		},
		Factory: func(models.Config) (execution.AgentEngine, error) {
			t.Fatal("reuse must not invoke per-submission factory")
			return nil, nil
		},
	})
	require.NoError(t, err)
	reused, err := service.Submit(t.Context(), captureWire(t, request, &source), backend, reuseHooks)
	require.NoError(t, err)
	require.Zero(t, reused.EngineCalls)
	require.Equal(t, result.Terminal, reused.Terminal)
}

func TestOwnedLifecycleSelectionRejectsContextFactsAndIncompleteMetadataBeforePublication(t *testing.T) {
	for _, control := range []string{"error", "deadline", "digest", "terminal", "process", "overflow", "nil-runtime", "invalid-native"} {
		t.Run(control, func(t *testing.T) {
			directory := t.TempDir()
			backend, err := artifactmanifest.OpenLocal(directory)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, backend.Close()) })
			hooks, err := CaptureHooks(runnerunit.SubmissionHooks{
				SelectLifecycle: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, selection runnerunit.LifecycleSelection) (runnerunit.LifecycleCapture, error) {
					capture := stageLifecycle(t, selection)
					switch control {
					case "error":
						return capture, errors.New("prepare/close failure")
					case "deadline":
						capture.Deadline = ""
					case "digest":
						capture.ObservationDigests[0] = strings.Repeat("a", 64)
					case "terminal":
						capture.Observations[1].Terminal.ExitCode = 2
					case "process":
						capture.Runtime.ProcessExit = "exited"
					case "overflow":
						capture.Runtime.Dropped = 1
					case "nil-runtime":
						capture.Runtime.Observations = nil
					case "invalid-native":
						capture.Runtime.OwnedEngineBound = true
						capture.Runtime.Observations = []execution.RuntimeObservation{{
							Sequence: 1, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
							Operation: "client-start-returned", Scope: "sdk-client", Result: "arbitrary private text",
						}}
					}
					return capture, nil
				},
				Terminal: func(context.Context, *executionledger.Ledger, executionledger.Creator,
					executionledger.Reservation, runnerterminal.Envelope) error {
					t.Fatal("invalid staged metadata must not reach terminal verification")
					return nil
				},
			})
			require.NoError(t, err)
			result, err := runnerunit.NewService(nil).Submit(t.Context(), captureWire(t, captureRequest(t), nil), backend, hooks)
			require.Error(t, err)
			require.Nil(t, result.Completion)
			require.Nil(t, result.Terminal)
			assertNoPublication(t, directory)
		})
	}
}

func TestOwnedLifecyclePreservesFailedCanceledAndTimeoutOriginals(t *testing.T) {
	for _, control := range []string{"grader-failure", "canceled", "timeout"} {
		t.Run(control, func(t *testing.T) {
			backend := captureBackend(t)
			request := captureRequest(t)
			wantCode := 1
			service := runnerunit.NewService(nil)
			if control == "grader-failure" {
				request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- missing-lifecycle-control", 1)
				require.Contains(t, request.Input.Task, "missing-lifecycle-control")
			} else {
				cause := context.Canceled
				wantCode = 3
				if control == "timeout" {
					cause, wantCode = context.DeadlineExceeded, 4
				}
				service = runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) { return nil, cause })
			}
			var staged runnerunit.LifecycleCapture
			hooks, err := CaptureHooks(runnerunit.SubmissionHooks{
				SelectLifecycle: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator,
					selection runnerunit.LifecycleSelection) (runnerunit.LifecycleCapture, error) {
					staged = stageLifecycle(t, selection)
					return staged, nil
				},
				Terminal: func(context.Context, *executionledger.Ledger, executionledger.Creator,
					executionledger.Reservation, runnerterminal.Envelope) error {
					// Canonical selected bytes must be detached before later host callbacks.
					staged.ObservationDigests[0] = "changed-after-preparation"
					staged.Observations[1].Terminal.ExitCode = 6
					return nil
				},
			})
			require.NoError(t, err)
			result, err := service.Submit(t.Context(), captureWire(t, request, nil), backend, hooks)
			require.NoError(t, err)
			require.Equal(t, wantCode, result.ExitCode)
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
			completed, err := ledger.ReadCompleted(t.Context(), source, "lifecycle.json", "terminal.json")
			require.NoError(t, err)
			var capture runnerunit.LifecycleCapture
			require.NoError(t, json.Unmarshal(completed.Publication.ArtifactBytes["lifecycle.json"], &capture))
			require.Equal(t, *result.Terminal, *capture.Observations[1].Terminal)
			require.NotEqual(t, staged.ObservationDigests, capture.ObservationDigests)
			request.Selection.Attempt++
			reused, err := service.Submit(t.Context(), captureWire(t, request, &source), backend, hooks)
			require.NoError(t, err)
			require.Equal(t, result.Terminal, reused.Terminal)
			require.Zero(t, reused.EngineCalls)
		})
	}
}

func TestLifecycleSelectorRejectsOrphanOrArbitraryArtifactPolicyBeforeReservation(t *testing.T) {
	for _, control := range []string{"orphan", "arbitrary"} {
		t.Run(control, func(t *testing.T) {
			directory := t.TempDir()
			backend, err := artifactmanifest.OpenLocal(directory)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, backend.Close()) })
			hooks := runnerunit.SubmissionHooks{
				SelectLifecycle: func(context.Context, *executionledger.Ledger, executionledger.Creator,
					runnerunit.LifecycleSelection) (runnerunit.LifecycleCapture, error) {
					t.Fatal("invalid composition must not reach selector")
					return runnerunit.LifecycleCapture{}, nil
				},
			}
			if control == "arbitrary" {
				hooks, err = CaptureHooks(hooks)
				require.NoError(t, err)
				hooks.Artifacts = func(context.Context, *executionledger.Ledger, executionledger.Creator,
					runnerunit.SubmissionSnapshot) ([]runnerunit.SubmissionArtifact, error) {
					t.Fatal("arbitrary raw-report callback must not reach protected composition")
					return nil, nil
				}
			}
			result, err := runnerunit.NewService(nil).Submit(t.Context(), captureWire(t, captureRequest(t), nil), backend, hooks)
			require.Error(t, err)
			require.Nil(t, result.Reservation)
			require.Zero(t, result.EngineCalls)
			entries, err := os.ReadDir(directory)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestSubmitFactoriesRemainPerInvocationOnSharedService(t *testing.T) {
	service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
		t.Fatal("shared fallback factory must not capture or replace per-invocation ownership")
		return nil, nil
	})
	backend := captureBackend(t)
	var counts [2]atomic.Int32
	var joined sync.WaitGroup
	for i := range counts {
		joined.Go(func() {
			hooks, err := CaptureHooks(runnerunit.SubmissionHooks{Factory: func(cfg models.Config) (execution.AgentEngine, error) {
				counts[i].Add(1)
				return execution.NewMockEngine(cfg.ModelID), nil
			}})
			require.NoError(t, err)
			result, err := service.Submit(t.Context(), captureWire(t, captureRequest(t), nil), backend, hooks)
			require.NoError(t, err)
			require.NotNil(t, result.Completion)
		})
	}
	joined.Wait()
	require.EqualValues(t, 1, counts[0].Load())
	require.EqualValues(t, 1, counts[1].Load())
}
