package ownedsubmission

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestPublicSubmissionActualOriginalDistinctRetryReuseAndFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failed-original"}[failed], func(t *testing.T) {
			backend := captureBackend(t)
			request := captureRequest(t)
			if failed {
				request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- absent-public-control", 1)
				require.Contains(t, request.Input.Task, "absent-public-control")
			}

			wire := captureWire(t, request, nil)
			require.NoError(t, ValidateInput(wire))
			service := runnerunit.NewService(nil)
			first, err := SubmitCaptured(t.Context(), service, wire, backend)
			require.NoError(t, err)
			firstJSON, err := runnerunit.EncodeSubmissionResult(first)
			require.NoError(t, err)
			require.NotContains(t, string(firstJSON), `"report"`)
			require.Equal(t, "completed", first.Status)
			require.Equal(t, map[bool]int{false: 0, true: 1}[failed], first.ExitCode)
			second, err := SubmitCaptured(t.Context(), service, wire, backend)
			require.NoError(t, err)
			require.NotEqual(t, first.Reservation.ExecutionID, second.Reservation.ExecutionID)
			require.Equal(t, first.Reservation.Logical, second.Reservation.Logical)
			require.Equal(t, first.Terminal.Selection.Attempt, second.Terminal.Selection.Attempt)
			childRequest, err := runnerunit.ParseSubmission(wire)
			require.NoError(t, err)
			childRequest.Policy = &runnerunit.SubmissionPolicy{
				Mode: "distinct", Parent: &executionattempt.Parent{ExecutionID: first.Reservation.ExecutionID, Relationship: "retry"},
			}
			childWire, err := json.Marshal(childRequest)
			require.NoError(t, err)
			child, err := SubmitCaptured(t.Context(), service, childWire, backend)
			require.NoError(t, err)
			require.Equal(t, first.Reservation.ExecutionID, child.Reservation.Parent.ExecutionID)
			require.NotEqual(t, first.Reservation.ExecutionID, child.Reservation.ExecutionID)
			require.Nil(t, first.Reservation.Parent)
			source := executionattempt.Source{
				ExecutionID: first.Reservation.ExecutionID, ManifestDigest: first.Completion.ManifestDigest,
			}
			request.Selection.Attempt++
			reused, err := SubmitCaptured(t.Context(), service, captureWire(t, request, &source), backend)
			require.NoError(t, err)
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, first.Terminal, reused.Terminal)
			require.Equal(t, first.Reservation, reused.Reservation)
			require.NotEqual(t, reused.Caller.Attempt, reused.Reservation.Binding.Identity.Attempt)
			_, err = runnerunit.EncodeSubmissionResult(reused)
			require.NoError(t, err)
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			stored, err := ledger.ReadCompleted(t.Context(), source, "execution-attempt.json", "lifecycle.json", "terminal.json")
			require.NoError(t, err)
			require.Equal(t, stored.Reservation.Bytes, stored.Publication.ArtifactBytes["execution-attempt.json"])
			closed := CloseFailed(first)
			require.Equal(t, "submission_error", closed.Status)
			require.Equal(t, 2, closed.ExitCode)
			require.Nil(t, closed.Completion)
			require.Equal(t, first.Terminal, closed.Terminal)
			require.Equal(t, first.EngineCalls, closed.EngineCalls)
			_, err = runnerunit.EncodeSubmissionResult(closed)
			require.NoError(t, err)
			require.NotNil(t, first.Completion, "close projection must not mutate the original result")
		})
	}
}

func TestPublicSubmissionRejectsUnadmittedInputAndUnavailableSourceWithoutDisclosure(t *testing.T) {
	backend := captureBackend(t)
	request := captureRequest(t)
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	raw = bytes.Replace(raw, []byte(`"attempt":2`), []byte(`"attempt":0`), 1)
	require.Contains(t, string(raw), `"attempt":0`)
	wire, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw,
	})
	require.NoError(t, err)
	require.Error(t, ValidateInput(wire))
	result, err := SubmitCaptured(t.Context(), runnerunit.NewService(nil), wire, backend)
	require.Error(t, err)
	require.Nil(t, result.Terminal)
	require.Nil(t, result.Caller)
	_, err = runnerunit.EncodeSubmissionResult(result)
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: "exec-" + strings.Repeat("b", 64), ManifestDigest: strings.Repeat("a", 64)}
	require.NoError(t, ValidateInput(captureWire(t, request, &source)))
	result, operationErr := SubmitCaptured(t.Context(), runnerunit.NewService(nil), captureWire(t, request, &source), backend)
	require.Error(t, operationErr)
	require.Nil(t, result.Terminal)
	require.Nil(t, result.Reservation)
	require.Zero(t, result.EngineCalls)
	encoded, err := runnerunit.EncodeSubmissionResult(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), source.ExecutionID)
	require.NotContains(t, string(encoded), source.ManifestDigest)
	for _, diagnostic := range result.Diagnostics {
		require.NotContains(t, diagnostic.Message, operationErr.Error())
	}
	for _, result := range []runnerunit.Submission{RejectPublic("invalid_submission"), FailPublic("storage_unavailable")} {
		_, err := runnerunit.EncodeSubmissionResult(result)
		require.NoError(t, err)
	}
}

