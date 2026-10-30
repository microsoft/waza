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
		require.Equal(t, tc.want, sd.String(), "%s must grant access only to the current user, without inherited ACEs", tc.path)
	}
}
