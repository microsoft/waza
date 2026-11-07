//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSaveOutcomePermissionApplicationUsesOwnedFile(t *testing.T) {
	for _, replacement := range []string{"none", "temporary leaf", "parent directory"} {
		t.Run(replacement, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "output")
			require.NoError(t, os.Mkdir(dir, 0o700))
			path := filepath.Join(dir, "checkpoint.json")
			require.NoError(t, os.WriteFile(path, []byte("previous checkpoint"), 0o644))
			require.NoError(t, os.Chmod(path, 0o644))
			target := filepath.Join(root, "unrelated-private-fixture")
			original := []byte("harmless private fixture")
			require.NoError(t, os.WriteFile(target, original, 0o600))
			require.NoError(t, os.Chmod(target, 0o600))

			previousCreate := createOutcomeTempFileForSave
			previousReplace := replaceOutcomeFileForSave
			t.Cleanup(func() {
				createOutcomeTempFileForSave = previousCreate
				replaceOutcomeFileForSave = previousReplace
			})
			var owned *os.File
			var restore func()
			createOutcomeTempFileForSave = func(destination string) (*os.File, os.FileMode, error) {
				file, mode, err := createOutcomeTempFile(destination)
				if err != nil {
					return nil, 0, err
				}
				owned = file
				switch replacement {
				case "temporary leaf":
					held := file.Name() + ".held"
					require.NoError(t, os.Rename(file.Name(), held))
					require.NoError(t, os.Symlink(target, file.Name()))
					restore = func() {
						require.NoError(t, os.Remove(file.Name()))
						require.NoError(t, os.Rename(held, file.Name()))
					}
				case "parent directory":
					held := dir + ".held"
					require.NoError(t, os.Rename(dir, held))
					require.NoError(t, os.Mkdir(dir, 0o700))
					require.NoError(t, os.Symlink(target, file.Name()))
					restore = func() {
						require.NoError(t, os.Remove(file.Name()))
						require.NoError(t, os.Remove(dir))
						require.NoError(t, os.Rename(held, dir))
					}
				}
				if replacement != "none" {
					link, linkErr := os.Readlink(file.Name())
					require.NoError(t, linkErr)
					require.Equal(t, target, link)
				}
				return file, mode, nil
			}
			published := false
			replaceOutcomeFileForSave = func(source, destination string) error {
				published = true
				info, err := os.Stat(target)
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "publication must not chmod the substituted symlink target")
				content, err := os.ReadFile(target)
				require.NoError(t, err)
				require.Equal(t, original, content)
				if restore != nil {
					// Restore only the test's substitutions; namespace-safe publication
					// is not a guarantee of the descriptor permission fix.
					restore()
				}
				info, err = os.Stat(source)
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
				_, err = owned.Stat()
				require.ErrorIs(t, err, os.ErrClosed)
				return replaceOutcomeFile(source, destination)
			}

			require.NoError(t, saveOutcome(&models.EvaluationOutcome{SchemaVersion: models.CurrentResultSchemaVersion}, path))
			require.True(t, published)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			var saved models.EvaluationOutcome
			require.NoError(t, json.Unmarshal(data, &saved))
			require.Equal(t, models.CurrentResultSchemaVersion, saved.SchemaVersion)
		})
	}
}
