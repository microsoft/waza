//go:build !linux && !darwin

package snapshot

import (
	"strings"
	"testing"
)

func TestPrivateWriterUnsupportedPlatform(t *testing.T) {
	published, err := writePrivateSnapshot(".", []byte("sensitive"))
	if err == nil || published != "" || !strings.Contains(err.Error(), "unsupported on this platform") {
		t.Fatalf("expected fail-closed platform diagnostic: %q, %v", published, err)
	}
}
