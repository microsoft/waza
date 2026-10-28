package main

import "os"

// Go's Windows file implementation cancels pending pipe I/O on Close.
// This handle is process-owned; supplied caller-owned handles are never closed.
func pollableRunnerStdio(source *os.File) (*os.File, func() error, error) {
	return source, func() error { return nil }, nil
}
