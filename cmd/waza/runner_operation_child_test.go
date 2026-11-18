package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunnerOperationWriterBoundsChildInput(t *testing.T) {
	data := make([]byte, runnerOperationWriterMax+1)
	err := writeRunnerOperationOutputChild(nil, data, time.Second)
	require.ErrorContains(t, err, "runner operation output exceeds")
}
