package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/jsonrpc"
)

func TestHandleInitialize(t *testing.T) {
	srv := NewServer(slog.Default())
	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`),
		ID:      json.RawMessage(`1`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response, got nil")
		return
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	data, _ := json.Marshal(resp.Result)
	var result initializeResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.ProtocolVersion != protocolVersion {
		t.Errorf("protocolVersion = %q, want %q", result.ProtocolVersion, protocolVersion)
	}
	if result.ServerInfo.Name != "waza" {
		t.Errorf("serverInfo.name = %q, want %q", result.ServerInfo.Name, "waza")
	}
	if result.Capabilities.Tools == nil {
		t.Error("expected tools capability")
	}
}

func TestHandleToolsList(t *testing.T) {
	srv := NewServer(slog.Default())
	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/list",
		Params:  json.RawMessage(`{}`),
		ID:      json.RawMessage(`2`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response, got nil")
		return
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	data, _ := json.Marshal(resp.Result)
	var result toolsListResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Tools) != 10 {
		t.Errorf("got %d tools, want 10", len(result.Tools))
	}

	// Check all expected tool names exist.
	expected := map[string]bool{
		"waza_eval_list":       false,
		"waza_eval_get":        false,
		"waza_eval_validate":   false,
		"waza_eval_run":        false,
		"waza_task_list":       false,
		"waza_run_status":      false,
		"waza_run_cancel":      false,
		"waza_results_summary": false,
		"waza_results_runs":    false,
		"waza_skill_check":     false,
	}
	for _, tool := range result.Tools {
		if _, ok := expected[tool.Name]; ok {
			expected[tool.Name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestHandleToolsCallEvalList(t *testing.T) {
	srv := NewServer(slog.Default())
	// Use a temp dir with no eval files — should return empty list.
	dir := t.TempDir()
	args, _ := json.Marshal(map[string]string{"dir": dir})
	params, _ := json.Marshal(toolsCallParams{Name: "waza_eval_list", Arguments: args})

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`3`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response, got nil")
		return
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	data, _ := json.Marshal(resp.Result)
	var result toolsCallResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %s", result.Content[0].Text)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("expected 1 text content block, got %d", len(result.Content))
	}
}

func TestHandleToolsCallSkillCheck(t *testing.T) {
	srv := NewServer(slog.Default())
	dir := t.TempDir()
	args, _ := json.Marshal(map[string]string{"skill_path": dir})
	params, _ := json.Marshal(toolsCallParams{Name: "waza_skill_check", Arguments: args})

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`4`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response, got nil")
		return
	}

	data, _ := json.Marshal(resp.Result)
	var result toolsCallResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %s", result.Content[0].Text)
	}

	// Parse the inner JSON to verify structure.
	var check skillCheckResult
	if err := json.Unmarshal([]byte(result.Content[0].Text), &check); err != nil {
		t.Fatalf("failed to unmarshal skill check: %v", err)
	}
	if check.HasSkill {
		t.Error("expected HasSkill=false for empty dir")
	}
	if check.HasEval {
		t.Error("expected HasEval=false for empty dir")
	}
}

func TestHandleToolsCallUnknownTool(t *testing.T) {
	srv := NewServer(slog.Default())
	params, _ := json.Marshal(toolsCallParams{Name: "nonexistent", Arguments: json.RawMessage(`{}`)})

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`5`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response, got nil")
		return
	}

	data, _ := json.Marshal(resp.Result)
	var result toolsCallResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for unknown tool")
	}
}

func TestHandleUnknownMethod(t *testing.T) {
	srv := NewServer(slog.Default())
	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "unknown/method",
		Params:  json.RawMessage(`{}`),
		ID:      json.RawMessage(`6`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response, got nil")
		return
	}
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != jsonrpc.CodeMethodNotFound {
		t.Errorf("error code = %d, want %d", resp.Error.Code, jsonrpc.CodeMethodNotFound)
	}
}

func TestHandleResultsSummary(t *testing.T) {
	srv := NewServer(slog.Default())
	dir := t.TempDir()
	args, _ := json.Marshal(map[string]string{"dir": dir})
	params, _ := json.Marshal(toolsCallParams{Name: "waza_results_summary", Arguments: args})

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`7`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response, got nil")
		return
	}
	data, _ := json.Marshal(resp.Result)
	var result toolsCallResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %s", result.Content[0].Text)
	}
}

func TestHandleResultsRuns(t *testing.T) {
	srv := NewServer(slog.Default())
	dir := t.TempDir()
	args, _ := json.Marshal(map[string]string{"dir": dir})
	params, _ := json.Marshal(toolsCallParams{Name: "waza_results_runs", Arguments: args})

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`8`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response, got nil")
		return
	}
	data, _ := json.Marshal(resp.Result)
	var result toolsCallResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %s", result.Content[0].Text)
	}
}

func TestServeStdio(t *testing.T) {
	// Send initialize + tools/list, then EOF.
	initReq := `{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}},"id":1}` + "\n"
	listReq := `{"jsonrpc":"2.0","method":"tools/list","params":{},"id":2}` + "\n"

	input := strings.NewReader(initReq + listReq)
	var output bytes.Buffer

	ServeStdio(context.Background(), input, &output, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Parse responses line by line.
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 response lines, got %d: %s", len(lines), output.String())
	}

	// Verify initialize response.
	var initResp jsonrpc.Response
	if err := json.Unmarshal([]byte(lines[0]), &initResp); err != nil {
		t.Fatalf("unmarshal init response: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("init error: %v", initResp.Error)
	}

	// Verify tools/list response.
	var listResp jsonrpc.Response
	if err := json.Unmarshal([]byte(lines[1]), &listResp); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if listResp.Error != nil {
		t.Fatalf("list error: %v", listResp.Error)
	}
}

func TestNotificationSkipped(t *testing.T) {
	// Notifications (no "id" field) should not produce a response.
	notif := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}` + "\n"
	input := strings.NewReader(notif)
	var output bytes.Buffer

	ServeStdio(context.Background(), input, &output, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if output.Len() != 0 {
		t.Errorf("expected no output for notification, got: %s", output.String())
	}
}
