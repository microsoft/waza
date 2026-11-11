//go:build windows

package artifactmanifest

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func retainedRootRenameBlocked(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
