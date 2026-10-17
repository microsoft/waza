package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecoveryInlineSequenceSchemaVersion(t *testing.T) {
	for _, version := range []string{"", "1.4", "2.0"} {
		for _, mocks := range []string{
			"command_mocks: [{name: az, responses: [{args: [], sequence: [{stdout: ready}]}]}]",
			"mcp_mocks: [{name: dependency, tools: {read: {responses: [{sequence: [{return: ready}]}]}}}]",
			"command_mocks: [{name: az, responses: [{args: [], stdout: ready}]}]",
			"mcp_mocks: [{name: dependency, tools: {read: {responses: [{return: ready}]}}}]",
		} {
			t.Run(version+mocks, func(t *testing.T) {
				prefix := "skill: example\n"
				if version == "2.0" {
					prefix = "scenario: recovery\n"
				}
				if version != "" {
					prefix += fmt.Sprintf("schemaVersion: %q\n", version)
				}
				data := prefix + "name: recovery\nconfig: {executor: copilot-sdk, model: harness, trials_per_task: 1, timeout_seconds: 10}\nmetrics: [{name: accuracy, weight: 1, threshold: 1}]\ntasks: [task.yaml]\n" + mocks
				hasSequence := strings.Contains(mocks, "sequence:")
				errs := ValidateEvalBytes([]byte(data))
				require.Equal(t, hasSequence && version != "2.0", len(errs) > 0, "%v", errs)
			})
		}
	}
}

func TestRecoveryTaskSequenceStaticAdmission(t *testing.T) {
	for _, version := range []string{"1.4", "2.0"} {
		t.Run(version, func(t *testing.T) {
			dir := t.TempDir()
			identity := "skill: example"
			if version == "2.0" {
				identity = "scenario: recovery"
			}
			eval := fmt.Sprintf("schemaVersion: %q\n%s\nname: recovery\nconfig: {executor: copilot-sdk, model: harness, trials_per_task: 1, timeout_seconds: 10}\nmetrics: [{name: accuracy, weight: 1, threshold: 1}]\ntasks: [task.yaml]\n", version, identity)
			task := "id: task\nname: Task\ninputs: {prompt: inspect}\ncommand_mocks: [{name: az, responses: [{args: [], sequence: [{stdout: ready}]}]}]\n"
			path := filepath.Join(dir, "eval.yaml")
			require.NoError(t, os.WriteFile(path, []byte(eval), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(task), 0600))
			evalErrs, taskErrs, err := ValidateEvalFile(path)
			require.NoError(t, err)
			require.Empty(t, evalErrs)
			require.Equal(t, version != "2.0", len(taskErrs["task.yaml"]) > 0)
		})
	}
}
