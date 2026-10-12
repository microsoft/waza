//go:build linux || darwin

package snapshot

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/pathadmission"
	"golang.org/x/sys/unix"
)

func writePrivateSnapshotWithHooks(root string, data []byte, hooks privateWriterHooks) (publishedPath string, err error) {
	if root == "" {
		return "", errors.New("snapshot: a private evidence directory is required")
	}
	root = filepath.Clean(root)
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", errors.New("snapshot: resolve private evidence directory failed")
	}
	absolute, err = pathadmission.SystemRootAlias(absolute)
	if err != nil {
		return "", errors.New("snapshot: system root alias is unavailable or changed")
	}
	dir, err := openPrivateDirectory(absolute, true)
	if err != nil {
		return "", err
	}
	defer func() {
		closeDirectory := dir.Close
		if hooks.closeDirectory != nil {
			closeDirectory = func() error { return hooks.closeDirectory(dir) }
		}
		if closeErr := closeDirectory(); closeErr != nil {
			// A successfully verified publication remains locatable even if the
			// final descriptor close fails after publication cleanup.
			err = errors.Join(err, errors.New("snapshot: close private evidence directory failed"))
		}
	}()
	if hooks.beforeAdmission != nil {
		hooks.beforeAdmission()
	}
	if identityErr := checkPrivatePublicationDirectoryIdentity(absolute, dir); identityErr != nil {
		return "", identityErr
	}
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
	if identityErr := checkPrivatePublicationDirectoryIdentity(absolute, dir); identityErr != nil {
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
	if identityErr := checkPrivatePublicationDirectoryIdentity(absolute, dir); identityErr != nil {
		return "", identityErr
	}
	return filepath.Join(root, name), nil
}

type privateDirectoryOps struct {
	open    func(string, int, uint32) (int, error)
	openat  func(int, string, int, uint32) (int, error)
	mkdirat func(int, string, uint32) error
	stat    func(*os.File) (os.FileInfo, error)
	close   func(*os.File) error
}

func privateDirectoryOperations() privateDirectoryOps {
	return privateDirectoryOps{
		open: unix.Open, openat: unix.Openat, mkdirat: unix.Mkdirat,
		stat: (*os.File).Stat, close: (*os.File).Close,
	}
}

func openPrivateDirectory(absolute string, create bool) (*os.File, error) {
	return openPrivateDirectoryWithOps(absolute, create, privateDirectoryOperations())
}

func openPrivateDirectoryWithOps(absolute string, create bool, ops privateDirectoryOps) (directory *os.File, err error) {
	fd, err := ops.open("/", directoryOpenFlags(), 0)
	if err != nil {
		return nil, errors.New("snapshot: open private evidence anchor failed")
	}
	current := os.NewFile(uintptr(fd), "private evidence anchor")
	defer func() {
		if current != nil {
			if closeErr := ops.close(current); closeErr != nil {
				err = errors.Join(err, errors.New("snapshot: close private evidence ancestor failed"))
			}
		}
	}()
	for _, part := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		if part == "" {
			continue
		}
		childFD, openErr := ops.openat(int(current.Fd()), part, directoryOpenFlags(), 0)
		if create && errors.Is(openErr, unix.ENOENT) {
			if mkdirErr := ops.mkdirat(int(current.Fd()), part, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				return nil, errors.New("snapshot: create private evidence ancestor failed")
			}
			childFD, openErr = ops.openat(int(current.Fd()), part, directoryOpenFlags(), 0)
		}
		if openErr != nil {
			return nil, errors.New("snapshot: private evidence ancestor is unavailable or not a non-symlink directory")
		}
		child := os.NewFile(uintptr(childFD), "private evidence directory")
		closeErr := ops.close(current)
		current = nil
		if closeErr != nil {
			problem := errors.New("snapshot: close private evidence ancestor failed")
			if childCloseErr := ops.close(child); childCloseErr != nil {
				return nil, errors.Join(problem, errors.New("snapshot: close admitted private evidence directory failed"))
			}
			return nil, problem
		}
		current = child
	}
	info, err := ops.stat(current)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, errors.New("snapshot: workspace evidence requires a non-symlink directory with mode 0700")
	}
	directory, current = current, nil
	return directory, nil
}

func checkPrivatePublicationDirectoryIdentity(root string, admitted *os.File) error {
	current, err := openPrivateDirectory(root, false)
	if err != nil {
		return errors.Join(errors.New("snapshot: private evidence directory identity changed during publication"), err)
	}
	return checkRetainedPrivateDirectoryIdentity(current, admitted)
}

// Materialization retains its existing leaf-only directory check.
func checkPrivateDirectoryIdentity(root string, admitted *os.File) error {
	fd, err := unix.Open(root, directoryOpenFlags(), 0)
	if err != nil {
		return errors.New("snapshot: private evidence directory identity changed during publication")
	}
	return checkRetainedPrivateDirectoryIdentity(os.NewFile(uintptr(fd), "private evidence directory"), admitted)
}

func checkRetainedPrivateDirectoryIdentity(current, admitted *os.File) (err error) {
	defer func() {
		if closeErr := current.Close(); closeErr != nil {
			err = errors.Join(err, errors.New("snapshot: close checked private evidence directory failed"))
		}
	}()
	before, beforeErr := admitted.Stat()
	after, afterErr := current.Stat()
	if beforeErr != nil || afterErr != nil || !after.IsDir() || before.Mode().Perm() != 0o700 || after.Mode().Perm() != 0o700 || !os.SameFile(before, after) {
		return errors.New("snapshot: private evidence directory identity changed during publication")
	}
	return nil
}
