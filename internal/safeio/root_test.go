// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadRegularFile_RejectsFileGrowthBeyondLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "growing.txt")
	require.NoError(t, os.WriteFile(path, []byte("1234"), 0o644))

	root, err := OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	grow := make(chan struct{})
	grown := make(chan error, 1)
	go func() {
		<-grow
		file, openErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if openErr != nil {
			grown <- openErr
			return
		}
		_, writeErr := file.WriteString("56789")
		if closeErr := file.Close(); writeErr == nil {
			writeErr = closeErr
		}
		grown <- writeErr
	}()

	_, _, err = root.readRegularFile("growing.txt", 4, func() {
		close(grow)
		require.NoError(t, <-grown)
	})

	require.ErrorIs(t, err, ErrFileTooLarge)
	require.ErrorContains(t, err, "grew beyond limit 4")
	require.True(t, errors.Is(err, ErrFileTooLarge))
}
