package snapshot

import (
	"fmt"
	"os"
)

// WritePrivateArtifact publishes a complete, exclusive local artifact using the
// same rooted directory admission and cleanup as private snapshot evidence.
// The destination must be private; unsupported platforms fail explicitly.
func WritePrivateArtifact(root string, data []byte, suffix string) (string, error) {
	if suffix != ".runner.json" {
		return "", fmt.Errorf("unsupported private artifact suffix %q", suffix)
	}
	return writePrivateSnapshotWithHooks(root, data, privateWriterHooks{suffix: suffix})
}

func writePrivateSnapshot(root string, data []byte) (string, error) {
	return writePrivateSnapshotWithHooks(root, data, privateWriterHooks{})
}

// Per-call hooks permit deterministic admission/publication race tests without
// mutable package globals or interference between concurrent writers.
type privateWriterHooks struct {
	suffix            string
	beforeAdmission   func()
	beforePublication func()
	afterPublication  func()
	closeDirectory    func(*os.File) error
	unlink            func(int, string, int) error
	write             func(*os.File, []byte) (int, error)
	residualOps       *privateResidualOps
}

type privateResidualOps struct {
	openat func(int, string, int, uint32) (int, error)
	stat   func(*os.File) (os.FileInfo, error)
	read   func(*os.File, int64) ([]byte, error)
	close  func(*os.File) error
}
