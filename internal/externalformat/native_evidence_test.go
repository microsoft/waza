package externalformat

import (
	"context"
	"os"
	"testing"

	"github.com/microsoft/waza/internal/graders"
	"github.com/stretchr/testify/require"
)

func TestNativeContainsDecisionControls(t *testing.T) {
	source, err := os.ReadFile("testdata/native-world.yaml")
	require.NoError(t, err)
	task := loadNative(t, TaskFile{Path: "native-world.yaml", Content: string(source)})
	require.Equal(t, "world", task.TestID)
	require.Equal(t, "Say hello to the world.", task.Stimulus.Message)
	require.Len(t, task.Validators, 1)

	for _, control := range []struct {
		output string
		score  float64
		passed bool
	}{
		{output: "Hello world", score: 1, passed: true},
		{output: "Hello World", score: 0, passed: false},
		{output: "", score: 0, passed: false},
	} {
		t.Run(control.output, func(t *testing.T) {
			results, err := graders.RunAll(context.Background(), nil, task,
				&graders.Context{Output: control.output, OutputPresent: true}, "", "", false)
			require.NoError(t, err)
			require.Len(t, results, 1)
			result := results["contains"]
			require.Equal(t, control.passed, result.Passed)
			require.Equal(t, control.score, result.Score)
			require.Equal(t, float64(1), result.Weight)
		})
	}
}
