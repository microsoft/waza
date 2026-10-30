package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const mcpAttemptRuntimeEval = `name: mcp-attempt-runtime
scenario: mcp-attempt-runtime
description: Synthetic native runtime regression for MCP sequence lifetime.
schemaVersion: "2.0"
version: "1.0"
config:
  trials_per_task: 2
  timeout_seconds: 90
  parallel: false
  executor: copilot-sdk
  model: gpt-4.1
mcp_mocks:
  - name: counter
    tools:
      read:
        description: Read the next synthetic counter value.
        input_schema:
          type: object
          properties:
            id: { type: string }
          required: [id]
        responses:
          - match: { id: item }
            sequence:
              - return: { ordinal: 1 }
              - return: { ordinal: 2 }
          - match: { id: item }
            return: { fallback: true }
graders:
  - name: completed
    type: text
    config:
      contains: [observed]
tasks:
  - tasks/*.yaml
`

const mcpAttemptRuntimeFollowUp = `id: follow-up-counter
name: Follow-up counter
inputs:
  prompt: Read the synthetic counter.
  follow_up_prompts:
    - Read the synthetic counter again.
    - Read the synthetic counter a third time.
`

const mcpAttemptRuntimeResponder = `id: responder-counter
name: Responder counter
inputs:
  prompt: Read the synthetic counter.
  responder:
    instructions: Ask for another synthetic counter read twice, then stop.
    max_followups: 3
`

