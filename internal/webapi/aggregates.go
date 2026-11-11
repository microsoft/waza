package webapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/projectconfig"
)

// AggregateSources opens explicitly configured read-only capabilities per
// request. Providers must not construct runtimes or discover worker-local state.
type AggregateSources func() (manifestaggregate.Sources, func() error, error)

func RegisterAggregateRoutes(mux *http.ServeMux, resultsDir string, config *projectconfig.StorageConfig) {
	provider := manifestInspectorFor(resultsDir, config)
	RegisterAggregateRoutesWithSources(mux, func() (manifestaggregate.Sources, func() error, error) {
		if config != nil && config.Enabled && config.Provider != "" && config.Provider != "local" {
			return manifestaggregate.Sources{}, nil, errors.New("aggregation does not support the configured active provider")
		}
		backend, closeBackend, err := provider.openBackend()
		if err != nil {
			return manifestaggregate.Sources{}, nil, err
		}
		namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
		if err != nil {
			return manifestaggregate.Sources{}, nil, errors.Join(err, closeBackend())
		}
		reader, err := namespace.Reader(func(read artifactmanifest.ReadBackend) artifactmanifest.ReadBackend { return read })
		if err != nil {
			return manifestaggregate.Sources{}, nil, errors.Join(err, closeBackend())
		}
		return manifestaggregate.Sources{Objects: backend, Namespace: reader}, closeBackend, nil
	})
}

func RegisterAggregateRoutesWithSources(mux *http.ServeMux, open AggregateSources) {
	mux.HandleFunc("POST /api/v1/aggregates", func(w http.ResponseWriter, r *http.Request) {
		inputComplete := false
		emit := func(status int, data []byte) {
			writeAggregateResponse(w, status, data, inputComplete)
		}
		reject := func(status int, code string) {
			data, err := json.Marshal(map[string]string{"error": code})
			if err != nil {
				logRunnerError("encoding aggregate transport error", "error", err)
				panic(http.ErrAbortHandler)
			}
			emit(status, append(data, '\n'))
		}
		controller := http.NewResponseController(w)
		deadline := time.Now().Add(5 * time.Second)
		if err := controller.SetReadDeadline(deadline); err != nil {
			logRunnerError("setting aggregate read deadline", "error", err)
			reject(http.StatusInternalServerError, "aggregate_transport_unavailable")
			return
		}
		if err := controller.SetWriteDeadline(time.Time{}); err != nil {
			logRunnerError("probing aggregate write deadline", "error", err)
			reject(http.StatusInternalServerError, "aggregate_transport_unavailable")
			return
		}
		data, inputErr := readRunnerHTTPBody(w, r, artifactmanifest.MaxManifestBytes, deadline)
		if inputErr != nil {
			logRunnerError("reading aggregate input", "code", inputErr.Code, "error", inputErr.Err)
			switch inputErr.Status {
			case http.StatusRequestTimeout:
				reject(http.StatusRequestTimeout, "aggregate_request_timeout")
			case http.StatusInternalServerError:
				reject(http.StatusInternalServerError, "aggregate_transport_unavailable")
			default:
				reject(http.StatusBadRequest, "invalid_aggregate_request")
			}
			return
		}
		inputComplete = true
		inputs, err := manifestaggregate.ParseInputs(data)
		if err != nil {
			reject(http.StatusBadRequest, "invalid_aggregate_request")
			return
		}
		sources, closeSources, err := open()
		if err != nil {
			logRunnerError("aggregate store unavailable", "error", err)
			reject(http.StatusServiceUnavailable, "aggregate_store_unavailable")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		report, buildErr := manifestaggregate.Build(ctx, sources, inputs, manifestaggregate.DefaultLimits())
		closeErr := closeSources()
		if report == nil || closeErr != nil {
			logRunnerError("aggregate evaluation unavailable", "error", errors.Join(buildErr, closeErr))
			reject(http.StatusServiceUnavailable, "aggregate_evaluation_unavailable")
			return
		}
		wire, encodeErr := manifestaggregate.Encode(*report)
		if encodeErr != nil {
			logRunnerError("aggregate encoding failed", "error", encodeErr)
			reject(http.StatusInternalServerError, "aggregate_encoding_failed")
			return
		}
		status := http.StatusOK
		if buildErr != nil {
			status = http.StatusConflict
			logRunnerError("aggregate admission incomplete", "error", buildErr)
		}
		emit(status, wire)
	})
}

func writeAggregateResponse(w http.ResponseWriter, status int, data []byte, inputComplete bool) {
	if !inputComplete {
		w.Header().Set("Connection", "close")
	}
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		logRunnerError("setting aggregate write deadline; aborting transport without output", "error", err)
		panic(http.ErrAbortHandler)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	written, err := w.Write(data)
	if written != len(data) {
		err = errors.Join(err, io.ErrShortWrite)
	}
	if err != nil {
		logRunnerError("writing aggregate response", "error", err)
		panic(http.ErrAbortHandler)
	}
	if err := controller.Flush(); err != nil {
		logRunnerError("flushing aggregate response", "error", err)
		panic(http.ErrAbortHandler)
	}
}
