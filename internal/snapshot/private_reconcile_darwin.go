//go:build darwin

package snapshot

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/pathadmission"
	"golang.org/x/sys/unix"
)

// Darwin lacks Linux's descriptor-relative RENAME_EXCHANGE. Quarantine then
// remove only the byte-verified owned original rather than overwriting a file
// that may have appeared after validation.
func replacePrivateArtifact(root, publishedPath string, expected, _ []byte) error {
	name, err := privateArtifactName(root, publishedPath)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return errors.New("snapshot: resolve private evidence directory failed")
	}
	absolute, err = pathadmission.SystemRootAlias(absolute)
	if err != nil {
		return errors.New("snapshot: system root alias is unavailable or changed")
	}
	dir, err := openPrivateDirectory(absolute, false)
	if err != nil {
		return err
	}
	defer dir.Close() //nolint:errcheck
	if err := checkPrivatePublicationDirectoryIdentity(absolute, dir); err != nil {
		return err
	}
	if err := verifyPrivateArtifact(dir, name, expected); err != nil {
		return err
	}
	marker := "." + name + ".publication-failed"
	if err := createPrivateQuarantine(dir, marker); err != nil {
		return errors.New("snapshot: quarantine private artifact failed")
	}
	if err := verifyPrivateArtifact(dir, name, expected); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
		return errors.New("snapshot: remove verified private artifact failed")
	}
	return errors.New("snapshot: atomic private artifact reconciliation is unsupported; verified original removed")
}

func privateArtifactName(root, publishedPath string) (string, error) {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(publishedPath))
	if err != nil || filepath.Base(relative) != relative || relative == "." || filepath.Ext(relative) != ".json" {
		return "", errors.New("snapshot: private artifact reconciliation path is invalid")
	}
	return relative, nil
}

func verifyPrivateArtifact(dir *os.File, name string, expected []byte) error {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("snapshot: open private artifact for reconciliation failed")
	}
	file := os.NewFile(uintptr(fd), "private artifact reconciliation")
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != int64(len(expected)) {
		return errors.New("snapshot: private artifact is not the verified original")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
	if err != nil || !bytes.Equal(data, expected) {
		return errors.New("snapshot: private artifact is not the verified original")
	}
	return nil
}

func createPrivateQuarantine(dir *os.File, name string) error {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "private artifact quarantine")
	_, writeErr := file.Write([]byte("withheld\n"))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = unix.Unlinkat(int(dir.Fd()), name, 0)
		return errors.New("snapshot: write private artifact quarantine failed")
	}
	return nil
}
