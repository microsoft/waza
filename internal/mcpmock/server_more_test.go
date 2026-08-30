package mcpmock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestNewServerNilLoggerUsesDefault(t *testing.T) {
	srv := NewServer(&Config{Name: "x"}, nil)
	if srv == nil || srv.logger == nil {
		t.Fatal("NewServer should default the logger when nil")
	}
}

func TestHandleRequestInitializeAndUnknownMethod(t *testing.T) {
	srv := NewServer(&Config{Name: "github", Tools: map[string]Tool{
		"x": {Responses: []Response{{Return: map[string]any{"ok": true}}}},
	}}, slog.Default())

	resp := srv.HandleRequest(context.Background(), &jsonrpc.Request{
		JSONRPC: "2.0", Method: "initialize", ID: json.RawMessage(`1`),
	})
	require.Nil(t, resp.Error)
	data, _ := json.Marshal(resp.Result)
	require.Contains(t, string(data), `"protocolVersion"`)
	require.Contains(t, string(data), `"github"`)

	// notifications/initialized returns nil (no response).
	if got := srv.HandleRequest(context.Background(), &jsonrpc.Request{Method: "notifications/initialized"}); got != nil {
		t.Fatalf("notifications/initialized should return nil, got %+v", got)
	}

	// Unknown method returns MethodNotFound.
	resp = srv.HandleRequest(context.Background(), &jsonrpc.Request{Method: "nope", ID: json.RawMessage(`2`)})
	require.NotNil(t, resp.Error)
	require.Equal(t, jsonrpc.CodeMethodNotFound, resp.Error.Code)
}

func TestHandleToolsCallInvalidParams(t *testing.T) {
	srv := NewServer(&Config{Name: "x", Tools: map[string]Tool{
		"t": {Responses: []Response{{Return: map[string]any{"ok": true}}}},
	}}, slog.Default())

	// Malformed top-level JSON for params.
	resp := srv.HandleRequest(context.Background(), &jsonrpc.Request{
		Method: "tools/call",
		Params: json.RawMessage(`not-json`),
		ID:     json.RawMessage(`1`),
	})
	require.NotNil(t, resp.Error)
	require.Equal(t, jsonrpc.CodeInvalidParams, resp.Error.Code)

	// Arguments is not a JSON object.
	params, _ := json.Marshal(map[string]any{"name": "t", "arguments": "scalar"})
	resp = srv.HandleRequest(context.Background(), &jsonrpc.Request{
		Method: "tools/call", Params: params, ID: json.RawMessage(`2`),
	})
	require.NotNil(t, resp.Error)
	require.Contains(t, fmt.Sprint(resp.Error.Data), "arguments must be a JSON object")
}

func TestHandleToolsCallNilArgumentsFallsThroughToWildcardMatch(t *testing.T) {
	srv := NewServer(&Config{Name: "x", Tools: map[string]Tool{
		"t": {Responses: []Response{{Return: map[string]any{"ok": true}}}},
	}}, slog.Default())

	params, _ := json.Marshal(map[string]any{"name": "t", "arguments": nil})
	resp := srv.HandleRequest(context.Background(), &jsonrpc.Request{
		Method: "tools/call", Params: params, ID: json.RawMessage(`1`),
	})
	require.Nil(t, resp.Error)
	data, _ := json.Marshal(resp.Result)
	require.Contains(t, string(data), `\"ok\":true`)
}

func TestHandleToolsCallFixtureErrorReturnsIsError(t *testing.T) {
	srv := NewServer(&Config{Name: "github", Tools: map[string]Tool{
		"t": {Responses: []Response{{Error: "fixture says no"}}},
	}}, slog.Default())

	params, _ := json.Marshal(map[string]any{"name": "t", "arguments": map[string]any{}})
	resp := srv.HandleRequest(context.Background(), &jsonrpc.Request{
		Method: "tools/call", Params: params, ID: json.RawMessage(`1`),
	})
	require.Nil(t, resp.Error)
	data, _ := json.Marshal(resp.Result)
	require.Contains(t, string(data), `"isError":true`)
	require.Contains(t, string(data), "fixture says no")
}

func TestResponseMatchesEmptyMatchersAlwaysMatch(t *testing.T) {
	if !(Response{}).matches(map[string]any{"any": 1}) {
		t.Fatal("empty matchers should match everything")
	}
}

func TestRegexMatchFailures(t *testing.T) {
	// missing field
	if regexMatch(map[string]string{"a": "x"}, map[string]any{}) {
		t.Fatal("missing field should not match")
	}
	// non-string value coerced via fmt.Sprint
	if !regexMatch(map[string]string{"n": "^4"}, map[string]any{"n": 42}) {
		t.Fatal("number stringified should match ^4")
	}
	// bad pattern returns false (covered by validation but server-time guard)
	if regexMatch(map[string]string{"a": "["}, map[string]any{"a": "x"}) {
		t.Fatal("bad regex should not match")
	}
}

