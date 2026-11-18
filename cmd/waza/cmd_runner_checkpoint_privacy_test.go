package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestCheckpointPublicCLIWithholdsUnapprovedSourceAndPreservesOriginal(t *testing.T) {
	directory := t.TempDir()
	metadata := "retained-checkpoint-metadata-control"
	wire := checkpointCLIWire(t)
	changed := bytes.Replace(wire, []byte("id: selected"), []byte("id: "+metadata), 1)
	require.NotEqual(t, wire, changed)
	wire = changed
	changed = bytes.Replace(wire, []byte(`"task":"selected"`), []byte(`"task":"`+metadata+`"`), 1)
	require.NotEqual(t, wire, changed)
	wire = changed
	factories := 0
	core := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	revision := func() (string, error) { return strings.Repeat("c", 40), nil }
	created, _ := runCheckpointCLI(t, core, revision, directory, wire, "create")
	require.Equal(t, metadata, created.Submission.Reservation.Binding.Selection.Task)
	incompatibleRevision := func() (string, error) { return strings.Repeat("d", 40), nil }
	rejected, _ := runCheckpointCLI(t, core, incompatibleRevision, directory, wire, "resume", created.FinalizedDigest)
	require.True(t, rejected.SourceWithheld)
	require.Equal(t, []string{"runner_revision"}, rejected.CompatibilityDifferences)
	public, err := json.Marshal(rejected)
	require.NoError(t, err)
	require.NotContains(t, string(public), metadata)
	require.Equal(t, 1, factories)

	failedWire := bytes.Replace(wire, []byte("- hello"), []byte("- original-private-terminal-control"), 1)
	require.NotEqual(t, wire, failedWire)
	backend, closeBackend, err := artifactmanifest.OpenBackend(directory, "")
	require.NoError(t, err)
	legacy, err := runnercheckpoint.NewService(core, strings.Repeat("c", 40))
	require.NoError(t, err)
	original, err := legacy.Submit(t.Context(), failedWire, backend, runnercheckpoint.Hooks{})
	require.NoError(t, err)
	require.NotEmpty(t, original.Submission.Terminal.Diagnostics)
	require.NotEqual(t, "[REDACTED]", original.Submission.Terminal.Diagnostics[0].Message)
	owner, err := executionledger.New(backend)
	require.NoError(t, err)
	source := executionattempt.Source{
		ExecutionID: original.Submission.Reservation.ExecutionID, ManifestDigest: original.Submission.Completion.ManifestDigest,
	}
	before, err := owner.ReadCompleted(t.Context(), source)
	require.NoError(t, err)
	terminalBefore := bytes.Clone(before.Publication.ArtifactBytes["terminal.json"])
	require.NoError(t, closeBackend())
	denied, _ := runCheckpointCLI(t, core, revision, directory, failedWire, "resume", original.FinalizedDigest)
	require.True(t, denied.SourceWithheld)
	require.Nil(t, denied.Source)
	require.Nil(t, denied.Submission.Terminal)
	require.Nil(t, denied.Submission.Completion)
	require.Nil(t, denied.RestoredUnits)
	require.Equal(t, 2, factories)
	public, err = json.Marshal(denied)
	require.NoError(t, err)
	require.NotContains(t, string(public), metadata)
	require.NotContains(t, string(public), "original-private-terminal-control")
	backend, closeBackend, err = artifactmanifest.OpenBackend(directory, "")
	require.NoError(t, err)
	owner, err = executionledger.New(backend)
	require.NoError(t, err)
	after, err := owner.ReadCompleted(t.Context(), source)
	require.NoError(t, err)
	require.Equal(t, terminalBefore, after.Publication.ArtifactBytes["terminal.json"])
	require.NoError(t, closeBackend())
}
