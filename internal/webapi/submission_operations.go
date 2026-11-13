package webapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/runnersubmission"
	"github.com/microsoft/waza/internal/runnerunit"
)

type submissionOperations struct {
	open   func() (artifactmanifest.Backend, func() error, error)
	runner *runnerunit.Service
}

// RegisterSubmissionRoutes exposes protected generic submission and the
// read-only logical-work lineage view over the configured immutable store.
func RegisterSubmissionRoutes(mux *http.ServeMux, resultsDir string, config *projectconfig.StorageConfig) {
	registerSubmissionRoutes(mux, submissionOperations{open: checkpointBackendFor(resultsDir, config), runner: runnerunit.NewService(nil)})
}

func registerSubmissionRoutes(mux *http.ServeMux, operations submissionOperations) {
	mux.HandleFunc("POST /api/v1/submissions", func(w http.ResponseWriter, r *http.Request) {
		inputComplete := false
		emit := func(status int, result runnerunit.Submission) {
			data, err := runnerunit.EncodeSubmissionResult(result)
			if err != nil {
				logRunnerError("validating submission output; aborting without disclosure", "error", err)
				panic(http.ErrAbortHandler)
			}
			if err := WriteRunnerOperationJSON(w, status, result.ExitCode, data, inputComplete); err != nil {
				logRunnerError("writing submission output; no retry", "error", err)
			}
		}
		data, inputErr := ReadRunnerOperationInput(w, r, runnerunit.MaxSubmissionBytes)
		if inputErr != nil {
			logRunnerError("submission input admission failed", "error", inputErr.Err)
			emit(inputErr.Status, runnersubmission.Rejected(inputErr.Code))
			return
		}
		inputComplete = true
		if _, err := runnerunit.ParseSubmission(data); err != nil {
			logRunnerError("submission parsing failed", "guidance", "Inspect the submission against the execution-submission schema.")
			emit(http.StatusBadRequest, runnersubmission.Rejected(runnersubmission.CodeInputInvalid))
			return
		}
		backend, closeBackend, err := operations.open()
		if err != nil {
			logRunnerError("submission immutable storage unavailable", "error", err)
			emit(http.StatusServiceUnavailable, runnersubmission.OperationError(runnersubmission.CodeStorageUnavailable))
			return
		}
		result, err := runnersubmission.SubmitCaptured(r.Context(), operations.runner, data, backend)
		if closeErr := closeBackend(); closeErr != nil {
			logRunnerError("submission immutable store close failed after operation", "error", errors.Join(err, closeErr))
			if result.Status != "completed" && result.Status != "reused" {
				failed := runnersubmission.OperationError(runnersubmission.CodeStorageClose)
				failed.EngineCalls = result.EngineCalls
				result = failed
			}
			emit(http.StatusServiceUnavailable, result)
			return
		}
		status := http.StatusOK
		if err != nil {
			logRunnerError("submission rejected or failed", "error", err)
			status = http.StatusConflict
			if result.Status == "submission_error" {
				status = http.StatusInternalServerError
			}
		}
		emit(status, result)
	})
	mux.HandleFunc("GET /api/v1/lineage", func(w http.ResponseWriter, r *http.Request) {
		if err := authorizeNativeRunnerHTTP(r); err != nil {
			writeError(w, http.StatusForbidden, "lineage inspection requires a loopback same-origin request")
			return
		}
		logical := executionattempt.Logical{Run: r.URL.Query().Get("run"), Work: r.URL.Query().Get("work")}
		if err := executionledger.ValidateLogical(logical); err != nil {
			writeError(w, http.StatusBadRequest, "run must match run-<sha256> and work must match trial-<sha256>")
			return
		}
		var options executionledger.LineageOptions
		if raw := r.URL.Query().Get("stale_after_ms"); raw != "" {
			ms, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || ms < 1 || ms > maxStaleAfterMS {
				writeError(w, http.StatusBadRequest, "stale_after_ms must be an integer between 1 and 604800000")
				return
			}
			options.StaleAfter = time.Duration(ms) * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		data, err := lineageFor(ctx, operations, logical, options)
		if err != nil {
			// Details stay in operator logs, never in Lab-visible errors.
			slog.Error("execution lineage inspection failed", "error", err)
			status := http.StatusServiceUnavailable
			if errors.Is(err, artifactmanifest.ErrConflict) || errors.Is(err, artifactmanifest.ErrCorrupt) ||
				errors.Is(err, executionledger.ErrContext) {
				status = http.StatusConflict
			}
			writeError(w, status, "execution lineage is unavailable; inspect operator logs and preserve existing records")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})
}

// maxStaleAfterMS bounds caller-supplied stale thresholds to one week.
const maxStaleAfterMS = 7 * 24 * 60 * 60 * 1000

func lineageFor(ctx context.Context, operations submissionOperations, logical executionattempt.Logical, options executionledger.LineageOptions) (data []byte, err error) {
	backend, closeBackend, err := operations.open()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, closeBackend()) }()
	ledger, err := executionledger.New(backend)
	if err != nil {
		return nil, err
	}
	lineage, err := ledger.Lineage(ctx, logical, options)
	if err != nil {
		return nil, err
	}
	return executionledger.EncodeLineage(*lineage)
}
