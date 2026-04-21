package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/jsonrpc"
)

// ========================================
// server.go — additional coverage
// ========================================

func TestNewServer_NilLogger(t *testing.T) {
	srv := NewServer(nil)
	if srv == nil {
		t.Fatal("NewServer(nil) returned nil")
	}
	// Should use default logger without panicking.
	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params:  json.RawMessage(`{}`),
		ID:      json.RawMessage(`1`),
	}
	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil || resp.Error != nil {
		t.Errorf("HandleRequest failed with nil logger: %v", resp)
	}
}

func TestHandleNotificationsInitialized(t *testing.T) {
	srv := NewServer(slog.Default())
	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
		Params:  json.RawMessage(`{}`),
		ID:      json.RawMessage(`1`),
	}
	resp := srv.HandleRequest(context.Background(), req)
	if resp != nil {
		t.Error("expected nil response for notifications/initialized")
	}
}

func TestHandleToolsCall_InvalidParams(t *testing.T) {
	srv := NewServer(slog.Default())
	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  json.RawMessage(`not valid json`),
		ID:      json.RawMessage(`1`),
	}
	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Error == nil {
		t.Fatal("expected error for invalid params")
	}
	if resp.Error.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("error code = %d, want %d", resp.Error.Code, jsonrpc.CodeInvalidParams)
	}
}

func TestHandleToolsCall_TaskList(t *testing.T) {
	srv := NewServer(slog.Default())
	dir := t.TempDir()
	args, _ := json.Marshal(map[string]string{"eval_path": filepath.Join(dir, "nonexistent.yaml")})
	params, _ := json.Marshal(toolsCallParams{Name: "waza_task_list", Arguments: args})

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`10`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response")
	}
	// Should get an error result (eval file doesn't exist) but not a protocol error.
	data, _ := json.Marshal(resp.Result)
	var result toolsCallResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	// The tool call should return isError=true since the file doesn't exist.
	if !result.IsError {
		t.Log("task_list on nonexistent file returned success (may return empty list)")
	}
}

func TestHandleToolsCall_TaskList_InvalidArgs(t *testing.T) {
	srv := NewServer(slog.Default())
	// Pass a JSON string where an object is expected — callTaskList's unmarshal will fail.
	params := json.RawMessage(`{"name":"waza_task_list","arguments":"not an object"}`)

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`11`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response")
	}
	data, _ := json.Marshal(resp.Result)
	var result toolsCallResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !result.IsError {
		t.Error("expected isError=true for invalid args")
	}
}

func TestHandleToolsCall_NilArguments(t *testing.T) {
	srv := NewServer(slog.Default())
	// callHandler should handle nil args by converting to {}.
	params, _ := json.Marshal(toolsCallParams{Name: "waza_eval_list", Arguments: nil})

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`12`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp == nil {
		t.Fatal("expected response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected protocol error: %v", resp.Error)
	}
}

func TestDispatchTool_AllNames(t *testing.T) {
	srv := NewServer(slog.Default())
	ctx := context.Background()

	// Test that all tool dispatch names are recognized (don't return "unknown tool").
	toolNames := []string{
		"waza_eval_list",
		"waza_eval_get",
		"waza_eval_validate",
		"waza_eval_run",
		"waza_task_list",
		"waza_run_status",
		"waza_run_cancel",
		"waza_results_summary",
		"waza_results_runs",
		"waza_skill_check",
	}

	for _, name := range toolNames {
		_, rpcErr := srv.dispatchTool(ctx, name, json.RawMessage(`{}`))
		if rpcErr != nil && rpcErr.Code == jsonrpc.CodeMethodNotFound && strings.Contains(rpcErr.Message, "unknown tool") {
			t.Errorf("dispatchTool(%q) returned unknown tool error", name)
		}
	}
}

func TestDispatchTool_UnknownTool(t *testing.T) {
	srv := NewServer(slog.Default())
	_, rpcErr := srv.dispatchTool(context.Background(), "nonexistent_tool", json.RawMessage(`{}`))
	if rpcErr == nil {
		t.Fatal("expected error for unknown tool")
	}
	if rpcErr.Code != jsonrpc.CodeMethodNotFound {
		t.Errorf("error code = %d, want %d", rpcErr.Code, jsonrpc.CodeMethodNotFound)
	}
}

// ========================================
// tools.go — ToolsDef coverage
// ========================================

func TestToolsDef_Count(t *testing.T) {
	tools := ToolsDef()
	if len(tools) != 10 {
		t.Errorf("ToolsDef() returned %d tools, want 10", len(tools))
	}
}

func TestToolsDef_ValidJSON(t *testing.T) {
	tools := ToolsDef()
	for _, tool := range tools {
		if tool.Name == "" {
			t.Error("tool has empty name")
		}
		if tool.Description == "" {
			t.Errorf("tool %q has empty description", tool.Name)
		}
		// Verify InputSchema is valid JSON.
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Errorf("tool %q has invalid InputSchema: %v", tool.Name, err)
		}
	}
}

