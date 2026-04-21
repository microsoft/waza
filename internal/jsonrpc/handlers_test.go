package jsonrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper to send a JSON-RPC request and decode the response
func rpcCall(t *testing.T, server *Server, method string, params any) Response {
	t.Helper()
	paramsJSON, err := json.Marshal(params)
	require.NoError(t, err)

	reqLine := fmt.Sprintf(`{"jsonrpc":"2.0","method":"%s","params":%s,"id":1}`, method, string(paramsJSON))
	var out bytes.Buffer
	server.ServeStdio(strings.NewReader(reqLine+"\n"), &out)

	var resp Response
	require.NoError(t, json.Unmarshal(out.Bytes(), &resp))
	return resp
}

func newTestServer() *Server {
	registry := NewMethodRegistry()
	hctx := NewHandlerContext()
	RegisterHandlers(registry, hctx)
	return NewServer(registry, nil)
}

func TestHandler_EvalList_InvalidParams(t *testing.T) {
	server := newTestServer()
	resp := rpcCall(t, server, "eval.list", "not an object")
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeInvalidParams, resp.Error.Code)
}

func TestHandler_EvalList_MissingDir(t *testing.T) {
	server := newTestServer()
	resp := rpcCall(t, server, "eval.list", map[string]string{})
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeInvalidParams, resp.Error.Code)
}

func TestHandler_EvalList_Success(t *testing.T) {
	// Create a temp dir with an eval.yaml
	dir := t.TempDir()
	evalContent := `name: test-eval
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
tasks:
  - "tasks/*.yaml"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte(evalContent), 0644))

	server := newTestServer()
	resp := rpcCall(t, server, "eval.list", map[string]string{"dir": dir})
	assert.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalListResult
	require.NoError(t, json.Unmarshal(data, &result))
	assert.Len(t, result.Evals, 1)
	assert.Equal(t, "test-eval", result.Evals[0].Name)
}

func TestHandler_EvalGet_NotFound(t *testing.T) {
	server := newTestServer()
	resp := rpcCall(t, server, "eval.get", map[string]string{"path": "/nonexistent/eval.yaml"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeEvalNotFound, resp.Error.Code)
}

func TestHandler_EvalGet_Success(t *testing.T) {
	dir := t.TempDir()
	evalContent := `name: my-eval
description: A test eval
skill: my-skill
config:
  trials_per_task: 2
  timeout_seconds: 60
  executor: mock
  model: gpt-4
graders:
  - type: code
    name: check-output
    config:
      assertions:
        - "len(output) > 0"
tasks:
  - "tasks/*.yaml"
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	server := newTestServer()
	resp := rpcCall(t, server, "eval.get", map[string]string{"path": evalPath})
	assert.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalGetResult
	require.NoError(t, json.Unmarshal(data, &result))
	assert.Equal(t, "my-eval", result.Name)
	assert.Equal(t, "my-skill", result.SkillName)
	assert.Equal(t, "mock", result.Config.EngineType)
}

func TestHandler_EvalValidate_Valid(t *testing.T) {
	dir := t.TempDir()
	evalContent := `name: valid-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	server := newTestServer()
	resp := rpcCall(t, server, "eval.validate", map[string]string{"path": evalPath})
	assert.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalValidateResult
	require.NoError(t, json.Unmarshal(data, &result))
	assert.True(t, result.Valid, "expected valid but got errors: %v", result.Errors)
	assert.Empty(t, result.Errors)
}

func TestHandler_EvalValidate_Invalid(t *testing.T) {
	dir := t.TempDir()
	// Missing required fields
	evalContent := `name: bad-eval
config:
  trials_per_task: 0
  timeout_seconds: 0
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	server := newTestServer()
	resp := rpcCall(t, server, "eval.validate", map[string]string{"path": evalPath})
	assert.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalValidateResult
	require.NoError(t, json.Unmarshal(data, &result))
	assert.False(t, result.Valid)
	assert.NotEmpty(t, result.Errors)
}

