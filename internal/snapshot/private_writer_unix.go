//go:build linux || darwin

package snapshot

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
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
			// A verified publication or residual remains locatable even if the
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
	suffix := hooks.suffix
	if suffix == "" {
		suffix = ".json"
	}
	name := "evidence-" + rand.Text() + suffix
	temporary := "." + name + ".tmp"
	fd, createErr := unix.Openat(int(dir.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if createErr != nil {
		return "", errors.New("snapshot: create private evidence file failed")
	}
	published := false
	var original os.FileInfo
	ops := privateResidualOperations()
	if hooks.residualOps != nil {
		ops = *hooks.residualOps
	}
	unlink := unix.Unlinkat
	if hooks.unlink != nil {
		unlink = hooks.unlink
	}
	defer func() {
		tempFailed := unlink(int(dir.Fd()), temporary, 0) != nil
		if tempFailed {
			err = errors.Join(err, errors.New("snapshot: remove incomplete private evidence failed"))
		}
		finalFailed := false
		if published && err != nil {
			if finalFailed = unlink(int(dir.Fd()), name, 0) != nil; finalFailed {
				err = errors.Join(err, errors.New("snapshot: remove unpublished private evidence failed"))
			}
		}
		if !tempFailed && !finalFailed {
			return
		}
		publishedPath = ""
		if tempFailed && finalFailed {
			err = errors.Join(err, errors.New("snapshot: additional private evidence residue may remain; only one verified location can be returned"))
		}
		for _, candidate := range []struct {
			name   string
			failed bool
		}{{temporary, tempFailed}, {name, finalFailed}} {
			if !candidate.failed {
				continue
			}
			valid, verifyErr := verifyPrivateResidual(absolute, dir, candidate.name, original, data, ops)
			if verifyErr != nil {
				err = errors.Join(err, verifyErr)
				break
			}
			if valid {
				publishedPath = filepath.Join(root, candidate.name)
				return
			}
		}
		err = errors.Join(err, errors.New("snapshot: remaining private evidence location is unavailable; inspect the selected destination"))
	}()
	file := os.NewFile(uintptr(fd), "private evidence")
	original, statErr := ops.stat(file)
	if statErr != nil {
		problem := errors.New("snapshot: inspect owned private evidence failed")
		if closeErr := file.Close(); closeErr != nil {
			problem = errors.Join(problem, errors.New("snapshot: close private evidence file failed"))
		}
		return "", problem
	}
	write := (*os.File).Write
	if hooks.write != nil {
		write = hooks.write
	}
	written, writeErr := write(file, data)
	closeErr := file.Close()
	if writeErr != nil || written != len(data) || closeErr != nil {
		problem := errors.New("snapshot: write private evidence failed")
		if closeErr != nil {
			problem = errors.Join(problem, errors.New("snapshot: close private evidence file failed"))
		}
		return "", problem
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

func privateResidualOperations() privateResidualOps {
	return privateResidualOps{
		openat: unix.Openat, stat: (*os.File).Stat, close: (*os.File).Close,
		read: func(file *os.File, limit int64) ([]byte, error) {
			return io.ReadAll(io.LimitReader(file, limit))
		},
	}
}

func verifyPrivateResidual(absolute string, dir *os.File, name string, original os.FileInfo, expected []byte, ops privateResidualOps) (valid bool, err error) {
	if original == nil {
		return false, errors.New("snapshot: original private evidence identity is unavailable")
	}
	if err := checkPrivatePublicationDirectoryIdentity(absolute, dir); err != nil {
		return false, err
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	fd, openErr := ops.openat(int(dir.Fd()), name, flags, 0)
	if openErr != nil {
		if errors.Is(openErr, unix.ENOENT) || errors.Is(openErr, unix.ELOOP) {
			return false, nil
		}
		return false, errors.New("snapshot: open residual private evidence failed")
	}
	file := os.NewFile(uintptr(fd), "residual private evidence")
	defer func() {
		if file != nil {
			if closeErr := ops.close(file); closeErr != nil {
				valid = false
				err = errors.Join(err, errors.New("snapshot: close residual private evidence failed"))
			}
		}
	}()
	before, statErr := ops.stat(file)
	if statErr != nil {
		return false, errors.New("snapshot: inspect residual private evidence failed")
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 ||
		!os.SameFile(original, before) || before.Size() != int64(len(expected)) {
		return false, nil
	}
	content, readErr := ops.read(file, int64(len(expected))+1)
	if readErr != nil {
		return false, errors.New("snapshot: read residual private evidence failed")
	}
	after, statErr := ops.stat(file)
	if statErr != nil {
		return false, errors.New("snapshot: inspect residual private evidence failed")
	}
	if !bytes.Equal(content, expected) || !sameObservedFile(before, after) {
		return false, nil
	}
	closeErr := ops.close(file)
	file = nil
	if closeErr != nil {
		return false, errors.New("snapshot: close residual private evidence failed")
	}
	nameFD, openErr := ops.openat(int(dir.Fd()), name, flags, 0)
	if openErr != nil {
		if errors.Is(openErr, unix.ENOENT) || errors.Is(openErr, unix.ELOOP) {
			return false, nil
		}
		return false, errors.New("snapshot: recheck residual private evidence name failed")
	}
	file = os.NewFile(uintptr(nameFD), "residual private evidence name")
	info, statErr := ops.stat(file)
	if statErr != nil {
		return false, errors.New("snapshot: inspect residual private evidence name failed")
	}
	if !sameObservedFile(after, info) || !os.SameFile(original, info) {
		return false, nil
	}
	closeErr = ops.close(file)
	file = nil
	if closeErr != nil {
		return false, errors.New("snapshot: close residual private evidence name failed")
	}
	if err := checkPrivatePublicationDirectoryIdentity(absolute, dir); err != nil {
		return false, err
	}
	return true, nil
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
