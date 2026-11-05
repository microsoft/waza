package main

import (
	"bytes"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestPrintUsageSummaryPreservesNanoPrecision(t *testing.T) {
	var summary bytes.Buffer
	printUsageSummary(&summary, &models.UsageStats{
		AICredits: utils.Ptr(0.000000001),
		ModelMetrics: map[string]models.ModelUsage{
			"reported": {AICredits: utils.Ptr(1.123456789)},
			"legacy":   {InputTokens: 1},
		},
	})
	output := summary.String()
	require.Contains(t, output, "AI Credits:")
	require.Contains(t, output, "0.000000001")
	require.Contains(t, output, "1.123456789")
	require.Contains(t, output, "n/a")
}
