//go:build !linux && !darwin

package snapshot

import "errors"

type workspaceRoot struct{}

func openWorkspaceRoot(string) (*workspaceRoot, error) {
	return nil, errors.New("workspace: safe no-symlink capture is unsupported on this platform")
}

func (*workspaceRoot) Close() error { return nil }

func (*workspaceRoot) readFile(string, int64) ([]byte, error) {
	return nil, errors.New("workspace: safe no-symlink capture is unsupported on this platform")
}
