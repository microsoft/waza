package executionledger_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestReaderAdmitsActualProtectedSubmissionWithoutCreatorHydration(t *testing.T) {
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	wrapper, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw,
	})
	require.NoError(t, err)
	hooks, err := ownedsubmission.CaptureHooks(runnerunit.SubmissionHooks{})
	require.NoError(t, err)
	submitted, err := runnerunit.NewService(nil).Submit(context.Background(), wrapper, backend, hooks)
	require.NoError(t, err)
	require.NotNil(t, submitted.Completion)
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	require.NoError(t, err)
	scoped, err := namespace.Reader(func(raw artifactmanifest.ReadBackend) artifactmanifest.ReadBackend { return raw })
	require.NoError(t, err)
	reader, err := executionledger.NewReader(scoped, backend)
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: submitted.Reservation.ExecutionID, ManifestDigest: submitted.Completion.ManifestDigest}
	completed, err := reader.ReadCompleted(context.Background(), source, "terminal.json", "execution-attempt.json")
	require.NoError(t, err)
	require.Equal(t, submitted.Reservation.Binding, completed.Publication.Manifest.Binding)
	require.Equal(t, completed.Reservation.Bytes, completed.Publication.ArtifactBytes["execution-attempt.json"])
	terminal, err := runnerterminal.Validate(completed.Publication.ArtifactBytes["terminal.json"], completed.Reservation.Attempt.Binding.TerminalBinding())
	require.NoError(t, err)
	require.Equal(t, submitted.Terminal.Selection.Attempt, terminal.Selection.Attempt)
	require.Equal(t, submitted.Terminal.Observation.Attempts, terminal.Observation.Attempts)
	require.Equal(t, submitted.Terminal.Outcome, terminal.Outcome)
	require.Empty(t, submitted.Report)
	for _, name := range []string{"Reserve", "Complete", "Distinct", "Reuse", "Reservation", "ValidateCreator"} {
		_, exposed := reflect.TypeOf(reader).MethodByName(name)
		require.False(t, exposed, name)
	}
}
