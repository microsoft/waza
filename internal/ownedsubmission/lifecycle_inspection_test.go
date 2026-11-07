package ownedsubmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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

type lifecycleReadBackend struct {
	artifactmanifest.Backend
	calls   map[string]int
	fail    string
	err     error
	corrupt string
}

func (b *lifecycleReadBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	b.calls[key]++
	if key == b.fail {
		return nil, b.err
	}
	data, err := b.Backend.Get(ctx, key, limit)
	if err == nil && key == b.corrupt && len(data) != 0 {
		data[len(data)-1] ^= 1
	}
	return data, err
}

func lifecycleSourceReader(t *testing.T, backend artifactmanifest.Backend) *executionledger.Reader {
	t.Helper()
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	require.NoError(t, err)
	scoped, err := namespace.Reader(func(read artifactmanifest.ReadBackend) artifactmanifest.ReadBackend { return read })
	require.NoError(t, err)
	reader, err := executionledger.NewReader(scoped, backend)
	require.NoError(t, err)
	return reader
}

func TestLifecycleSourceInspectionActualProtectedOriginalAndSingleSourcePass(t *testing.T) {
	for _, control := range []string{"success", "failed", "canceled"} {
		t.Run(control, func(t *testing.T) {
			backend := captureBackend(t)
			request := captureRequest(t)
			wantOutcome, wantCode := runnerterminal.Success, 0
			if control == "failed" {
				changed := strings.Replace(request.Input.Task, "- hello", "- lifecycle-private-verdict-control", 1)
				require.NotEqual(t, request.Input.Task, changed)
				request.Input.Task = changed
				wantOutcome, wantCode = runnerterminal.EvaluationFailure, 1
			}
			calls := 0
			service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
				calls++
				if control == "canceled" {
					return nil, context.Canceled
				}
				return execution.NewMockEngine(config.ModelID), nil
			})
			if control == "canceled" {
				wantOutcome, wantCode = runnerterminal.Canceled, 3
			}
			result, err := SubmitCaptured(t.Context(), service, captureWire(t, request, nil), backend)
			require.NoError(t, err)
			require.Equal(t, wantCode, result.ExitCode)
			require.Equal(t, wantOutcome, result.Terminal.Outcome)
			require.Equal(t, 1, calls)
			source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
			namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
			require.NoError(t, err)
			scoped, err := namespace.Reader(func(read artifactmanifest.ReadBackend) artifactmanifest.ReadBackend { return read })
			require.NoError(t, err)
			recorded := &lifecycleReadBackend{Backend: backend, calls: map[string]int{}}
			reader, err := executionledger.NewReader(scoped, recorded)
			require.NoError(t, err)
			inspection, err := InspectLifecycle(t.Context(), reader, source, "", 1000)
			require.NoError(t, err)
			require.Equal(t, "verified-nonsnapshot-content", inspection.Verification)
			require.Equal(t, "original-source-declarations", inspection.Context)
			require.Equal(t, source, inspection.Source)
			require.Equal(t, "caller-declared-unverified", inspection.Snapshot.Evidence)
			require.Equal(t, result.Reservation.ExecutionID, inspection.Snapshot.ExecutionID)
			require.Equal(t, "terminal-recorded", inspection.Snapshot.State)
			require.Equal(t, wantOutcome, inspection.Snapshot.Terminal.Outcome)
			require.Equal(t, wantCode, inspection.Snapshot.Terminal.ExitCode)
			require.NotEmpty(t, inspection.Snapshot.Deadline)
			require.Len(t, inspection.Snapshot.Observations, 2)
			require.Equal(t, "none", inspection.Snapshot.Cancellation)
			require.Equal(t, "unknown", inspection.Runtime.ProcessExit)
			require.False(t, inspection.Runtime.OwnedEngineBound)
			require.Empty(t, inspection.Runtime.Observations)
			require.Nil(t, inspection.Snapshot.SuspectedOrphan)
			require.NotEmpty(t, recorded.calls)
			for key, count := range recorded.calls {
				require.Equal(t, 1, count, "source object must not be reread: %s", key)
			}
			data, err := json.Marshal(inspection)
			require.NoError(t, err)
			require.NotContains(t, string(data), "lifecycle-private-verdict-control")
			require.NotContains(t, string(data), `"report"`)
			request.Selection.Attempt++
			reused, err := SubmitCaptured(t.Context(), service, captureWire(t, request, &source), backend)
			require.NoError(t, err)
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, result.Terminal, reused.Terminal)
			require.Equal(t, 1, calls)
			again, err := InspectLifecycle(t.Context(), reader, source, "", 1000)
			require.NoError(t, err)
			require.Equal(t, inspection, again)
		})
	}
}

