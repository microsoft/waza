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
		Jobs map[string]struct {
			Timeout int `yaml:"timeout-minutes"`
			Steps   []struct {
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	job := workflow.Jobs["build-and-test"]
	require.Positive(t, job.Timeout)
	selected := 0
	for _, step := range job.Steps {
		if step.Name == "Public single-unit container conformance" {
			selected++
			require.Equal(t, "matrix.os == 'ubuntu-latest'", step.If)
			require.Equal(t, "false", step.Env["ENABLE_COPILOT_TESTS"])
			require.Contains(t, step.Run, "bash examples/runner/build.sh")
			require.Contains(t, step.Run, "bash examples/runner/smoke.sh")
		}
	}
	require.Equal(t, 1, selected)
}