type syntheticChatRequest struct {
	Messages []struct {
		Role string `json:"role"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

func syntheticToolCall(id int64, name string, args any) map[string]any {
	encoded, _ := json.Marshal(args)
	return map[string]any{
		"role": "assistant", "content": nil,
		"tool_calls": []any{map[string]any{
			"id": fmt.Sprintf("call-%d", id), "type": "function",
			"function": map[string]any{"name": name, "arguments": string(encoded)},
		}},
	}
}

func mcpAttemptRuntimeEnvironment(stateRoot, cli, providerURL string) ([]string, error) {
	env := []string{
		"NO_COLOR=1", "WAZA_NO_UPDATE_CHECK=1",
		"COPILOT_BASE_URL=" + providerURL + "/v1", "COPILOT_API_KEY=synthetic-loopback",
		"COPILOT_PROVIDER_TYPE=openai", "COPILOT_WIRE_API=completions",
	}
	for _, directory := range []struct{ key, relative string }{
		{"HOME", "home"},
		{"USERPROFILE", "home"},
		{"COPILOT_HOME", filepath.Join("home", ".copilot")},
		{"APPDATA", "appdata"},
		{"LOCALAPPDATA", "localappdata"},
		{"XDG_CONFIG_HOME", "config"},
		{"XDG_CACHE_HOME", "cache"},
		{"XDG_DATA_HOME", "data"},
		{"XDG_STATE_HOME", "state"},
		{"XDG_RUNTIME_DIR", "runtime"},
		{"TMPDIR", "temp"},
		{"TMP", "temp"},
		{"TEMP", "temp"},
	} {
		path := filepath.Join(stateRoot, directory.relative)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("creating native runtime %s directory %q: %w", directory.key, path, err)
		}
		env = append(env, directory.key+"="+path)
	}
	if cli != "" {
		env = append(env, "COPILOT_CLI_PATH="+cli)
	}
	for _, key := range []string{"PATH", "SystemRoot", "WINDIR", "ComSpec", "PATHEXT"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env, nil
}

func inspectMCPAttemptRuntimeState(stateRoot string) error {
	return filepath.WalkDir(stateRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("inspecting native runtime state %q: %w", path, err)
		}
		if strings.HasPrefix(entry.Name(), "waza-mcp-") {
			return fmt.Errorf("leftover MCP state %q", path)
		}
		return nil
	})
}

// This opt-in probe uses the real Waza CLI and Copilot runtime, but only a
// loopback provider with synthetic responses. It never calls a hosted model.
// Profile, cache, and temporary roots are test-owned, not inherited.
func TestMCPMockSequenceSpansNativeRuntimeTurns(t *testing.T) {
	if os.Getenv("WAZA_MCP_RUNTIME_TEST") != "1" {
		t.Skip("set WAZA_MCP_RUNTIME_TEST=1 to run the synthetic native runtime probe")
	}
	cli := os.Getenv("COPILOT_CLI_PATH")
	if cli != "" {
		require.True(t, filepath.IsAbs(cli), "COPILOT_CLI_PATH must be absolute")
		info, err := os.Stat(cli)
		require.NoError(t, err)
		require.False(t, info.IsDir())
	}
	binary := buildRecoveryCLI(t)
	root := t.TempDir()
	evalDir, stateRoot := filepath.Join(root, "eval"), filepath.Join(root, "runtime-state")
	require.NoError(t, os.MkdirAll(filepath.Join(evalDir, "tasks"), 0o700))
	for name, content := range map[string]string{
		"eval.yaml":            mcpAttemptRuntimeEval,
		"tasks/follow-up.yaml": mcpAttemptRuntimeFollowUp,
		"tasks/responder.yaml": mcpAttemptRuntimeResponder,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(evalDir, filepath.FromSlash(name)), []byte(content), 0o600))
	}

	var requests, classifications, counterResults atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		count := requests.Add(1)
		var request syntheticChatRequest
		if err := json.Unmarshal(body, &request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tools := make([]string, 0, len(request.Tools))
		for _, tool := range request.Tools {
			tools = append(tools, tool.Function.Name)
		}
		last := ""
		if len(request.Messages) > 0 {
			last = request.Messages[len(request.Messages)-1].Role
		}
		message := map[string]any{"role": "assistant", "content": "observed synthetic counter result"}
		finish := "stop"
		if slices.Contains(tools, "responder_reply") {
			if last == "user" {
				finish = "tool_calls"
				if classifications.Add(1)%3 == 0 {
					message = syntheticToolCall(count, "responder_stop", map[string]any{})
				} else {
					message = syntheticToolCall(count, "responder_reply", map[string]any{"answer": "Read the synthetic counter again."})
				}
			}
		} else if last == "user" {
			for _, name := range tools {
				if strings.HasSuffix(name, "counter-read") {
					message, finish = syntheticToolCall(count, name, map[string]any{"id": "item"}), "tool_calls"
					counterResults.Add(1)
					break
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"id": fmt.Sprintf("synthetic-%d", count), "object": "chat.completion", "model": "gpt-4.1",
			"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
		}); err != nil {
			t.Errorf("writing synthetic response: %v", err)
		}
	}))
	defer server.Close()

	env, err := mcpAttemptRuntimeEnvironment(stateRoot, cli, server.URL)
	require.NoError(t, err, "isolating native runtime writable state")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	results := filepath.Join(root, "results.json")
	command := exec.CommandContext(ctx, binary, "run", "eval.yaml", "--output", results)
	command.Dir, command.Env = evalDir, env
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)

	data, err := os.ReadFile(results)
	require.NoError(t, err)
	var outcome struct {
		Tasks []struct {
			TestID string `json:"test_id"`
			Runs   []struct {
				RunNumber  int    `json:"run_number"`
				Status     string `json:"status"`
				ErrorMsg   string `json:"error_msg"`
				ToolEvents []struct {
					ToolName string          `json:"tool_name"`
					Success  bool            `json:"success"`
					Result   json.RawMessage `json:"result"`
					Error    string          `json:"error"`
				} `json:"tool_events"`
			} `json:"runs"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(data, &outcome))
	seen := map[string]int{}
	for _, task := range outcome.Tasks {
		require.Len(t, task.Runs, 2, task.TestID)
		for _, run := range task.Runs {
			seen[task.TestID]++
			require.Equal(t, "passed", run.Status, "%s run %d: %s", task.TestID, run.RunNumber, run.ErrorMsg)
			var counter []string
			var successes []bool
			for _, event := range run.ToolEvents {
				if event.ToolName != "counter-read" {
					continue
				}
				counter = append(counter, string(event.Result)+event.Error)
				successes = append(successes, event.Success)
			}
			require.Len(t, counter, 3, "%s run %d must call the mock on each actor turn", task.TestID, run.RunNumber)
			require.Contains(t, counter[0], `\"ordinal\":1`)
			require.Contains(t, counter[1], `\"ordinal\":2`, "follow-up/responder turn reset the sequence")
			require.Contains(t, counter[2], "response sequence exhausted")
			require.NotContains(t, counter[2], "fallback", "an exhausted first match must not fall through")
			require.Equal(t, []bool{true, true, false}, successes)
		}
	}
	require.Equal(t, map[string]int{"follow-up-counter": 2, "responder-counter": 2}, seen)
	require.EqualValues(t, 12, counterResults.Load(), "every actor turn must request the counter")
	require.NoError(t, inspectMCPAttemptRuntimeState(stateRoot))
}

