package webapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/microsoft/waza/internal/runnercontract"
)

const runnerArchiveVersion = "1.1"
const maxRunnerPageBytes = 17 << 20
const maxRunnerPageItems = 100

type runnerArchiveEntry struct {
	Artifact string          `json:"artifact"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
}

type runnerArchivePage struct {
	SchemaVersion string               `json:"schema_version"`
	Results       []runnerArchiveEntry `json:"results"`
	NextCursor    *string              `json:"next_cursor"`
}

type runnerArchiveCursor struct {
	Version string `json:"version"`
	After   string `json:"after"`
}

func archiveCursor(after string) string {
	data, _ := json.Marshal(runnerArchiveCursor{runnerArchiveVersion, base64.RawURLEncoding.EncodeToString([]byte(after))})
	return base64.RawURLEncoding.EncodeToString(data)
}

func parseArchiveCursor(text string) (string, error) {
	if text == "" {
		return "", nil
	}
	if len(text) > 1024 {
		return "", fmt.Errorf("invalid runner archive cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return "", fmt.Errorf("invalid runner archive cursor")
	}
	var cursor runnerArchiveCursor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != runnerArchiveVersion {
		return "", fmt.Errorf("invalid or unsupported runner archive cursor")
	}
	after, err := base64.RawURLEncoding.DecodeString(cursor.After)
	if err != nil || len(after) == 0 || archiveCursor(string(after)) != text {
		return "", fmt.Errorf("invalid or unsupported runner archive cursor")
	}
	return string(after), nil
}

func runnerResultsHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 20
		if text := r.URL.Query().Get("limit"); text != "" {
			value, err := strconv.Atoi(text)
			if err != nil || value < 1 || value > maxRunnerPageItems {
				writeError(w, http.StatusBadRequest, "limit must be an integer from 1 to 100")
				return
			}
			limit = value
		}
		after, err := parseArchiveCursor(r.URL.Query().Get("cursor"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		page, err := readRunnerPage(r.Context(), dir, after, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, page)
	}
}

func runnerArtifactNames(root *os.Root, prefix string) ([]string, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return nil, fmt.Errorf("listing runner artifacts: %w", errors.Join(err, closeErr))
	}
	var names []string
	for _, entry := range entries {
		if prefix == "" && entry.Name() == ".runner" {
			if !entry.IsDir() {
				names = append(names, ".runner")
				continue
			}
			child, err := root.OpenRoot(".runner")
			if err != nil {
				names = append(names, ".runner")
				continue
			}
			nested, err := runnerArtifactNames(child, ".runner/")
			_ = child.Close()
			if err != nil {
				names = append(names, ".runner")
			} else {
				names = append(names, nested...)
			}
		} else if strings.HasSuffix(entry.Name(), ".runner.json") {
			names = append(names, prefix+entry.Name())
		}
	}
	return names, nil
}

func readRunnerArchiveEntry(root *os.Root, name string) runnerArchiveEntry {
	entry := runnerArchiveEntry{Artifact: name}
	if name == ".runner" {
		entry.Error = "runner artifact directory .runner could not be enumerated as a directory"
		return entry
	}
	if runnerArtifactQuarantined(root, name) {
		entry.Error = "runner artifact withheld after failed publication reconciliation"
		return entry
	}
	data, err := func() (data []byte, err error) {
		before, err := root.Lstat(name)
		if err != nil || !before.Mode().IsRegular() {
			return nil, fmt.Errorf("not a readable regular file")
		}
		file, err := openRunnerArtifact(root, name)
		if err != nil {
			return nil, err
		}
		defer func() { err = errors.Join(err, file.Close()) }()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
			return nil, fmt.Errorf("not a readable regular file")
		}
		if info.Size() > runnercontract.MaxResultBytes {
			return nil, fmt.Errorf("artifact exceeds 16 MiB")
		}
		data, err = io.ReadAll(io.LimitReader(file, runnercontract.MaxResultBytes+1))
		if err != nil {
			return nil, err
		}
		result, err := runnercontract.ParseResult(data)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}()
	if err != nil {
		message := err.Error()
		if len(message) > 2048 {
			message = strings.ToValidUTF8(message[:2048], "") + " [truncated]"
		}
		entry.Error = message
	} else {
		entry.Result = data
	}
	return entry
}

func readRunnerPage(ctx context.Context, dir, after string, limit int) (runnerArchivePage, error) {
	page := runnerArchivePage{SchemaVersion: runnerArchiveVersion, Results: []runnerArchiveEntry{}}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return page, err
	}
	defer root.Close() //nolint:errcheck
	names, err := runnerArtifactNames(root, "")
	if err != nil {
		return page, err
	}
	slices.Sort(names)
	size := 8192 // Reserve wrapper, commas, cursor and JSON escaping of artifact names.
	for _, name := range names {
		if name <= after {
			continue
		}
		if err := ctx.Err(); err != nil {
			return page, err
		}
		if len(page.Results) == limit {
			cursor := archiveCursor(page.Results[len(page.Results)-1].Artifact)
			page.NextCursor = &cursor
			break
		}
		entry := readRunnerArchiveEntry(root, name)
		encoded, err := json.Marshal(entry)
		if err != nil {
			return page, err
		}
		if size+len(encoded)+1 > maxRunnerPageBytes && len(page.Results) > 0 {
			cursor := archiveCursor(page.Results[len(page.Results)-1].Artifact)
			page.NextCursor = &cursor
			break
		}
		page.Results = append(page.Results, entry)
		size += len(encoded) + 1
	}
	return page, nil
}
