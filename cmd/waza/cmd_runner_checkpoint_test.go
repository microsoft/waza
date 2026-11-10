package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func checkpointCLIWire(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	data, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw,
	})
	require.NoError(t, err)
	return data
}

func runCheckpointCLI(t *testing.T, service *runnerunit.Service, revision func() (string, error),
	directory string, wire []byte, args ...string,
) (runnercheckpoint.Result, string) {
	t.Helper()
	command := newRunnerCheckpointCommandWithRevision(service, revision)
	command.SetArgs(append(args, "--store", directory))
	command.SetIn(bytes.NewReader(wire))
	var output, progress bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&progress)
	err := command.ExecuteContext(t.Context())
	result, decodeErr := runnercheckpoint.ParseResult(output.Bytes())
	require.NoError(t, decodeErr, "%s", output.Bytes())
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte{'\n'}))
	if result.ExitCode == 0 {
		require.NoError(t, err)
	} else {
		exit, ok := errors.AsType[*ExitCodeError](err)
		require.True(t, ok, "%v", err)
		require.Equal(t, result.ExitCode, exit.Code)
	}
	return *result, progress.String()
}

func TestCheckpointPublicCLIOriginalOutcomeAndStaleResume(t *testing.T) {
	for _, control := range []string{"success", "failed-checks", "canceled"} {
		t.Run(control, func(t *testing.T) {
			directory := t.TempDir()
			factories := 0
			service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
				factories++
				if control == "canceled" {
					return nil, context.Canceled
				}
				return execution.NewMockEngine(config.ModelID), nil
			})
			wire := checkpointCLIWire(t)
			wantCode := 0
			if control == "failed-checks" {
				changed := bytes.Replace(wire, []byte("- hello"), []byte("- missing-output-control"), 1)
				require.NotEqual(t, wire, changed)
				wire, wantCode = changed, 1
			}
			if control == "canceled" {
				wantCode = 3
			}
			revision := func() (string, error) { return strings.Repeat("c", 40), nil }
			created, progress := runCheckpointCLI(t, service, revision, directory, wire, "create")
			require.Equal(t, wantCode, created.ExitCode)
			require.Contains(t, progress, "checkpoint before-unit "+created.BeforeDigest+"\n")
			require.Contains(t, progress, "checkpoint finalized-unit "+created.FinalizedDigest+"\n")
			require.NotNil(t, created.Submission.Completion)
			require.Empty(t, created.Submission.Report)
			original, err := json.Marshal(created.Submission.Terminal)
			require.NoError(t, err)
			destination := bytes.Replace(wire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
			require.NotEqual(t, wire, destination)
			reused, progress := runCheckpointCLI(t, service, revision, directory, destination, "resume", created.FinalizedDigest)
			require.Empty(t, progress)
			require.Equal(t, wantCode, reused.ExitCode)
			require.Zero(t, reused.Submission.EngineCalls)
			require.Equal(t, 1, factories)
			require.Equal(t, 2, reused.Submission.Terminal.Selection.Attempt)
			terminal, err := json.Marshal(reused.Submission.Terminal)
			require.NoError(t, err)
			require.Equal(t, original, terminal)
			rejected, _ := runCheckpointCLI(t, service, revision, directory, wire, "resume", created.BeforeDigest)
			require.Equal(t, "rejected", rejected.Status)
			require.True(t, rejected.SourceWithheld)
			require.Nil(t, rejected.Source)
			require.Nil(t, rejected.RestoredUnits)
			require.Equal(t, 1, factories)
		})
	}
}

func TestCheckpointPublicCLIRejectsBeforeProvider(t *testing.T) {
	for _, control := range []string{"invalid-input", "over-limit", "missing-revision", "arguments", "invalid-digest", "output-marker-input"} {
		t.Run(control, func(t *testing.T) {
			wire := checkpointCLIWire(t)
			revisions, factories := 0, 0
			service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
				factories++
				return nil, errors.New("must not construct")
			})
			revision := func() (string, error) {
				revisions++
				return "", errors.New("private-build-metadata-control")
			}
			args := []string{"create"}
			switch control {
			case "invalid-input":
				wire = []byte("{}")
			case "over-limit":
				wire = append(wire, bytes.Repeat([]byte(" "), runnerunit.MaxSubmissionBytes)...)
			case "output-marker-input":
				changed := bytes.Replace(wire, []byte(`"schema_version":"1.0"`), []byte(`"source_withheld":true,"schema_version":"1.0"`), 1)
				require.NotEqual(t, wire, changed)
				wire = changed
			case "arguments":
				args = append(args, "extra")
			case "invalid-digest":
				args = []string{"resume", strings.Repeat("A", 64)}
			}
			directory := t.TempDir() + "/must-not-open"
			result, _ := runCheckpointCLI(t, service, revision, directory, wire, args...)
			require.Equal(t, "rejected", result.Status)
			require.Nil(t, result.Submission)
			require.Zero(t, factories)
			if control == "missing-revision" {
				require.Equal(t, 1, revisions)
				require.Equal(t, "checkpoint_build_incompatible", result.Diagnostics[0].Code)
			} else {
				require.Zero(t, revisions)
			}
			_, err := os.Stat(directory)
			require.True(t, os.IsNotExist(err))
		})
	}
}