func TestLifecycleSourceInspectionRetainsCausesAndWithholdsPartialMetadata(t *testing.T) {
	backend := captureBackend(t)
	request := captureRequest(t)
	created, err := SubmitCaptured(t.Context(), runnerunit.NewService(nil), captureWire(t, request, nil), backend)
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: created.Reservation.ExecutionID, ManifestDigest: created.Completion.ManifestDigest}
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	original, err := ledger.ReadCompleted(t.Context(), source, runnerunit.LifecycleCaptureArtifact)
	require.NoError(t, err)
	var capDigest string
	for _, artifact := range original.Publication.Manifest.Artifacts {
		if artifact.Name == runnerunit.LifecycleCaptureArtifact {
			capDigest = artifact.SHA256
		}
	}
	require.NotEmpty(t, capDigest)
	limitErr := errors.New("injected shared read limit")
	for _, control := range []struct {
		name string
		err  error
		key  string
	}{
		{"missing-capture", artifactmanifest.ErrMissing, "blob-" + capDigest},
		{"read-limit", limitErr, "blob-" + capDigest},
		{"missing-final-marker", artifactmanifest.ErrMissing, "final-" + source.ManifestDigest},
	} {
		t.Run(control.name, func(t *testing.T) {
			namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
			require.NoError(t, err)
			scoped, err := namespace.Reader(func(read artifactmanifest.ReadBackend) artifactmanifest.ReadBackend { return read })
			require.NoError(t, err)
			reader, err := executionledger.NewReader(scoped, &lifecycleReadBackend{
				Backend: backend, calls: map[string]int{}, fail: control.key, err: control.err,
			})
			require.NoError(t, err)
			inspection, err := InspectLifecycle(t.Context(), reader, source, "", 1000)
			require.ErrorIs(t, err, control.err)
			require.Equal(t, LifecycleInspection{}, inspection)
		})
	}
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	require.NoError(t, err)
	scoped, err := namespace.Reader(func(read artifactmanifest.ReadBackend) artifactmanifest.ReadBackend { return read })
	require.NoError(t, err)
	corruptReader, err := executionledger.NewReader(scoped, &lifecycleReadBackend{
		Backend: backend, calls: map[string]int{}, corrupt: "blob-" + capDigest,
	})
	require.NoError(t, err)
	corruptInspection, err := InspectLifecycle(t.Context(), corruptReader, source, "", 1000)
	require.ErrorIs(t, err, artifactmanifest.ErrCorrupt)
	require.Equal(t, LifecycleInspection{}, corruptInspection)
	reader := lifecycleSourceReader(t, backend)
	for _, stale := range []int64{0, -1, 86400001} {
		inspection, err := InspectLifecycle(t.Context(), reader, source, "", stale)
		require.ErrorIs(t, err, executionlifecycle.ErrInvalid)
		require.Equal(t, LifecycleInspection{}, inspection)
	}
	inspection, err := InspectLifecycle(t.Context(), nil, source, "", 1000)
	require.ErrorIs(t, err, executionledger.ErrCapability)
	require.Equal(t, LifecycleInspection{}, inspection)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	inspection, err = InspectLifecycle(ctx, reader, source, "", 1000)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, LifecycleInspection{}, inspection)
	after, err := ledger.ReadCompleted(t.Context(), source, runnerunit.LifecycleCaptureArtifact)
	require.NoError(t, err)
	require.True(t, bytes.Equal(original.Publication.ManifestBytes, after.Publication.ManifestBytes))
	require.Equal(t, original.Publication.ArtifactBytes, after.Publication.ArtifactBytes)
}

