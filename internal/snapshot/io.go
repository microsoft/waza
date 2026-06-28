package snapshot

import (
	"io"
)

// copyTo is io.Copy with a small wrapper so fixtures.go does not need an
// extra import. Returns the number of bytes copied.
func copyTo(dst io.Writer, src io.Reader) (int64, error) {
	return io.Copy(dst, src)
}
