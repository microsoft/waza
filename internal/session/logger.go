package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger defines the interface for session event logging.
type Logger interface {
	Log(event Event) error
	Close() error
}

// JSONLogger writes events as newline-delimited JSON (NDJSON).
type JSONLogger struct {
	mu   sync.Mutex
	file *os.File
	enc  *json.Encoder
	path string
}

// NewJSONLogger creates a logger that writes NDJSON to the given path.
// Parent directories are created automatically.
func NewJSONLogger(path string) (*JSONLogger, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating session log directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening session log: %w", err)
	}

	return &JSONLogger{
		file: f,
		enc:  json.NewEncoder(f),
		path: path,
	}, nil
}

// Log writes a single event as one JSON line.
func (l *JSONLogger) Log(event Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enc.Encode(event)
}

// Close flushes and closes the underlying file.
func (l *JSONLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

// Path returns the file path of the session log.
func (l *JSONLogger) Path() string {
	return l.path
}

// NopLogger discards all events. Useful as a default when logging is disabled.
type NopLogger struct{}

// Log is a no-op.
func (NopLogger) Log(Event) error { return nil }

// Close is a no-op.
func (NopLogger) Close() error { return nil }

// LogFileGlob matches every file name DefaultLogPath produces.
const LogFileGlob = "????????T??????Z-session.jsonl"

// DefaultLogPath returns a timestamped session log path inside dir.
func DefaultLogPath(dir string) string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	return filepath.Join(dir, fmt.Sprintf("%s-session.jsonl", ts))
}
