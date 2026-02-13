package tokens

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEstimatingCounter(t *testing.T) {
	counter := NewEstimatingCounter()
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"test", 1},
		{"testing", 2},
		{"The quick brown fox jumps over the lazy dog.", 11},
		{string(make([]byte, 100)), 25},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, counter.Count(tt.input), "Count(%q)", tt.input)
	}
}
