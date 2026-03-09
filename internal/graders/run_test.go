package graders

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithModel(t *testing.T) {
	t.Run("nil params", func(t *testing.T) {
		result := WithModel(nil, "claude-opus-4.6")
		assert.Equal(t, "claude-opus-4.6", result["model"])
		assert.Len(t, result, 1)
	})

	t.Run("empty params", func(t *testing.T) {
		result := WithModel(map[string]any{}, "claude-opus-4.6")
		assert.Equal(t, "claude-opus-4.6", result["model"])
		assert.Len(t, result, 1)
	})

	t.Run("preserves existing params", func(t *testing.T) {
		original := map[string]any{
			"prompt":           "Check something",
			"continue_session": true,
		}
		result := WithModel(original, "gpt-4o")
		assert.Equal(t, "gpt-4o", result["model"])
		assert.Equal(t, "Check something", result["prompt"])
		assert.Equal(t, true, result["continue_session"])
		assert.Len(t, result, 3)
	})

	t.Run("overrides existing model", func(t *testing.T) {
		original := map[string]any{
			"prompt": "Check something",
			"model":  "gpt-4o-mini",
		}
		result := WithModel(original, "claude-opus-4.6")
		assert.Equal(t, "claude-opus-4.6", result["model"])
		assert.Equal(t, "gpt-4o-mini", original["model"])
	})

	t.Run("does not mutate original", func(t *testing.T) {
		original := map[string]any{
			"prompt": "Test prompt",
		}
		result := WithModel(original, "new-model")
		result["extra"] = "should not appear in original"
		_, exists := original["extra"]
		assert.False(t, exists)
		_, exists = original["model"]
		assert.False(t, exists)
	})
}
