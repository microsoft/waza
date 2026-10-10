//go:build linux || darwin

package assurance

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func qualificationDirectName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00") && len(name) < 256
}
func qualificationSecureOpen(ctx context.Context, root *os.Root, name string, flags int) (*os.File, error) {
	if root == nil || !qualificationDirectName(name) {
		return nil, errors.New("qualification: borrowed root/direct filename required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	fd, openErr := unix.Openat(int(directory.Fd()), name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	closeErr := directory.Close()
	if openErr != nil {
		return nil, errors.Join(openErr, closeErr)
	}
	file := os.NewFile(uintptr(fd), name)
	info, statErr := file.Stat()
	if closeErr != nil || statErr != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(errors.New("qualification: no-follow regular file required"), closeErr, statErr, file.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func qualificationSecureRead(ctx context.Context, root *os.Root, name string, limit uint64) ([]byte, error) {
	if limit > qualificationEncodedLimit {
		return nil, errors.New("qualification: explicit bounded read limit")
	}
	file, err := qualificationSecureOpen(ctx, root, name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	if statErr != nil || info.Size() < 0 || uint64(info.Size()) > limit {
		return nil, errors.Join(errors.New("qualification: regular file byte limit"), statErr, file.Close())
	}
	var buffer strings.Builder
	chunk := make([]byte, 64*1024)
	reader := io.LimitReader(file, int64(limit)+1)
	for {
		if err = ctx.Err(); err != nil {
			break
		}
		n, readErr := reader.Read(chunk)
		buffer.Write(chunk[:n])
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				err = readErr
			}
			break
		}
	}
	after, afterErr := file.Stat()
	pathInfo, pathErr := root.Lstat(name)
	closeErr := file.Close()
	if err != nil || afterErr != nil || pathErr != nil || closeErr != nil {
		return nil, errors.Join(err, afterErr, pathErr, closeErr)
	}
	if !os.SameFile(info, pathInfo) || !os.SameFile(info, after) || info.Size() != after.Size() ||
		!info.ModTime().Equal(after.ModTime()) || uint64(buffer.Len()) > limit {
		return nil, errors.New("qualification: source replaced/changed or oversized while reading")
	}
	return []byte(buffer.String()), ctx.Err()
}

func (qualificationOSJournalIO) ReadDirect(ctx context.Context, root *os.Root, name string, limit uint64) ([]byte, error) {
	return qualificationSecureRead(ctx, root, name, limit)
}
func (qualificationOSJournalIO) WriteExclusive(ctx context.Context, root *os.Root, name string, data []byte) error {
	if len(data) > qualificationDocumentLimit {
		return errors.New("qualification: journal projection byte limit")
	}
	file, err := qualificationSecureOpen(ctx, root, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, ctx.Err(), file.Close())
}
func (qualificationOSJournalIO) SyncFile(ctx context.Context, root *os.Root, name string) error {
	file, err := qualificationSecureOpen(ctx, root, name, unix.O_RDONLY)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), ctx.Err(), file.Close())
}
func (qualificationOSJournalIO) SyncDirectory(ctx context.Context, root *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), ctx.Err(), file.Close())
}
func (io qualificationOSJournalIO) CreateDirectDirectory(ctx context.Context, root *os.Root, name string) (*os.Root, error) {
	if root == nil || !qualificationDirectName(name) {
		return nil, errors.New("qualification: direct registry directory required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := root.Mkdir(name, 0o700); err != nil {
		return nil, err
	}
	return io.OpenDirectDirectory(ctx, root, name)
}
func (qualificationOSJournalIO) OpenDirectDirectory(ctx context.Context, root *os.Root, name string) (*os.Root, error) {
	if root == nil || !qualificationDirectName(name) {
		return nil, errors.New("qualification: direct registry directory required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before, err := root.Lstat(name)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("qualification: real directory required")
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	actual, statErr := child.Stat(".")
	after, pathErr := root.Lstat(name)
	if statErr != nil || pathErr != nil || !os.SameFile(before, actual) || !os.SameFile(before, after) || after.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(errors.New("qualification: directory replaced"), statErr, pathErr, child.Close())
	}
	return child, nil
}

type qualificationUnixLock struct{ file *os.File }

func (lock *qualificationUnixLock) Unlock() error {
	return errors.Join(unix.Flock(int(lock.file.Fd()), unix.LOCK_UN), lock.file.Close())
}
func (qualificationOSJournalIO) Lock(ctx context.Context, root *os.Root) (qualificationRegistryLock, error) {
	flags := unix.O_RDWR
	if ctx.Value(qualificationReadLockKey{}) == nil {
		flags |= unix.O_CREAT
	}
	file, err := qualificationSecureOpen(ctx, root, "registry.lock", flags)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, file.Close())
		}
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			opened, statErr := file.Stat()
			current, pathErr := root.Lstat("registry.lock")
			if statErr != nil || pathErr != nil || !os.SameFile(opened, current) || current.Mode()&os.ModeSymlink != 0 {
				return nil, errors.Join(errors.New("qualification: process lock path replaced"), statErr, pathErr,
					unix.Flock(int(file.Fd()), unix.LOCK_UN), file.Close())
			}
			return &qualificationUnixLock{file}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return nil, errors.Join(err, file.Close())
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), file.Close())
		case <-time.After(5 * time.Millisecond):
		}
	}
}
