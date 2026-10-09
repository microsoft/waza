//go:build !linux && !darwin

package snapshot

import (
	"strings"
	"testing"
)

func TestWorkspaceCaptureUnsupportedPlatform(t *testing.T) {
	files, err := CaptureWorkspace(".", []string{"a.txt"}, nil, nil, WorkspaceLimits{100, 200})
	if err == nil || len(files) != 0 || !strings.Contains(err.Error(), "unsupported on this platform") {
		t.Fatalf("expected explicit platform diagnostic: %#v, %v", files, err)
	}
}
