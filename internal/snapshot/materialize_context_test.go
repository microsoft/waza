package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/microsoft/waza/internal/models"
)

func TestMaterializeContextReportsEvidenceRejection(t *testing.T) {
	want := VerifyWorkspace(nil, models.EvidenceOrigin{}, "", []string{"output.txt"})
	workspace, err := MaterializeWorkspaceContext(t.Context(), nil, models.EvidenceOrigin{}, "", []string{"output.txt"})
	require.Nil(t, workspace)
	require.EqualError(t, err, want.Error())
	rejection, ok := errors.AsType[*WorkspaceVerificationError](err)
	require.True(t, ok)
	require.Equal(t, err.Error(), errors.Unwrap(rejection).Error())
	require.ErrorIs(t, err, rejection.Unwrap())
}

func TestMaterializeContextCancellationPrecedesRestoration(t *testing.T) {
	snap := capturedWorkspace(t, "actual output")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	workspace, err := MaterializeWorkspaceContext(ctx, snap, snap.Evidence.Origin, snap.Evidence.SHA256, []string{"output.txt"})
	require.Nil(t, workspace)
	require.ErrorIs(t, err, context.Canceled)
	_, rejection := errors.AsType[*WorkspaceVerificationError](err)
	require.False(t, rejection, "cancellation is operational, not invalid evidence")
}

func TestMaterializeContextRechecksRawAndTypedContentEveryCall(t *testing.T) {
	for _, mutation := range []string{"raw", "typed"} {
		t.Run(mutation, func(t *testing.T) {
			snap := capturedWorkspace(t, "actual output")
			data, err := json.Marshal(snap)
			require.NoError(t, err)
			snap, err = ParseSnapshot(data, "context restoration regression")
			require.NoError(t, err)
			origin, identity := snap.Evidence.Origin, snap.Evidence.SHA256
			paths := []string{"output.txt"}
			workspace, err := MaterializeWorkspaceContext(t.Context(), snap, origin, identity, paths)
			require.NoError(t, err)
			content, err := os.ReadFile(filepath.Join(workspace.Path(), paths[0]))
			require.NoError(t, err)
			require.Equal(t, "actual output", string(content))
			require.NoError(t, workspace.Close())

			changed := snap.WorkspaceFiles[0]
			changed.Content = "mutated after earlier successful restoration"
			if mutation == "typed" {
				snap.WorkspaceFiles[0] = changed
			} else {
				snap.rawWorkspaceFiles[0], err = json.Marshal(changed)
				require.NoError(t, err)
			}
			workspace, err = MaterializeWorkspaceContext(t.Context(), snap, origin, identity, paths)
			require.Nil(t, workspace)
			require.Error(t, err)
			_, rejected := errors.AsType[*WorkspaceVerificationError](err)
			require.True(t, rejected)
		})
	}
}

type materializeMutationContext struct {
	context.Context
	mutate func()
}

func (ctx materializeMutationContext) Err() error {
	ctx.mutate()
	return nil
}

func TestMaterializeContextOwnsVerifiedRestorationInputs(t *testing.T) {
	snap := capturedWorkspace(t, "actual output")
	paths := []string{"output.txt"}
	ctx := materializeMutationContext{
		Context: t.Context(),
		mutate: func() {
			snap.WorkspaceFiles[0].Content = "substituted after verification"
			paths[0] = "unverified.txt"
		},
	}
	workspace, err := MaterializeWorkspaceContext(ctx, snap, snap.Evidence.Origin, snap.Evidence.SHA256, paths)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(workspace.Path(), "output.txt"))
	require.NoError(t, err)
	require.Equal(t, "actual output", string(content))
	_, err = os.Stat(filepath.Join(workspace.Path(), "unverified.txt"))
	require.True(t, os.IsNotExist(err))
	require.NoError(t, workspace.Close())
	workspace, err = MaterializeWorkspaceContext(t.Context(), snap, snap.Evidence.Origin, snap.Evidence.SHA256, []string{"output.txt"})
	require.Nil(t, workspace)
	require.Error(t, err, "a later call must observe caller mutation")
}
