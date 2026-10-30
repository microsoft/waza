//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// lockUndeletableEntry creates dir containing a file whose removal fails:
// deny ACEs for the current user block deleting the file and its parent's
// child deletion.
func lockUndeletableEntry(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		return err
	}
	sid, err := currentUserSIDString()
	if err != nil {
		return err
	}
	if err := icacls(file, "/deny", "*"+sid+":(DE)"); err != nil {
		return err
	}
	return icacls(dir, "/deny", "*"+sid+":(DC)")
}

func unlockUndeletableEntry(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	sid, err := currentUserSIDString()
	if err != nil {
		return err
	}
	if err := icacls(dir, "/remove:d", "*"+sid); err != nil {
		return err
	}
	return icacls(filepath.Join(dir, "f"), "/remove:d", "*"+sid)
}

func undeletableEntriesEnforced() bool { return true }

func icacls(args ...string) error {
	if out, err := exec.Command("icacls", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("icacls %v: %w: %s", args, err, out)
	}
	return nil
}

func currentUserSIDString() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}
