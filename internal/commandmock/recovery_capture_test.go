package commandmock

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRecoveryCancellationAfterAdmissionConsumesStep(t *testing.T) {
	root := commandMockTestRoot(t)
	workspace := t.TempDir()
	session, err := NewSession(workspace, []models.CommandMockConfig{{
		Name: "az", ExpectCalls: new(2), Responses: []models.CommandMockResponse{{
			Args: []string{"account"}, Sequence: []models.CommandMockStep{{Stderr: "first", ExitCode: 1}, {Stdout: "recovered"}},
		}},
	}}, t.TempDir())
	require.NoError(t, err)
	config, err := readStoredConfig(filepath.Join(session.dir, "config.json"))
	require.NoError(t, err)
	path, attempt, err := startInvocation(session.logDir, "az", []string{"account"}, 0)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceled, err := runStartedSequence(ctx, config.Mocks[0].Responses[0], path, attempt)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 130, canceled.ExitCode)
	require.Empty(t, canceled.Stdout)
	require.Empty(t, canceled.Stderr)
	_, err = os.Stat(filepath.Join(config.Mocks[0].Responses[0].SequenceDir, "step-00000000"))
	require.NoError(t, err)
	retried, err := Invoke(root, session.ID(), "az", []string{"account"}, workspace)
	require.NoError(t, err)
	require.Equal(t, "recovered", string(retried.Stdout))
	records, err := session.Close()
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, new(130), records[0].ExitCode)
	require.Equal(t, new(0), records[1].ExitCode)
}

func TestRecoverySequenceDeadlineReachesCallers(t *testing.T) {
	root := commandMockTestRoot(t)
	for _, native := range []bool{false, true} {
		name := "shim"
		if native {
			name = "native fallback"
		}
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			session, err := NewSession(workspace, []models.CommandMockConfig{{
				Name: "az", ExpectCalls: new(1), Responses: []models.CommandMockResponse{{
					Args: []string{"account"}, Sequence: []models.CommandMockStep{{
						DelayMS: 300000, Stdout: "not delivered", Stderr: "not delivered", ExitCode: 7,
					}},
				}},
			}}, workspace)
			require.NoError(t, err)
			t.Chdir(workspace)
			t.Setenv(SessionEnvironmentVariable, session.ID())
			stderr, err := os.CreateTemp(t.TempDir(), "stderr")
			require.NoError(t, err)
			original := os.Stderr
			os.Stderr = stderr
			t.Cleanup(func() {
				os.Stderr = original
				require.NoError(t, stderr.Close())
			})
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			var code int
			if native {
				code, err = RunNativeCommand(ctx, root, session.ID(), "az", []string{"account"}, workspace, NativeDispatchOptions{})
				require.Equal(t, 127, code, "native fallback preserves its existing error status")
			} else {
				code, err = RunCommandContext(ctx, root, "az", []string{"account"})
				require.Equal(t, 124, code)
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
			info, err := stderr.Stat()
			require.NoError(t, err)
			require.Zero(t, info.Size(), "cancellation must return its cause, not emit a diagnostic")
			records, err := session.Close()
			require.NoError(t, err)
			require.Len(t, records, 1, "the deadline must occur after admission, not before invocation")
			require.Equal(t, []string{"account"}, records[0].Args)
			require.Equal(t, 0, records[0].ResponseIndex)
			require.Equal(t, new(124), records[0].ExitCode)
		})
	}
}

func TestRecoveryCanceledAttemptPublicationFailure(t *testing.T) {
	commandMockTestRoot(t)
	session, err := NewSession(t.TempDir(), []models.CommandMockConfig{{
		Name: "az", Responses: []models.CommandMockResponse{{
			Args: []string{}, Sequence: []models.CommandMockStep{{Stdout: "not delivered"}},
		}},
	}}, t.TempDir())
	require.NoError(t, err)
	config, err := readStoredConfig(filepath.Join(session.dir, "config.json"))
	require.NoError(t, err)
	path, attempt, err := startInvocation(session.logDir, "az", nil, 0)
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(path+".completed", 0700))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := runStartedSequence(ctx, config.Mocks[0].Responses[0], path, attempt)
	require.ErrorIs(t, err, context.Canceled, "publication failure must not erase the cancellation cause")
	require.ErrorContains(t, err, "recording completed command-mock invocation")
	require.Empty(t, result.Stdout)
	require.Empty(t, result.Stderr)
	require.NoError(t, os.Remove(path+".completed"))
	records, err := session.Close()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Nil(t, records[0].ExitCode, "failed publication must not invent completed evidence")
}

func TestRecoveryIncompleteAndCorruptCapture(t *testing.T) {
	commandMockTestRoot(t)
	for _, test := range []struct {
		name, completion, wantErr string
	}{
		{name: "incomplete remains observed"},
		{name: "corrupt completion", completion: "{", wantErr: "decoding completed"},
		{name: "completion without exit", completion: `{"invocation":{"command":"az"}}`, wantErr: "missing its exit code"},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, err := NewSession(t.TempDir(), []models.CommandMockConfig{{
				Name: "az", ExpectCalls: new(1), Responses: []models.CommandMockResponse{{Args: []string{"account"}, Stdout: "ready"}},
			}}, t.TempDir())
			require.NoError(t, err)
			path, _, err := startInvocation(session.logDir, "az", []string{"account"}, 0)
			require.NoError(t, err)
			require.Equal(t, ".json", filepath.Ext(path))
			if test.completion != "" {
				require.NoError(t, os.WriteFile(path+".completed", []byte(test.completion), 0600))
			}
			records, err := session.Close()
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
				require.Len(t, records, 1)
				require.Nil(t, records[0].ExitCode)
			}
		})
	}
}
