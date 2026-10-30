package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/copilotconfig"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func callMCPMockProcess(t *testing.T, binary string, args []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, args...)
	child.Env = append(os.Environ(), "WAZA_NO_UPDATE_CHECK=1")
	child.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"method\":\"initialize\",\"id\":1}\n" +
		"{\"jsonrpc\":\"2.0\",\"method\":\"tools/call\",\"params\":{\"name\":\"read\",\"arguments\":{\"id\":\"item\"}},\"id\":2}\n")
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	require.NoError(t, child.Run(), "%s", stderr.String())
	scanner := bufio.NewScanner(&stdout)
	var last map[string]any
	for scanner.Scan() {
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &last))
	}
	require.NoError(t, scanner.Err())
	require.Equal(t, float64(2), last["id"])
	data, err := json.Marshal(last["result"])
	require.NoError(t, err)
	return string(data)
}

func TestMCPMockAttemptProcessesShareSequenceState(t *testing.T) {
	binary := buildRecoveryCLI(t)
	tmp := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, tmp)
	}
	mock := models.MCPMockConfig{Name: "counter", Tools: map[string]models.MCPMockTool{"read": {Responses: []models.MCPMockResponse{{
		Match:    map[string]any{"id": "item"},
		Sequence: []models.MCPMockStep{{Return: map[string]any{"ordinal": 1}}, {Return: map[string]any{"ordinal": 2}}},
	}}}}}
	servers, attempt, err := copilotconfig.ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{mock}, tmp, models.ScenarioSchemaVersion, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, attempt.Close()) })
	stdio, ok := servers["counter"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)

	require.Contains(t, callMCPMockProcess(t, binary, stdio.Args), `{\"ordinal\":1}`)
	require.Contains(t, callMCPMockProcess(t, binary, stdio.Args), `{\"ordinal\":2}`)
	exhausted := callMCPMockProcess(t, binary, stdio.Args)
	require.Contains(t, exhausted, `"isError":true`)
	require.Contains(t, exhausted, "response sequence exhausted")
	require.FileExists(t, stdio.Args[2], "the attempt, not a server process, owns retained configuration")

	other, otherAttempt, err := copilotconfig.ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{mock}, tmp, models.ScenarioSchemaVersion, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, otherAttempt.Close()) })
	otherStdio, ok := other["counter"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Contains(t, callMCPMockProcess(t, binary, otherStdio.Args), `{\"ordinal\":1}`)
}

func TestMCPMockConfigSourcesKeepLegacyOwnership(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy.json")
	retained := filepath.Join(dir, "retained.json")
	for _, path := range []string{legacy, retained} {
		require.NoError(t, os.WriteFile(path, []byte(`{"name":"counter"}`), 0600))
	}

	data, err := readMCPMockConfig("", legacy, "")
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"counter"}`, string(data))
	require.NoFileExists(t, legacy, "legacy one-shot configuration remains server-owned")

	data, err = readMCPMockConfig("", "", retained)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"counter"}`, string(data))
	require.FileExists(t, retained)

	_, err = readMCPMockConfig("", filepath.Join(dir, "other.json"), retained)
	require.ErrorContains(t, err, "cannot be combined")
	_, err = readMCPMockConfig("e30=", "", retained)
	require.ErrorContains(t, err, "cannot be combined")
	require.FileExists(t, retained)
}
