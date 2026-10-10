//go:build linux || darwin

package snapshot

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func writePrivateSnapshotWithHooks(root string, data []byte, hooks privateWriterHooks) (publishedPath string, err error) {
	if root == "" {
		return "", errors.New("snapshot: a private evidence directory is required")
	}
	root = filepath.Clean(root)
	if mkdirErr := os.MkdirAll(root, 0o700); mkdirErr != nil {
		return "", errors.New("snapshot: create private evidence directory failed")
	}
	if hooks.beforeAdmission != nil {
		hooks.beforeAdmission()
	}
	dir, err := openPrivateDirectory(root)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := dir.Close(); closeErr != nil {
			publishedPath = ""
			err = errors.Join(err, errors.New("snapshot: close private evidence directory failed"))
		}
	}()
	name := "evidence-" + rand.Text() + ".json"
	temporary := "." + name + ".tmp"
	fd, createErr := unix.Openat(int(dir.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if createErr != nil {
		return "", errors.New("snapshot: create private evidence file failed")
	}
	published := false
	defer func() {
		if removeErr := unix.Unlinkat(int(dir.Fd()), temporary, 0); removeErr != nil {
			publishedPath = ""
			err = errors.Join(err, errors.New("snapshot: remove incomplete private evidence failed"))
		}
		if published && err != nil {
			if removeErr := unix.Unlinkat(int(dir.Fd()), name, 0); removeErr != nil {
				err = errors.Join(err, errors.New("snapshot: remove unpublished private evidence failed"))
			}
		}
	}()
	file := os.NewFile(uintptr(fd), "private evidence")
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return "", errors.New("snapshot: write private evidence failed")
	}
	if hooks.beforePublication != nil {
		hooks.beforePublication()
	}
	if identityErr := checkPrivateDirectoryIdentity(root, dir); identityErr != nil {
		return "", identityErr
	}
	// Descriptor-relative linking is atomic and cannot replace an existing name.
	if linkErr := unix.Linkat(int(dir.Fd()), temporary, int(dir.Fd()), name, 0); linkErr != nil {
		return "", errors.New("snapshot: publish private evidence failed")
	}
	published = true
	if hooks.afterPublication != nil {
		hooks.afterPublication()
	}
	if identityErr := checkPrivateDirectoryIdentity(root, dir); identityErr != nil {
		return "", identityErr
	}
	return filepath.Join(root, name), nil
}

func openPrivateDirectory(root string) (*os.File, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("snapshot: open non-symlink private evidence directory failed")
	}
	dir := os.NewFile(uintptr(fd), "private evidence directory")
	info, err := dir.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		if closeErr := dir.Close(); closeErr != nil {
			return nil, errors.New("snapshot: inspect and close private evidence directory failed")
		}
		return nil, errors.New("snapshot: workspace evidence requires a non-symlink directory with mode 0700")
	}
	return dir, nil
}

func checkPrivateDirectoryIdentity(root string, admitted *os.File) (err error) {
	current, err := openPrivateDirectory(root)
	if err != nil {
		return errors.New("snapshot: private evidence directory identity changed during publication")
	}
	defer func() {
		if closeErr := current.Close(); closeErr != nil {
			err = errors.Join(err, errors.New("snapshot: close checked private evidence directory failed"))
		}
	}()
	before, beforeErr := admitted.Stat()
	after, afterErr := current.Stat()
	if beforeErr != nil || afterErr != nil || before.Mode().Perm() != 0o700 || after.Mode().Perm() != 0o700 || !os.SameFile(before, after) {
		return errors.New("snapshot: private evidence directory identity changed during publication")
	}
	return nil
}
