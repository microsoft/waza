package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInspectEvidenceSelectionsIncludesDisabledTasksWithoutReadingOutput(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("selected-file declarations are unsupported on this platform")
	}
	for _, test := range []struct {
		name, selected, excluded string
		disabled, invalid        bool
	}{
		{"valid absent output", "report.txt", "references", false, false},
		{"conflicting union", "references/report.txt", "references", false, true},
		{"disabled conflicting union", "references/report.txt", "references", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			eval := strings.ReplaceAll(validEval, "executor: mock", "executor: copilot-sdk")
			eval += "graders:\n  - type: prompt\n    name: final\n    config:\n      prompt: Judge\n      evidence_context:\n        files: [" + test.selected + "]\n"
			task := fmt.Sprintf("id: task\nname: Task\nenabled: %t\ninputs: {prompt: Inspect}\ngraders:\n  - type: prompt\n    name: task-judge\n    config:\n      prompt: Judge\n      evidence_context:\n        evaluator_only: [%s]\n", !test.disabled, test.excluded)
			path, dir := writeSuite(t, eval, task)
			require.NoError(t, os.Mkdir(filepath.Join(dir, "fixtures"), 0700))
			require.NoError(t, os.Mkdir(filepath.Join(dir, "fixtures", "references"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "fixtures", "references", "report.txt"), []byte("PRIVATE_EVALUATOR_CANARY"), 0000))
			report := Inspect(path, Options{})
			require.Equal(t, test.invalid, hasDiagnostic(report, "grader.evidence_selection", Invalid), "%+v", report.Diagnostics)
			require.NotContains(t, fmt.Sprint(report), "PRIVATE_EVALUATOR_CANARY")
			if !test.invalid {
				require.False(t, report.Failed(false), "%+v", report.Diagnostics)
			}
		})
	}
}
