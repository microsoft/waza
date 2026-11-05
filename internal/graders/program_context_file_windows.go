//go:build windows

package graders

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows a Unix mode is not an access restriction and new entries inherit
// the TEMP directory's ACL, so the directory and file are created with a
// protected DACL that grants access only to the current user.

func mkdirPrivateTemp(pattern string) (string, error) {
	sa, err := ownerOnlySecurityAttributes(true)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(os.TempDir(), pattern+rand.Text())
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return "", err
	}
	if err := windows.CreateDirectory(p, sa); err != nil {
		return "", fmt.Errorf("%s: %w", dir, err)
	}
	return dir, nil
}

func createPrivateFile(path string) (*os.File, error) {
	sa, err := ownerOnlySecurityAttributes(false)
	if err != nil {
		return nil, err
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, windows.FILE_SHARE_READ, sa,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// ownerOnlySecurityAttributes returns a protected DACL granting full access
// only to the current user; inherit applies it to children of a directory.
func ownerOnlySecurityAttributes(inherit bool) (*windows.SecurityAttributes, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, fmt.Errorf("resolving current user for private grading context: %w", err)
	}
	flags := ""
	if inherit {
		flags = "OICI"
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;" + flags + ";FA;;;" + sid + ")")
	if err != nil {
		return nil, fmt.Errorf("building owner-only security descriptor: %w", err)
	}
	sa := &windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	return sa, nil
}

func currentUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

// holdEntry is a no-op on Windows: NTFS file IDs are not promptly reused, and
// an open handle could block the grader program's own use of the entry.
func holdEntry(string) (io.Closer, error) {
	return nil, nil
}
