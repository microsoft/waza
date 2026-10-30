//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

// lockUndeletableEntry creates dir containing a file whose removal fails for
// a non-root owner because dir is not writable.
func lockUndeletableEntry(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		return err
	}
	return os.Chmod(dir, 0o500)
}

func unlockUndeletableEntry(dir string) error {
	err := os.Chmod(dir, 0o700)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func undeletableEntriesEnforced() bool { return os.Geteuid() != 0 }