func TestHandler_EvalRun_Success(t *testing.T) {
	dir := t.TempDir()
	evalContent := `name: run-eval
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
tasks:
  - "tasks/*.yaml"
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	server := newTestServer()
	resp := rpcCall(t, server, "eval.run", map[string]string{"path": evalPath})
	assert.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalRunResult
	require.NoError(t, json.Unmarshal(data, &result))
	assert.NotEmpty(t, result.RunID)
	assert.True(t, strings.HasPrefix(result.RunID, "run-"))
}

func TestHandler_EvalRun_NotFound(t *testing.T) {
	server := newTestServer()
	resp := rpcCall(t, server, "eval.run", map[string]string{"path": "/nonexistent/eval.yaml"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeEvalNotFound, resp.Error.Code)
}

func TestHandler_RunStatus(t *testing.T) {
	dir := t.TempDir()
	evalContent := `name: status-eval
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
tasks:
  - "tasks/*.yaml"
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	// Create server with shared handler context
	registry := NewMethodRegistry()
	hctx := NewHandlerContext()
	RegisterHandlers(registry, hctx)
	server := NewServer(registry, nil)

	// Start a run via direct handler call (since rpcCall creates fresh stdin)
	resp := rpcCall(t, server, "eval.run", map[string]string{"path": evalPath})
	require.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var runResult EvalRunResult
	require.NoError(t, json.Unmarshal(data, &runResult))

	// Wait briefly for the goroutine to complete
	time.Sleep(50 * time.Millisecond)

	// Check status
	resp = rpcCall(t, server, "run.status", map[string]string{"run_id": runResult.RunID})
	assert.Nil(t, resp.Error)

	data, err = json.Marshal(resp.Result)
	require.NoError(t, err)
	var state RunState
	require.NoError(t, json.Unmarshal(data, &state))
	assert.Equal(t, runResult.RunID, state.ID)
}

func TestHandler_RunStatus_NotFound(t *testing.T) {
	server := newTestServer()
	resp := rpcCall(t, server, "run.status", map[string]string{"run_id": "nonexistent"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeInvalidParams, resp.Error.Code)
}

func TestHandler_RunCancel_NotFound(t *testing.T) {
	server := newTestServer()
	resp := rpcCall(t, server, "run.cancel", map[string]string{"run_id": "nonexistent"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeInvalidParams, resp.Error.Code)
}

func TestHandler_TaskList_NotFound(t *testing.T) {
	server := newTestServer()
	resp := rpcCall(t, server, "task.list", map[string]string{"path": "/nonexistent/eval.yaml"})
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeEvalNotFound, resp.Error.Code)
}

func TestHandler_TaskGet_MissingParams(t *testing.T) {
	server := newTestServer()
	resp := rpcCall(t, server, "task.get", map[string]string{"path": ""})
	require.NotNil(t, resp.Error)
	assert.Equal(t, CodeInvalidParams, resp.Error.Code)
}

func TestHandler_CancelFuncCleanup(t *testing.T) {
	dir := t.TempDir()
	evalContent := `name: cleanup-eval
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
tasks:
  - "tasks/*.yaml"
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	registry := NewMethodRegistry()
	hctx := NewHandlerContext()
	RegisterHandlers(registry, hctx)
	server := NewServer(registry, nil)

	// Start a run
	resp := rpcCall(t, server, "eval.run", map[string]string{"path": evalPath})
	require.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var runResult EvalRunResult
	require.NoError(t, json.Unmarshal(data, &runResult))

	// Wait for the goroutine to complete and clean up
	time.Sleep(50 * time.Millisecond)

	// Verify cancelFunc was cleaned up
	hctx.mu.Lock()
	_, exists := hctx.cancelFuncs[runResult.RunID]
	hctx.mu.Unlock()

	assert.False(t, exists, "cancelFunc should be cleaned up after run completes")
}

func TestAllMethodsRegistered(t *testing.T) {
	registry := NewMethodRegistry()
	hctx := NewHandlerContext()
	RegisterHandlers(registry, hctx)

	expected := []string{
		"eval.list", "eval.get", "eval.validate", "eval.run",
		"task.list", "task.get", "run.status", "run.cancel",
	}

	for _, method := range expected {
		assert.NotNil(t, registry.Lookup(method), "method %q should be registered", method)
	}
}

// --- Schema validation tests ---

func TestHandler_EvalValidate_Schema_InvalidGraderType(t *testing.T) {
	dir := t.TempDir()
	evalContent := `name: grader-type-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
graders:
  - type: bogus_grader
    name: bad-grader
tasks:
  - "tasks/*.yaml"
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	server := newTestServer()
	resp := rpcCall(t, server, "eval.validate", map[string]string{"path": evalPath})
	assert.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalValidateResult
	require.NoError(t, json.Unmarshal(data, &result))
	assert.False(t, result.Valid, "expected invalid for bogus grader type")

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "schema:") && strings.Contains(e, "graders") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected schema error mentioning graders, got: %v", result.Errors)
}

