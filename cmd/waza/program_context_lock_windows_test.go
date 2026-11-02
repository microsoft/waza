//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// lockUndeletableEntry creates dir containing a file whose removal fails:
// protected DACLs deny both deletion routes for the current SID. Unlike an
// open handle, these restrictions survive the grader subprocess exiting.
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
	const fileDeleteChild = 0x40 // FILE_DELETE_CHILD is not the SDDL "DC" right.
	sddl := fmt.Sprintf("D:P(D;;0x%x;;;%s)(A;;FA;;;%s)", windows.DELETE|fileDeleteChild, sid, sid)
	for _, path := range []string{dir, file} {
		if err := setProgramContextTestDACL(path, sddl); err != nil {
			return errors.Join(err, unlockUndeletableEntry(dir))
		}
	}
	// Exercise both Windows deletion paths, without consuming the leaf, before
	// trusting the fixture to make the production cleanup fail.
	for _, probe := range []struct {
		name   string
		remove func() error
	}{
		{"os.Remove(file)", func() error { return os.Remove(file) }},
		{"os.RemoveAll(dir)", func() error { return os.RemoveAll(dir) }},
	} {
		if err := probe.remove(); !errors.Is(err, os.ErrPermission) {
			return errors.Join(fmt.Errorf("%w: %s for %s, got %v", errDeletionNotDenied, probe.name, dir, err), unlockUndeletableEntry(dir))
		}
	}
	return nil
}

func unlockUndeletableEntry(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	sid, err := currentUserSIDString()
	if err != nil {
		return err
	}
	var errs []error
	for _, path := range []string{dir, filepath.Join(dir, "f")} {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}
		errs = append(errs, setProgramContextTestDACL(path, "D:P(A;;FA;;;"+sid+")"))
	}
	return errors.Join(errs...)
}

func setProgramContextTestDACL(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

func currentUserSIDString() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func TestProgramContextUndeletableEntry(t *testing.T) {
	requireUndeletableEntries(t)
	dir := filepath.Join(t.TempDir(), "locked")
	t.Cleanup(func() {
		require.NoError(t, unlockUndeletableEntry(dir))
	})
	require.NoError(t, lockUndeletableEntry(dir))
	require.FileExists(t, filepath.Join(dir, "f"))
	require.ErrorIs(t, os.RemoveAll(dir), os.ErrPermission)
	require.FileExists(t, filepath.Join(dir, "f"))
	require.NoError(t, unlockUndeletableEntry(dir))
	require.NoError(t, os.RemoveAll(dir))
	require.NoDirExists(t, dir)
}
