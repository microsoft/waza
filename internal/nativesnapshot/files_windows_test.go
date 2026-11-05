//go:build windows

package nativesnapshot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSourceReadDirWindowsFailsClosed(t *testing.T) {
	files, _ := reader(t, context.Background())
	entries, err := files.ReadDir(".")
	require.ErrorContains(t, err, "unsupported native directory discovery on Windows")
	require.ErrorContains(t, err, "prepare native snapshots on a supported Unix platform")
	require.Nil(t, entries)
	require.Empty(t, files.dirs)
	require.Error(t, files.sticky, "native glob must not hide unsupported discovery")
}
