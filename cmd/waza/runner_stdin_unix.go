//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Inherited Unix stdio is initially blocking and cannot use Go deadlines.
// Only process-owned stdio is duplicated and temporarily made pollable;
// supplied caller-owned files never pass through this function.
func pollableRunnerStdio(source *os.File) (*os.File, func() error, error) {
	original := int(source.Fd())
	flags, err := unix.FcntlInt(uintptr(original), unix.F_GETFL, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("reading standard I/O flags: %w", err)
	}
	fd, err := unix.Dup(original)
	if err != nil {
		return nil, nil, fmt.Errorf("duplicating standard I/O: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("enabling interruptible standard I/O: %w", err), unix.Close(fd))
	}
	file := os.NewFile(uintptr(fd), "runner-stdio")
	return file, func() error {
		return errors.Join(file.Close(), unix.SetNonblock(original, flags&unix.O_NONBLOCK != 0))
	}, nil
}
