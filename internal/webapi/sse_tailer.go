package webapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SessionLogTailer watches a directory for `*-session.jsonl` files
// (the format written by `waza run --session-log`) and publishes the
// decoded session events to a Broker as SSE-shaped events.
//
// It is intentionally simple: it polls the directory for the most
// recently modified `*-session.jsonl` file and tails it. When a newer
// session log appears, it switches to the new file. The previous file
// is closed and any unread tail is read first to flush late events.
type SessionLogTailer struct {
	dir      string
	broker   *Broker
	logger   *slog.Logger
	interval time.Duration
}

// NewSessionLogTailer creates a tailer that watches dir and publishes
// to broker. interval controls how often the directory is rescanned
// for newer session files; pass 0 for the default (500ms).
func NewSessionLogTailer(dir string, broker *Broker, logger *slog.Logger, interval time.Duration) *SessionLogTailer {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionLogTailer{
		dir:      dir,
		broker:   broker,
		logger:   logger,
		interval: interval,
	}
}

// Run blocks until ctx is canceled, watching the directory for
// session log files and publishing events to the broker.
func (t *SessionLogTailer) Run(ctx context.Context) {
	if t.broker == nil {
		return
	}

	var (
		curPath string
		curFile *os.File
		reader  *bufio.Reader
	)

	closeCurrent := func() {
		if curFile != nil {
			_ = curFile.Close()
		}
		curFile = nil
		reader = nil
		curPath = ""
	}
	defer closeCurrent()

	tick := time.NewTicker(t.interval)
	defer tick.Stop()

	// Track which files we've already seen so we don't re-publish
	// historical events when serve starts after a run completes. We
	// only tail files that appear OR change after the tailer starts.
	startTime := time.Now()
	seenInitial := make(map[string]struct{})
	if entries, err := listSessionLogs(t.dir); err == nil {
		for _, e := range entries {
			seenInitial[e.path] = struct{}{}
		}
	}

	var emitter sessionEmitter

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}

		newest, err := newestSessionLog(t.dir)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				t.logger.Debug("session-log tailer: scan error", "error", err)
			}
			continue
		}
		if newest.path == "" {
			continue
		}

		// Skip files that already existed when we started AND haven't
		// been modified since we started. This avoids replaying
		// completed runs into new clients.
		if _, ok := seenInitial[newest.path]; ok && !newest.modTime.After(startTime) {
			continue
		}

		if newest.path != curPath {
			closeCurrent()
			f, err := os.Open(newest.path)
			if err != nil {
				t.logger.Debug("session-log tailer: open failed", "path", newest.path, "error", err)
				continue
			}
			curFile = f
			curPath = newest.path
			reader = bufio.NewReader(f)
			emitter = sessionEmitter{}
			t.logger.Debug("session-log tailer: tailing new file", "path", newest.path)
		}

		// Read any new lines available.
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				emitter.emit(t.broker, strings.TrimRight(line, "\r\n"))
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.logger.Debug("session-log tailer: read error", "path", curPath, "error", err)
				break
			}
		}
	}
}

// sessionEmitter converts decoded session.Event lines into SSE events
// and tracks per-run aggregates needed to synthesize the run_complete
// event when the underlying log records session_complete.
type sessionEmitter struct {
	totalTasks int
	passCount  int
}

func (e *sessionEmitter) emit(broker *Broker, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var raw struct {
		Timestamp time.Time      `json:"timestamp"`
		Type      string         `json:"type"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return
	}
	switch raw.Type {
	case "task_start":
		broker.Publish(SSEEvent{
			Type:      "task_start",
			Timestamp: raw.Timestamp,
			Data: map[string]any{
				"taskName": stringField(raw.Data, "task_name"),
			},
		})
	case "task_complete":
		passed := stringField(raw.Data, "status") == "pass"
		if passed {
			e.passCount++
		}
		e.totalTasks++
		broker.Publish(SSEEvent{
			Type:      "task_complete",
			Timestamp: raw.Timestamp,
			Data: map[string]any{
				"taskName": stringField(raw.Data, "task_name"),
				"outcome":  stringField(raw.Data, "status"),
				"score":    floatField(raw.Data, "score"),
				"duration": floatField(raw.Data, "duration_ms"),
			},
		})
	case "grader_result":
		broker.Publish(SSEEvent{
			Type:      "grader_result",
			Timestamp: raw.Timestamp,
			Data: map[string]any{
				"graderName": stringField(raw.Data, "grader_name"),
				"graderType": stringField(raw.Data, "grader_type"),
				"passed":     boolField(raw.Data, "passed"),
				"score":      floatField(raw.Data, "score"),
				"message":    stringField(raw.Data, "feedback"),
			},
		})
	case "session_complete":
		total := intField(raw.Data, "total_tests")
		pass := intField(raw.Data, "passed")
		if total == 0 {
			total = e.totalTasks
		}
		if pass == 0 {
			pass = e.passCount
		}
		broker.Publish(SSEEvent{
			Type:      "run_complete",
			Timestamp: raw.Timestamp,
			Data: map[string]any{
				"totalTasks": total,
				"passCount":  pass,
			},
		})
		e.totalTasks = 0
		e.passCount = 0
	}
}

type sessionLogEntry struct {
	path    string
	modTime time.Time
}

func listSessionLogs(dir string) ([]sessionLogEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []sessionLogEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, "-session.jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, sessionLogEntry{
			path:    filepath.Join(dir, name),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].modTime.After(out[j].modTime)
	})
	return out, nil
}

func newestSessionLog(dir string) (sessionLogEntry, error) {
	entries, err := listSessionLogs(dir)
	if err != nil {
		return sessionLogEntry{}, err
	}
	if len(entries) == 0 {
		return sessionLogEntry{}, nil
	}
	return entries[0], nil
}

// StartSessionLogTailer is a convenience that spawns a goroutine
// running the tailer for the lifetime of ctx.
func StartSessionLogTailer(ctx context.Context, dir string, broker *Broker, logger *slog.Logger) {
	t := NewSessionLogTailer(dir, broker, logger, 0)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t.Run(ctx)
	}()
	go func() {
		<-ctx.Done()
		wg.Wait()
	}()
}

func stringField(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func boolField(m map[string]any, k string) bool {
	if v, ok := m[k]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

func floatField(m map[string]any, k string) float64 {
	if v, ok := m[k]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		}
	}
	return 0
}

func intField(m map[string]any, k string) int {
	if v, ok := m[k]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		}
	}
	return 0
}
