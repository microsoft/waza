//go:build unix

package commandmock

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestSessionCloseWaitsForInvocationInspection(t *testing.T) {
	commandMockTestRoot(t)
	for _, native := range []bool{false, true} {
		name := "legacy"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			var prepare NativePrepare
			if native {
				prepare = func(context.Context, NativePrepareRequest) (json.RawMessage, error) {
					return json.RawMessage(`{}`), nil
				}
			}
			session, err := NewNativeSession(t.Context(), t.TempDir(), []models.CommandMockConfig{{
				Name: "az", Responses: []models.CommandMockResponse{{Args: []string{}, Stdout: "unused"}},
			}}, t.TempDir(), prepare)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := session.Close()
				require.NoError(t, err)
			})
			record, err := json.Marshal(invocationRecord{
				RecordedAt: time.Now().UTC(),
				Invocation: models.CommandInvocation{Command: "probe", ExitCode: new(7)},
			})
			require.NoError(t, err)
			path := filepath.Join(session.logDir, "probe.json")
			require.NoError(t, unix.Mkfifo(path, 0600))
			read := make(chan []models.CommandInvocation, 1)
			go func() { read <- session.Invocations() }()
			writer, err := os.OpenFile(path, os.O_WRONLY, 0)
			require.NoError(t, err)
			writerClosed := false
			t.Cleanup(func() {
				if !writerClosed {
					err := os.Remove(path)
					if !errors.Is(err, os.ErrNotExist) {
						require.NoError(t, err)
					}
					require.NoError(t, writer.Close())
				}
			})
			// Opening the FIFO writer proves Invocations is inside its read.
			// Holding only a bool/atomic guard would still allow state removal.
			if session.mu.TryLock() {
				session.mu.Unlock()
				t.Fatal("Invocations did not retain the lifecycle read lock")
			}
			type closeResult struct {
				records []models.CommandInvocation
				err     error
			}
			closed := make(chan closeResult, 1)
			go func() {
				records, err := session.Close()
				closed <- closeResult{records, err}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for session.mu.TryRLock() {
				session.mu.RUnlock()
				require.True(t, time.Now().Before(deadline), "Close did not queue its exclusive lifecycle lock")
				runtime.Gosched()
			}
			require.DirExists(t, session.dir, "Close must not remove state during an admitted inspection")
			// Close must read a regular final record, not reopen the FIFO.
			replacement := filepath.Join(session.logDir, "replacement")
			require.NoError(t, os.WriteFile(replacement, record, 0600))
			require.NoError(t, os.Rename(replacement, path))
			_, err = writer.Write(record)
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			writerClosed = true
			want := []models.CommandInvocation{{Command: "probe", ExitCode: new(7)}}
			require.Equal(t, want, <-read)
			result := <-closed
			require.NoError(t, result.err)
			require.Equal(t, want, result.records)
			require.NoDirExists(t, session.dir)
			require.Nil(t, session.Invocations(), "inspection after Close must not reopen removed state")
		})
	}
}
