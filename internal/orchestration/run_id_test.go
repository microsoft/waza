package orchestration

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewRunIDIsUniqueForSameInstant(t *testing.T) {
	now := time.Date(2100, 1, 2, 3, 4, 5, 6, time.UTC)

	first := newRunID(now)
	second := newRunID(now)

	require.NotEqual(t, first, second)
	firstValue, err := strconv.ParseInt(strings.TrimPrefix(first, "run-"), 10, 64)
	require.NoError(t, err)
	secondValue, err := strconv.ParseInt(strings.TrimPrefix(second, "run-"), 10, 64)
	require.NoError(t, err)
	require.Equal(t, firstValue+1, secondValue)
}