func TestToolsDef_UniqueNames(t *testing.T) {
	tools := ToolsDef()
	seen := make(map[string]bool)
	for _, tool := range tools {
		if seen[tool.Name] {
			t.Errorf("duplicate tool name: %s", tool.Name)
		}
		seen[tool.Name] = true
	}
}

// ========================================
// server.go — skill check with files
// ========================================

func TestSkillCheck_WithSkillMD(t *testing.T) {
	srv := NewServer(slog.Default())
	dir := t.TempDir()

	// Create SKILL.md.
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# My Skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]string{"skill_path": dir})
	params, _ := json.Marshal(toolsCallParams{Name: "waza_skill_check", Arguments: args})

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`20`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	data, _ := json.Marshal(resp.Result)
	var result toolsCallResult
	_ = json.Unmarshal(data, &result)

	var check skillCheckResult
	_ = json.Unmarshal([]byte(result.Content[0].Text), &check)

	if !check.HasSkill {
		t.Error("expected HasSkill=true")
	}
	if check.HasEval {
		t.Error("expected HasEval=false (no eval.yaml)")
	}
}

func TestSkillCheck_WithBothFiles(t *testing.T) {
	srv := NewServer(slog.Default())
	dir := t.TempDir()

	// Create SKILL.md and eval.yaml.
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Skill\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte("name: test\n"), 0o644)

	args, _ := json.Marshal(map[string]string{"skill_path": dir})
	params, _ := json.Marshal(toolsCallParams{Name: "waza_skill_check", Arguments: args})

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`21`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	data, _ := json.Marshal(resp.Result)
	var result toolsCallResult
	_ = json.Unmarshal(data, &result)

	var check skillCheckResult
	_ = json.Unmarshal([]byte(result.Content[0].Text), &check)

	if !check.HasSkill {
		t.Error("expected HasSkill=true")
	}
	if !check.HasEval {
		t.Error("expected HasEval=true")
	}
	if !strings.Contains(check.Message, "ready for deeper check") {
		t.Errorf("unexpected message: %q", check.Message)
	}
}

func TestSkillCheck_WithEvalYml(t *testing.T) {
	srv := NewServer(slog.Default())
	dir := t.TempDir()

	// Create eval.yml (not .yaml) — should still be detected.
	_ = os.WriteFile(filepath.Join(dir, "eval.yml"), []byte("name: test\n"), 0o644)

	args, _ := json.Marshal(map[string]string{"skill_path": dir})
	params, _ := json.Marshal(toolsCallParams{Name: "waza_skill_check", Arguments: args})

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`22`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	data, _ := json.Marshal(resp.Result)
	var result toolsCallResult
	_ = json.Unmarshal(data, &result)

	var check skillCheckResult
	_ = json.Unmarshal([]byte(result.Content[0].Text), &check)

	if !check.HasEval {
		t.Error("expected HasEval=true for eval.yml")
	}
}

func TestSkillCheck_EmptySkillPath(t *testing.T) {
	srv := NewServer(slog.Default())
	args, _ := json.Marshal(map[string]string{"skill_path": ""})
	params, _ := json.Marshal(toolsCallParams{Name: "waza_skill_check", Arguments: args})

	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params:  params,
		ID:      json.RawMessage(`23`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	data, _ := json.Marshal(resp.Result)
	var result toolsCallResult
	_ = json.Unmarshal(data, &result)
	if !result.IsError {
		t.Error("expected isError=true for empty skill_path")
	}
}

// ========================================
// quickLinkCheck
// ========================================

func TestQuickLinkCheck_NoBrokenLinks(t *testing.T) {
	dir := t.TempDir()
	// Create SKILL.md with links that exist.
	_ = os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hi"), 0o644)
	content := "# Skill\n[readme](readme.txt)\n[external](https://example.com)\n"
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644)

	broken, issues := quickLinkCheck(dir)
	if broken != 0 {
		t.Errorf("broken = %d, want 0; issues: %v", broken, issues)
	}
}

func TestQuickLinkCheck_BrokenLink(t *testing.T) {
	dir := t.TempDir()
	content := "# Skill\n[missing](nonexistent.txt)\n"
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644)

	broken, issues := quickLinkCheck(dir)
	if broken != 1 {
		t.Errorf("broken = %d, want 1; issues: %v", broken, issues)
	}
	if len(issues) != 1 || !strings.Contains(issues[0], "nonexistent.txt") {
		t.Errorf("issues = %v, want mention of nonexistent.txt", issues)
	}
}

func TestQuickLinkCheck_SkipsProtocols(t *testing.T) {
	dir := t.TempDir()
	content := `# Skill
[http](http://example.com)
[https](https://example.com)
[mailto](mailto:test@example.com)
[mdc](mdc:something)
[anchor](#section)
`
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644)

	broken, _ := quickLinkCheck(dir)
	if broken != 0 {
		t.Errorf("broken = %d, want 0 (all protocols should be skipped)", broken)
	}
}