func TestHandler_EvalValidate_Schema_MissingRequiredName(t *testing.T) {
	dir := t.TempDir()
	// Missing "name" which is required by the schema
	evalContent := `skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	server := newTestServer()
	resp := rpcCall(t, server, "eval.validate", map[string]string{"path": evalPath})
	assert.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalValidateResult
	require.NoError(t, json.Unmarshal(data, &result))
	assert.False(t, result.Valid, "expected invalid for missing name")

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "name") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected error about missing 'name', got: %v", result.Errors)
}

func TestHandler_EvalValidate_Schema_ActionSequenceMissingConfig(t *testing.T) {
	dir := t.TempDir()
	// action_sequence grader requires config with expected_actions
	evalContent := `name: action-seq-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
graders:
  - type: action_sequence
    name: seq-check
tasks:
  - "tasks/*.yaml"
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	server := newTestServer()
	resp := rpcCall(t, server, "eval.validate", map[string]string{"path": evalPath})
	assert.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalValidateResult
	require.NoError(t, json.Unmarshal(data, &result))
	assert.False(t, result.Valid, "expected invalid for action_sequence without config")

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "schema:") && strings.Contains(e, "config") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected schema error about missing config, got: %v", result.Errors)
}

// --- Task validation tests ---

func TestHandler_EvalValidate_WithValidTasks(t *testing.T) {
	dir := t.TempDir()

	evalContent := `name: valid-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	tasksDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0755))

	taskContent := `id: task-1
name: Basic Task
inputs:
  prompt: "Explain hello world"
`
	require.NoError(t, os.WriteFile(filepath.Join(tasksDir, "basic.yaml"), []byte(taskContent), 0644))

	server := newTestServer()
	resp := rpcCall(t, server, "eval.validate", map[string]string{"path": evalPath})
	assert.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalValidateResult
	require.NoError(t, json.Unmarshal(data, &result))
	assert.True(t, result.Valid, "expected valid but got errors: %v", result.Errors)
	assert.Empty(t, result.Errors)
}

func TestHandler_EvalValidate_WithInvalidTask(t *testing.T) {
	dir := t.TempDir()

	evalContent := `name: invalid-task-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	tasksDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0755))

	// Missing required "id" field
	taskContent := `name: Bad Task
inputs:
  prompt: "Do something"
`
	require.NoError(t, os.WriteFile(filepath.Join(tasksDir, "bad.yaml"), []byte(taskContent), 0644))

	server := newTestServer()
	resp := rpcCall(t, server, "eval.validate", map[string]string{"path": evalPath})
	assert.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalValidateResult
	require.NoError(t, json.Unmarshal(data, &result))
	assert.False(t, result.Valid, "expected invalid for task missing id")

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "bad.yaml") && strings.Contains(e, "id") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected task error about missing 'id', got: %v", result.Errors)
}

func TestHandler_EvalValidate_TaskMissingPrompt(t *testing.T) {
	dir := t.TempDir()

	evalContent := `name: missing-prompt-eval
skill: test-skill
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 30
  executor: mock
  model: test
metrics:
  - name: accuracy
    weight: 1.0
    threshold: 0.8
tasks:
  - "tasks/*.yaml"
`
	evalPath := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(evalPath, []byte(evalContent), 0644))

	tasksDir := filepath.Join(dir, "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0755))

	// Missing required "inputs.prompt"
	taskContent := `id: task-no-prompt
name: No Prompt Task
inputs:
  context:
    key: value
`
	require.NoError(t, os.WriteFile(filepath.Join(tasksDir, "noprompt.yaml"), []byte(taskContent), 0644))

	server := newTestServer()
	resp := rpcCall(t, server, "eval.validate", map[string]string{"path": evalPath})
	assert.Nil(t, resp.Error)

	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalValidateResult
	require.NoError(t, json.Unmarshal(data, &result))
	assert.False(t, result.Valid, "expected invalid for task missing prompt")

	found := false
	for _, e := range result.Errors {
		if strings.Contains(e, "noprompt.yaml") && strings.Contains(e, "prompt") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected task error about missing 'prompt', got: %v", result.Errors)
}
