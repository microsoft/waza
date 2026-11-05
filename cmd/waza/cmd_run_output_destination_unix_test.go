//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestSaveOutcomeRejectsSpecialFileDestinations(t *testing.T) {
	for _, path := range []string{os.DevNull, filepath.Join(t.TempDir(), "results.fifo")} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if path != os.DevNull {
				require.NoError(t, unix.Mkfifo(path, 0o600))
			}
			before, err := os.Lstat(path)
			require.NoError(t, err)

			err = saveOutcome(&models.EvaluationOutcome{SchemaVersion: models.CurrentResultSchemaVersion}, path)
			require.ErrorContains(t, err, "not a regular file")

			after, statErr := os.Lstat(path)
			require.NoError(t, statErr)
			require.Equal(t, before.Mode().Type(), after.Mode().Type())
		})
	}
}
