package dev

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPromptConfirm(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"yes lowercase", "y\n", true},
		{"yes uppercase", "Y\n", true},
		{"no lowercase", "n\n", false},
		{"no uppercase", "N\n", false},
		{"empty defaults to no", "\n", false},
		{"other defaults to no", "x\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := bufio.NewScanner(strings.NewReader(tt.input))
			w := new(bytes.Buffer)

			got := PromptConfirm(s, w, "Apply?")
			require.Equal(t, tt.want, got)
		})
	}
}

func TestPromptConfirm_ShowsQuestion(t *testing.T) {
	s := bufio.NewScanner(strings.NewReader("n\n"))
	w := new(bytes.Buffer)

	_ = PromptConfirm(s, w, "Do you want to continue?")
	require.Contains(t, w.String(), "Do you want to continue?")
}

func TestPromptConfirm_EOF(t *testing.T) {
	s := bufio.NewScanner(strings.NewReader(""))
	w := new(bytes.Buffer)

	got := PromptConfirm(s, w, "Continue?")
	require.False(t, got, "EOF should default to false")
}

func TestSharedScanner_ConsecutiveReads(t *testing.T) {
	s := bufio.NewScanner(strings.NewReader("y\nn\n"))
	w := new(bytes.Buffer)

	require.True(t, PromptConfirm(s, w, "First?"))
	require.False(t, PromptConfirm(s, w, "Second?"))
}
