package runnerunit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func lifecycleFixtureBytes(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	return data
}

func assertLifecycleUnpublished(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	for _, entry := range entries {
		for _, prefix := range []string{"blob-", "manifest-", "final-"} {
			require.False(t, strings.HasPrefix(entry.Name(), prefix), entry.Name())
		}
	}
}

func TestPreparedLifecycleObserverRequiresExplicitClock(t *testing.T) {
	observer, err := runnerunit.NewPreparedLifecycleObserver(nil)
	require.Error(t, err)
	require.Nil(t, observer)
}

func TestPreparedLifecycleObserverActualCheckpointCancellationAndFinalizedRestore(t *testing.T) {
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	observer, err := runnerunit.NewPreparedLifecycleObserver(time.Now)
	require.NoError(t, err)
	next := observer.Hooks()
	next.Factory = func(models.Config) (execution.AgentEngine, error) {
		t.Fatal("cancellation after before-unit publication must not construct an engine")
		return nil, nil
	}
	protected, err := ownedsubmission.CaptureHooks(next)
	require.NoError(t, err)
	service, err := runnercheckpoint.NewService(runnerunit.NewService(nil), strings.Repeat("c", 40))
	require.NoError(t, err)
	wire, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: lifecycleFixtureBytes(t),
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	published := []runnercheckpoint.Reference{}
	hooks := runnercheckpoint.Hooks{
		Submission: protected,
		Published: func(_ context.Context, reference runnercheckpoint.Reference) error {
			published = append(published, reference)
			if reference.Boundary == "before-unit" {
				cancel()
			}
			return nil
		},
	}
	result, err := service.Submit(ctx, wire, backend, hooks)
	require.NoError(t, err)
	require.Len(t, published, 2)
	require.Equal(t, "before-unit", published[0].Boundary)
	require.Equal(t, "finalized-unit", published[1].Boundary)
	require.Equal(t, runnerterminal.Canceled, result.Submission.Terminal.Outcome)
	require.Equal(t, 3, result.ExitCode)
	require.Zero(t, result.Submission.EngineCalls)
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: result.Submission.Reservation.ExecutionID,
		ManifestDigest: result.Submission.Completion.ManifestDigest}
	completed, err := ledger.ReadCompleted(t.Context(), source, runnerunit.LifecycleCaptureArtifact, "terminal.json")
	require.NoError(t, err)
	var capture runnerunit.LifecycleCapture
	require.NoError(t, json.Unmarshal(completed.Publication.ArtifactBytes[runnerunit.LifecycleCaptureArtifact], &capture))
	require.Equal(t, runnerterminal.Canceled, capture.Observations[1].Terminal.Outcome)
	require.False(t, capture.Runtime.OwnedEngineBound)
	require.Equal(t, "unknown", capture.Runtime.ProcessExit)
	before, err := backend.Get(t.Context(), "blob-"+result.BeforeDigest, executionlifecycle.MaxBytes)
	require.NoError(t, err)
	finalized, err := backend.Get(t.Context(), "blob-"+result.FinalizedDigest, executionlifecycle.MaxBytes)
	require.NoError(t, err)
	restored, err := service.Resume(t.Context(), wire, backend, result.FinalizedDigest, hooks)
	require.NoError(t, err)
	require.Equal(t, result.Submission.Terminal, restored.Submission.Terminal)
	require.Zero(t, restored.Submission.EngineCalls)
	require.Equal(t, 3, restored.ExitCode)
	require.Len(t, published, 2)
	reread, err := ledger.ReadCompleted(t.Context(), source, runnerunit.LifecycleCaptureArtifact, "terminal.json")
	require.NoError(t, err)
	require.Equal(t, completed.Publication.ArtifactBytes, reread.Publication.ArtifactBytes)
	afterBefore, err := backend.Get(t.Context(), "blob-"+result.BeforeDigest, executionlifecycle.MaxBytes)
	require.NoError(t, err)
	afterFinalized, err := backend.Get(t.Context(), "blob-"+result.FinalizedDigest, executionlifecycle.MaxBytes)
	require.NoError(t, err)
	require.Equal(t, before, afterBefore)
	require.Equal(t, finalized, afterFinalized)
}

