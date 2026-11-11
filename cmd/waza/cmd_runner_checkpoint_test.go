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
	"testing/iotest"

	"github.com/microsoft/waza/internal/artifactmanifest"
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
	return runCheckpointCLIWithBackend(t, service, revision, artifactmanifest.OpenBackend, directory, wire, args...)
}

func runCheckpointCLIWithBackend(t *testing.T, service *runnerunit.Service, revision func() (string, error),
	openBackend func(string, string) (artifactmanifest.Backend, func() error, error),
	directory string, wire []byte, args ...string,
) (runnercheckpoint.Result, string) {
	t.Helper()
	command := newRunnerCheckpointCommandWithBackend(service, revision, openBackend)
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
	for _, control := range []string{"invalid-input", "over-limit", "missing-revision", "arguments", "invalid-digest", "output-marker-input", "conflicting-store-flags"} {
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
			case "conflicting-store-flags":
				args = append(args, "--azure-container-url", "https://fixture.invalid/container")
			}

			directory := t.TempDir() + "/must-not-open"
			result, diagnostic := runCheckpointCLI(t, service, revision, directory, wire, args...)
			if control == "missing-revision" {
				require.Equal(t, "operation_error", result.Status)
				require.Equal(t, runnercheckpoint.ErrorCodeBuildUnavailable, result.ErrorCode)
			} else {
				require.Equal(t, "rejected", result.Status)
			}
			require.Nil(t, result.Submission)
			require.Zero(t, factories)
			if control == "missing-revision" {
				require.Equal(t, 1, revisions)
				require.Equal(t, runnercheckpoint.ErrorCodeBuildUnavailable, result.ErrorCode)
				require.Contains(t, diagnostic, "checkpoint build admission: private-build-metadata-control")
				encoded, err := runnercheckpoint.EncodeResult(result)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "private-build-metadata-control")
			} else {
				require.Zero(t, revisions)
			}
			_, err := os.Stat(directory)
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestCheckpointPublicCLIRetainsPrivateOperationCauseOnlyInDiagnostics(t *testing.T) {
	digest := strings.Repeat("e", 64)
	result, diagnostic := runCheckpointCLI(t, runnerunit.NewService(nil),
		func() (string, error) { return strings.Repeat("c", 40), nil },
		t.TempDir(), checkpointCLIWire(t), "resume", digest)
	require.Equal(t, "rejected", result.Status)
	require.Equal(t, 6, result.ExitCode)
	require.Contains(t, diagnostic, "checkpoint operation:")
	require.Contains(t, diagnostic, "reading checkpoint blob "+digest+": missing immutable object")
	data, err := runnercheckpoint.EncodeResult(result)
	require.NoError(t, err)
	require.NotContains(t, string(data), digest)
}

func TestCheckpointPublicCLIRetainsFlagFailureOnlyInOperatorDiagnostics(t *testing.T) {
	for _, action := range []string{"create", "resume"} {
		t.Run(action, func(t *testing.T) {
			earlyCalls := 0
			service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
				earlyCalls++
				return nil, errors.New("must not construct")
			})
			revision := func() (string, error) {
				earlyCalls++
				return strings.Repeat("c", 40), nil
			}

			args := []string{action}
			if action == "resume" {
				args = append(args, strings.Repeat("e", 64))
			}
			const flag = "private-unknown-flag-control"
			args = append(args, "--"+flag)
			directory := t.TempDir() + "/must-not-open"
			result, diagnostic := runCheckpointCLI(t, service, revision, directory, checkpointCLIWire(t), args...)
			require.Equal(t, "checkpoint_flags_invalid", result.Diagnostics[0].Code)
			require.Contains(t, diagnostic, "checkpoint flags: unknown flag: --"+flag)
			require.Zero(t, earlyCalls)
			require.NoDirExists(t, directory)
			public, err := runnercheckpoint.EncodeResult(result)
			require.NoError(t, err)
			require.NotContains(t, string(public), flag)
		})
	}
}

func TestCheckpointPublicCLIInputAdmissionFailuresRemainRejections(t *testing.T) {
	invalid := []byte("{")
	_, parseErr := runnerunit.ParseSubmission(invalid)
	require.Error(t, parseErr)
	for _, action := range []string{"create", "resume"} {
		for _, stage := range []string{"read", "admission"} {
			t.Run(action+"-"+stage, func(t *testing.T) {
				earlyCalls := 0
				service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
					earlyCalls++
					return nil, errors.New("must not construct")
				})
				command := newRunnerCheckpointCommandWithRevision(service, func() (string, error) {
					earlyCalls++
					return strings.Repeat("c", 40), nil
				})
				args := []string{action}
				if action == "resume" {
					args = append(args, strings.Repeat("e", 64))
				}
				directory := t.TempDir() + "/must-not-open"
				command.SetArgs(append(args, "--store", directory))
				cause, code := parseErr, "checkpoint_input_invalid"
				command.SetIn(bytes.NewReader(invalid))
				if stage == "read" {
					cause, code = errors.New("private-stdin-read-control"), "checkpoint_input_unavailable"
					command.SetIn(iotest.ErrReader(cause))
				}
				var output, diagnostic bytes.Buffer
				command.SetOut(&output)
				command.SetErr(&diagnostic)
				err := command.ExecuteContext(t.Context())
				exit, ok := errors.AsType[*ExitCodeError](err)
				require.True(t, ok, "%v", err)
				result, err := runnercheckpoint.ParseResult(output.Bytes())
				require.NoError(t, err)
				require.Equal(t, exit.Code, result.ExitCode)
				require.Equal(t, "1.0", result.SchemaVersion)
				require.Equal(t, "rejected", result.Status)
				require.Equal(t, 6, result.ExitCode)
				require.Equal(t, code, result.Diagnostics[0].Code)
				require.Contains(t, diagnostic.String(), "checkpoint input "+stage+":")
				require.Contains(t, diagnostic.String(), cause.Error())
				require.NotContains(t, output.String(), cause.Error())
				require.Zero(t, earlyCalls)
				require.NoDirExists(t, directory)
			})
		}
	}
}

