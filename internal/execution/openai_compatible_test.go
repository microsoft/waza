package execution

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeOpenAICompatibleEndpoint(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "host root", in: "http://127.0.0.1:1234", want: "http://127.0.0.1:1234/v1/chat/completions"},
		{name: "v1 base", in: "http://127.0.0.1:1234/v1", want: "http://127.0.0.1:1234/v1/chat/completions"},
		{name: "full chat URL", in: "http://127.0.0.1:1234/v1/chat/completions", want: "http://127.0.0.1:1234/v1/chat/completions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeOpenAICompatibleEndpoint(tt.in)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOpenAICompatibleEngineExecute(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{"message": {"role": "assistant", "content": "hello from lm studio"}}],
			"usage": {"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}
		}`))
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "test-key")

	engine, err := NewOpenAICompatibleEngine(server.URL, "", "")
	require.NoError(t, err)
	require.NoError(t, engine.Initialize(t.Context()))
	resp, err := engine.Execute(t.Context(), &ExecutionRequest{
		Message: "Say hello",
		Context: map[string]any{"case": "unit"},
		Resources: []ResourceFile{
			{Path: "example.txt", Content: []byte("fixture content")},
		},
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.Equal(t, "hello from lm studio", resp.FinalOutput)
	require.Equal(t, defaultOpenAICompatibleModel, resp.ModelID)
	require.Equal(t, "/v1/chat/completions", gotPath)
	require.Equal(t, "Bearer test-key", gotAuth)
	require.Equal(t, defaultOpenAICompatibleModel, gotBody.Model)
	require.Len(t, gotBody.Messages, 2)
	require.Contains(t, gotBody.Messages[0].Content, "fixture content")
	require.Equal(t, "Say hello", gotBody.Messages[1].Content)
	require.Equal(t, 7, resp.Usage.InputTokens)
	require.Equal(t, 3, resp.Usage.OutputTokens)
	require.NotEmpty(t, resp.WorkspaceDir)

	require.NoError(t, engine.Shutdown(t.Context()))
	_, statErr := os.Stat(resp.WorkspaceDir)
	require.True(t, os.IsNotExist(statErr), "shutdown should remove temporary workspace")
}

func TestOpenAICompatibleEngineHTTPErrorReturnsExecutionResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad model", http.StatusBadRequest)
	}))
	defer server.Close()

	engine, err := NewOpenAICompatibleEngine(server.URL, "custom-model", "")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, engine.Shutdown(t.Context()))
	}()
	resp, err := engine.Execute(t.Context(), &ExecutionRequest{Message: "hello"})
	require.NoError(t, err)
	require.False(t, resp.Success)
	require.Contains(t, resp.ErrorMsg, "bad model")
	require.Equal(t, "custom-model", resp.ModelID)
}

func TestOpenAICompatibleEngineConfiguredAPIKeyOverridesEnvironment(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "env-key")

	engine, err := NewOpenAICompatibleEngine(server.URL, "local-model", "configured-key")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, engine.Shutdown(t.Context()))
	}()
	resp, err := engine.Execute(t.Context(), &ExecutionRequest{Message: "hello"})
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.Equal(t, "Bearer configured-key", gotAuth)
}

func TestOpenAICompatibleEngineInjectsSkillContext(t *testing.T) {
	var gotBody openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	skillRoot := t.TempDir()
	skillDir := filepath.Join(skillRoot, "test-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: test-skill
description: Test skill.
---

# Test Skill

Always mention injected skill context.
`), 0o644))

	engine, err := NewOpenAICompatibleEngine(server.URL, "local-model", "")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, engine.Shutdown(t.Context()))
	}()

	resp, err := engine.Execute(t.Context(), &ExecutionRequest{
		Message:   "hello",
		SkillName: "test-skill",
		SkillPaths: []string{
			skillRoot,
		},
		SourceDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.NotEmpty(t, gotBody.Messages)
	require.Equal(t, "system", gotBody.Messages[0].Role)
	require.Contains(t, gotBody.Messages[0].Content, "<skill_context>")
	require.Contains(t, gotBody.Messages[0].Content, "Always mention injected skill context.")
	require.Contains(t, gotBody.Messages[0].Content, "<available_skills>")
}
