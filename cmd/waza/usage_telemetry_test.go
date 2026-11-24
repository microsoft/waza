package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/usagetelemetry"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type usageCollector struct {
	server *httptest.Server
	mu     sync.Mutex
	events []map[string]any
}

func newUsageCollector(t *testing.T) *usageCollector {
	t.Helper()
	c := &usageCollector{}
	c.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event map[string]any
		if err := json.NewDecoder(r.Body).Decode(&event); err == nil {
			c.mu.Lock()
			c.events = append(c.events, event)
			c.mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *usageCollector) take() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	events := c.events
	c.events = nil
	return events
}

// isolateUsageTelemetry gives the test a private home directory and clears
// every ambient variable that influences the usage telemetry policy.
func isolateUsageTelemetry(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{usagetelemetry.OptOutEnv, usagetelemetry.NestedEnv, usagetelemetry.CIEnv, "DO_NOT_TRACK",
		"AZURE_DEV_COLLECT_TELEMETRY", "AZD_SERVER", "CI", "GITHUB_ACTIONS", "TF_BUILD", "JENKINS_URL",
		"CODEBUILD_BUILD_ID", "GITLAB_CI", "BUILDKITE", "CIRCLECI", "TEAMCITY_VERSION", "BITBUCKET_BUILD_NUMBER"} {
		t.Setenv(name, "")
	}
	t.Chdir(t.TempDir())
	return filepath.Join(home, ".waza", "telemetry.json")
}

type invocation struct {
	code   int
	stdout string
	stderr string
}

// invokeCLI runs the full process entrypoint and captures its output. Policy
// propagation mutates the environment, so it is reset between invocations.
func invokeCLI(t *testing.T, args ...string) invocation {
	t.Helper()
	resetRunGlobals()
	require.NoError(t, os.Setenv(usagetelemetry.OptOutEnv, ""))
	require.NoError(t, os.Setenv(usagetelemetry.NestedEnv, ""))

	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	stdout := os.Stdout
	os.Stdout = writer
	captured := make(chan string)
	go func() {
		data, _ := io.ReadAll(reader)
		captured <- string(data)
	}()
	var stderr bytes.Buffer
	code := runMain(&stderr, args)
	os.Stdout = stdout
	require.NoError(t, writer.Close())
	return invocation{code: code, stdout: <-captured, stderr: stderr.String()}
}

// An earlier in-process CLI invocation under CI propagates the internal CI
// marker; isolation must clear it so later tests observe events regardless of
// test order.
func TestUsageTelemetry_IsolationClearsPropagatedCIMarker(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv(usagetelemetry.CIEnv, "")
	t.Setenv(usagetelemetry.OptOutEnv, "")
	t.Setenv(usagetelemetry.NestedEnv, "")
	invokeCLI(t, "--no-update-check", "--no-telemetry", "definitely-not-a-command")
	require.Equal(t, "1", os.Getenv(usagetelemetry.CIEnv), "setup must propagate the CI marker")

	isolateUsageTelemetry(t)
	collector := newUsageCollector(t)
	t.Cleanup(usagetelemetry.OverrideActivationForTesting(collector.server.URL, collector.server.Client(), true, false))
	invokeCLI(t, "--no-update-check", "definitely-not-a-command")
	require.Len(t, collector.take(), 1)
}

func TestUsageTelemetry_OutputAndExitUnchanged(t *testing.T) {
	isolateUsageTelemetry(t)
	collector := newUsageCollector(t)
	cases := map[string][]string{
		"unknown command": {"--no-update-check", "definitely-not-a-command", `C:\secret\path`},
		"bad flag":        {"--no-update-check", "run", "--no-such-flag=secret"},
		"missing spec":    {"--no-update-check", "run", filepath.Join(t.TempDir(), "missing.yaml")},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			off := invokeCLI(t, args...)
			restore := usagetelemetry.OverrideActivationForTesting(collector.server.URL, collector.server.Client(), true, false)
			on := invokeCLI(t, args...)
			restore()
			assert.Equal(t, off, on)
			assert.NotEqual(t, ExitSuccess, on.code)
			events := collector.take()
			require.Len(t, events, 1)
			body, err := json.Marshal(events[0])
			require.NoError(t, err)
			assert.NotContains(t, string(body), "secret")
			assert.NotContains(t, string(body), "missing.yaml")
		})
	}
}

