package manifestaggregate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func lifecycleEvidenceFixture(t *testing.T, backend artifactmanifest.Backend) (executionattempt.Source, string, evidencestate.Document, *int) {
	t.Helper()
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	wire, err := json.Marshal(runnerunit.SubmissionRequest{Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: data})
	require.NoError(t, err)
	calls := 0
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		calls++
		return execution.NewMockEngine(config.ModelID), nil
	})
	result, err := ownedsubmission.SubmitCaptured(t.Context(), service, wire, backend)
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	require.Equal(t, 1, result.EngineCalls)
	source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	completed, err := ledger.ReadCompleted(t.Context(), source, evidencestate.CaptureArtifactName)
	require.NoError(t, err)
	capture, err := evidencestate.ParseCapture(completed.Publication.ArtifactBytes[evidencestate.CaptureArtifactName])
	require.NoError(t, err)
	document := evidencestate.Document{
		Kind: "waza.execution-evidence", SchemaVersion: evidencestate.Version, Publication: "derived",
		OriginManifest: source.ManifestDigest, ExecutionSource: &source, AttemptArtifact: "execution-attempt.json",
		Categories: capture.Categories, Lineage: []evidencestate.Lineage{},
	}
	published, err := evidencestate.Publish(t.Context(), backend, document)
	require.NoError(t, err)
	return source, published.EvidenceSHA256, document, &calls
}

func TestEvidenceReaderLifecycleSharesCategoryAndSourceBudget(t *testing.T) {
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, backend.Close()) }()
	source, digest, document, calls := lifecycleEvidenceFixture(t, backend)
	reader, err := NewEvidenceReader(backend)
	require.NoError(t, err)
	require.Zero(t, reader.backend.read)
	_, categories, err := reader.InspectCategories(t.Context(), digest)
	require.NoError(t, err)
	require.Len(t, categories, 6)
	afterCategories := reader.backend.read
	view, err := reader.InspectSourceContext(t.Context(), document)
	require.NoError(t, err)
	require.Equal(t, "verified", view.State)
	require.Nil(t, view.ParentContext)
	afterSource := reader.backend.read
	require.Greater(t, afterSource, afterCategories)
	lifecycle, err := reader.InspectLifecycle(t.Context(), source, "", 1000)
	require.NoError(t, err)
	require.Greater(t, reader.backend.read, afterSource, "lifecycle namespace and Store reads use the original counter")
	require.Equal(t, source, lifecycle.Source)
	require.Equal(t, "verified-nonsnapshot-content", lifecycle.Verification)
	require.Equal(t, "original-source-declarations", lifecycle.Context)
	require.Equal(t, "caller-declared-unverified", lifecycle.Snapshot.Evidence)
	require.Equal(t, "terminal-recorded", lifecycle.Snapshot.State)
	require.Equal(t, source.ExecutionID, lifecycle.Snapshot.ExecutionID)
	require.Len(t, lifecycle.Snapshot.Observations, 2)
	require.Equal(t, "unknown", lifecycle.Runtime.ProcessExit)
	require.False(t, lifecycle.Runtime.OwnedEngineBound)
	require.Empty(t, lifecycle.Runtime.Observations)
	require.Equal(t, 1, *calls, "read-only facade never constructs another worker or model")
	expected, err := json.Marshal(lifecycle)
	require.NoError(t, err)
	lifecycle.Snapshot.Observations[0].Event = "changed-by-caller"
	lifecycle.Snapshot.Terminal.Outcome = "invalid"
	again, err := reader.InspectLifecycle(t.Context(), source, "", 1000)
	require.NoError(t, err)
	wire, err := json.Marshal(again)
	require.NoError(t, err)
	require.Equal(t, expected, wire, "returned projections own their nested observations and terminal")
	require.Equal(t, 1, *calls)
}

func TestEvidenceReaderLifecycleFailuresWithholdAllMetadata(t *testing.T) {
	for _, control := range []string{"missing", "corrupt", "limit", "canceled", "invalid-clock", "nil", "zero"} {
		t.Run(control, func(t *testing.T) {
			directory := t.TempDir()
			backend, err := artifactmanifest.OpenLocal(directory)
			require.NoError(t, err)
			defer func() { require.NoError(t, backend.Close()) }()
			source, _, _, calls := lifecycleEvidenceFixture(t, backend)
			reader, err := NewEvidenceReader(backend)
			require.NoError(t, err)
			ctx := t.Context()
			staleAfter := int64(1000)
			expected := artifactmanifest.ErrMissing
			switch control {
			case "missing", "corrupt":
				ledger, err := executionledger.New(backend)
				require.NoError(t, err)
				completed, err := ledger.ReadCompleted(t.Context(), source, "lifecycle.json")
				require.NoError(t, err)
				data := completed.Publication.ArtifactBytes["lifecycle.json"]
				path := filepath.Join(directory, "blob-"+artifactmanifest.Hash(data))
				require.FileExists(t, path)
				if control == "missing" {
					require.NoError(t, os.Remove(path))
				} else {
					expected = artifactmanifest.ErrCorrupt
					data[len(data)-1] ^= 1
					require.NoError(t, os.Chmod(path, 0o600))
					require.NoError(t, os.WriteFile(path, data, 0o600))
				}
			case "limit":
				_, _, err := reader.InspectCategories(t.Context(), strings.Repeat("0", 64))
				require.ErrorIs(t, err, artifactmanifest.ErrMissing)
				require.Positive(t, reader.backend.read)
				reader.backend.read = MaxReadBytes - artifactmanifest.MaxNamespaceObjectBytes
				expected = ErrLimit
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				expected = context.Canceled
			case "invalid-clock":
				staleAfter = 0
				expected = executionlifecycle.ErrInvalid
			case "nil":
				reader = nil
				expected = ErrInvalid
			case "zero":
				reader = &EvidenceReader{}
				expected = ErrInvalid
			}
			value, err := reader.InspectLifecycle(ctx, source, "", staleAfter)
			require.ErrorIs(t, err, expected)
			require.Equal(t, ownedsubmission.LifecycleInspection{}, value)
			require.Equal(t, 1, *calls)
			if control == "limit" {
				before := reader.backend.read
				_, err := reader.InspectLifecycle(t.Context(), source, "", 1000)
				require.ErrorIs(t, err, ErrLimit)
				require.Equal(t, before, reader.backend.read, "denied lifecycle repeats do not create a second budget")
			}
			if control == "canceled" || control == "invalid-clock" {
				require.Zero(t, reader.backend.read, "input/stop rejection occurs before source I/O")
			}
		})
	}
}