func TestPreparedLifecycleObserverConcurrentInvocationFactories(t *testing.T) {
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
		return nil, errors.New("shared fallback factory must not be selected")
	})
	captures := make(chan *execution.OwnedRuntimeObservations, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		unblock()
		workers.Wait()
	})
	type response struct {
		submission runnerunit.Submission
		err        error
	}
	results := make(chan response, 2)
	for i := range 2 {
		request, err := runnercontract.Parse(lifecycleFixtureBytes(t))
		require.NoError(t, err)
		request.Selection.Attempt += i
		request.TimeoutMS = 10000
		data, err := json.Marshal(request)
		require.NoError(t, err)
		wire, err := json.Marshal(runnerunit.SubmissionRequest{
			Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: data,
		})
		require.NoError(t, err)
		observer, err := runnerunit.NewPreparedLifecycleObserver(time.Now)
		require.NoError(t, err)
		next := observer.Hooks()
		next.Factory = func(config models.Config) (execution.AgentEngine, error) {
			capture, err := observer.RuntimeObservations()
			if err != nil {
				return nil, err
			}
			captures <- capture
			<-release
			return execution.NewMockEngine(config.ModelID), nil
		}
		hooks, err := ownedsubmission.CaptureHooks(next)
		require.NoError(t, err)
		workers.Add(1)
		go func() {
			defer workers.Done()
			submission, err := service.Submit(ctx, wire, backend, hooks)
			results <- response{submission: submission, err: err}
		}()
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	sinks := []*execution.OwnedRuntimeObservations{}
	for len(sinks) != 2 {
		select {
		case sink := <-captures:
			sinks = append(sinks, sink)
		case result := <-results:
			t.Fatalf("submission returned before its factory barrier: %v", result.err)
		case <-deadline.C:
			t.Fatal("both invocation factories did not reach the deterministic barrier")
		}
	}
	require.NotSame(t, sinks[0], sinks[1])
	unblock()
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	ids := []string{}
	for range 2 {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			require.Equal(t, runnerterminal.Success, result.submission.Terminal.Outcome)
			source := executionattempt.Source{ExecutionID: result.submission.Reservation.ExecutionID,
				ManifestDigest: result.submission.Completion.ManifestDigest}
			completed, err := ledger.ReadCompleted(t.Context(), source, runnerunit.LifecycleCaptureArtifact)
			require.NoError(t, err)
			var capture runnerunit.LifecycleCapture
			require.NoError(t, json.Unmarshal(completed.Publication.ArtifactBytes[runnerunit.LifecycleCaptureArtifact], &capture))
			require.Equal(t, source.ExecutionID, capture.Observations[0].ExecutionID)
			require.Equal(t, source.ExecutionID, capture.Observations[1].ExecutionID)
			require.Equal(t, completed.Reservation.Bytes, []byte(capture.Reservation))
			require.Equal(t, "unknown", capture.Runtime.ProcessExit)
			ids = append(ids, source.ExecutionID)
		case <-deadline.C:
			t.Fatal("released submission did not publish within the caller deadline")
		}
	}
	require.NotEqual(t, ids[0], ids[1])
}

