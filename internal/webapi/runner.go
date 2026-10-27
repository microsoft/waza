package webapi

import (
	"bytes"
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

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/snapshot"
)

// RegisterRunnerRoutes exposes synchronous single-unit execution, not a job API.
// Sources are captured inline; the HTTP caller cannot name host input paths.
// Compatibility adapters (#761) and the reference (#762) add no dashboard UI state.
func RegisterRunnerRoutes(mux *http.ServeMux, resultsDir string) {
	mux.HandleFunc("POST /api/v1/runner/run", func(w http.ResponseWriter, r *http.Request) {
		var result runnercontract.Result
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
			data, err := io.ReadAll(io.LimitReader(r.Body, runnercontract.MaxRequestBytes+1))
			if err != nil {
				result = runnercontract.Rejected(fmt.Sprintf("reading request: %v", err))
			} else {
				result = runnercontract.NewService().Run(r.Context(), data)
				if err := persistRunnerResult(resultsDir, &result); err != nil {
					result = runnercontract.PublicationFailed(result, err)
				}
			}
		}
		// All admitted HTTP invocations return a terminal envelope. Outcome and
		// exit_code, not an HTTP transport status, carry execution semantics.
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
	mux.HandleFunc("GET /api/v1/lab/runner-results", func(w http.ResponseWriter, _ *http.Request) {
		results, err := readRunnerResults(resultsDir)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, struct {
			SchemaVersion string                  `json:"schema_version"`
			Results       []runnercontract.Result `json:"results"`
		}{runnercontract.ResultVersion, results})
	})
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
