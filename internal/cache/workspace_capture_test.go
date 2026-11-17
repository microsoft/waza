package cache

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceCaptureCacheKeysSeparateModes(t *testing.T) {
	spec := &models.EvalSpec{Config: models.Config{}}
	task := &models.TestCase{TestID: "task"}
	keys := map[string]string{}
	for _, mode := range []string{"", "full", "none"} {
		spec.Config.WorkspaceCapture = mode
		key, err := CacheKey(spec, task, "")
		require.NoError(t, err)
		keys[mode] = key
	}
	require.Equal(t, keys[""], keys["full"])
	require.NotEqual(t, keys["full"], keys["none"])
	// The old key could contain a no-capture result despite representing full.
	const legacyKey = "eafa803f7cd788f61d60589a925322a0b3c90198bfedded42e77bda7b28b4d0f"
	require.NotEqual(t, legacyKey, keys["full"])
	require.NotEqual(t, legacyKey, keys["none"])
}
