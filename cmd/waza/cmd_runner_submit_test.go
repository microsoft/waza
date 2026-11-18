package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func submissionCLIWire(t *testing.T, attempt int, policy *runnerunit.SubmissionPolicy) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	require.Contains(t, string(raw), `"attempt": 2`)
	raw = bytes.Replace(raw, []byte(`"attempt": 2`), []byte(`"attempt": `+strconv.Itoa(attempt)), 1)
	data, err := json.Marshal(runnerunit.SubmissionRequest{Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw, Policy: policy})
	require.NoError(t, err)
	return data
}

func runSubmitCLI(t *testing.T, directory string, wire []byte, args ...string) (runnerunit.Submission, string) {
	t.Helper()
	command := newRunnerSubmitCommand(runnerunit.NewService(nil), artifactmanifest.OpenBackend)
	command.SetArgs(append(args, "--store", directory))
	command.SetIn(bytes.NewReader(wire))
	var output, progress bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&progress)
	err := command.ExecuteContext(t.Context())
	result, decodeErr := runnerunit.ParseSubmissionResult(output.Bytes())
	require.NoError(t, decodeErr, "%s", output.Bytes())
	if result.ExitCode == 0 {
		require.NoError(t, err)
	} else {
		exit, ok := errors.AsType[*ExitCodeError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, result.ExitCode, exit.Code)
	}
	return *result, progress.String()
}

func runLineageCLI(t *testing.T, directory string, args ...string) ([]byte, error) {
	t.Helper()
	command := newRunnerLineageCommand(artifactmanifest.OpenBackend)
	command.SetArgs(append(args, "--store", directory))
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&bytes.Buffer{})
	err := command.ExecuteContext(t.Context())
	return output.Bytes(), err
}

func TestRunnerSubmitAndLineageCLI(t *testing.T) {
	directory := t.TempDir()
	first, _ := runSubmitCLI(t, directory, submissionCLIWire(t, 1, nil))
	require.Equal(t, "completed", first.Status)
	require.Equal(t, 1, first.EngineCalls)
	retry, _ := runSubmitCLI(t, directory, submissionCLIWire(t, 2, &runnerunit.SubmissionPolicy{Mode: "distinct",
		Parent: &executionattempt.Parent{ExecutionID: first.Reservation.ExecutionID, Relationship: "retry"}}))
	require.Equal(t, "completed", retry.Status)
	duplicate, _ := runSubmitCLI(t, directory, submissionCLIWire(t, 1, nil))
	require.Equal(t, "completed", duplicate.Status)
	require.NotEqual(t, first.Reservation.ExecutionID, duplicate.Reservation.ExecutionID)
	source := executionattempt.Source{ExecutionID: retry.Reservation.ExecutionID, ManifestDigest: retry.Completion.ManifestDigest}
	reused, _ := runSubmitCLI(t, directory, submissionCLIWire(t, 3, &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &source}))
	require.Equal(t, "reused", reused.Status)
	require.Zero(t, reused.EngineCalls)

	logical := first.Reservation.Logical
	data, err := runLineageCLI(t, directory, "--run", logical.Run, "--work", logical.Work)
	require.NoError(t, err)
	lineage, err := executionledger.ParseLineage(data)
	require.NoError(t, err)
	require.Len(t, lineage.Attempts, 3)
	byID := map[string]executionledger.LineageAttempt{}
	for _, attempt := range lineage.Attempts {
		byID[attempt.ExecutionID] = attempt
	}
	require.Equal(t, first.Reservation.ExecutionID, byID[retry.Reservation.ExecutionID].Parent.ExecutionID)
	require.Equal(t, []string{reused.Decision.DecisionID}, byID[retry.Reservation.ExecutionID].ReusedBy)
	require.Len(t, lineage.Diagnostics, 1)
	require.Equal(t, "concurrent_distinct_attempts", lineage.Diagnostics[0].Code)
	require.ElementsMatch(t, []string{first.Reservation.ExecutionID, duplicate.Reservation.ExecutionID}, lineage.Diagnostics[0].ExecutionIDs)
}

func TestRunnerSubmitCLIRejectsWithoutEcho(t *testing.T) {
	directory := t.TempDir()
	result, progress := runSubmitCLI(t, directory, []byte(`{"secret-control":1}`))
	require.Equal(t, "rejected", result.Status)
	require.Equal(t, 6, result.ExitCode)
	require.Equal(t, "submission_input_invalid", result.Diagnostics[0].Code)
	require.Contains(t, progress, "input admission")

	conflict, _ := runSubmitCLI(t, directory, submissionCLIWire(t, 1, nil), "--azure-container-url", "https://example.blob.core.windows.net/c")
	require.Equal(t, "submission_flags_invalid", conflict.Diagnostics[0].Code)
}

func TestRunnerLineageCLIRejectsInvalidLogical(t *testing.T) {
	_, err := runLineageCLI(t, t.TempDir(), "--run", "run-x", "--work", "trial-y")
	exit, ok := errors.AsType[*ExitCodeError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, 6, exit.Code)
	require.True(t, strings.Contains(err.Error(), "run-<sha256>"))
}

func TestRunnerSubmitCLICloseFailureOverridesTerminalExit(t *testing.T) {
	directory := t.TempDir()
	wire := submissionCLIWire(t, 1, nil)
	failing := bytes.Replace(wire, []byte("- hello"), []byte("- missing-output-control"), 1)
	require.NotEqual(t, wire, failing)
	command := newRunnerSubmitCommand(runnerunit.NewService(nil), func(directory, azureURL string) (artifactmanifest.Backend, func() error, error) {
		backend, closeBackend, err := artifactmanifest.OpenBackend(directory, azureURL)
		if err != nil {
			return nil, nil, err
		}
		return backend, func() error { return errors.Join(closeBackend(), errors.New("close-control")) }, nil
	})
	command.SetArgs([]string{"--store", directory})
	command.SetIn(bytes.NewReader(failing))
	var output, progress bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&progress)
	err := command.ExecuteContext(t.Context())
	result, decodeErr := runnerunit.ParseSubmissionResult(output.Bytes())
	require.NoError(t, decodeErr, "%s", output.Bytes())
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 1, result.ExitCode)
	exit, ok := errors.AsType[*ExitCodeError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, 2, exit.Code)
	require.Contains(t, progress.String(), "close-control")
}
