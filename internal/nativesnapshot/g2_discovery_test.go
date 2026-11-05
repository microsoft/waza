package nativesnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/microsoft/waza/internal/orchestration"
	"github.com/stretchr/testify/require"
)

type originalGlobQueries struct {
	discoveryFiles
	queries []string
}

func (f *originalGlobQueries) Stat(path string) (os.FileInfo, error) {
	f.queries = append(f.queries, "kind:"+path)
	return f.discoveryFiles.Stat(path)
}

func (f *originalGlobQueries) ReadDir(path string) ([]fs.DirEntry, error) {
	f.queries = append(f.queries, "directory:"+path)
	return f.discoveryFiles.ReadDir(path)
}

func TestG2TypedDiscoveryMatchesOriginalGlobQueriesAndResults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native directory capture is explicitly unsupported; covered by the platform negative")
	}
	for _, pattern := range []string{"tasks/a.yaml", "tasks/missing.yaml", "tasks/*.yaml",
		"*/a.yaml", "tasks/*/*.yaml", "tasks/*/*/*.yaml", "tasks/[ab].yaml", "tasks/?.yaml",
		"empty/*", "tasks/no-match-*.yaml", "tasks/[", "tasks/\\*.yaml"} {
		t.Run(pattern, func(t *testing.T) {
			files, base := reader(t, context.Background())
			write(t, base, "tasks/a.yaml", "a")
			write(t, base, "tasks/b.yaml", "b")
			write(t, base, "tasks/nested/a.yaml", "nested")
			write(t, base, "tasks/nested/deeper/a.yaml", "deeper")
			require.NoError(t, os.MkdirAll(filepath.Join(base, "empty"), 0700))
			original := &originalGlobQueries{discoveryFiles: discoveryFiles{files}}
			expected, expectedErr := fs.Glob(original, pattern)
			actualFiles, _ := readerAtRoot(t, files.root, base)
			recorder := newAssociationRecorder(context.Background(), base, &associationBudget{})
			recorder.set(discoveryPhase, 0)
			actual, actualErr := fs.Glob(associatedDiscoveryFiles{discoveryFiles{actualFiles}, recorder}, pattern)
			require.Equal(t, expectedErr, actualErr)
			require.Equal(t, expected, actual)
			if actualFiles.sticky != nil {
				require.NotNil(t, files.sticky)
				return
			}
			associated, err := recorder.finish(nil)
			require.NoError(t, err)
			var queries []string
			for _, event := range associated.Events {
				queries = append(queries, string(event.Op)+":"+event.Path)
			}
			require.Equal(t, original.queries, queries)
			retained := newRetainedArmInputs(context.Background(), base, associated, nil)
			retained.set(discoveryPhase, 0, "task")
			reconstructed, reconstructedErr := matchCapturedPattern(context.Background(), pattern, &retainedDiscoveryInputs{retained})
			require.Equal(t, expectedErr, reconstructedErr)
			require.Equal(t, expected, reconstructed)
			require.Equal(t, len(associated.Events), retained.position)
		})
	}
}

func readerAtRoot(t *testing.T, root *os.Root, base string) (*capturedFiles, string) {
	t.Helper()
	total := 0
	return &capturedFiles{ctx: context.Background(), root: root, base: base,
		data: map[string][]byte{}, info: map[string]os.FileInfo{}, dirs: map[string][]fs.DirEntry{},
		roles: map[string]map[string]bool{}, total: &total}, base
}

func TestG2WalkRejectsPartialTapeAndEmptyDirectoryOmission(t *testing.T) {
	for _, name := range []string{"missing_end", "missing_entry", "missing_empty_directory", "reordered_entries", "nested_walk", "walk_callback_error", "walk_cancel"} {
		t.Run(name, func(t *testing.T) {
			base, locations := fixture(t)
			write(t, base, "baseline/tasks/task.yaml", "id: task\ninputs: {prompt: hello, context: {fixture: fixture}}\n")
			require.NoError(t, os.MkdirAll(filepath.Join(base, "baseline/fixture/empty"), 0700))
			write(t, base, "baseline/fixture/file", "actual")
			capture, err := prepareProjectionCapture(context.Background(), locations)
			require.NoError(t, err)
			if name == "walk_callback_error" || name == "walk_cancel" {
				var all captureAssociations
				require.NoError(t, json.Unmarshal(capture.associations.canonical, &all))
				arm := all.Arms["baseline"]
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r := newRetainedArmInputs(ctx, base, arm, capture.prepared.sources["baseline"])
				for i, event := range arm.Events {
					if event.Op == walkBeginOperation {
						r.position = i
						r.set(event.Phase, event.ScopeIndex, event.Role)
						break
					}
				}
				sentinel := errors.New("consumer failed")
				err := r.Walk(filepath.Join(base, "baseline/fixture"), func(string, orchestration.RequestInputKind) error {
					if name == "walk_cancel" {
						cancel()
						return nil
					}
					return sentinel
				})
				require.Error(t, err)
				require.Error(t, r.sticky)
				if name == "walk_cancel" {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.ErrorIs(t, err, sentinel)
				}
				require.Less(t, r.position, len(r.associated.Events))
				return
			}
			resealG2(t, capture, func(all *captureAssociations) {
				arm := all.Arms["baseline"]
				for i, event := range arm.Events {
					remove := name == "missing_end" && event.Op == walkEndOperation || name == "missing_entry" && event.Op == walkEntryOperation
					if remove {
						arm.Events = append(arm.Events[:i], arm.Events[i+1:]...)
						break
					}
					if name == "nested_walk" && event.Op == walkEntryOperation {
						arm.Events[i].Op, arm.Events[i].Node = walkBeginOperation, nil
						break
					}
					if name == "reordered_entries" && event.Op == walkEntryOperation && i+1 < len(arm.Events) && arm.Events[i+1].Op == walkEntryOperation {
						arm.Events[i], arm.Events[i+1] = arm.Events[i+1], arm.Events[i]
						break
					}
				}
				if name == "missing_empty_directory" {
					for i, directory := range arm.Directories {
						if directory.Path == "baseline/fixture/empty" {
							arm.Directories = append(arm.Directories[:i], arm.Directories[i+1:]...)
							break
						}
					}
				}
				for i := range arm.Events {
					arm.Events[i].Ordinal = uint32(i + 1)
				}
				all.Arms["baseline"] = arm
			})
			input, err := detachProjectionInput(context.Background(), capture)
			if err == nil {
				result, reconstructErr := reconstructProjection(context.Background(), input)
				require.Error(t, reconstructErr)
				require.Nil(t, result)
			} else {
				require.Equal(t, detachedProjectionInput{}, input)
			}
		})
	}
}
