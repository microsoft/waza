package snapshot

func writePrivateSnapshot(root string, data []byte) (string, error) {
	return writePrivateSnapshotWithHooks(root, data, privateWriterHooks{})
}

// Per-call hooks permit deterministic admission/publication race tests without
// mutable package globals or interference between concurrent writers.
type privateWriterHooks struct {
	beforeAdmission   func()
	beforePublication func()
	afterPublication  func()
}
