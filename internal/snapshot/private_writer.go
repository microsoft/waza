package snapshot

import "os"

func writePrivateSnapshot(root string, data []byte) (string, error) {
	return writePrivateSnapshotWithHooks(root, data, privateWriterHooks{})
}

// Per-call hooks permit deterministic admission/publication race tests without
// mutable package globals or interference between concurrent writers.
type privateWriterHooks struct {
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
