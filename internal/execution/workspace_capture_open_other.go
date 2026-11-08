//go:build windows || plan9 || js || wasip1

package execution

import "os"

func openWorkspaceCaptureFile(root *os.Root, path string) (*os.File, error) {
	return root.Open(path)
}
