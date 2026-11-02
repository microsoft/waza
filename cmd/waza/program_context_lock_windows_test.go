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
	"unsafe"

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
	expectedShape, err := programContextDACLDiagnostics(expectedSD, sid)
	if err != nil {
		return errors.Join(fmt.Errorf("describing expected DACL: %w", err), unlockUndeletableEntry(dir))
	}
	diagnostics = append(diagnostics, "expected_dacl="+expectedShape)
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
		shape, err := programContextDACLDiagnostics(sd, sid)
		if err != nil {
			verificationErrors = append(verificationErrors, fmt.Errorf("describing installed %s DACL: %w", entry.name, err))
		} else {
			diagnostics = append(diagnostics, entry.name+"_dacl="+shape)
		}
		if !matches {
			verificationErrors = append(verificationErrors, fmt.Errorf("installed %s DACL matches expected=false", entry.name))
		}
	}
	// A readback mismatch must not hide independent identity or access evidence.
	sidMatches, impersonating, err := programContextEffectiveSIDMatches(sid)
	if err != nil {
		verificationErrors = append(verificationErrors, err)
	} else {
		diagnostics = append(diagnostics, fmt.Sprintf("effective_sid_matches=%t thread_impersonation_present=%t", sidMatches, impersonating))
		if !sidMatches {
			verificationErrors = append(verificationErrors, errors.New("effective SID does not match fixture principal"))
		}
	}
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
			verificationErrors = append(verificationErrors, fmt.Errorf("encoding %s probe path: %w", access.name, err))
			continue
		}
		handle, err := windows.CreateFile(path, access.right,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			nil, windows.OPEN_EXISTING, access.flags, 0)
		if err != nil && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			verificationErrors = append(verificationErrors, fmt.Errorf("opening %s probe: %w", access.name, err))
			continue
		}
		granted := err == nil
		if granted {
			verificationErrors = append(verificationErrors, fmt.Errorf("%s unexpectedly granted", access.name))
			if err := windows.CloseHandle(handle); err != nil {
				verificationErrors = append(verificationErrors, fmt.Errorf("closing %s probe: %w", access.name, err))
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
		err := probe.remove()
		diagnostics = append(diagnostics, fmt.Sprintf("%s_denied=%t", probe.name, errors.Is(err, os.ErrPermission)))
		if !errors.Is(err, os.ErrPermission) {
			verificationErrors = append(verificationErrors, fmt.Errorf("expected deletion denial from %s for %s, got %v", probe.name, dir, err))
		}
	}
	if err := errors.Join(verificationErrors...); err != nil {
		return errors.Join(fmt.Errorf("%w; fixture diagnostics: %v", err, diagnostics), unlockUndeletableEntry(dir))
	}
	return nil
}

// Describe only control bits and ACE structure, never account names or SID text.
func programContextDACLDiagnostics(sd *windows.SECURITY_DESCRIPTOR, sid string) (string, error) {
	currentSID, err := windows.StringToSid(sid)
	if err != nil {
		return "", err
	}
	control, _, err := sd.Control()
	if err != nil {
		return "", err
	}
	dacl, defaulted, err := sd.DACL()
	if err != nil {
		return "", err
	}
	parts := []string{fmt.Sprintf("control=0x%x protected=%t auto_inherited=%t auto_inherit_requested=%t defaulted=%t null=%t",
		control, control&windows.SE_DACL_PROTECTED != 0,
		control&windows.SE_DACL_AUTO_INHERITED != 0,
		control&windows.SE_DACL_AUTO_INHERIT_REQ != 0, defaulted, dacl == nil)}
	if dacl == nil {
		return strings.Join(parts, " "), nil
	}
	parts = append(parts, fmt.Sprintf("ace_count=%d", dacl.AceCount))
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return "", fmt.Errorf("reading ACE %d: %w", i, err)
		}
		header := fmt.Sprintf("ace[%d]={type=%d flags=0x%x", i, ace.Header.AceType, ace.Header.AceFlags)
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
			parts = append(parts, header+" principal=REDACTED}")
			continue
		}
		// Basic allow/deny ACEs share the mask and inline SID layout.
		if uintptr(ace.Header.AceSize) < unsafe.Offsetof(ace.SidStart)+8 {
			return "", fmt.Errorf("ACE %d is too short for a SID", i)
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		matches := aceSID.Equals(currentSID)
		principal := "REDACTED"
		if matches {
			principal = "CURRENT"
		}
		parts = append(parts, fmt.Sprintf("%s deny=%t mask=0x%x principal=%s principal_matches=%t}",
			header, ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE, ace.Mask, principal, matches))
	}
	return strings.Join(parts, " "), nil
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
// flag, which Windows may add when a protected DACL is installed. Ignore only
// that bookkeeping bit; protection, inheritance requests and ACEs remain exact.
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

