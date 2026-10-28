package webapi

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRunnerBrowserIntegrationHasGoAndBackendDiscovery(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/web-ci.yml")
	require.NoError(t, err)
	var workflow struct {
		On struct {
			Push struct {
				Paths []string `yaml:"paths"`
			} `yaml:"push"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Timeout int               `yaml:"timeout-minutes"`
			Env     map[string]string `yaml:"env"`
			Steps   []struct {
				Uses string            `yaml:"uses"`
				If   string            `yaml:"if"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	require.Equal(t, "read", workflow.Permissions["contents"])
	job := workflow.Jobs["build"]
	require.Positive(t, job.Timeout)
	require.Equal(t, "/usr/bin/false", job.Env["COPILOT_CLI_PATH"])
	require.Equal(t, "false", job.Env["ENABLE_COPILOT_TESTS"])
	require.Equal(t, "false", job.Env["ENABLE_LIVE_COPILOT_TESTS"])
	var filters map[string][]string
	goConfigured := false
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "dorny/paths-filter@") {
			require.NoError(t, yaml.Unmarshal([]byte(step.With["filters"]), &filters))
		}
		if strings.HasPrefix(step.Uses, "actions/setup-go@") {
			goConfigured = true
			require.Equal(t, "go.mod", step.With["go-version-file"])
			require.Equal(t, "go.sum", step.With["cache-dependency-path"])
			require.Contains(t, step.If, "steps.filter.outputs.web")
			require.Contains(t, step.If, "github.event_name != 'pull_request'")
		}
	}
	require.True(t, goConfigured, "real Go API browser coverage cannot rely on an ambient compiler")
	for _, pattern := range []string{
		"web/**", "cmd/waza/**", "internal/runnercontract/**", "internal/webapi/**",
		"internal/webserver/**", "internal/snapshot/**", "schemas/runner-*.schema.json", "go.mod", "go.sum",
	} {
		require.Contains(t, workflow.On.Push.Paths, pattern)
		require.Contains(t, filters["web"], pattern, "backend-only changes must not silently skip browser contract coverage")
	}
}
