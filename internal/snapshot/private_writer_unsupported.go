//go:build !linux && !darwin

package snapshot

import "errors"

func writePrivateSnapshotWithHooks(string, []byte, privateWriterHooks) (string, error) {
	return "", errors.New("snapshot: safe private evidence publication is unsupported on this platform")
}