func TestNormalizeProgramContextDACL(t *testing.T) {
	const aces = "(D;;0x10040;;;S-1-5-21-1-2-3-500)(A;;FA;;;S-1-5-21-1-2-3-500)"
	for _, tc := range []struct{ name, input, want string }{
		{"auto inherited", "D:PAI" + aces, "D:P" + aces},
		{"already exact", "D:P" + aces, "D:P" + aces},
		{"inheritance request", "D:PARAI" + aces, "D:PAR" + aces},
		{"empty DACL", "D:PAI", "D:P"},
		{"ACE flags unchanged", "D:PAI(A;CIID;FR;;;BA)", "D:P(A;CIID;FR;;;BA)"},
		{"non DACL prefix", "O:AID:PAI" + aces, "O:AID:PAI" + aces},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, normalizeProgramContextDACL(tc.input))
		})
	}
	for _, nearMiss := range []struct{ name, input string }{
		{"missing protection", "D:AI" + aces},
		{"inheritance requested", "D:PARAI" + aces},
		{"ACE inheritance flags", "D:PAI(D;CI;0x10040;;;S-1-5-21-1-2-3-500)(A;;FA;;;S-1-5-21-1-2-3-500)"},
		{"different deny right", "D:PAI(D;;0x10000;;;S-1-5-21-1-2-3-500)(A;;FA;;;S-1-5-21-1-2-3-500)"},
		{"different principal", "D:PAI(D;;0x10040;;;BA)(A;;FA;;;S-1-5-21-1-2-3-500)"},
		{"different ACE order", "D:PAI(A;;FA;;;S-1-5-21-1-2-3-500)(D;;0x10040;;;S-1-5-21-1-2-3-500)"},
	} {
		t.Run(nearMiss.name, func(t *testing.T) {
			require.NotEqual(t, normalizeProgramContextDACL("D:P"+aces), normalizeProgramContextDACL(nearMiss.input))
		})
	}
}

func TestProgramContextDACLDiagnostics(t *testing.T) {
	const sid = "S-1-5-21-1-2-3-500"
	for _, tc := range []struct {
		name, sddl string
		want       []string
	}{
		{"expected", "D:P(D;;0x10040;;;" + sid + ")(A;;FA;;;" + sid + ")", []string{
			"protected=true", "auto_inherited=false", "ace_count=2",
			"ace[0]={type=1 flags=0x0 deny=true mask=0x10040 principal=CURRENT principal_matches=true}",
			"ace[1]={type=0 flags=0x0 deny=false mask=0x1f01ff principal=CURRENT principal_matches=true}",
		}},
		{"other principal", "D:P(A;;FA;;;S-1-5-32-544)", []string{
			"ace_count=1", "principal=REDACTED principal_matches=false",
		}},
		{"inheritance controls", "D:PAI(A;OICI;FR;;;" + sid + ")", []string{
			"protected=true", "auto_inherited=true", "flags=0x3", "mask=0x120089",
		}},
		{"object ACE", "D:P(OA;;FR;00000000-0000-0000-0000-000000000001;;" + sid + ")", []string{
			"ace_count=1", "ace[0]={type=5 flags=0x0 principal=REDACTED}",
		}},
		{"empty DACL", "D:P", []string{"null=false", "ace_count=0"}},
		{"null DACL", "D:NO_ACCESS_CONTROL", []string{"null=true"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(tc.sddl)
			require.NoError(t, err)
			got, err := programContextDACLDiagnostics(sd, sid)
			require.NoError(t, err)
			for _, want := range tc.want {
				require.Contains(t, got, want)
			}
			require.NotContains(t, got, "S-1-")
			require.NotContains(t, got, "BA")
			require.NotContains(t, got, "LA")
		})
	}
	t.Run("absent DACL", func(t *testing.T) {
		sd, err := windows.SecurityDescriptorFromString("O:SY")
		require.NoError(t, err)
		got, err := programContextDACLDiagnostics(sd, sid)
		require.ErrorIs(t, err, windows.ERROR_OBJECT_NOT_FOUND)
		require.Empty(t, got)
	})
	t.Run("invalid current SID", func(t *testing.T) {
		sd, err := windows.SecurityDescriptorFromString("D:P")
		require.NoError(t, err)
		got, err := programContextDACLDiagnostics(sd, "not-a-SID")
		require.Error(t, err)
		require.Empty(t, got)
	})
}
