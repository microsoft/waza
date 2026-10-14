//go:build !linux && !darwin

package snapshot

import "errors"

func materializeVerifiedWorkspace(*Snapshot, []string) (*MaterializedWorkspace, error) {
	return nil, errors.New("snapshot: secure workspace materialization is unsupported on this platform")
}
