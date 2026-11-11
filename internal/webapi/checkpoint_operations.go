package webapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerunit"
)

type checkpointOperations struct {
	open      func() (artifactmanifest.Backend, func() error, error)
	runner    *runnerunit.Service
	revision  func() (string, error)
	observers runnercheckpoint.Hooks
}

func RegisterCheckpointOperationRoutes(mux *http.ServeMux, resultsDir string, config *projectconfig.StorageConfig) {
	registerCheckpointOperationRoutes(mux, checkpointOperations{
		open:   checkpointBackendFor(resultsDir, config),
		runner: runnerunit.NewService(nil), revision: executioncheckpoint.CurrentRunnerRevision,
	})
}

func checkpointBackendFor(resultsDir string, config *projectconfig.StorageConfig) func() (artifactmanifest.Backend, func() error, error) {
	provider := manifestInspectorFor(resultsDir, config)
	return func() (artifactmanifest.Backend, func() error, error) {
		if config != nil && config.Enabled && config.Provider != "" &&
			config.Provider != "local" && config.Provider != "azure-blob" {
			return nil, nil, fmt.Errorf("checkpoint operations do not support the configured active provider")
		}
		return provider.openBackend()
	}
}

func registerCheckpointOperationRoutes(mux *http.ServeMux, operations checkpointOperations) {
	for _, action := range []string{"create", "resume"} {
		path := "POST /api/v1/checkpoints"
		if action == "resume" {
			path = "POST /api/v1/checkpoints/{digest}/resume"
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			inputComplete := false
			emit := func(status int, result runnercheckpoint.Result) {
				data, err := runnercheckpoint.EncodeResult(result)
				if err != nil {
					logRunnerError("validating checkpoint operation output; aborting without disclosure", "error", err)
					panic(http.ErrAbortHandler)
				}
				if err := WriteRunnerOperationJSON(w, status, result.ExitCode, data, inputComplete); err != nil {
					logRunnerError("writing checkpoint operation output; no retry", "error", err)
				}
			}
			data, inputErr := ReadRunnerOperationInput(w, r, runnerunit.MaxSubmissionBytes)
			if inputErr != nil {
				logRunnerError("checkpoint input admission failed", "error", inputErr.Err)
				emit(inputErr.Status, runnercheckpoint.RejectPublic(inputErr.Code))
				return
			}
			inputComplete = true
			if action == "resume" {
				if err := executioncheckpoint.ValidateDigest(r.PathValue("digest")); err != nil {
					emit(http.StatusBadRequest, runnercheckpoint.RejectPublic("checkpoint_digest_invalid"))
					return
				}
			}
			if _, err := runnerunit.ParseSubmission(data); err != nil {
				emit(http.StatusBadRequest, runnercheckpoint.RejectPublic("checkpoint_input_invalid"))
				return
			}
			revision, err := operations.revision()
			if err != nil {
				logRunnerError("checkpoint build admission failed", "error", err)
				emit(http.StatusServiceUnavailable, runnercheckpoint.OperationErrorResult(runnercheckpoint.ErrorCodeBuildUnavailable))
				return
			}
			checkpoints, err := runnercheckpoint.NewService(operations.runner, revision)
			if err != nil {
				logRunnerError("checkpoint runner unavailable", "error", err)
				emit(http.StatusServiceUnavailable, runnercheckpoint.OperationErrorResult(runnercheckpoint.ErrorCodeRunnerUnavailable))
				return
			}
			backend, closeBackend, err := operations.open()
			if err != nil {
				logRunnerError("checkpoint immutable storage unavailable", "error", err)
				emit(http.StatusServiceUnavailable, runnercheckpoint.OperationErrorResult(runnercheckpoint.ErrorCodeStorageUnavailable))
				return
			}
			var result runnercheckpoint.Result
			if action == "resume" {
				result, err = checkpoints.ResumeCaptured(r.Context(), data, backend, r.PathValue("digest"), operations.observers)
			} else {
				result, err = checkpoints.SubmitCaptured(r.Context(), data, backend, operations.observers)
			}
			if closeErr := closeBackend(); closeErr != nil {
				logRunnerError("checkpoint immutable store close failed after operation", "error", errors.Join(err, closeErr))
				emit(http.StatusServiceUnavailable, runnercheckpoint.WithOperationError(result, runnercheckpoint.ErrorCodeStorageCloseFailed))
				return
			}
			status := http.StatusOK
			if err != nil {
				logRunnerError("checkpoint operation rejected or failed", "error", err)
				status = http.StatusConflict
				if result.Status == "submission_error" || result.Status == "checkpoint_error" {
					status = http.StatusInternalServerError
				}
			}
			emit(status, result)
		})
	}
}
