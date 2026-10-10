//go:build windows

package nativesnapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func TestG2WindowsDirectoryCaptureRemainsUnsupported(t *testing.T) {
	base := t.TempDir()
	write(t, base, "eval.yaml", snapshotEval)
	write(t, base, "tasks/task.yaml", "id: task\ninputs: {prompt: hello}\n")
	write(t, base, "evaluator", "offline bytes")
	root, err := os.OpenRoot(base)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	location := ArmLocation{Root: root, EvalPath: "eval.yaml", CWD: filepath.Clean(base), Executable: "evaluator"}
	capture, err := prepareProjectionCapture(context.Background(), map[releasepolicy.Arm]ArmLocation{
		releasepolicy.Baseline: location, releasepolicy.Candidate: location,
	})
	require.Error(t, err)
	require.Nil(t, capture)
}
