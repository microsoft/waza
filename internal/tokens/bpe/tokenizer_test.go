package bpe

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	imStart = "<|im_start|>"
	imEnd   = "<|im_end|>"
)

func TestTokenizerCore(t *testing.T) {
	specialTokens := map[string]int{
		imStart: 100264,
		imEnd:   100265,
	}
	tokenizer, err := NewTokenizerForModel("gpt-5", specialTokens)
	require.NoError(t, err)

	t.Run("hello world", func(t *testing.T) {
		str := "hello world"
		encoded := tokenizer.Encode(str, nil)
		require.Equal(t, []int{24912, 2375}, encoded)
		require.Equal(t, str, tokenizer.Decode(encoded))
	})

	t.Run("single punctuation", func(t *testing.T) {
		str := "!"
		encoded := tokenizer.Encode(str, nil)
		require.Equal(t, []int{0}, encoded)
		require.Equal(t, str, tokenizer.Decode(encoded))
	})

	t.Run("empty string", func(t *testing.T) {
		str := ""
		encoded := tokenizer.Encode(str, nil)
		require.Empty(t, encoded)
		require.Equal(t, str, tokenizer.Decode(encoded))
	})

	t.Run("encode trim suffix 3", func(t *testing.T) {
		str := strings.Repeat("t", 4000)
		encoded := tokenizer.Encode(str, nil)
		trimmed := tokenizer.EncodeTrimSuffix(str, 5, []string{})
		require.Len(t, trimmed.TokenIDs, 5)
		require.Equal(t, encoded[:5], trimmed.TokenIDs)
	})

	t.Run("encode trim prefix 3", func(t *testing.T) {
		str := strings.Repeat("t", 4000)
		encoded := tokenizer.Encode(str, nil)
		trimmed := tokenizer.EncodeTrimPrefix(str, 5, []string{})
		require.Len(t, trimmed.TokenIDs, 5)
		require.Equal(t, encoded[len(encoded)-5:], trimmed.TokenIDs)
	})
}