func TestMCPAttemptRuntimeIsolation(t *testing.T) {
	root := t.TempDir()
	stateRoot, hostRoot := filepath.Join(root, "runtime-state"), filepath.Join(root, "host-state")
	profileKeys := []string{
		"HOME", "USERPROFILE", "COPILOT_HOME", "APPDATA", "LOCALAPPDATA",
		"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR",
		"TMPDIR", "TMP", "TEMP",
	}
	for _, key := range append(slices.Clone(profileKeys), "HOMEDRIVE", "HOMEPATH", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS") {
		t.Setenv(key, filepath.Join(hostRoot, key))
	}
	system := map[string]string{
		"PATH": "synthetic-path", "SystemRoot": "synthetic-system-root", "WINDIR": "synthetic-windir",
		"ComSpec": "synthetic-comspec", "PATHEXT": ".EXE;.CMD",
	}
	for key, value := range system {
		t.Setenv(key, value)
	}
	cli := filepath.Join(root, "reject-runtime")
	env, err := mcpAttemptRuntimeEnvironment(stateRoot, cli, "http://127.0.0.1:12345")
	require.NoError(t, err)
	values := map[string]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		require.True(t, ok, "invalid child environment entry %q", item)
		require.NotContains(t, values, key, "duplicate child environment entry %q", key)
		require.NotContains(t, value, hostRoot, "inherited host state through %s", key)
		values[key] = value
	}
	for key, value := range system {
		require.Equal(t, value, values[key], "required system environment %s", key)
	}
	require.Equal(t, cli, values["COPILOT_CLI_PATH"])
	require.Equal(t, "http://127.0.0.1:12345/v1", values["COPILOT_BASE_URL"])
	for _, key := range []string{"HOMEDRIVE", "HOMEPATH", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		require.NotContains(t, values, key, "non-allowlisted parent environment %s", key)
	}
	require.NoError(t, inspectMCPAttemptRuntimeState(stateRoot))
	for _, key := range profileKeys {
		t.Run(key, func(t *testing.T) {
			path := values[key]
			require.NotEmpty(t, path, "missing child state root %s", key)
			relative, err := filepath.Rel(stateRoot, path)
			require.NoError(t, err, "resolving child state root %s", key)
			require.True(t, filepath.IsLocal(relative), "child state root %s escapes %q: %q", key, stateRoot, path)
			require.DirExists(t, path)
			leaked := filepath.Join(path, "nested", "waza-mcp-leaked")
			require.NoError(t, os.MkdirAll(leaked, 0o700), "planting MCP residue beneath %s", key)
			require.ErrorContains(t, inspectMCPAttemptRuntimeState(stateRoot), fmt.Sprintf("%q", leaked), "cleanup must inspect %s", key)
			require.NoError(t, os.RemoveAll(filepath.Dir(leaked)), "removing planted MCP residue beneath %s", key)
			require.NoError(t, inspectMCPAttemptRuntimeState(stateRoot))
		})
	}
	require.NoDirExists(t, hostRoot, "environment setup must not create inherited host state")
}

func TestMCPAttemptRuntimeStateErrors(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "not-a-directory")
	require.NoError(t, os.WriteFile(blocked, nil, 0o600))
	env, err := mcpAttemptRuntimeEnvironment(blocked, "", "http://127.0.0.1:12345")
	require.ErrorContains(t, err, "creating native runtime HOME directory")
	require.ErrorContains(t, err, blocked)
	require.Nil(t, env)

	missing := filepath.Join(root, "missing")
	err = inspectMCPAttemptRuntimeState(missing)
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.ErrorContains(t, err, "inspecting native runtime state")
	require.ErrorContains(t, err, missing)
}
