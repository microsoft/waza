//go:build unix

package provenance

import "syscall"

const openNonblock = syscall.O_NONBLOCK
