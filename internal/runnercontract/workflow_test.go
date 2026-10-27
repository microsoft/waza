package runnercontract

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRunnerContainerConformanceSelectedByRequiredLinuxJob(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/go-ci.yml")
	require.NoError(t, err)
	var workflow struct {
		On map[string]struct {
			Paths       []string `yaml:"paths"`
			PathsIgnore []string `yaml:"paths-ignore"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Timeout int      `yaml:"timeout-minutes"`
			Needs   []string `yaml:"needs"`
			Steps   []struct {
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	require.Empty(t, workflow.On["pull_request"].Paths)
	require.Empty(t, workflow.On["pull_request"].PathsIgnore)
	require.Equal(t, "read", workflow.Permissions["contents"])
	require.Contains(t, workflow.Jobs["linux-gate"].Needs, "build-and-test")
	job := workflow.Jobs["build-and-test"]
	require.Positive(t, job.Timeout)
	selected := 0
	for _, step := range job.Steps {
		if step.Name == "Public single-unit container conformance" {
			selected++
			require.Equal(t, "matrix.os == 'ubuntu-latest'", step.If)
			require.Equal(t, "false", step.Env["ENABLE_COPILOT_TESTS"])
			require.Contains(t, step.Run, "bash examples/runner/build.sh")
			require.Contains(t, step.Run, "bash examples/runner/test.sh")
		}
	}
	require.Equal(t, 1, selected)
}

func TestRunnerPublicationIsExplicitAndBounded(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/runner-reference-publish.yml")
	require.NoError(t, err)
	var workflow struct {
		On          map[string]interface{} `yaml:"on"`
		Permissions map[string]string      `yaml:"permissions"`
		Jobs        map[string]struct {
			If          string            `yaml:"if"`
			Timeout     int               `yaml:"timeout-minutes"`
			Permissions map[string]string `yaml:"permissions"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	require.Len(t, workflow.On, 1)
	require.Contains(t, workflow.On, "workflow_dispatch")
	require.Equal(t, "read", workflow.Permissions["contents"])
	job := workflow.Jobs["publish"]
	require.Positive(t, job.Timeout)
	require.Equal(t, map[string]string{"contents": "read", "packages": "write"}, job.Permissions)
	require.Contains(t, job.If, "github.repository == 'microsoft/waza'")
	require.Contains(t, job.If, "github.ref == 'refs/heads/main'")
}