func TestPreparedLifecycleObserverActualSinglePublicationAndReuse(t *testing.T) {
	for _, control := range []string{"success", "failed", "canceled", "timeout", "publication-failure", "retagged", "deadline", "clock"} {
		t.Run(control, func(t *testing.T) {
			directory := t.TempDir()
			backend, err := artifactmanifest.OpenLocal(directory)
			require.NoError(t, err)
			closed := false
			t.Cleanup(func() {
				if !closed {
					require.NoError(t, backend.Close())
				}
			})
			at := time.Date(2026, 11, 7, 17, 0, 0, 0, time.UTC)
			observer, err := runnerunit.NewPreparedLifecycleObserver(func() time.Time {
				if control == "clock" {
					return time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				current := at
				at = at.Add(time.Second)
				return current
			})
			require.NoError(t, err)
			_, err = observer.RuntimeObservations()
			require.ErrorIs(t, err, executionledger.ErrCapability)
			request, err := runnercontract.Parse(lifecycleFixtureBytes(t))
			require.NoError(t, err)
			if control == "failed" {
				original := request.Input.Task
				request.Input.Task = strings.Replace(original, "    - hello\n", "    - absent-prepared-output-control\n", 1)
				require.NotEqual(t, original, request.Input.Task)
			}
			data, err := json.Marshal(request)
			require.NoError(t, err)
			wire, err := json.Marshal(runnerunit.SubmissionRequest{
				Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: data,
			})
			require.NoError(t, err)
			calls, selectors, terminals := 0, 0, 0
			service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
				t.Fatal("shared default factory must not capture invocation-owned metadata")
				return nil, nil
			})
			next := observer.Hooks()
			next.Factory = func(config models.Config) (execution.AgentEngine, error) {
				calls++
				capture, err := observer.RuntimeObservations()
				require.NoError(t, err)
				require.NotNil(t, capture)
				if control == "canceled" {
					return nil, context.Canceled
				}
				if control == "timeout" {
					return nil, context.DeadlineExceeded
				}
				return execution.NewMockEngine(config.ModelID), nil
			}
			var staged runnerunit.LifecycleCapture
			selectLifecycle := next.SelectLifecycle
			next.SelectLifecycle = func(ctx context.Context, ledger *executionledger.Ledger,
				creator executionledger.Creator, selection runnerunit.LifecycleSelection,
			) (runnerunit.LifecycleCapture, error) {
				selectors++
				assertLifecycleUnpublished(t, directory)
				if control == "deadline" {
					selection.Deadline = "2026-11-07T17:00:00Z"
				}
				capture, err := selectLifecycle(ctx, ledger, creator, selection)
				if err != nil {
					return capture, err
				}
				staged = capture
				if control == "retagged" {
					capture.ObservationDigests[0] = strings.Repeat("b", 64)
				}
				return capture, nil
			}
			terminal := next.Terminal
			next.Terminal = func(ctx context.Context, ledger *executionledger.Ledger,
				creator executionledger.Creator, reservation executionledger.Reservation, envelope runnerterminal.Envelope,
			) error {
				terminals++
				assertLifecycleUnpublished(t, directory)
				staged.Reservation[0] = 'X'
				staged.ObservationDigests[0] = strings.Repeat("c", 64)
				if err := terminal(ctx, ledger, creator, reservation, envelope); err != nil {
					return err
				}
				if control == "publication-failure" {
					require.NoError(t, backend.Close())
					closed = true
				}
				return nil
			}
			hooks, err := ownedsubmission.CaptureHooks(next)
			require.NoError(t, err)
			require.Nil(t, hooks.Artifacts)
			originalNames := []string{}
			validate := hooks.ValidatePublication
			hooks.ValidatePublication = func(ctx context.Context, snapshot runnerunit.SubmissionSnapshot,
				publication runnerunit.SubmissionPublication,
			) error {
				require.Equal(t, 1, selectors)
				require.Zero(t, terminals)
				assertLifecycleUnpublished(t, directory)
				for _, artifact := range publication.Artifacts {
					originalNames = append(originalNames, artifact.Name)
				}
				return validate(ctx, snapshot, publication)
			}
			result, operationErr := service.Submit(t.Context(), wire, backend, hooks)
			require.NotNil(t, result.Reservation, "submission status=%s error=%v diagnostics=%v", result.Status, operationErr, result.Diagnostics)
			if closed {
				backend, err = artifactmanifest.OpenLocal(directory)
				require.NoError(t, err)
				closed = false
			}
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			if control == "clock" || control == "deadline" || control == "retagged" || control == "publication-failure" {
				require.Error(t, operationErr)
				require.Nil(t, result.Completion)
				assertLifecycleUnpublished(t, directory)
				if control == "clock" {
					require.Zero(t, calls)
					require.Zero(t, selectors)
				} else {
					require.Equal(t, 1, calls)
					require.Equal(t, 1, selectors)
				}
				if control == "publication-failure" {
					require.ErrorIs(t, operationErr, os.ErrClosed)
					require.Equal(t, 1, terminals)
					require.Equal(t, runnerterminal.Success, result.Terminal.Outcome)
				} else {
					require.Zero(t, terminals)
				}
				_, err := ledger.ReadCompletion(t.Context(), result.Reservation.ExecutionID)
				require.ErrorIs(t, err, artifactmanifest.ErrMissing)
				return
			}
			require.NoError(t, operationErr)
			require.Equal(t, 1, calls)
			require.Equal(t, 1, selectors)
			require.Equal(t, 1, terminals)
			wantOutcome, wantCode := runnerterminal.Success, 0
			switch control {
			case "failed":
				wantOutcome, wantCode = runnerterminal.EvaluationFailure, 1
			case "canceled":
				wantOutcome, wantCode = runnerterminal.Canceled, 3
			case "timeout":
				wantOutcome, wantCode = runnerterminal.Timeout, 4
			}
			require.Equal(t, wantOutcome, result.Terminal.Outcome)
			require.Equal(t, wantCode, result.ExitCode)
			source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
			completed, err := ledger.ReadCompleted(t.Context(), source, runnerunit.LifecycleCaptureArtifact,
				"lifecycle-observation-001.json", "lifecycle-observation-002.json", "terminal.json")
			require.NoError(t, err)
			names := []string{}
			for _, artifact := range completed.Publication.Manifest.Artifacts {
				names = append(names, artifact.Name)
			}
			require.Contains(t, originalNames, "execution-attempt.json")
			require.Contains(t, originalNames, "execution-evidence-capture.json")
			require.NotContains(t, originalNames, "report.json")
			require.Contains(t, originalNames, "terminal.json")
			require.ElementsMatch(t, append(originalNames, "lifecycle.json",
				"lifecycle-observation-001.json", "lifecycle-observation-002.json"), names)
			original := bytes.Clone(completed.Publication.ArtifactBytes[runnerunit.LifecycleCaptureArtifact])
			var captured runnerunit.LifecycleCapture
			require.NoError(t, json.Unmarshal(original, &captured))
			require.Equal(t, completed.Reservation.Bytes, []byte(captured.Reservation))
			require.Equal(t, result.Terminal.Outcome, captured.Observations[1].Terminal.Outcome)
			require.NotEmpty(t, captured.Deadline)
			require.Equal(t, "unknown", captured.Runtime.ProcessExit)
			require.False(t, captured.Runtime.OwnedEngineBound)
			for i, name := range []string{"lifecycle-observation-001.json", "lifecycle-observation-002.json"} {
				encoded, err := executionlifecycle.Encode(captured.Observations[i])
				require.NoError(t, err)
				require.Equal(t, encoded, completed.Publication.ArtifactBytes[name])
				require.Equal(t, artifactmanifest.Hash(encoded), captured.ObservationDigests[i])
			}
			encodedTerminal, err := runnerterminal.Encode(*captured.Observations[1].Terminal, result.Reservation.Binding.TerminalBinding())
			require.NoError(t, err)
			require.Equal(t, completed.Publication.ArtifactBytes["terminal.json"], encodedTerminal)
			_, err = observer.RuntimeObservations()
			require.ErrorIs(t, err, executionledger.ErrCapability)
			request.Selection.Attempt++
			data, err = json.Marshal(request)
			require.NoError(t, err)
			wire, err = json.Marshal(runnerunit.SubmissionRequest{
				Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: data,
				Policy: &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &source},
			})
			require.NoError(t, err)
			reused, err := service.Submit(t.Context(), wire, backend, hooks)
			require.NoError(t, err)
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, result.Terminal, reused.Terminal)
			require.Equal(t, 1, calls)
			require.Equal(t, 1, selectors)
			require.Equal(t, 1, terminals)
			reread, err := ledger.ReadCompleted(t.Context(), source, runnerunit.LifecycleCaptureArtifact)
			require.NoError(t, err)
			require.Equal(t, original, reread.Publication.ArtifactBytes[runnerunit.LifecycleCaptureArtifact])
		})
	}
}
