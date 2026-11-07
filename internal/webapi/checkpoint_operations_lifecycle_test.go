package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestCheckpointHTTPActualLifecycleFirstPublicationAndProtectedReuse(t *testing.T) {
	for _, control := range []string{"success", "failed-checks", "canceled"} {
		t.Run(control, func(t *testing.T) {
			wire := checkpointOperationWire(t)
			wantOutcome, wantCode := runnerterminal.Success, 0
			if control == "failed-checks" {
				changed := bytes.Replace(wire, []byte("- hello"), []byte("- absent-http-lifecycle-control"), 1)
				require.NotEqual(t, wire, changed)
				wire = changed
				wantOutcome, wantCode = runnerterminal.EvaluationFailure, 1
			}
			var calls atomic.Int32
			factory := func(config models.Config) (execution.AgentEngine, error) {
				calls.Add(1)
				if control == "canceled" {
					return nil, context.Canceled
				}
				return execution.NewMockEngine(config.ModelID), nil
			}
			if control == "canceled" {
				wantOutcome, wantCode = runnerterminal.Canceled, 3
			}
			revision := strings.Repeat("c", 40)
			baselineBackend, closeBaseline, err := artifactmanifest.OpenBackend(t.TempDir(), "")
			require.NoError(t, err)
			legacy, err := runnercheckpoint.NewService(runnerunit.NewService(factory), revision)
			require.NoError(t, err)
			baseline, err := legacy.SubmitCaptured(t.Context(), wire, baselineBackend, runnercheckpoint.Hooks{})
			require.NoError(t, err)
			require.Equal(t, wantOutcome, baseline.Submission.Terminal.Outcome)
			baselineLedger, err := executionledger.New(baselineBackend)
			require.NoError(t, err)
			original, err := baselineLedger.ReadCompleted(t.Context(), executionattempt.Source{
				ExecutionID:    baseline.Submission.Reservation.ExecutionID,
				ManifestDigest: baseline.Submission.Completion.ManifestDigest,
			})
			require.NoError(t, err)
			names := []string{}
			for _, artifact := range original.Publication.Manifest.Artifacts {
				names = append(names, artifact.Name)
			}
			require.Contains(t, names, "execution-attempt.json")
			require.Contains(t, names, "execution-evidence-capture.json")
			require.NotContains(t, names, runnerunit.LifecycleCaptureArtifact)
			require.NoError(t, closeBaseline())
			require.Equal(t, int32(1), calls.Load())
			calls.Store(0)
			directory := t.TempDir()
			mux := http.NewServeMux()
			registerCheckpointOperationRoutes(mux, checkpointOperations{
				open: func() (artifactmanifest.Backend, func() error, error) {
					return artifactmanifest.OpenBackend(directory, "")
				},
				runner:   runnerunit.NewService(factory),
				revision: func() (string, error) { return revision, nil },
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			created := postCheckpointOperation(t, server, "/api/v1/checkpoints", wire, http.StatusOK)
			require.Equal(t, wantCode, created.ExitCode)
			require.Nil(t, created.Submission.Report)
			require.Equal(t, wantOutcome, created.Submission.Terminal.Outcome)
			require.Equal(t, int32(1), calls.Load())
			backend, closeBackend, err := artifactmanifest.OpenBackend(directory, "")
			require.NoError(t, err)
			defer func() { require.NoError(t, closeBackend()) }()
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			source := executionattempt.Source{
				ExecutionID:    created.Submission.Reservation.ExecutionID,
				ManifestDigest: created.Submission.Completion.ManifestDigest,
			}
			stored, err := ledger.ReadCompleted(t.Context(), source, runnerunit.LifecycleCaptureArtifact,
				"lifecycle-observation-001.json", "lifecycle-observation-002.json", "terminal.json")
			require.NoError(t, err)
			actualNames := []string{}
			for _, artifact := range stored.Publication.Manifest.Artifacts {
				actualNames = append(actualNames, artifact.Name)
			}
			require.ElementsMatch(t, append(names, runnerunit.LifecycleCaptureArtifact,
				"lifecycle-observation-001.json", "lifecycle-observation-002.json"), actualNames)
			var capture runnerunit.LifecycleCapture
			require.NoError(t, json.Unmarshal(stored.Publication.ArtifactBytes[runnerunit.LifecycleCaptureArtifact], &capture))
			require.Equal(t, stored.Reservation.Bytes, []byte(capture.Reservation))
			require.Equal(t, "mock", capture.OriginalAdmission.ExecutionMode)
			require.False(t, capture.Runtime.OwnedEngineBound)
			require.Equal(t, "unknown", capture.Runtime.ProcessExit)
			require.Len(t, capture.Observations, 2)
			require.Equal(t, *created.Submission.Terminal, *capture.Observations[1].Terminal)
			destination := bytes.Replace(wire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
			require.NotEqual(t, wire, destination)
			reused := postCheckpointOperation(t, server, "/api/v1/checkpoints/"+created.FinalizedDigest+"/resume", destination, http.StatusOK)
			require.Equal(t, wantCode, reused.ExitCode)
			require.Zero(t, reused.Submission.EngineCalls)
			require.Equal(t, created.Submission.Terminal, reused.Submission.Terminal)
			require.Equal(t, created.Submission.Reservation, reused.Submission.Reservation)
			require.Equal(t, int32(1), calls.Load())
			after, err := ledger.ReadCompleted(t.Context(), source, runnerunit.LifecycleCaptureArtifact,
				"lifecycle-observation-001.json", "lifecycle-observation-002.json", "terminal.json")
			require.NoError(t, err)
			require.Equal(t, stored.Publication.ArtifactBytes, after.Publication.ArtifactBytes)
		})
	}
}