func TestUsageTelemetry_NoTelemetryIsAdmittedByRunnerCompat(t *testing.T) {
	isolateUsageTelemetry(t)
	spec := "./" + filepath.ToSlash(filepath.Join(filepath.Base(t.TempDir()), "missing.yaml"))
	without := invokeCLI(t, "--no-update-check", "run", spec, "--runner-compat", "--no-summary")
	with := invokeCLI(t, "--no-update-check", "--no-telemetry", "run", spec, "--runner-compat", "--no-summary")
	assert.Equal(t, without, with, "--no-telemetry must not change a runner-compat invocation")
	assert.NotContains(t, with.stderr, "--no-telemetry")
}

func TestUsageTelemetry_RunRecordsCommandAndOneExecution(t *testing.T) {
	isolateUsageTelemetry(t)
	collector := newUsageCollector(t)
	passing := createTestSpec(t, "mock")
	failing := createUsageFailingSpec(t)

	offPass := invokeCLI(t, "--no-update-check", "run", passing)
	offFail := invokeCLI(t, "--no-update-check", "run", failing)
	t.Cleanup(usagetelemetry.OverrideActivationForTesting(collector.server.URL, collector.server.Client(), true, false))

	onPass := invokeCLI(t, "--no-update-check", "run", passing)
	assert.Equal(t, offPass.code, onPass.code)
	assert.Equal(t, ExitSuccess, onPass.code)
	assertUsageEvents(t, collector.take(), "success", "passed")

	onFail := invokeCLI(t, "--no-update-check", "run", failing)
	assert.Equal(t, offFail.code, onFail.code)
	assert.Equal(t, ExitTestFailed, onFail.code)
	assertUsageEvents(t, collector.take(), "test_failed", "failed")
}

func assertUsageEvents(t *testing.T, events []map[string]any, commandOutcome, executionOutcome string) {
	t.Helper()
	var commands, executions []map[string]any
	for _, event := range events {
		switch event["event"] {
		case "command_completed":
			commands = append(commands, event)
		case "execution_completed":
			executions = append(executions, event)
		}
	}
	require.Len(t, commands, 1)
	require.Len(t, executions, 1, "a logical execution is counted exactly once")
	assert.Equal(t, "run", commands[0]["command"])
	assert.Equal(t, commandOutcome, commands[0]["outcome"])
	assert.Equal(t, "benchmark", executions[0]["surface"])
	assert.Equal(t, "mock", executions[0]["execution_mode"])
	assert.Equal(t, executionOutcome, executions[0]["outcome"])
	assert.Equal(t, "1", executions[0]["task_count_bucket"])
}

func TestUsageTelemetry_DenialSendsNothingAndPropagates(t *testing.T) {
	settings := isolateUsageTelemetry(t)
	collector := newUsageCollector(t)
	t.Cleanup(usagetelemetry.OverrideActivationForTesting(collector.server.URL, collector.server.Client(), true, false))

	invokeCLI(t, "--no-update-check", "--no-telemetry", "run", "missing.yaml")
	assert.Equal(t, "1", os.Getenv(usagetelemetry.OptOutEnv), "denial must reach child processes")

	t.Setenv(usagetelemetry.OptOutEnv, "")
	invokeCLI(t, "--no-update-check", "run", "missing.yaml", "--output", "--", "--no-telemetry")
	assert.Equal(t, "1", os.Getenv(usagetelemetry.OptOutEnv), "a flag pflag consumes as a value must still deny")

	t.Setenv("DO_NOT_TRACK", "1")
	invokeCLI(t, "--no-update-check", "run", "missing.yaml")
	assert.Empty(t, collector.take())
	assert.NoFileExists(t, settings)
}

