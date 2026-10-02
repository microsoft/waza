package commandmock

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/models"
)

type storedConfig struct {
	Workspace string       `json:"workspace"`
	LogDir    string       `json:"log_dir"`
	CreatedAt time.Time    `json:"created_at"`
	Mocks     []storedMock `json:"mocks"`
}

type storedMock struct {
	Name        string           `json:"name"`
	ExpectCalls *int             `json:"expect_calls,omitempty"`
	Responses   []storedResponse `json:"responses"`
}

type storedResponse struct {
	Args        []string          `json:"args,omitempty"`
	ArgsRegex   []string          `json:"args_regex,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	WorkDir     string            `json:"workdir,omitempty"`
	HasWorkDir  bool              `json:"has_workdir,omitempty"`
	Stdout      []byte            `json:"stdout,omitempty"`
	Stderr      string            `json:"stderr,omitempty"`
	ExitCode    int               `json:"exit_code"`
}

type invocationRecord struct {
	RecordedAt time.Time                `json:"recorded_at"`
	Invocation models.CommandInvocation `json:"invocation"`
}

// Session owns the temporary configuration and invocation history for a task.
type Session struct {
	id        string
	dir       string
	logDir    string
	workspace string
	mocks     []models.CommandMockConfig
	closed    bool
}

// NewSession validates and materializes task-scoped mock config outside the
// workspace so fixtures and credentials are not captured in workspace snapshots.
func NewSession(workspace string, mocks []models.CommandMockConfig, baseDir string) (*Session, error) {
	if err := models.ValidateCommandMocks(mocks); err != nil {
		return nil, err
	}
	root, shimDir, err := runtimePaths()
	if err != nil {
		return nil, err
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolving command-mock workspace: %w", err)
	}
	if baseDir == "" {
		baseDir = "."
	}
	baseDir, err = filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving command-mock fixture directory: %w", err)
	}

	stored := make([]storedMock, 0, len(mocks))
	for _, mock := range mocks {
		if err := ensureShim(mock.Name, shimDir); err != nil {
			return nil, err
		}
		item := storedMock{Name: mock.Name, ExpectCalls: mock.ExpectCalls}
		for _, response := range mock.Responses {
			output, err := responseOutput(response, baseDir)
			if err != nil {
				return nil, fmt.Errorf("command mock %q response fixture: %w", mock.Name, err)
			}
			workDir := ""
			if response.WorkDir != "" {
				workDir = filepath.ToSlash(filepath.Clean(response.WorkDir))
			}
			item.Responses = append(item.Responses, storedResponse{
				Args:        response.Args,
				ArgsRegex:   response.ArgsRegex,
				Environment: response.Environment,
				WorkDir:     workDir,
				HasWorkDir:  response.WorkDir != "",
				Stdout:      output,
				Stderr:      response.Stderr,
				ExitCode:    response.ExitCode,
			})
		}
		stored = append(stored, item)
	}

	sessionRoot := filepath.Join(root, "sessions")
	dir, err := os.MkdirTemp(sessionRoot, "task-*")
	if err != nil {
		return nil, fmt.Errorf("creating command-mock task directory: %w", err)
	}
	logDir := filepath.Join(dir, "calls")
	if err := os.Mkdir(logDir, 0700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("creating command-mock history directory: %w", err)
	}
	config := storedConfig{
		Workspace: workspace,
		LogDir:    logDir,
		CreatedAt: time.Now().UTC(),
		Mocks:     stored,
	}
	data, err := json.Marshal(config)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("encoding command-mock config: %w", err)
	}
	configFile, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("writing command-mock config: %w", err)
	}
	configPath := configFile.Name()
	defer func() {
		_ = os.Remove(configPath)
	}()
	if _, err := configFile.Write(data); err != nil {
		_ = configFile.Close()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("writing command-mock config: %w", err)
	}
	if err := configFile.Close(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("writing command-mock config: %w", err)
	}
	if err := os.Rename(configPath, filepath.Join(dir, "config.json")); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("writing command-mock config: %w", err)
	}
	return &Session{id: filepath.Base(dir), dir: dir, logDir: logDir, workspace: workspace, mocks: mocks}, nil
}

// ID returns the opaque task identifier passed to command-mock shims.
func (s *Session) ID() string {
	if s == nil {
		return ""
	}
	return s.id
}

func responseOutput(response models.CommandMockResponse, baseDir string) ([]byte, error) {
	if response.Fixture != "" {
		fixture := filepath.Join(baseDir, filepath.FromSlash(response.Fixture))
		data, err := os.ReadFile(fixture)
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	if response.Stdout == nil {
		return nil, nil
	}
	if text, ok := response.Stdout.(string); ok {
		return []byte(text), nil
	}
	data, err := json.Marshal(response.Stdout)
	if err != nil {
		return nil, fmt.Errorf("encoding stdout as JSON: %w", err)
	}
	return data, nil
}

// Invocations returns sanitized invocation records in chronological order.
func (s *Session) Invocations() []models.CommandInvocation {
	if s == nil || s.closed {
		return nil
	}
	entries, err := os.ReadDir(s.logDir)
	if err != nil {
		return nil
	}
	var records []invocationRecord
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.logDir, entry.Name()))
		if err != nil {
			continue
		}
		var record invocationRecord
		if json.Unmarshal(data, &record) == nil {
			records = append(records, record)
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].RecordedAt.Before(records[j].RecordedAt)
	})
	invocations := make([]models.CommandInvocation, len(records))
	for i, record := range records {
		invocations[i] = record.Invocation
	}
	return invocations
}

// Close removes task-private configuration and verifies exact call counts.
func (s *Session) Close() ([]models.CommandInvocation, error) {
	if s == nil {
		return nil, nil
	}
	invocations := s.Invocations()
	var expectationErr error
	for _, mock := range s.mocks {
		if mock.ExpectCalls == nil {
			continue
		}
		count := 0
		for _, invocation := range invocations {
			if strings.EqualFold(invocation.Command, mock.Name) {
				count++
			}
		}
		if count != *mock.ExpectCalls {
			expectationErr = fmt.Errorf("command mock %q expected %d call(s), got %d", mock.Name, *mock.ExpectCalls, count)
			break
		}
	}
	if !s.closed {
		s.closed = true
		if err := os.RemoveAll(s.dir); err != nil && expectationErr == nil {
			expectationErr = fmt.Errorf("removing command-mock task data: %w", err)
		}
	}
	return invocations, expectationErr
}

type invocationResult struct {
	Stdout        []byte
	Stderr        string
	ExitCode      int
	ResponseIndex int
	PassThrough   bool
}

// Invoke chooses the first matching response for the task session. The cwd
// fallback supports callers without an explicit session ID.
func Invoke(root, sessionID, name string, args []string, cwd string) (invocationResult, error) {
	config, mock, err := findMock(root, sessionID, name, cwd)
	if err != nil {
		return invocationResult{}, err
	}
	if mock == nil {
		return invocationResult{PassThrough: true}, nil
	}
	for i, response := range mock.Responses {
		matched, err := responseMatches(response, args, cwd, config.Workspace)
		if err != nil {
			return invocationResult{}, err
		}
		if !matched {
			continue
		}
		result := invocationResult{
			Stdout:        response.Stdout,
			Stderr:        response.Stderr,
			ExitCode:      response.ExitCode,
			ResponseIndex: i,
		}
		if err := recordInvocation(config.LogDir, name, args, result.ExitCode, i); err != nil {
			return invocationResult{}, err
		}
		return result, nil
	}
	message := fmt.Sprintf("waza command mock: no matching response for executable %q; add a command_mocks.responses fixture for this CLI invocation", name)
	if err := recordInvocation(config.LogDir, name, args, 127, -1); err != nil {
		return invocationResult{}, err
	}
	return invocationResult{Stderr: message + "\n", ExitCode: 127, ResponseIndex: -1}, nil
}

func findMock(root, sessionID, name, cwd string) (storedConfig, *storedMock, error) {
	if sessionID != "" {
		if !strings.HasPrefix(sessionID, "task-") || filepath.Base(sessionID) != sessionID {
			return storedConfig{}, nil, fmt.Errorf("invalid command-mock session")
		}
		config, err := readStoredConfig(filepath.Join(root, "sessions", sessionID, "config.json"))
		if err != nil {
			return storedConfig{}, nil, err
		}
		return config, findStoredMock(config.Mocks, name), nil
	}

	sessions, err := os.ReadDir(filepath.Join(root, "sessions"))
	if err != nil {
		return storedConfig{}, nil, err
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return storedConfig{}, nil, fmt.Errorf("resolving command-mock working directory: %w", err)
	}
	var selected storedConfig
	var selectedMock *storedMock
	for _, session := range sessions {
		if !session.IsDir() {
			continue
		}
		path := filepath.Join(root, "sessions", session.Name(), "config.json")
		config, err := readStoredConfig(path)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(config.Workspace, absCWD)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		mock := findStoredMock(config.Mocks, name)
		if mock != nil && (selectedMock == nil || config.CreatedAt.After(selected.CreatedAt)) {
			selected = config
			selectedMock = mock
		}
	}
	return selected, selectedMock, nil
}

func readStoredConfig(path string) (storedConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return storedConfig{}, fmt.Errorf("reading command-mock config: %w", err)
	}
	var config storedConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return storedConfig{}, fmt.Errorf("reading command-mock config: %w", err)
	}
	return config, nil
}

func findStoredMock(mocks []storedMock, name string) *storedMock {
	for i := range mocks {
		if sameCommand(mocks[i].Name, name) {
			mock := mocks[i]
			return &mock
		}
	}
	return nil
}

func sameCommand(left, right string) bool {
	if os.PathSeparator == '\\' {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func responseMatches(response storedResponse, args []string, cwd, workspace string) (bool, error) {
	if response.Args != nil {
		if len(response.Args) != len(args) {
			return false, nil
		}
		for i := range args {
			if response.Args[i] != args[i] {
				return false, nil
			}
		}
	} else {
		if len(response.ArgsRegex) != len(args) {
			return false, nil
		}
		for i, pattern := range response.ArgsRegex {
			re, err := regexp.Compile("^(?:" + pattern + ")$")
			if err != nil {
				return false, fmt.Errorf("invalid command-mock regex: %w", err)
			}
			if !re.MatchString(args[i]) {
				return false, nil
			}
		}
	}
	for key, expected := range response.Environment {
		if actual, ok := os.LookupEnv(key); !ok || actual != expected {
			return false, nil
		}
	}
	if response.HasWorkDir {
		rel, err := filepath.Rel(workspace, cwd)
		if err != nil || filepath.ToSlash(filepath.Clean(rel)) != response.WorkDir {
			return false, nil
		}
	}
	return true, nil
}

func recordInvocation(logDir, name string, args []string, exitCode, responseIndex int) error {
	invocation := models.CommandInvocation{
		Command:       name,
		Args:          sanitizeArgs(args, os.Environ()),
		ExitCode:      exitCode,
		ResponseIndex: responseIndex,
	}
	record := invocationRecord{RecordedAt: time.Now().UTC(), Invocation: invocation}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(logDir, "call-*.json")
	if err != nil {
		return fmt.Errorf("recording command-mock invocation: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return fmt.Errorf("recording command-mock invocation: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return fmt.Errorf("recording command-mock invocation: %w", err)
	}
	return nil
}
