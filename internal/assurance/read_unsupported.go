//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package assurance

import (
	"errors"
	"os"
)

func openReferenceDocument(_ *os.Root, _ string) (*os.File, error) {
	return nil, errors.New("assurance: secure reference reads are unsupported on this platform")
}