func TestUsageTelemetry_ManagementCommandsAreLocalOnly(t *testing.T) {
	settings := isolateUsageTelemetry(t)
	collector := newUsageCollector(t)

	status := invokeCLI(t, "telemetry", "status")
	assert.Equal(t, ExitSuccess, status.code)
	assert.Contains(t, status.stdout, "Product usage telemetry: inactive")
	assert.Contains(t, status.stdout, usagetelemetry.ReasonNoEndpoint)

	t.Cleanup(usagetelemetry.OverrideActivationForTesting(collector.server.URL, collector.server.Client(), true, false))
	for _, args := range [][]string{{"telemetry", "status"}, {"telemetry", "enable"}, {"telemetry", "reset-id"}, {"help"}, {"--no-update-check", "run", "--help"}, {"--no-update-check", "runner"}} {
		assert.Equal(t, ExitSuccess, invokeCLI(t, args...).code, args)
	}
	assert.Empty(t, collector.take(), "telemetry, help, and help-only parent commands are never collected")
	data, err := os.ReadFile(settings)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "installationId", "management commands never create an ID")

	invokeCLI(t, "--no-update-check", "run", "missing.yaml")
	require.Len(t, collector.take(), 1)
	data, err = os.ReadFile(settings)
	require.NoError(t, err)
	assert.Contains(t, string(data), "installationId")

	disable := invokeCLI(t, "telemetry", "disable")
	assert.Equal(t, ExitSuccess, disable.code)
	data, err = os.ReadFile(settings)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "installationId")
	invokeCLI(t, "--no-update-check", "run", "missing.yaml")
	assert.Empty(t, collector.take())
}

func TestUsageTelemetry_CommandOutcome(t *testing.T) {
	assert.Equal(t, usagetelemetry.CommandSuccess, commandOutcome(nil, ExitSuccess, true))
	assert.Equal(t, usagetelemetry.CommandCancelled, commandOutcome(context.Canceled, ExitError, true))
	assert.Equal(t, usagetelemetry.CommandSetupError, commandOutcome(assert.AnError, ExitError, false))
	assert.Equal(t, usagetelemetry.CommandTestFailed, commandOutcome(assert.AnError, ExitTestFailed, true))
	assert.Equal(t, usagetelemetry.CommandRuntimeError, commandOutcome(assert.AnError, ExitError, true))
}

// The contract's command enum must list exactly the collectable command tree.
func TestUsageTelemetry_SchemaCommandsMatchCommandTree(t *testing.T) {
	root := newRootCommand()
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	want := []string{"root", "unknown"}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, child := range cmd.Commands() {
			if collectableCommand(child) {
				want = append(want, commandName(child))
			}
			walk(child)
		}
	}
	walk(root)
	var got []string
	for name := range usagetelemetry.CommandNames() {
		got = append(got, name)
	}
	sort.Strings(want)
	sort.Strings(got)
	assert.Equal(t, want, got)
	for _, name := range got {
		assert.False(t, strings.HasPrefix(name, "telemetry"), name)
	}
}

// createUsageFailingSpec returns a mock eval whose text grader never matches.
func createUsageFailingSpec(t *testing.T) string {
	t.Helper()
	specPath := createTestSpec(t, "mock")
	data, err := os.ReadFile(specPath)
	require.NoError(t, err)
	graders := "graders:\n  - type: text\n    name: never-matches\n    config:\n      contains: [NEVER_MATCH_VALUE_12345]\ntasks:"
	require.NoError(t, os.WriteFile(specPath, []byte(strings.Replace(string(data), "tasks:", graders, 1)), 0o644))
	return specPath
}