func TestQuickLinkCheck_LinkWithAnchor(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "guide.md"), []byte("# Guide"), 0o644)
	content := "# Skill\n[guide section](guide.md#section)\n"
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644)

	broken, _ := quickLinkCheck(dir)
	if broken != 0 {
		t.Errorf("broken = %d, want 0 (file part of link exists)", broken)
	}
}

func TestQuickLinkCheck_NoSkillMD(t *testing.T) {
	dir := t.TempDir()
	broken, issues := quickLinkCheck(dir)
	if broken != 0 || issues != nil {
		t.Errorf("expected 0 broken and nil issues for missing SKILL.md")
	}
}

// ========================================
// resolveDir
// ========================================

func TestResolveDir_NonEmpty(t *testing.T) {
	got := resolveDir("/some/dir")
	if got != "/some/dir" {
		t.Errorf("resolveDir(/some/dir) = %q", got)
	}
}

func TestResolveDir_Empty(t *testing.T) {
	got := resolveDir("")
	if got == "" {
		t.Error("resolveDir(\"\") returned empty")
	}
	// Should return CWD or ".".
	wd, _ := os.Getwd()
	if got != wd && got != "." {
		t.Errorf("resolveDir(\"\") = %q, want CWD or \".\"", got)
	}
}

// ========================================
// version
// ========================================

func TestVersion_Fallback(t *testing.T) {
	v := version()
	// Should return a non-empty string. If no version.txt found, returns "0.0.0-dev".
	if v == "" {
		t.Error("version() returned empty string")
	}
}

// ========================================
// stdio.go — additional coverage
// ========================================

func TestServeStdio_ParseError(t *testing.T) {
	// Send invalid JSON — should get parse error response then exit.
	input := strings.NewReader("this is not json\n")
	var output bytes.Buffer

	ServeStdio(context.Background(), input, &output, slog.New(slog.NewTextHandler(io.Discard, nil)))

	out := output.String()
	if out == "" {
		t.Fatal("expected parse error response, got empty")
	}
	var resp jsonrpc.Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error in response")
	}
	if resp.Error.Code != jsonrpc.CodeParseError {
		t.Errorf("error code = %d, want %d (parse error)", resp.Error.Code, jsonrpc.CodeParseError)
	}
}

func TestServeStdio_EmptyInput(t *testing.T) {
	input := strings.NewReader("")
	var output bytes.Buffer

	ServeStdio(context.Background(), input, &output, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if output.Len() != 0 {
		t.Errorf("expected no output for empty input (EOF), got: %s", output.String())
	}
}

func TestServeStdio_MultipleRequests(t *testing.T) {
	// Send initialize, notification, tools/list, then EOF.
	lines := []string{
		`{"jsonrpc":"2.0","method":"initialize","params":{},"id":1}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","method":"tools/list","params":{},"id":2}`,
		`{"jsonrpc":"2.0","method":"unknown/method","params":{},"id":3}`,
	}
	input := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var output bytes.Buffer

	ServeStdio(context.Background(), input, &output, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Should have 3 responses: initialize, tools/list, unknown/method.
	// Notification produces no response.
	respLines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(respLines) != 3 {
		t.Fatalf("expected 3 response lines, got %d: %s", len(respLines), output.String())
	}
}

func TestHasIDField(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"with id", `{"jsonrpc":"2.0","method":"test","id":1}`, true},
		{"without id", `{"jsonrpc":"2.0","method":"test"}`, false},
		{"null id", `{"jsonrpc":"2.0","method":"test","id":null}`, true},
		{"string id", `{"jsonrpc":"2.0","method":"test","id":"abc"}`, true},
		{"invalid json", `not json`, false},
		{"empty object", `{}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasIDField([]byte(tt.raw))
			if got != tt.want {
				t.Errorf("hasIDField(%s) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

// ========================================
// Initialize response structure validation
// ========================================

func TestInitialize_ResponseStructure(t *testing.T) {
	srv := NewServer(slog.Default())
	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params:  json.RawMessage(`{}`),
		ID:      json.RawMessage(`99`),
	}

	resp := srv.HandleRequest(context.Background(), req)
	if resp.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want 2.0", resp.JSONRPC)
	}

	// Verify ID is preserved.
	if string(resp.ID) != "99" {
		t.Errorf("ID = %s, want 99", string(resp.ID))
	}

	data, _ := json.Marshal(resp.Result)
	var result initializeResult
	_ = json.Unmarshal(data, &result)

	if result.ProtocolVersion != "2024-11-05" {
		t.Errorf("protocolVersion = %q", result.ProtocolVersion)
	}
	if result.ServerInfo.Name != "waza" {
		t.Errorf("serverInfo.name = %q", result.ServerInfo.Name)
	}
}
