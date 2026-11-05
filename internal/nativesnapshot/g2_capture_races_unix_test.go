//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package nativesnapshot

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestG2AdapterActualFIFOAndDirectoryFIFOFailClosed(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "source", true: "directory"}[directory], func(t *testing.T) {
			ctx := &readContext{Context: context.Background()}
			files, base := reader(t, ctx)
			target := filepath.Join(base, "input")
			if directory {
				require.NoError(t, os.Mkdir(target, 0700))
			} else {
				write(t, base, "input", "regular input")
			}
			ctx.onCheck = func(call int) error {
				if call != 3 {
					return nil
				}
				if err := os.Remove(target); err != nil {
					return err
				}
				return unix.Mkfifo(target, 0600)
			}
			recorder := newAssociationRecorder(ctx, base, &associationBudget{})
			recorder.set(requestPhase, 0)
			adapter := &semanticRequestInputs{inputs: orchestration.AdaptRequestFiles(files), files: files, recorder: recorder, role: "context_fixture"}
			done := make(chan error, 1)
			go func() {
				if directory {
					_, err := (associatedDiscoveryFiles{discoveryFiles{files}, recorder}).ReadDir("input")
					done <- err
				} else {
					_, err := adapter.ReadFile(target)
					done <- err
				}
			}()
			select {
			case err := <-done:
				require.Error(t, err)
				require.Empty(t, files.data)
				require.Empty(t, files.dirs)
				require.Empty(t, recorder.events)
			case <-time.After(time.Second):
				fd, err := unix.Open(target, unix.O_RDWR|unix.O_NONBLOCK, 0)
				require.NoError(t, err)
				<-done
				require.NoError(t, unix.Close(fd))
				t.Fatal("capture waited for a FIFO writer")
			}
		})
	}
}

func TestG2UnmatchedSpecialAndSymlinkNamesRemainIgnored(t *testing.T) {
	base, locations := fixture(t)
	require.NoError(t, unix.Mkfifo(filepath.Join(base, "baseline/tasks/ignored.fifo"), 0600))
	require.NoError(t, os.Symlink("missing-target", filepath.Join(base, "baseline/tasks/ignored.link")))
	original := frozenOriginalCapture(t, locations)
	capture, err := prepareProjectionCapture(context.Background(), locations)
	require.NoError(t, err)
	require.Equal(t, original.Canonical, capture.prepared.canonical)
	var associated captureAssociations
	require.NoError(t, json.Unmarshal(capture.associations.canonical, &associated))
	var names []string
	for _, dir := range associated.Arms[releasepolicy.Baseline].Directories {
		if dir.Path == "baseline/tasks" {
			for _, entry := range dir.Entries {
				names = append(names, entry.Name)
			}
		}
	}
	require.Contains(t, names, "ignored.fifo")
	require.Contains(t, names, "ignored.link")
	input, err := detachProjectionInput(context.Background(), capture)
	require.NoError(t, err)
	projection, err := reconstructProjection(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, original.Projection, projection)
}

func TestG2VisitedSpecialInputStillRejected(t *testing.T) {
	base, locations := fixture(t)
	require.NoError(t, os.Remove(filepath.Join(base, "baseline/context/a.txt")))
	require.NoError(t, unix.Mkfifo(filepath.Join(base, "baseline/context/a.txt"), 0600))
	capture, err := prepareProjectionCapture(context.Background(), locations)
	require.Error(t, err)
	require.Nil(t, capture)
	require.NotErrorIs(t, err, fs.ErrNotExist)
}