func TestPublicSubmissionRestrictedBindingAndOriginalRemainUndisclosed(t *testing.T) {
	backend := captureBackend(t)
	request := captureRequest(t)
	restricted := "ghp_" + strings.Repeat("A", 36)
	request.Input.Task = strings.Replace(request.Input.Task, "id: selected", "id: "+restricted, 1)
	request.Selection.Task = restricted
	result, operationErr := SubmitCaptured(t.Context(), runnerunit.NewService(nil), captureWire(t, request, nil), backend)
	require.Error(t, operationErr)
	require.Nil(t, result.Caller)
	require.Nil(t, result.Reservation)
	require.Nil(t, result.Terminal)
	encoded, err := runnerunit.EncodeSubmissionResult(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), restricted)
	request = captureRequest(t)
	request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- missing-private-original-control", 1)
	require.Contains(t, request.Input.Task, "missing-private-original-control")
	service := runnerunit.NewService(nil)
	original, err := service.Submit(t.Context(), captureWire(t, request, nil), backend, runnerunit.SubmissionHooks{})
	require.NoError(t, err)
	require.NotEmpty(t, original.Terminal.Diagnostics)
	source := executionattempt.Source{ExecutionID: original.Reservation.ExecutionID, ManifestDigest: original.Completion.ManifestDigest}
	originalBytes, err := runnerterminal.Encode(*original.Terminal, original.Reservation.Binding.TerminalBinding())
	require.NoError(t, err)
	result, operationErr = SubmitCaptured(t.Context(), service, captureWire(t, request, &source), backend)
	require.Error(t, operationErr)
	require.Nil(t, result.Reservation)
	require.Nil(t, result.Terminal)
	require.Nil(t, result.Completion)
	require.Zero(t, result.EngineCalls)
	encoded, err = runnerunit.EncodeSubmissionResult(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "missing-private-original-control")
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	after, err := ledger.ReadCompleted(t.Context(), source, "terminal.json")
	require.NoError(t, err)
	require.Equal(t, originalBytes, after.Publication.ArtifactBytes["terminal.json"])
}

func TestPublicSubmissionRetainsActualFactoryCancellationAndReuse(t *testing.T) {
	backend := captureBackend(t)
	calls := 0
	service := runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
		calls++
		return nil, context.Canceled
	})
	request := captureRequest(t)
	original, err := SubmitCaptured(t.Context(), service, captureWire(t, request, nil), backend)
	require.NoError(t, err)
	require.Equal(t, 3, original.ExitCode)
	require.Equal(t, "completed", original.Status)
	require.Equal(t, 1, calls)
	_, err = runnerunit.EncodeSubmissionResult(original)
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: original.Reservation.ExecutionID, ManifestDigest: original.Completion.ManifestDigest}
	request.Selection.Attempt++
	reused, err := SubmitCaptured(t.Context(), service, captureWire(t, request, &source), backend)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Zero(t, reused.EngineCalls)
	require.Equal(t, original.Terminal, reused.Terminal)
	_, err = runnerunit.EncodeSubmissionResult(reused)
	require.NoError(t, err)
}

func TestPublicSubmissionPublicationFailureHasNoCompletionOrLeakedCause(t *testing.T) {
	directory := t.TempDir()
	backend, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	service := runnerunit.NewService(nil)
	wire := captureWire(t, captureRequest(t), nil)
	original, err := SubmitCaptured(t.Context(), service, wire, backend)
	require.NoError(t, err)
	terminal, err := runnerterminal.Encode(*original.Terminal, original.Reservation.Binding.TerminalBinding())
	require.NoError(t, err)
	key := "blob-" + artifactmanifest.Hash(terminal)
	before, err := backend.Get(t.Context(), key, int64(len(terminal)))
	require.NoError(t, err)
	require.Equal(t, terminal, before)
	private := []byte("private-publication-corruption-control")
	require.NoError(t, os.Chmod(filepath.Join(directory, key), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, key), private, 0o600))
	corrupt, err := os.ReadFile(filepath.Join(directory, key))
	require.NoError(t, err)
	require.Equal(t, private, corrupt)
	_, err = backend.Get(t.Context(), key, int64(len(terminal)))
	require.ErrorIs(t, err, artifactmanifest.ErrCorrupt)
	result, operationErr := SubmitCaptured(t.Context(), service, wire, backend)
	require.Error(t, operationErr)
	require.Equal(t, "submission_error", result.Status)
	require.Equal(t, 2, result.ExitCode)
	require.Equal(t, 1, result.EngineCalls)
	require.NotNil(t, result.Terminal)
	require.Equal(t, 0, result.Terminal.ExitCode)
	require.Nil(t, result.Completion)
	encoded, err := runnerunit.EncodeSubmissionResult(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), string(private))
	require.NotContains(t, string(encoded), operationErr.Error())
}
