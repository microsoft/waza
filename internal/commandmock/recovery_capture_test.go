package commandmock

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
	require.NoError(t, err)
	require.Equal(t, 130, canceled.ExitCode)
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
