package runnerunit

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

func TestLifecycleArtifactsBoundToProtectedOriginalManifest(t *testing.T) {
	backend := submissionBackend(t)
	observer, err := NewLifecycleObserver(backend, lifecycleClock())
	require.NoError(t, err)
	request := fixture(t)
	before := request.Input.Task
	request.Input.Task = strings.Replace(before, "    - hello\n", "    - impossible-output-control\n", 1)
	require.NotEqual(t, before, request.Input.Task)
	hooks := observer.Hooks()
	hooks.PreparePublication = func(_ context.Context, _ *executionledger.Ledger,
		_ executionledger.Creator, snapshot SubmissionSnapshot) (SubmissionPublication, error) {
		require.Equal(t, runnerterminal.EvaluationFailure, snapshot.Terminal.Outcome)
		require.NotEmpty(t, snapshot.Report, "actual private producer report exists before protection")
		require.NotEmpty(t, snapshot.Terminal.Diagnostics)
		snapshot.Terminal.Diagnostics[0].Message = "[REDACTED]"
		return SubmissionPublication{Terminal: snapshot.Terminal}, nil
	}
	hooks.Artifacts = func(ctx context.Context, ledger *executionledger.Ledger,
		creator executionledger.Creator, snapshot SubmissionSnapshot) ([]SubmissionArtifact, error) {
		require.Equal(t, "[REDACTED]", snapshot.Terminal.Diagnostics[0].Message)
		snapshot.Report = json.RawMessage(`{"private":"PRIVATE_LIFECYCLE_REPORT_CANARY"}`)
		return observer.Artifacts(ctx, ledger, creator, snapshot)
	}
	service := NewService(nil)
	result, err := service.Submit(t.Context(), submissionWire(t, request, nil), backend, hooks)
	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 1, result.ExitCode)
	require.Empty(t, result.Report)
	require.NotNil(t, result.Completion)
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID,
		ManifestDigest: result.Completion.ManifestDigest}
	completed, err := ledger.ReadCompleted(t.Context(), source, LifecycleCaptureArtifact, "terminal.json")
	require.NoError(t, err)
	require.Len(t, completed.Publication.Manifest.Artifacts, 2)
	data := completed.Publication.ArtifactBytes[LifecycleCaptureArtifact]
	require.NotEmpty(t, data)
	require.NotContains(t, string(data), "PRIVATE_LIFECYCLE_REPORT_CANARY")
	var capture LifecycleCapture
	require.NoError(t, json.Unmarshal(data, &capture))
	require.Equal(t, "waza.execution-lifecycle-capture", capture.Kind)
	require.Equal(t, completed.Reservation.Bytes, []byte(capture.Reservation))
	require.Equal(t, completed.Reservation.Digest, capture.ReservationDigest)
	require.Equal(t, completed.Reservation.Attempt.Binding, capture.OriginalAdmission)
	require.Len(t, capture.Observations, 2)
	require.Len(t, capture.ObservationDigests, 2)
	for i, observation := range capture.Observations {
		encoded, err := executionlifecycle.Encode(observation)
		require.NoError(t, err)
		require.Equal(t, capture.ObservationDigests[i], artifactmanifest.Hash(encoded))
	}
	terminal, err := runnerterminal.Encode(*capture.Observations[1].Terminal,
		completed.Reservation.Attempt.Binding.TerminalBinding())
	require.NoError(t, err)
	require.NotEmpty(t, completed.Publication.ArtifactBytes["terminal.json"])
	require.Equal(t, completed.Publication.ArtifactBytes["terminal.json"], terminal)
	require.Equal(t, runnerterminal.EvaluationFailure, capture.Observations[1].Terminal.Outcome)
	require.Equal(t, "[REDACTED]", capture.Observations[1].Terminal.Diagnostics[0].Message)
	require.Equal(t, "unknown", capture.Runtime.ProcessExit)
	require.False(t, capture.Runtime.OwnedEngineBound, "real mock producer does not invent native execution")

	originalCapture := bytes.Clone(data)
	request.Selection.Attempt++
	reused, err := service.Submit(t.Context(), submissionWire(t, request,
		&SubmissionPolicy{Mode: "reuse", Source: &source}), backend, observer.Hooks())
	require.NoError(t, err)
	require.Zero(t, reused.EngineCalls)
	require.Equal(t, result.Terminal, reused.Terminal)
	reread, err := ledger.ReadCompleted(t.Context(), source, LifecycleCaptureArtifact)
	require.NoError(t, err)
	require.Equal(t, originalCapture, reread.Publication.ArtifactBytes[LifecycleCaptureArtifact])
}

func TestLifecycleArtifactsRejectRetaggingBeforeCompletion(t *testing.T) {
	for _, control := range []string{"reservation", "terminal", "blob"} {
		t.Run(control, func(t *testing.T) {
			backend := submissionBackend(t)
			observer, err := NewLifecycleObserver(backend, lifecycleClock())
			require.NoError(t, err)
			request := fixture(t)
			before := request.Input.Task
			request.Input.Task = strings.Replace(before, "    - hello\n", "    - impossible-output-control\n", 1)
			require.NotEqual(t, before, request.Input.Task)
			hooks := observer.Hooks()
			hooks.Artifacts = func(ctx context.Context, ledger *executionledger.Ledger,
				creator executionledger.Creator, snapshot SubmissionSnapshot) ([]SubmissionArtifact, error) {
				switch control {
				case "reservation":
					snapshot.Reservation.Digest = strings.Repeat("b", 64)
				case "terminal":
					require.NotEmpty(t, snapshot.Terminal.Diagnostics)
					snapshot.Terminal.Diagnostics[0].Message += " changed-after-protection"
				case "blob":
					observer.digests[0] = strings.Repeat("c", 64)
				}
				return observer.Artifacts(ctx, ledger, creator, snapshot)
			}
			result, err := NewService(nil).Submit(t.Context(), submissionWire(t, request, nil), backend, hooks)
			require.Error(t, err)
			require.Equal(t, "submission_error", result.Status)
			require.Equal(t, 2, result.ExitCode)
			require.Nil(t, result.Completion)
			require.NotNil(t, result.Terminal)
			require.Equal(t, runnerterminal.EvaluationFailure, result.Terminal.Outcome)
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			_, err = ledger.ReadCompletion(t.Context(), result.Reservation.ExecutionID)
			require.ErrorIs(t, err, artifactmanifest.ErrMissing)
		})
	}
}
