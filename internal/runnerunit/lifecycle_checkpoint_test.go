package runnerunit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestLifecycleObserverActualProtectedCheckpointAndRestore(t *testing.T) {
	fixture := lifecycleFixtureBytes(t)
	for _, control := range []string{"success", "failed", "canceled-after-before", "terminal-observer-failure"} {
		t.Run(control, func(t *testing.T) {
			backend, err := artifactmanifest.OpenLocal(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, backend.Close()) })
			clock := time.Date(2026, 11, 7, 17, 0, 0, 0, time.UTC)
			observer, err := runnerunit.NewLifecycleObserver(backend, func() time.Time {
				at := clock
				clock = clock.Add(time.Second)
				return at
			})
			require.NoError(t, err)
			request, err := runnercontract.Parse(bytes.Clone(fixture))
			require.NoError(t, err)
			if control == "failed" {
				original := request.Input.Task
				request.Input.Task = strings.Replace(original, "    - hello\n", "    - absent-lifecycle-checkpoint-control\n", 1)
				require.NotEqual(t, original, request.Input.Task)
			}
			requestBytes, err := json.Marshal(request)
			require.NoError(t, err)
			wire, err := json.Marshal(runnerunit.SubmissionRequest{
				Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: requestBytes,
			})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			factories := 0
			service, err := runnercheckpoint.NewService(runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
				factories++
				capture, err := observer.RuntimeObservations()
				require.NoError(t, err, "the real checkpoint caller must reserve before selecting its observer")
				require.NotNil(t, capture)
				return execution.NewMockEngine(config.ModelID), nil
			}), strings.Repeat("c", 40))
			require.NoError(t, err)
			observedTerminals := 0
			next := observer.Hooks()
			terminal := next.Terminal
			observerFailure := errors.New("controlled lifecycle checkpoint observation failure")
			next.Terminal = func(ctx context.Context, ledger *executionledger.Ledger,
				creator executionledger.Creator, reservation executionledger.Reservation, envelope runnerterminal.Envelope,
			) error {
				observedTerminals++
				for _, diagnostic := range envelope.Diagnostics {
					require.True(t, diagnostic.Message == "" || diagnostic.Message == "[REDACTED]")
				}
				if err := terminal(ctx, ledger, creator, reservation, envelope); err != nil {
					return err
				}
				if control == "terminal-observer-failure" {
					return observerFailure
				}
				return nil
			}
			protected, err := ownedsubmission.CaptureHooks(next)
			require.NoError(t, err)
			require.Nil(t, protected.Artifacts, "public protection must not select arbitrary lifecycle artifacts")
			published := []runnercheckpoint.Reference{}
			hooks := runnercheckpoint.Hooks{
				Submission: protected,
				Published: func(_ context.Context, reference runnercheckpoint.Reference) error {
					published = append(published, reference)
					if reference.Boundary == "before-unit" && control == "canceled-after-before" {
						cancel()
					}
					return nil
				},
			}
			result, err := service.Submit(ctx, wire, backend, hooks)
			require.NotNil(t, result.Submission)
			require.NotEmpty(t, result.BeforeDigest)
			require.Equal(t, 1, observedTerminals)
			require.NotNil(t, result.Submission.Terminal)
			if control == "terminal-observer-failure" {
				require.ErrorIs(t, err, observerFailure)
				require.Equal(t, "submission_error", result.Status)
				require.Equal(t, 2, result.ExitCode)
				require.Equal(t, runnerterminal.Success, result.Submission.Terminal.Outcome)
				require.Equal(t, 1, factories)
				require.Equal(t, 1, result.Submission.EngineCalls)
				require.Nil(t, result.Submission.Completion)
				require.Empty(t, result.FinalizedDigest)
				require.Len(t, published, 1)
				require.Nil(t, result.RestoredUnits)
				require.Nil(t, result.RemainingWork)
				ledger, err := executionledger.New(backend)
				require.NoError(t, err)
				_, err = ledger.ReadCompletion(t.Context(), result.Submission.Reservation.ExecutionID)
				require.ErrorIs(t, err, artifactmanifest.ErrMissing)
				return
			}
			require.NoError(t, err)
			wantOutcome, wantCode, wantFactories := runnerterminal.Success, 0, 1
			if control == "failed" {
				wantOutcome, wantCode = runnerterminal.EvaluationFailure, 1
			}
			if control == "canceled-after-before" {
				wantOutcome, wantCode, wantFactories = runnerterminal.Canceled, 3, 0
			}
			require.Equal(t, wantOutcome, result.Submission.Terminal.Outcome)
			require.Equal(t, wantCode, result.ExitCode)
			require.Equal(t, wantFactories, factories)
			require.NotEmpty(t, result.FinalizedDigest)
			require.Len(t, published, 2)
			require.Equal(t, "before-unit", published[0].Boundary)
			require.Equal(t, "finalized-unit", published[1].Boundary)
			beforeBytes, err := backend.Get(t.Context(), "blob-"+result.BeforeDigest, executionlifecycle.MaxBytes)
			require.NoError(t, err)
			finalizedBytes, err := backend.Get(t.Context(), "blob-"+result.FinalizedDigest, executionlifecycle.MaxBytes)
			require.NoError(t, err)
			require.NotEmpty(t, beforeBytes)
			require.NotEmpty(t, finalizedBytes)
			inspection, err := observer.Inspection(t.Context(), "", 60000)
			require.NoError(t, err)
			inspectionBytes, err := json.Marshal(inspection)
			require.NoError(t, err)
			projection, err := executionlifecycle.Inspect(t.Context(), backend, inspectionBytes)
			require.NoError(t, err)
			require.Equal(t, "caller-declared-unverified", projection.Evidence)
			require.Equal(t, wantOutcome, projection.Terminal.Outcome)
			require.Equal(t, "none", projection.Cancellation)
			require.Empty(t, projection.Heartbeat)
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			source := executionattempt.Source{
				ExecutionID: result.Submission.Reservation.ExecutionID, ManifestDigest: result.Submission.Completion.ManifestDigest,
			}
			original, err := ledger.ReadCompleted(t.Context(), source, "terminal.json")
			require.NoError(t, err)
			originalTerminal := bytes.Clone(original.Publication.ArtifactBytes["terminal.json"])
			require.NotEmpty(t, originalTerminal)
			for _, artifact := range original.Publication.Manifest.Artifacts {
				require.NotEqual(t, runnerunit.LifecycleCaptureArtifact, artifact.Name)
				require.NotEqual(t, "report.json", artifact.Name)
			}
			request.Selection.Attempt++
			requestBytes, err = json.Marshal(request)
			require.NoError(t, err)
			wire, err = json.Marshal(runnerunit.SubmissionRequest{
				Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: requestBytes,
			})
			require.NoError(t, err)
			restored, err := service.Resume(t.Context(), wire, backend, result.FinalizedDigest, hooks)
			require.NoError(t, err)
			require.Equal(t, wantCode, restored.ExitCode)
			require.Equal(t, wantFactories, factories)
			require.Equal(t, 1, observedTerminals)
			require.Len(t, published, 2)
			require.Zero(t, restored.Submission.EngineCalls)
			require.Equal(t, result.Submission.Terminal, restored.Submission.Terminal)
			require.Equal(t, result.Submission.Reservation.ExecutionID, restored.Submission.Reservation.ExecutionID)
			require.NotNil(t, restored.RestoredUnits)
			require.NotNil(t, restored.RemainingWork)
			require.Equal(t, 1, *restored.RestoredUnits)
			require.Equal(t, 0, *restored.RemainingWork)
			restoredTerminal, err := runnerterminal.Encode(*restored.Submission.Terminal, restored.Submission.Reservation.Binding.TerminalBinding())
			require.NoError(t, err)
			require.Equal(t, originalTerminal, restoredTerminal)
			reread, err := ledger.ReadCompleted(t.Context(), source, "terminal.json")
			require.NoError(t, err)
			require.Equal(t, originalTerminal, reread.Publication.ArtifactBytes["terminal.json"])
			restoredBefore, err := backend.Get(t.Context(), "blob-"+result.BeforeDigest, executionlifecycle.MaxBytes)
			require.NoError(t, err)
			restoredFinalized, err := backend.Get(t.Context(), "blob-"+result.FinalizedDigest, executionlifecycle.MaxBytes)
			require.NoError(t, err)
			require.Equal(t, beforeBytes, restoredBefore)
			require.Equal(t, finalizedBytes, restoredFinalized)
			_, err = runnercheckpoint.EncodeResult(restored)
			require.NoError(t, err)
		})
	}
}
