//go:build !linux && !darwin

package snapshot

import (
	"strings"
	"testing"
)

func TestMaterializeUnsupportedPlatform(t *testing.T) {
	snap := &Snapshot{WorkspaceFiles: []WorkspaceFile{{Path: "output.txt", Content: "known file"}}}
	workspace, err := materializeVerifiedWorkspace(snap, []string{"output.txt"})
	if err == nil || workspace != nil || !strings.Contains(err.Error(), "unsupported on this platform") {
		t.Fatalf("expected fail-closed materialization: %v", err)
	}
}
