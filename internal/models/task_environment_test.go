package models

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadTestCaseUnsupportedEnvironment(t *testing.T) {
	for _, tt := range []struct {
		name, field string
		wantWarning bool
	}{
		{"absent", "", false},
		{"null", "  environment: null\n", false},
		{"empty", "  environment: {}\n", false},
		{"populated", "  environment: {PRIVATE_SETTING: sensitive-value}\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte("id: env-task\nname: Environment\ninputs:\n  prompt: Say ready.\n" + tt.field)
			path := filepath.Join(t.TempDir(), "task.yaml")
			require.NoError(t, os.WriteFile(path, data, 0600))
			var warnings bytes.Buffer
			logger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&warnings, nil)))
			t.Cleanup(func() { slog.SetDefault(logger) })
			task, err := LoadTestCase(path)
			require.NoError(t, err, "unsupported environment must not reject legacy tasks")
			require.Equal(t, "Say ready.", task.Stimulus.Message)
			require.Equal(t, data, task.SourceBytes())
			if tt.wantWarning {
				require.Equal(t, map[string]string{"PRIVATE_SETTING": "sensitive-value"}, task.Stimulus.Environment)
				require.Contains(t, warnings.String(), "level=WARN")
				require.Contains(t, warnings.String(), "inputs.environment is unsupported")
				require.Contains(t, warnings.String(), "not applied during task execution")
			} else {
				require.Empty(t, warnings.String())
				require.Empty(t, task.Stimulus.Environment)
			}
			require.NotContains(t, warnings.String(), "PRIVATE_SETTING")
			require.NotContains(t, warnings.String(), "sensitive-value")
		})
	}
}
