//go:build !darwin && !linux

package main

import (
	"io"
	"strings"
	"testing"
)

func TestUnsupportedHostFailsExplicitly(t *testing.T) {
	sha := strings.Repeat("a", 40)
	err := run([]string{
		"pack", "--input", ".", "--destination", "must-not-be-created",
		"--repository", "microsoft/waza", "--source-sha", sha, "--checkout-sha", sha,
		"--run-id", "123", "--run-attempt", "1",
	}, io.Discard, hooks{})
	if err == nil || !strings.Contains(err.Error(), "supports Linux and macOS hosts only") {
		t.Fatalf("unsupported host did not fail explicitly: %v", err)
	}
}
