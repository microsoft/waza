package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func runSubmissionCLI(t *testing.T, directory string, wire []byte, args ...string) runnerunit.Submission {
	return runSubmissionWithService(t, runnerunit.NewService(nil), directory, wire, args...)
}

func runSubmissionWithService(t *testing.T, service *runnerunit.Service, directory string, wire []byte, args ...string) runnerunit.Submission {
	t.Helper()
	command := newRunnerSubmitCommand(service)
	command.SetArgs(append(args, "--store", directory))
	command.SetIn(bytes.NewReader(wire))
	var output bytes.Buffer
	command.SetOut(&output)
	err := command.ExecuteContext(t.Context())
	result, decodeErr := runnerunit.ParseSubmissionResult(output.Bytes())
	require.NoError(t, decodeErr, "%s", output.Bytes())
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte{'\n'}))
	if result.ExitCode == 0 {
		require.NoError(t, err)
	} else {
		exit, selected := errors.AsType[*ExitCodeError](err)
		require.True(t, selected, "%v", err)
		require.Equal(t, result.ExitCode, exit.Code)
	}
	require.Empty(t, result.Report)
	return *result
}

func TestPublicSubmissionCLIActualOriginalFailuresDistinctAndAlternateOrdinalReuse(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failed-original"}[failed], func(t *testing.T) {
			directory := t.TempDir()
			wire := checkpointCLIWire(t)
			if failed {
				changed := bytes.Replace(wire, []byte("- hello"), []byte("- absent-public-cli-control"), 1)
				require.NotEqual(t, wire, changed)
				wire = changed
			}
			first := runSubmissionCLI(t, directory, wire)
			require.Equal(t, map[bool]int{false: 0, true: 1}[failed], first.ExitCode)
			second := runSubmissionCLI(t, directory, wire)
			require.NotEqual(t, first.Reservation.ExecutionID, second.Reservation.ExecutionID)
			request, err := runnerunit.ParseSubmission(wire)
			require.NoError(t, err)
			request.Request = bytes.Replace(request.Request, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
			require.Contains(t, string(request.Request), `"attempt":3`)
			request.Policy = &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &executionattempt.Source{
				ExecutionID: first.Reservation.ExecutionID, ManifestDigest: first.Completion.ManifestDigest,
			}}
			reuseWire, err := json.Marshal(request)
			require.NoError(t, err)
			reused := runSubmissionCLI(t, directory, reuseWire)
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, "reused", reused.Status)
			require.Equal(t, first.Terminal, reused.Terminal)
			require.Equal(t, first.Reservation, reused.Reservation)
		})
	}
}

func TestPublicSubmissionCLIHandledInputAndProviderFailuresEmitOneResult(t *testing.T) {
	result := runSubmissionCLI(t, t.TempDir(), []byte("{}"))
	require.Equal(t, "rejected", result.Status)
	result = runSubmissionCLI(t, t.TempDir(), checkpointCLIWire(t), "unexpected-argument")
	require.Equal(t, "rejected", result.Status)
	result = runSubmissionCLI(t, t.TempDir(), checkpointCLIWire(t), "--unknown-flag")
	require.Equal(t, "rejected", result.Status)
	result = runSubmissionCLI(t, t.TempDir(), []byte(strings.Repeat(" ", runnerunit.MaxSubmissionBytes+1)))
	require.Equal(t, "rejected", result.Status)
	directory := filepath.Join(t.TempDir(), "not-a-store")
	require.NoError(t, os.WriteFile(directory, []byte("owned invalid store fixture"), 0o600))
	result = runSubmissionCLI(t, directory, checkpointCLIWire(t))
	require.Equal(t, "submission_error", result.Status)
	require.Equal(t, 2, result.ExitCode)
	require.Nil(t, result.Terminal)
}

func TestPublicSubmissionCLIActualCanceledOriginalAndNoReuseFactory(t *testing.T) {
	directory := t.TempDir()
	calls := 0
	service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
		calls++
		return nil, context.Canceled
	})
	wire := checkpointCLIWire(t)
	original := runSubmissionWithService(t, service, directory, wire)
	require.Equal(t, 3, original.ExitCode)
	require.Equal(t, "completed", original.Status)
	require.Equal(t, 1, calls)
	request, err := runnerunit.ParseSubmission(wire)
	require.NoError(t, err)
	request.Policy = &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &executionattempt.Source{
		ExecutionID: original.Reservation.ExecutionID, ManifestDigest: original.Completion.ManifestDigest,
	}}
	reuseWire, err := json.Marshal(request)
	require.NoError(t, err)
	reused := runSubmissionWithService(t, service, directory, reuseWire)
	require.Equal(t, 1, calls)
	require.Zero(t, reused.EngineCalls)
	require.Equal(t, original.Terminal, reused.Terminal)
}
