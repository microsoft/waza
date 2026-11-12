package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func runCheckpointAvailabilityCLI(t *testing.T, service *runnerunit.Service, revision func() (string, error),
	directory string, wire []byte, args ...string,
) runnercheckpoint.Availability {
	t.Helper()
	command := newRunnerCheckpointCommandWithBackend(service, revision, artifactmanifest.OpenBackend)
	command.SetArgs(append(append([]string{"availability"}, args...), "--store", directory))
	command.SetIn(bytes.NewReader(wire))
	var output, progress bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&progress)
	err := command.ExecuteContext(t.Context())
	result, decodeErr := runnercheckpoint.ParseAvailability(output.Bytes())
	require.NoError(t, decodeErr, "%s", output.Bytes())
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte{'\n'}))
	if result.ExitCode == 0 {
		require.NoError(t, err)
	} else {
		exit, ok := errors.AsType[*ExitCodeError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, result.ExitCode, exit.Code)
	}
	return *result
}

func TestCheckpointAvailabilityCLIMatchesResumeWithoutExecuting(t *testing.T) {
	for _, control := range []string{"success", "failed-checks"} {
		t.Run(control, func(t *testing.T) {
			directory := t.TempDir()
			factories := 0
			service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
				factories++
				return execution.NewMockEngine(config.ModelID), nil
			})
			wire := checkpointCLIWire(t)
			if control == "failed-checks" {
				wire = bytes.Replace(wire, []byte("- hello"), []byte("- missing-output-control"), 1)
			}
			revision := func() (string, error) { return strings.Repeat("c", 40), nil }
			created, _ := runCheckpointCLI(t, service, revision, directory, wire, "create")
			require.Equal(t, 1, factories)

			finalized := runCheckpointAvailabilityCLI(t, service, revision, directory, wire, created.FinalizedDigest)
			require.Equal(t, "available", finalized.Status)
			require.Equal(t, runnercheckpoint.ResumeReuseOriginalTerminal, finalized.ResumeAction)
			require.Equal(t, created.Submission.Reservation.ExecutionID, finalized.Source.ExecutionID)
			require.Equal(t, 1, *finalized.RestoredUnits)

			stale := runCheckpointAvailabilityCLI(t, service, revision, directory, wire, created.BeforeDigest)
			require.Equal(t, "unavailable", stale.Status)
			require.Equal(t, "checkpoint_stale", stale.Diagnostics[0].Code)

			other := func() (string, error) { return strings.Repeat("d", 40), nil }
			incompatible := runCheckpointAvailabilityCLI(t, service, other, directory, wire, created.FinalizedDigest)
			require.Equal(t, "incompatible", incompatible.CompatibilityResult)
			require.NotEmpty(t, incompatible.CompatibilityDifferences)

			invalid := runCheckpointAvailabilityCLI(t, service, revision, directory, wire, "not-a-digest")
			require.Equal(t, "rejected", invalid.Status)
			require.Empty(t, invalid.Checkpoint)
			require.Equal(t, 1, factories, "availability must never construct an engine")

			reused, _ := runCheckpointCLI(t, service, revision, directory, wire, "resume", created.FinalizedDigest)
			require.Equal(t, "reused", reused.Status)
		})
	}
}