func TestCheckpointPublicCLIStoreOpenFailureEmitsOperationError(t *testing.T) {
	for _, control := range []string{"missing", "inaccessible"} {
		t.Run(control, func(t *testing.T) {
			parent := t.TempDir()
			directory := filepath.Join(parent, "missing")
			if control == "inaccessible" {
				require.NoError(t, os.WriteFile(directory, []byte("not-a-directory"), 0o600))
				directory = filepath.Join(directory, "child")
			}
			result, diagnostic := runCheckpointCLI(t, runnerunit.NewService(nil),
				func() (string, error) { return strings.Repeat("c", 40), nil },
				directory, checkpointCLIWire(t), "create")
			require.Equal(t, "1.1", result.SchemaVersion)
			require.Equal(t, "operation_error", result.Status)
			require.Equal(t, 2, result.ExitCode)
			require.Equal(t, runnercheckpoint.ErrorCodeStorageUnavailable, result.ErrorCode)
			require.Nil(t, result.Submission)
			require.Nil(t, result.Source)
			require.Contains(t, diagnostic, "checkpoint store open:")
			require.NotContains(t, mustEncodeCheckpointResult(t, result), "not-a-directory")
		})
	}
}

func TestCheckpointPublicCLICloseFailurePreservesCreateAndFinalizedResume(t *testing.T) {
	directory := t.TempDir()
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	revision := func() (string, error) { return strings.Repeat("c", 40), nil }
	wire := checkpointCLIWire(t)
	created, _ := runCheckpointCLI(t, service, revision, directory, wire, "create")
	require.Equal(t, "completed", created.Status)

	openWithCloseError := func(directory, azureURL string) (artifactmanifest.Backend, func() error, error) {
		backend, closeBackend, err := artifactmanifest.OpenBackend(directory, azureURL)
		if err != nil {
			return nil, nil, err
		}
		return backend, func() error {
			return errors.Join(closeBackend(), errors.New("private-close-control"))
		}, nil
	}
	closedCreate, diagnostic := runCheckpointCLIWithBackend(t, service, revision, openWithCloseError,
		directory, wire, "create")
	require.Equal(t, runnercheckpoint.ErrorCodeStorageCloseFailed, closedCreate.ErrorCode)
	require.Equal(t, "operation_error", closedCreate.Status)
	require.Equal(t, 2, closedCreate.ExitCode)
	require.Contains(t, diagnostic, "checkpoint before-unit "+closedCreate.BeforeDigest+"\n")
	require.Contains(t, diagnostic, "checkpoint finalized-unit "+closedCreate.FinalizedDigest+"\n")
	require.Equal(t, created.Submission.Status, closedCreate.Submission.Status)
	require.Equal(t, created.Submission.ExitCode, closedCreate.Submission.ExitCode)
	require.Contains(t, diagnostic, "private-close-control")
	require.NotContains(t, mustEncodeCheckpointResult(t, closedCreate), "private-close-control")

	destination := bytes.Replace(wire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
	require.NotEqual(t, wire, destination)
	reused, _ := runCheckpointCLI(t, service, revision, directory, destination, "resume", closedCreate.FinalizedDigest)
	closedReuse, diagnostic := runCheckpointCLIWithBackend(t, service, revision, openWithCloseError,
		directory, destination, "resume", closedCreate.FinalizedDigest)
	require.Equal(t, "reused", reused.Submission.Status)
	require.Equal(t, runnercheckpoint.ErrorCodeStorageCloseFailed, closedReuse.ErrorCode)
	require.Equal(t, "operation_error", closedReuse.Status)
	require.Equal(t, 2, closedReuse.ExitCode)
	require.Equal(t, reused.Submission.Status, closedReuse.Submission.Status)
	require.Equal(t, reused.Submission.ExitCode, closedReuse.Submission.ExitCode)
	require.Equal(t, reused.Source, closedReuse.Source)
	require.Contains(t, diagnostic, "private-close-control")
	require.NotContains(t, mustEncodeCheckpointResult(t, closedReuse), "private-close-control")
}

func mustEncodeCheckpointResult(t *testing.T, result runnercheckpoint.Result) string {
	t.Helper()
	data, err := runnercheckpoint.EncodeResult(result)
	require.NoError(t, err)
	return string(data)
}
