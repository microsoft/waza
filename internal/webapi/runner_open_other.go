//go:build !linux && !darwin

package webapi

import "os"

func openRunnerArtifact(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