func TestLifecycleSourceInspectionCanonicalProfileAndOriginalContextControls(t *testing.T) {
	backend := captureBackend(t)
	request := captureRequest(t)
	created, err := SubmitCaptured(t.Context(), runnerunit.NewService(nil), captureWire(t, request, nil), backend)
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: created.Reservation.ExecutionID, ManifestDigest: created.Completion.ManifestDigest}
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	original, err := ledger.ReadCompleted(t.Context(), source, runnerunit.LifecycleCaptureArtifact, "terminal.json")
	require.NoError(t, err)
	data := original.Publication.ArtifactBytes[runnerunit.LifecycleCaptureArtifact]
	require.NotEmpty(t, data)
	capture, err := runnerunit.ParseLifecycleCapture(data)
	require.NoError(t, err)
	accepted, err := runnerunit.ValidateLifecycleCaptureSource(original)
	require.NoError(t, err)
	require.Equal(t, capture, accepted)
	for _, bad := range [][]byte{
		nil,
		append(bytes.Clone(data), '\n'),
		[]byte(strings.Replace(string(data), `"kind":`, `"extra":false,"kind":`, 1)),
		[]byte(strings.Replace(string(data), `"kind":`, `"kind":"duplicate","kind":`, 1)),
		[]byte(strings.Replace(string(data), `"schema_version":"1.0"`, `"schema_version":"2.0"`, 1)),
		bytes.Repeat([]byte(" "), executionlifecycle.MaxBytes+1),
	} {
		require.NotEqual(t, data, bad)
		rejected, err := runnerunit.ParseLifecycleCapture(bad)
		require.ErrorIs(t, err, executionlifecycle.ErrInvalid)
		require.Nil(t, rejected)
	}
	for _, control := range []struct {
		name  string
		edit  func(*runnerunit.LifecycleCapture)
		cause error
	}{
		{"original-admission", func(c *runnerunit.LifecycleCapture) { c.OriginalAdmission.Identity.Run += "-retagged" }, executionlifecycle.ErrContext},
		{"reservation-digest", func(c *runnerunit.LifecycleCapture) { c.ReservationDigest = strings.Repeat("e", 64) }, executionlifecycle.ErrContext},
		{"event-digest", func(c *runnerunit.LifecycleCapture) { c.ObservationDigests[0] = strings.Repeat("e", 64) }, executionlifecycle.ErrContext},
		{"terminal-exit", func(c *runnerunit.LifecycleCapture) { c.Observations[1].Terminal.ExitCode = 3 }, executionlifecycle.ErrInvalid},
		{"invented-process-exit", func(c *runnerunit.LifecycleCapture) { c.Runtime.ProcessExit = "killed" }, execution.ErrRuntimeObservation},
	} {
		t.Run(control.name, func(t *testing.T) {
			changed, err := runnerunit.ParseLifecycleCapture(data)
			require.NoError(t, err)
			control.edit(changed)
			encoded, err := json.Marshal(changed)
			require.NoError(t, err)
			require.NotEqual(t, data, encoded)
			copy := *original
			copy.Publication = original.Publication
			copy.Publication.ArtifactBytes = map[string][]byte{}
			for name, value := range original.Publication.ArtifactBytes {
				copy.Publication.ArtifactBytes[name] = bytes.Clone(value)
			}
			copy.Publication.ArtifactBytes[runnerunit.LifecycleCaptureArtifact] = encoded
			rejected, err := runnerunit.ValidateLifecycleCaptureSource(&copy)
			require.ErrorIs(t, err, control.cause)
			require.Nil(t, rejected)
			require.Equal(t, data, original.Publication.ArtifactBytes[runnerunit.LifecycleCaptureArtifact])
		})
	}
	for _, control := range []string{"missing-event", "event-digest", "extra-event", "attempt-digest"} {
		t.Run(control, func(t *testing.T) {
			copy := *original
			copy.Publication = original.Publication
			copy.Publication.Manifest.Artifacts = append([]artifactmanifest.Artifact(nil), original.Publication.Manifest.Artifacts...)
			require.NotEmpty(t, copy.Publication.Manifest.Artifacts)
			if control == "extra-event" {
				copy.Publication.Manifest.Artifacts = append(copy.Publication.Manifest.Artifacts, artifactmanifest.Artifact{
					Name: "lifecycle-observation-999.json", SHA256: strings.Repeat("a", 64), SizeBytes: 1, MediaType: "application/json",
				})
			} else {
				found := false
				for i, artifact := range copy.Publication.Manifest.Artifacts {
					selected := "lifecycle-observation-001.json"
					if control == "attempt-digest" {
						selected = "execution-attempt.json"
					}
					if artifact.Name != selected {
						continue
					}
					found = true
					if control == "missing-event" {
						copy.Publication.Manifest.Artifacts = append(copy.Publication.Manifest.Artifacts[:i], copy.Publication.Manifest.Artifacts[i+1:]...)
					} else {
						copy.Publication.Manifest.Artifacts[i].SHA256 = strings.Repeat("f", 64)
					}
					break
				}
				require.True(t, found)
			}
			rejected, err := runnerunit.ValidateLifecycleCaptureSource(&copy)
			require.ErrorIs(t, err, executionlifecycle.ErrContext)
			require.Nil(t, rejected)
		})
	}
}