func TestSchemaMatchInvalidSchemaDoesNotPanic(t *testing.T) {
	if schemaMatch(map[string]any{"type": 42}, map[string]any{}) {
		t.Fatal("invalid schema should not match")
	}
}

func TestHasIDField(t *testing.T) {
	cases := map[string]bool{
		`{"id":1}`:       true,
		`{"method":"x"}`: false,
		`not-json`:       false,
		`{}`:             false,
		`{"id":null}`:    true,
	}
	for raw, want := range cases {
		if got := hasIDField([]byte(raw)); got != want {
			t.Errorf("hasIDField(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestServeStdioRespondsToInitializeAndExitsOnEOF(t *testing.T) {
	cfg := &Config{Name: "demo", Tools: map[string]Tool{
		"t": {Responses: []Response{{Return: map[string]any{"ok": true}}}},
	}}

	// One initialize request followed by EOF.
	in := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	var out bytes.Buffer

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ServeStdio(context.Background(), cfg, in, &out, logger)

	require.Contains(t, out.String(), `"protocolVersion"`)
	require.Contains(t, out.String(), `"demo"`)
}

func TestServeStdioWriteFailureAborts(t *testing.T) {
	cfg := &Config{Name: "demo", Tools: map[string]Tool{
		"t": {Responses: []Response{{Return: map[string]any{"ok": true}}}},
	}}
	in := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// failingWriter forces WriteResponse to error so ServeStdio takes its
	// "write error" branch and returns.
	ServeStdio(context.Background(), cfg, in, failingWriter{}, logger)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestLoadFixtureDirSkipsHiddenAndNonJSON(t *testing.T) {
	dir := t.TempDir()
	// Hidden dir is skipped.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".hidden", "skip.json"), []byte(`{"responses":[{"return":{}}]}`), 0o644))
	// Non-json file is ignored.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644))
	// Bundle file (top-level "tools" key) registers multiple tools.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bundle.json"), []byte(`{"tools":{"alpha":{"responses":[{"return":{"v":1}}]},"beta":{"responses":[{"return":{"v":2}}]}}}`), 0o644))
	// Single-tool file uses its basename as the tool name.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gamma.json"), []byte(`{"responses":[{"return":{"v":3}}]}`), 0o644))

	cfg, err := FromEvalConfig(models.MCPMockConfig{Name: "fx", Fixtures: dir}, "")
	require.NoError(t, err)
	for _, tool := range []string{"alpha", "beta", "gamma"} {
		require.Contains(t, cfg.Tools, tool)
	}
	require.NotContains(t, cfg.Tools, "skip")
}

func TestLoadFixtureDirMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644))
	_, err := FromEvalConfig(models.MCPMockConfig{Name: "fx", Fixtures: dir}, "")
	require.Error(t, err)
}

func TestFromEvalConfigRelativeFixtures(t *testing.T) {
	base := t.TempDir()
	rel := "fixtures"
	require.NoError(t, os.MkdirAll(filepath.Join(base, rel), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, rel, "t.json"), []byte(`{"responses":[{"return":{"v":1}}]}`), 0o644))

	cfg, err := FromEvalConfig(models.MCPMockConfig{Name: "fx", Fixtures: rel}, base)
	require.NoError(t, err)
	require.Contains(t, cfg.Tools, "t")
}

func TestFromEvalConfigErrorPaths(t *testing.T) {
	// Missing name
	_, err := FromEvalConfig(models.MCPMockConfig{}, "")
	require.ErrorContains(t, err, "missing name")

	// No tools
	_, err = FromEvalConfig(models.MCPMockConfig{Name: "x"}, "")
	require.ErrorContains(t, err, "at least one tool")

	// Tool with no responses
	_, err = FromEvalConfig(models.MCPMockConfig{
		Name:  "x",
		Tools: map[string]models.MCPMockTool{"t": {}},
	}, "")
	require.ErrorContains(t, err, "at least one response")

	// Fixture dir does not exist
	_, err = FromEvalConfig(models.MCPMockConfig{Name: "x", Fixtures: filepath.Join(t.TempDir(), "missing")}, "")
	require.Error(t, err)

	// Fixture path that is a file, not a directory
	dir := t.TempDir()
	notDir := filepath.Join(dir, "file.txt")
	require.NoError(t, os.WriteFile(notDir, []byte("hi"), 0o644))
	_, err = FromEvalConfig(models.MCPMockConfig{Name: "x", Fixtures: notDir}, "")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "not a directory") || strings.Contains(err.Error(), "fixtures"))
}
