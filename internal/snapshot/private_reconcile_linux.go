//go:build linux

package snapshot

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/pathadmission"
	"golang.org/x/sys/unix"
)

func replacePrivateArtifact(root, publishedPath string, expected, replacement []byte) (err error) {
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
	defer func() { err = errors.Join(err, dir.Close()) }()
	if err := checkPrivatePublicationDirectoryIdentity(absolute, dir); err != nil {
		return err
	}
	if err := verifyPrivateArtifact(dir, name, expected); err != nil {
		return err
	}
	quarantine := "." + name + ".publication-failed"
	if err := createPrivateQuarantine(dir, quarantine); err != nil {
		if removeErr := removeVerifiedPrivateArtifact(dir, name, expected); removeErr != nil {
			return errors.Join(errors.New("snapshot: quarantine private artifact failed"), removeErr)
		}
		return errors.New("snapshot: quarantine private artifact failed; verified original removed")
	}
	quarantined := true
	defer func() {
		if quarantined && unix.Unlinkat(int(dir.Fd()), quarantine, 0) != nil {
			err = errors.Join(err, errors.New("snapshot: retain private artifact quarantine failed"))
		}
	}()
	temporary := ".reconcile-" + rand.Text() + ".tmp"
	fd, err := unix.Openat(int(dir.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return errors.New("snapshot: create private reconciliation file failed")
	}
	file := os.NewFile(uintptr(fd), "private reconciliation")
	written, writeErr := file.Write(replacement)
	closeErr := file.Close()
	if writeErr != nil || written != len(replacement) || closeErr != nil {
		_ = unix.Unlinkat(int(dir.Fd()), temporary, 0)
		return errors.New("snapshot: write private reconciliation failed")
	}
	staged := true
	defer func() {
		if staged && unix.Unlinkat(int(dir.Fd()), temporary, 0) != nil {
			err = errors.Join(err, errors.New("snapshot: remove private reconciliation staging failed"))
		}
	}()
	if err := checkPrivatePublicationDirectoryIdentity(absolute, dir); err != nil {
		return err
	}
	if err := verifyPrivateArtifact(dir, name, expected); err != nil {
		return err
	}
	// Exchange leaves a competing target in staging.  We only remove staging
	// after proving it is our original, so a racing user file is never deleted.
	if err := unix.Renameat2(int(dir.Fd()), temporary, int(dir.Fd()), name, unix.RENAME_EXCHANGE); err != nil {
		if removeErr := removeVerifiedPrivateArtifact(dir, name, expected); removeErr != nil {
			return errors.Join(errors.New("snapshot: atomically replace private artifact failed"), removeErr)
		}
		return errors.New("snapshot: atomically replace private artifact failed; verified original removed")
	}
	staged = false
	if err := verifyPrivateArtifact(dir, temporary, expected); err != nil {
		// The exchange did not leave our original in staging. Do not remove it.
		return errors.Join(errors.New("snapshot: competing private artifact detected during reconciliation"), err)
	}
	if err := unix.Unlinkat(int(dir.Fd()), temporary, 0); err != nil {
		return errors.New("snapshot: remove replaced private artifact failed")
	}
	if err := unix.Unlinkat(int(dir.Fd()), quarantine, 0); err != nil {
		return errors.New("snapshot: remove private artifact quarantine failed")
	}
	quarantined = false
	return checkPrivatePublicationDirectoryIdentity(absolute, dir)
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

func removeVerifiedPrivateArtifact(dir *os.File, name string, expected []byte) error {
	if err := verifyPrivateArtifact(dir, name, expected); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
		return errors.New("snapshot: remove verified private artifact failed")
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
