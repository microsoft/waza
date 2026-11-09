//go:build windows

package graders

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func assertPrivateContextFile(t *testing.T, path string) {
	t.Helper()
	sid, err := currentUserSID()
	require.NoError(t, err)
	for _, tc := range []struct {
		path, want string
	}{
		{filepath.Dir(path), "D:P(A;OICI;FA;;;" + sid + ")"},
		{path, "D:P(A;;FA;;;" + sid + ")"},
	} {
		sd, err := windows.GetNamedSecurityInfo(tc.path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		require.NoError(t, err)
		require.Equal(t, canonicalContextDACL(t, tc.want), sd.String(), "%s must grant access only to the current user, without inherited ACEs", tc.path)
	}
}

func canonicalContextDACL(t *testing.T, sddl string) string {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	require.NoError(t, err)
	return sd.String()
}

func TestCanonicalContextDACL(t *testing.T) {
	// A well-known SID exercises abbreviation even on non-administrator hosts.
	want := canonicalContextDACL(t, "D:P(A;OICI;FA;;;S-1-5-32-544)")
	require.Equal(t, "D:P(A;OICI;FA;;;BA)", want)
	for _, tc := range []struct {
		name, sddl string
		equivalent bool
	}{
		{"abbreviated SID", "D:P(A;OICI;FA;;;BA)", true},
		{"extra ACE", "D:P(A;OICI;FA;;;BA)(A;;FR;;;WD)", false},
		{"unprotected DACL", "D:(A;OICI;FA;;;BA)", false},
		{"missing inheritance flags", "D:P(A;;FA;;;BA)", false},
		{"reduced access", "D:P(A;OICI;FR;;;BA)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.equivalent, want == canonicalContextDACL(t, tc.sddl))
		})
	}
}
