package optimizer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoCaptureAdmitsBothSelectedSplits(t *testing.T) {
	suite := copySuite(t)
	replaceInFile("eval.yaml", "  executor: mock\n", "  workspace_capture: none\n  executor: mock\n")(t, suite)
	unselected := `id: unrelated
name: unrelated
tags: [other]
inputs: {prompt: hello}
graders:
  - name: file
    type: file
    config: {must_exist: [report.txt]}
`
	require.NoError(t, os.WriteFile(filepath.Join(suite, "tasks", "unselected.yaml"), []byte(unselected), 0o600))
	fake, err := LoadFakeScript(filepath.Join(suite, "fake-script.json"))
	require.NoError(t, err)
	cfg := harnessConfig(t, suite, fake)
	require.NotContains(t, cfg.Pin.Optimization.TestIDs, "unrelated")
	require.NotContains(t, cfg.Pin.Qualification.TestIDs, "unrelated")
	report, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, OutcomeAccepted, report.Outcome, report.Failure)
	var optimization, qualification bool
	for _, ev := range report.Evaluations {
		optimization = optimization || ev.Split == SplitOptimization
		qualification = qualification || ev.Split == SplitQualification
	}
	require.True(t, optimization)
	require.True(t, qualification)
	require.NotEmpty(t, fake.Requests())
}
