package scripts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

// This uses a real built CLI and SDK with a synthetic subprocess, not a native
// Copilot runtime or a provider. The child gets no inherited credentials/config.
func TestRunnerNativeProductionProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("these process controls currently execute on Unix")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	buildDir := t.TempDir()
	binary := filepath.Join(buildDir, "waza")
	fixture := filepath.Join(buildDir, "synthetic-runtime")
	for _, build := range []struct{ output, source string }{
		{binary, "./cmd/waza"},
		{fixture, "./.github/scripts/testdata/runner-runtime"},
	} {
		cmd := exec.CommandContext(t.Context(), "go", "build", "-o", build.output, build.source)
		cmd.Dir = root
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "build %s: %s", build.source, output)
	}
	data, err := os.ReadFile(filepath.Join(root, "examples", "runner", "copilot-unit.json"))
	require.NoError(t, err)
	t.Setenv("GITHUB_TOKEN", "synthetic-ambient-credential-must-not-reach-child")
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "http://synthetic-provider-must-not-reach-child.invalid")
	for _, control := range []struct {
		mode    string
		outcome runnerterminal.Outcome
		calls   int
	}{
		{"success", runnerterminal.Success, 1},
		{"failed-check", runnerterminal.EvaluationFailure, 1},
		{"session-error", runnerterminal.InfrastructureFailure, 1},
		{"timeout", runnerterminal.Timeout, 1},
		{"startup-failure", runnerterminal.InfrastructureFailure, 0},
		{"bad-protocol", runnerterminal.InfrastructureFailure, 0},
		{"unsupported", runnerterminal.UnsupportedCapability, 0},
	} {
		t.Run(control.mode, func(t *testing.T) {
			request, err := runnercontract.Parse(data)
			require.NoError(t, err)
			request.Input.Eval = strings.ReplaceAll(request.Input.Eval, "gpt-4.1", "synthetic-no-provider")
			if control.mode == "timeout" {
				request.TimeoutMS = 5000
			}
			if control.mode == "unsupported" {
				request.RequiredCapabilities = append(request.RequiredCapabilities, "synthetic.unsupported")
			}
			input, err := json.Marshal(request)
			require.NoError(t, err)
			scratch := t.TempDir()
			logPath := filepath.Join(scratch, "rpc.ndjson")
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "runner", "run")
			cmd.WaitDelay = 2 * time.Second
			cmd.Dir = scratch
			cmd.Env = []string{
				"HOME=" + scratch, "COPILOT_HOME=" + scratch, "TMPDIR=" + scratch,
				"PATH=" + buildDir, "NO_COLOR=1", "COPILOT_CLI_PATH=" + fixture,
				"WAZA_SYNTHETIC_MODE=" + control.mode, "WAZA_SYNTHETIC_LOG=" + logPath,
			}
			cmd.Stdin = bytes.NewReader(input)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			runErr := cmd.Run()
			log, logErr := os.ReadFile(logPath)
			if runErr != nil && control.outcome == runnerterminal.Success {
				t.Logf("stdout=%s stderr=%s RPC=%s", stdout.String(), stderr.String(), log)
			}
			require.NoError(t, ctx.Err(), "outer process timeout: %s", stderr.String())
			wantExit, ok := control.outcome.ExitCode()
			require.True(t, ok)
			if wantExit == 0 {
				require.NoError(t, runErr, "%s", stderr.String())
			} else {
				exit, ok := errors.AsType[*exec.ExitError](runErr)
				require.True(t, ok, "%v: %s", runErr, stderr.String())
				require.Equal(t, wantExit, exit.ExitCode(), "%s", stdout.String())
			}
			var binding *runnerterminal.Binding
			if control.mode != "unsupported" {
				identity, err := runneridentity.Derive(request)
				require.NoError(t, err)
				binding = &runnerterminal.Binding{
					Selection: request.Selection, Identity: identity,
					GraderIDs: []string{"_output_contains"}, ExecutionMode: "copilot",
				}
			}
			terminal, err := runnerterminal.Validate(stdout.Bytes(), binding)
			require.NoError(t, err, "%s\nstderr: %s", stdout.String(), stderr.String())
			require.Equal(t, control.outcome, terminal.Outcome)
			require.Equal(t, control.calls, terminal.EngineCalls)
			if control.mode == "unsupported" {
				_, err := os.Stat(logPath)
				require.ErrorIs(t, err, os.ErrNotExist, "rejected request must not launch the runtime")
				return
			}
			require.Equal(t, &request.Selection, terminal.Selection)
			require.NoError(t, logErr)
			decoder := json.NewDecoder(bytes.NewReader(log))
			counts := make(map[string]int)
			for {
				var entry struct {
					Method          string         `json:"method"`
					Args            []string       `json:"args"`
					EnvironmentKeys []string       `json:"environment_keys"`
					Params          map[string]any `json:"params"`
				}
				err := decoder.Decode(&entry)
				if err == io.EOF {
					break
				}
				require.NoError(t, err)
				counts[entry.Method]++
				switch entry.Method {
				case "fixture.start":
					require.True(t, slices.Contains(entry.Args, "--stdio"))
					require.ElementsMatch(t, []string{
						"HOME", "COPILOT_HOME", "TMPDIR", "PATH", "NO_COLOR",
						"COPILOT_CLI_PATH", "WAZA_SYNTHETIC_MODE", "WAZA_SYNTHETIC_LOG",
						"COPILOT_RUNTIME_PROCESS_FILE_LOGGING",
					}, entry.EnvironmentKeys, "only the explicit child environment and SDK logging flag may reach the fixture")
				case "session.create":
					require.Equal(t, "synthetic-no-provider", entry.Params["model"])
					require.Equal(t, false, entry.Params["enableSkills"])
					require.Equal(t, []any{}, entry.Params["availableTools"])
					require.Empty(t, entry.Params["mcpServers"])
					require.Empty(t, entry.Params["tools"])
				case "session.send":
					require.Equal(t, "hello", entry.Params["prompt"])
				}
			}
			expectedCounts := map[string]int{"fixture.start": 1}
			if control.mode != "startup-failure" {
				expectedCounts["connect"] = 1
			}
			if control.calls == 1 {
				expectedCounts["auth.getStatus"] = 1
				expectedCounts["session.create"] = 1
				expectedCounts["session.send"] = 1
				if control.mode != "timeout" {
					expectedCounts["session.usage.getMetrics"] = 1
					expectedCounts["session.detach"] = 1
					expectedCounts["session.delete"] = 1
					expectedCounts["runtime.shutdown"] = 1
				}
			}
			require.Equal(t, expectedCounts, counts, "no adjacent session, retry, resume or other RPC is permitted")
			if control.outcome == runnerterminal.Success || control.outcome == runnerterminal.EvaluationFailure {
				require.NotNil(t, terminal.Observation)
				require.Equal(t, []runnerterminal.Verdict{{
					Identifier: "_output_contains", Passed: control.outcome == runnerterminal.Success,
				}}, terminal.Observation.Validations)
			}
			if terminal.Observation != nil {
				require.Equal(t, request.Selection, terminal.Observation.Selection)
				require.Equal(t, 2, terminal.Observation.RunNumber)
				require.Equal(t, request.Selection.Attempt, terminal.Observation.Attempts)
			}
			t.Logf("synthetic subprocess only: outcome=%s selection=%+v calls=%d RPC=%v", terminal.Outcome, request.Selection, terminal.EngineCalls, counts)
		})
	}
}
