package tokens

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBPECounter(t *testing.T) {
	counter, err := NewCounter(TokenizerBPE)
	require.NoError(t, err)
	for _, tt := range []struct {
		input string
		want  int
	}{
		{"", 0},
		{"hello world", 2},
		{"The quick brown fox jumps over the lazy dog.", 10},
	} {
		require.Equal(t, tt.want, counter.Count(tt.input), "Count(%q)", tt.input)
	}
}

func TestEstimatingCounter(t *testing.T) {
	counter, err := NewCounter(TokenizerEstimate)
	require.NoError(t, err)
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

var benchInput = strings.Repeat("The quick brown fox jumps over the lazy dog. ", 100)

func BenchmarkBPECounter(b *testing.B) {
	counter, err := newBPECounter()
	require.NoError(b, err)
	b.ResetTimer()
	for b.Loop() {
		counter.Count(benchInput)
	}
}

func BenchmarkEstimatingCounter(b *testing.B) {
	counter := &estimatingCounter{}
	b.ResetTimer()
	for b.Loop() {
		counter.Count(benchInput)
	}
}
