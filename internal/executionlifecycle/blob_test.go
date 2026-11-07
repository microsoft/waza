package executionlifecycle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/stretchr/testify/require"
)

func TestActualLocalBlobRoundTripAndOriginalPreservation(t *testing.T) {
	directory := t.TempDir()
	local, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, local.Close()) })
	reservation, _, binding := source(t)
	p, err := New(reservation, binding, "")
	require.NoError(t, err)
	first := event(p, "started", origin)
	blobs := NewBlobs(local)
	digest, err := blobs.Put(t.Context(), first)
	require.NoError(t, err)
	canonical, err := Encode(first)
	require.NoError(t, err)
	prior, err := os.ReadFile(filepath.Join(directory, "blob-"+digest))
	require.NoError(t, err)
	require.Equal(t, canonical, prior)
	loaded, err := blobs.Load(t.Context(), digest)
	require.NoError(t, err)
	require.NoError(t, p.Apply(*loaded))
	same, err := blobs.Put(t.Context(), first)
	require.NoError(t, err)
	require.Equal(t, digest, same)
	second := event(p, "heartbeat", origin)
	other, err := blobs.Put(t.Context(), second)
	require.NoError(t, err)
	require.NotEqual(t, digest, other)
	retained, err := os.ReadFile(filepath.Join(directory, "blob-"+digest))
	require.NoError(t, err)
	require.Equal(t, prior, retained)
	require.Len(t, view(t, p, "").Observations, 1, "publication alone does not mutate projection")
	_, err = blobs.Load(t.Context(), strings.Repeat("b", 64))
	require.ErrorIs(t, err, artifactmanifest.ErrMissing)
}

type faultyBackend struct {
	data []byte
	err  error
	gets int
}

func (b *faultyBackend) Create(context.Context, string, []byte) (bool, error) {
	return false, b.err
}

func (b *faultyBackend) Get(context.Context, string, int64) ([]byte, error) {
	b.gets++
	return b.data, b.err
}

func TestBlobFailuresRemainFailures(t *testing.T) {
	reservation, _, binding := source(t)
	p, err := New(reservation, binding, "")
	require.NoError(t, err)
	first := event(p, "started", origin)
	data, err := Encode(first)
	require.NoError(t, err)
	sentinel := errors.New("controlled provider error")
	backend := &faultyBackend{data: data, err: sentinel}
	blobs := NewBlobs(backend)
	digest, err := blobs.Put(t.Context(), first)
	require.ErrorIs(t, err, sentinel)
	require.Empty(t, digest)
	_, err = blobs.Load(t.Context(), artifactmanifest.Hash(data))
	require.ErrorIs(t, err, sentinel)
	backend.err = nil
	backend.data = append(bytes.Clone(data), '\n')
	_, err = blobs.Load(t.Context(), artifactmanifest.Hash(backend.data))
	require.ErrorIs(t, err, artifactmanifest.ErrCorrupt, "matching digest cannot admit noncanonical content")
	backend.data = data
	_, err = blobs.Load(t.Context(), strings.Repeat("b", 64))
	require.ErrorIs(t, err, artifactmanifest.ErrCorrupt)
	backend.data = []byte("{}")
	_, err = blobs.Load(t.Context(), artifactmanifest.Hash(backend.data))
	require.ErrorIs(t, err, artifactmanifest.ErrCorrupt, "stored malformed data is corruption, not invalid caller input")
	backend.data = bytes.Repeat([]byte("x"), MaxBytes+1)
	_, err = blobs.Load(t.Context(), artifactmanifest.Hash(backend.data))
	require.ErrorIs(t, err, artifactmanifest.ErrCorrupt)
	before := backend.gets
	for _, invalid := range []string{"", "../object", strings.Repeat("A", 64), strings.Repeat("a", 63)} {
		_, err = blobs.Load(t.Context(), invalid)
		require.ErrorIs(t, err, ErrInvalid)
	}
	require.Equal(t, before, backend.gets)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = blobs.Load(ctx, artifactmanifest.Hash(data))
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, before, backend.gets)
	_, err = NewBlobs(nil).Load(t.Context(), artifactmanifest.Hash(data))
	require.Error(t, err)
	_, err = NewBlobs(nil).Put(t.Context(), first)
	require.Error(t, err)
}
