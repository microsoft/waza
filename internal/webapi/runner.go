package webapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/snapshot"
)

// RegisterRunnerRoutes exposes synchronous single-unit execution, not a job API.
// Sources are captured inline; the HTTP caller cannot name host input paths.
// Compatibility adapters (#761) and the reference (#762) add no dashboard UI state.
func RegisterRunnerRoutes(mux *http.ServeMux, resultsDir string) {
	registerRunnerRoutes(mux, resultsDir, runnercontract.NewService())
}

func registerRunnerRoutes(mux *http.ServeMux, resultsDir string, service *runnercontract.Service) {
	mux.HandleFunc("POST /api/v1/runner/run", func(w http.ResponseWriter, r *http.Request) {
		var result runnercontract.Result
		bodyRead := false
		mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		origin := r.Header.Get("Origin")
		parsedOrigin, originErr := url.Parse(origin)
		host := r.Host
		if name, _, err := net.SplitHostPort(host); err == nil {
			host = name
		}
		address, _ := netip.ParseAddr(strings.Trim(host, "[]"))
		if !strings.EqualFold(host, "localhost") && !address.IsLoopback() {
			result = runnercontract.Rejected("runner execution requires a localhost or literal loopback Host; DNS aliases are not permitted")
		} else if mediaErr != nil || mediaType != "application/json" {
			result = runnercontract.Rejected("runner API requires Content-Type: application/json")
		} else if origin != "" && (originErr != nil || parsedOrigin.Host != r.Host ||
			parsedOrigin.Scheme != "http" && parsedOrigin.Scheme != "https") {
			result = runnercontract.Rejected("cross-origin runner execution is not permitted")
		} else {
			bodyRead = true
			data, err := readRunnerBody(w, r, 30*time.Second)
			if err != nil {
				// No complete request was admitted. Do not invent a runner
				// identity or terminal envelope for incomplete transport.
				writeError(w, http.StatusBadRequest, fmt.Sprintf("reading request before admission: %v", err))
				return
			} else {
				result = service.Run(r.Context(), data)
				if err := persistRunnerResult(resultsDir, &result); err != nil {
					result = runnercontract.PublicationFailed(result, err)
				}
			}
		}
		// All admitted HTTP invocations return a terminal envelope. Outcome and
		// exit_code, not an HTTP transport status, carry execution semantics.
		if !bodyRead {
			closeRunnerBody(w, r)
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("GET /api/v1/runner/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"schema_version":   runnercontract.RequestVersion,
			"versions":         []string{runnercontract.RequestVersion},
			"result_versions":  []string{runnercontract.ResultVersion},
			"identity_version": runnercontract.IdentityVersion,
			"capabilities":     runnercontract.Capabilities(),
		})
	})
	mux.HandleFunc("GET /api/v1/lab/runner-results", runnerResultsHandler(resultsDir))
}

func readRunnerBody(w http.ResponseWriter, r *http.Request, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(timeout))
	// Interrupt the network read before closing: net/http Body.Close can wait
	// for an outstanding Read. Close also wakes injected/non-network bodies.
	finished := make(chan struct{})
	complete := false
	stop := context.AfterFunc(ctx, func() {
		defer close(finished)
		_ = controller.SetReadDeadline(time.Now())
		_ = r.Body.Close()
	})
	defer func() {
		if !stop() {
			<-finished
		}
		if complete {
			_ = controller.SetReadDeadline(time.Time{})
		} else {
			closeRunnerBody(w, r)
		}
	}()
	data, err := io.ReadAll(io.LimitReader(r.Body, runnercontract.MaxRequestBytes+1))
	if err != nil {
		return nil, errors.Join(err, ctx.Err())
	}
	complete = len(data) <= runnercontract.MaxRequestBytes
	return data, nil
}

func closeRunnerBody(w http.ResponseWriter, r *http.Request) {
	// Prevent net/http's response-time drain from blocking on a body whose
	// transport was rejected, interrupted, or stopped at the size bound.
	w.Header().Set("Connection", "close")
	_ = http.NewResponseController(w).SetReadDeadline(time.Now())
	_ = r.Body.Close()
}

func persistRunnerResult(dir string, result *runnercontract.Result) error {
	var data bytes.Buffer
	if err := runnercontract.Write(&data, result); err != nil {
		return fmt.Errorf("encoding runner artifact: %w", err)
	}
	// Own a private child rather than chmod an existing historical results
	// directory. CLI-created *.runner.json artifacts remain readable at its root.
	name, err := snapshot.WritePrivateArtifact(filepath.Join(dir, ".runner"), data.Bytes(), ".runner.json")
	if err != nil {
		return fmt.Errorf("persisting runner artifact (residue %q): %w", name, err)
	}
	return nil
}

func readRunnerResults(dir string) (results []runnercontract.Result, err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return readRunnerRoot(root, true)
}

func readRunnerRoot(root *os.Root, includeHTTP bool) (results []runnercontract.Result, err error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	results = []runnercontract.Result{}
	for _, entry := range entries {
		if includeHTTP && entry.Name() == ".runner" {
			if !entry.IsDir() {
				return nil, fmt.Errorf("runner artifact directory is not a regular directory")
			}
			child, err := root.OpenRoot(entry.Name())
			if err != nil {
				return nil, err
			}
			nested, readErr := readRunnerRoot(child, false)
			if closeErr := child.Close(); readErr != nil || closeErr != nil {
				return nil, errors.Join(readErr, closeErr)
			}
			results = append(results, nested...)
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".runner.json") {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("runner artifact %s is not a regular file", entry.Name())
		}
		// Retain the root throughout each read; reject nonregular entries.
		f, err := openRunnerArtifact(root, entry.Name())
		if err != nil {
			return nil, err
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = f.Close()
			return nil, fmt.Errorf("runner artifact %s is not a readable regular file", entry.Name())
		}
		data, readErr := io.ReadAll(io.LimitReader(f, runnercontract.MaxResultBytes+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || len(data) > runnercontract.MaxResultBytes {
			return nil, fmt.Errorf("reading runner artifact %s failed or exceeded 16 MiB", entry.Name())
		}
		result, err := runnercontract.ParseResult(data)
		if err != nil {
			return nil, fmt.Errorf("runner artifact %s: %w", entry.Name(), err)
		}
		results = append(results, *result)
	}
	return results, nil
}
