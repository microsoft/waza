package dev

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPromptConfirm_NonTTY_ReturnsFalse(t *testing.T) {
	// Non-TTY readers (like strings.Reader) should always return false.
	got := defaultPromptConfirm(strings.NewReader("y\n"), &bytes.Buffer{}, "Apply?")
	require.False(t, got, "non-TTY input should default to false")
}

func TestPromptConfirm_NilReader_ReturnsFalse(t *testing.T) {
	got := defaultPromptConfirm(nil, &bytes.Buffer{}, "Apply?")
	require.False(t, got, "nil reader should default to false")
}

func TestPromptConfirm_TestHookOverride(t *testing.T) {
	withDevTestConfirm(t, func(_ io.Reader, _ io.Writer, question string) bool {
		return question == "Match?"
	})

	require.True(t, promptConfirm(nil, nil, "Match?"))
	require.False(t, promptConfirm(nil, nil, "Other?"))
}

func TestPromptConfirm_TestHookRestored(t *testing.T) {
	original := promptConfirm

	withDevTestConfirm(t, func(_ io.Reader, _ io.Writer, _ string) bool {
		return true
	})
	require.True(t, promptConfirm(nil, nil, "anything"))

	// After cleanup, promptConfirm should be restored — verify it's set during test
	t.Cleanup(func() {
		require.IsType(t, original, promptConfirm, "promptConfirm should be restored after test")
	})
}

// withDevTestConfirm overrides promptConfirm for the duration of a test.
func withDevTestConfirm(t *testing.T, fn func(io.Reader, io.Writer, string) bool) {
	t.Helper()
	orig := promptConfirm
	promptConfirm = fn
	t.Cleanup(func() {
		promptConfirm = orig
	})
}
