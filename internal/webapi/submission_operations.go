package webapi

import (
	"errors"
	"net/http"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/runnerunit"
)

type submissionOperations struct {
	open   func() (artifactmanifest.Backend, func() error, error)
	runner *runnerunit.Service
}

func RegisterSubmissionRoutes(mux *http.ServeMux, resultsDir string, config *projectconfig.StorageConfig) {
	registerSubmissionRoutes(mux, submissionOperations{
		open: checkpointBackendFor(resultsDir, config), runner: runnerunit.NewService(nil),
	})
}

func registerSubmissionRoutes(mux *http.ServeMux, operations submissionOperations) {
	mux.HandleFunc("POST /api/v1/runner/submit", func(w http.ResponseWriter, r *http.Request) {
		inputComplete := false
		emit := func(status int, result runnerunit.Submission) {
			data, err := runnerunit.EncodeSubmissionResult(result)
			if err != nil {
				logRunnerError("validating protected submission output; aborting without disclosure", "error", err)
				panic(http.ErrAbortHandler)
			}
			if err := WriteRunnerOperationJSON(w, status, result.ExitCode, data, inputComplete); err != nil {
				logRunnerError("writing submission output; no retry", "error", err)
			}
		}
		data, inputErr := ReadRunnerOperationInput(w, r, runnerunit.MaxSubmissionBytes)
		if inputErr != nil {
			logRunnerError("submission input admission failed", "error", inputErr.Err)
			emit(inputErr.Status, ownedsubmission.RejectPublic(inputErr.Code))
			return
		}
		inputComplete = true
		if err := ownedsubmission.ValidateInput(data); err != nil {
			logRunnerError("submission captured input rejected", "error", err)
			emit(http.StatusBadRequest, ownedsubmission.RejectPublic("submission_input_invalid"))
			return
		}
		backend, closeBackend, err := operations.open()
		if err != nil {
			logRunnerError("submission immutable storage unavailable", "error", err)
			emit(http.StatusServiceUnavailable, ownedsubmission.FailPublic("submission_storage_unavailable"))
			return
		}
		result, operationErr := ownedsubmission.SubmitCaptured(r.Context(), operations.runner, data, backend)
		if err := closeBackend(); err != nil {
			logRunnerError("submission store close failed", "error", errors.Join(operationErr, err))
			emit(http.StatusServiceUnavailable, ownedsubmission.CloseFailed(result))
			return
		}
		status := http.StatusOK
		if operationErr != nil {
			logRunnerError("submission failed or source admission denied", "error", operationErr)
			status = http.StatusInternalServerError
			if errors.Is(operationErr, executionledger.ErrContext) ||
				errors.Is(operationErr, artifactmanifest.ErrMissing) ||
				errors.Is(operationErr, artifactmanifest.ErrCorrupt) ||
				errors.Is(operationErr, artifactmanifest.ErrConflict) {
				status = http.StatusConflict
			}
		}
		emit(status, result)
	})
}
