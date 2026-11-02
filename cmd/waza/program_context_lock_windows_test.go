//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// lockUndeletableEntry creates dir containing a file whose removal fails:
// protected DACLs deny both deletion routes for the current SID. Unlike an
// open handle, these restrictions survive the grader subprocess exiting.
func lockUndeletableEntry(dir string) error {
	// Keep the effective thread identity consistent through diagnostics and probes.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

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
	expectedSD, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return errors.Join(err, unlockUndeletableEntry(dir))
	}
	expectedDACL := normalizeProgramContextDACL(expectedSD.String())
	if expectedDACL == "" {
		return errors.Join(errors.New("canonicalizing expected DACL: empty descriptor"), unlockUndeletableEntry(dir))
	}
	for _, path := range []string{dir, file} {
		if err := setProgramContextTestDACL(path, sddl); err != nil {
			return errors.Join(err, unlockUndeletableEntry(dir))
		}
	}
	// Compare only DACL information, canonicalized through the same Windows API.
	var diagnostics []string
	var verificationErrors []error
	for _, entry := range []struct{ name, path string }{{"dir", dir}, {"file", file}} {
		sd, err := windows.GetNamedSecurityInfo(entry.path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			verificationErrors = append(verificationErrors, fmt.Errorf("reading installed %s DACL: %w", entry.name, err))
			continue
		}
		if sd == nil {
			verificationErrors = append(verificationErrors, fmt.Errorf("reading installed %s DACL: nil descriptor", entry.name))
			continue
		}
		actualDACL := normalizeProgramContextDACL(sd.String())
		if actualDACL == "" {
			verificationErrors = append(verificationErrors, fmt.Errorf("canonicalizing installed %s DACL: empty descriptor", entry.name))
			continue
		}
		matches := actualDACL == expectedDACL
		diagnostics = append(diagnostics, fmt.Sprintf("%s_dacl_matches=%t", entry.name, matches))
		if !matches {
			verificationErrors = append(verificationErrors, fmt.Errorf("installed %s DACL matches expected=false", entry.name))
		}
	}
	if err := errors.Join(verificationErrors...); err != nil {
		// The runner could not install the deny ACEs as written, so deletion
		// cannot be denied here; report it as such so callers can skip.
		return errors.Join(fmt.Errorf("%w: %w", errDeletionNotDenied, err), unlockUndeletableEntry(dir))
	}
	sidMatches, impersonating, err := programContextEffectiveSIDMatches(sid)
	if err != nil {
		return errors.Join(err, unlockUndeletableEntry(dir))
	}
	diagnostics = append(diagnostics, fmt.Sprintf("effective_sid_matches=%t thread_impersonation_present=%t", sidMatches, impersonating))
	for _, access := range []struct {
		name  string
		path  string
		right uint32
		flags uint32
	}{
		{"file_delete_granted", file, windows.DELETE, 0},
		{"dir_delete_child_granted", dir, fileDeleteChild, windows.FILE_FLAG_BACKUP_SEMANTICS},
	} {
		path, err := windows.UTF16PtrFromString(access.path)
		if err != nil {
			return errors.Join(fmt.Errorf("encoding %s probe path: %w", access.name, err), unlockUndeletableEntry(dir))
		}
		handle, err := windows.CreateFile(path, access.right,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, access.flags, 0)
		if err != nil && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return errors.Join(fmt.Errorf("opening %s probe: %w", access.name, err), unlockUndeletableEntry(dir))
		}
		granted := err == nil
		if granted {
			if err := windows.CloseHandle(handle); err != nil {
				return errors.Join(fmt.Errorf("closing %s probe: %w", access.name, err), unlockUndeletableEntry(dir))
			}
		}
		diagnostics = append(diagnostics, fmt.Sprintf("%s=%t", access.name, granted))
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
			return errors.Join(fmt.Errorf("%w: %s for %s, got %v; before probes: %v", errDeletionNotDenied, probe.name, dir, err, diagnostics), unlockUndeletableEntry(dir))
		}
	}
	return nil
}

// The caller must remain on the same OS thread while using these diagnostics.
func programContextEffectiveSIDMatches(sid string) (matches, impersonating bool, err error) {
	var token windows.Token
	err = windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token)
	switch {
	case errors.Is(err, windows.ERROR_NO_TOKEN):
		token = windows.GetCurrentProcessToken()
	case err != nil:
		return false, false, fmt.Errorf("opening effective thread token: %w", err)
	default:
		impersonating = true
		defer func() {
			if closeErr := token.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("closing effective thread token: %w", closeErr))
			}
		}()
	}
	user, err := token.GetTokenUser()
	if err != nil {
		return false, impersonating, fmt.Errorf("querying effective token user: %w", err)
	}
	if user == nil || user.User.Sid == nil {
		return false, impersonating, errors.New("querying effective token user: nil user SID")
	}
	return user.User.Sid.String() == sid, impersonating, nil
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

// normalizeProgramContextDACL drops the "AI" (auto-inherited) DACL control
// flag, which Windows adds when a protected DACL is installed; the ACEs that
// decide deletion are compared unchanged.
func normalizeProgramContextDACL(sddl string) string {
	if rest, ok := strings.CutPrefix(sddl, "D:"); ok {
		end := strings.IndexByte(rest, '(')
		if end < 0 {
			end = len(rest)
		}
		return "D:" + strings.ReplaceAll(rest[:end], "AI", "") + rest[end:]
	}
	return sddl
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
