package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

const mcpMockDiagnosticOutputLimit = 2048

func runMCPMockProcess(ctx context.Context, invocation string, child *exec.Cmd, started time.Time, stdout, stderr *bytes.Buffer) error {
	runStarted := time.Now()
	err := child.Start()
	startElapsed, startContextErr := time.Since(runStarted), ctx.Err()
	var waitElapsed time.Duration
	if err == nil {
		waitStarted := time.Now()
		err = child.Wait()
		waitElapsed = time.Since(waitStarted)
	}
	runElapsed := time.Since(runStarted)
	if err == nil {
		return nil
	}
	exitCode := -1
	if child.ProcessState != nil {
		exitCode = child.ProcessState.ExitCode()
	}
	bounded := func(output string) string {
		if len(output) > mcpMockDiagnosticOutputLimit {
			return output[:mcpMockDiagnosticOutputLimit] + "...[truncated]"
		}
		return output
	}
	return fmt.Errorf("MCP mock invocation=%q started_at=%s start_elapsed=%s run_elapsed=%s ctx_err=%v exit_code=%d process_state=%v stdout=%q stderr=%q process_start_elapsed=%s wait_elapsed=%s start_ctx_err=%v: %w",
		invocation, started.Format(time.RFC3339Nano), runStarted.Sub(started), runElapsed, ctx.Err(),
		exitCode, child.ProcessState, bounded(stdout.String()), bounded(stderr.String()), startElapsed, waitElapsed, startContextErr, err)
}

func callMCPMockProcess(t *testing.T, invocation, binary string, args []string) string {
	t.Helper()
	started := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, args...)
	child.Env = append(os.Environ(), "WAZA_NO_UPDATE_CHECK=1")
	child.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"method\":\"initialize\",\"id\":1}\n" +
		"{\"jsonrpc\":\"2.0\",\"method\":\"tools/call\",\"params\":{\"name\":\"read\",\"arguments\":{\"id\":\"item\"}},\"id\":2}\n")
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	require.NoError(t, runMCPMockProcess(ctx, invocation, child, started, &stdout, &stderr))
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

	require.Contains(t, callMCPMockProcess(t, "first attempt, invocation 1", binary, stdio.Args), `{\"ordinal\":1}`)
	require.Contains(t, callMCPMockProcess(t, "first attempt, invocation 2", binary, stdio.Args), `{\"ordinal\":2}`)
	exhausted := callMCPMockProcess(t, "first attempt, exhausted invocation 3", binary, stdio.Args)
	require.Contains(t, exhausted, `"isError":true`)
	require.Contains(t, exhausted, "response sequence exhausted")
	require.FileExists(t, stdio.Args[2], "the attempt, not a server process, owns retained configuration")

	other, otherAttempt, err := copilotconfig.ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{mock}, tmp, models.ScenarioSchemaVersion, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, otherAttempt.Close()) })
	otherStdio, ok := other["counter"].(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Contains(t, callMCPMockProcess(t, "independent attempt, invocation 1", binary, otherStdio.Args), `{\"ordinal\":1}`)
}

func runMCPMockDiagnosticChild(mode string) (int, error) {
	if _, err := fmt.Fprint(os.Stdout, strings.Repeat("o", mcpMockDiagnosticOutputLimit)+"stdout-tail"); err != nil {
		return 1, fmt.Errorf("writing synthetic stdout: %w", err)
	}
	if _, err := fmt.Fprint(os.Stderr, strings.Repeat("e", mcpMockDiagnosticOutputLimit)+"stderr-tail"); err != nil {
		return 1, fmt.Errorf("writing synthetic stderr: %w", err)
	}
	if mode == "success" {
		return 0, nil
	}
	return 7, nil
}

func TestMCPMockProcessFailureDiagnostics(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	for _, mode := range []string{"exit", "success", "canceled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			cacheBlocker := filepath.Join(t.TempDir(), "cache-must-not-be-used")
			require.NoError(t, os.WriteFile(cacheBlocker, []byte("unchanged"), 0o600))
			started := time.Now()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if mode == "deadline" {
				expired, expiredCancel := context.WithDeadline(t.Context(), started.Add(-time.Second))
				defer expiredCancel()
				ctx = expired
			}
			child := exec.CommandContext(ctx, binary, "-test.run=^TestMCPMockProcessFailureDiagnostics$")
			child.Env = append(os.Environ(), "WAZA_TEST_MCP_DIAGNOSTIC_CHILD="+mode)
			for _, key := range []string{"LOCALAPPDATA", "XDG_CACHE_HOME", "HOME", "USERPROFILE"} {
				child.Env = append(child.Env, key+"="+cacheBlocker)
			}
			var stdout, stderr bytes.Buffer
			child.Stdout, child.Stderr = &stdout, &stderr
			err := runMCPMockProcess(ctx, "controlled "+mode, child, started, &stdout, &stderr)
			cacheContents, readErr := os.ReadFile(cacheBlocker)
			require.NoError(t, readErr)
			require.Equal(t, "unchanged", string(cacheContents))
			if mode == "success" {
				require.NoError(t, err)
				require.Equal(t, strings.Repeat("o", mcpMockDiagnosticOutputLimit)+"stdout-tail", stdout.String())
				require.Equal(t, strings.Repeat("e", mcpMockDiagnosticOutputLimit)+"stderr-tail", stderr.String())
				return
			}
			require.Error(t, err)
			message := err.Error()
			require.Contains(t, message, fmt.Sprintf("invocation=%q", "controlled "+mode))
			require.Contains(t, message, "started_at="+started.Format(time.RFC3339Nano))
			require.Regexp(t, `start_elapsed=[0-9.]+[a-zµ]+ run_elapsed=[0-9.]+[a-zµ]+ ctx_err=`, message)
			require.Regexp(t, `process_start_elapsed=[0-9.]+[a-zµ]+ wait_elapsed=[0-9.]+[a-zµ]+ start_ctx_err=`, message)
			if mode == "exit" {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				require.Equal(t, 7, exitErr.ExitCode())
				require.Contains(t, message, "ctx_err=<nil> exit_code=7 process_state="+child.ProcessState.String())
				require.Contains(t, message, "start_ctx_err=<nil>")
				require.Contains(t, message, `stdout="`+strings.Repeat("o", mcpMockDiagnosticOutputLimit)+`...[truncated]"`)
				require.Contains(t, message, `stderr="`+strings.Repeat("e", mcpMockDiagnosticOutputLimit)+`...[truncated]"`)
				require.NotContains(t, message, "stdout-tail")
				require.NotContains(t, message, "stderr-tail")
				require.Less(t, len(message), 2*mcpMockDiagnosticOutputLimit+1024)
			} else {
				expected := context.Canceled
				if mode == "deadline" {
					expected = context.DeadlineExceeded
				}
				require.True(t, errors.Is(err, expected))
				require.Contains(t, message, "ctx_err="+expected.Error()+" exit_code=-1 process_state=<nil>")
				require.Contains(t, message, `stdout="" stderr=""`)
				require.Contains(t, message, "wait_elapsed=0s start_ctx_err="+expected.Error())
			}
		})
	}
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
