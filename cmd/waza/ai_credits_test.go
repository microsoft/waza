package main

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestPrintUsageSummaryPreservesNanoPrecision(t *testing.T) {
	output := captureStdout(t, func() {
		printUsageSummary(&models.UsageStats{
			AICredits: utils.Ptr(0.000000001),
			ModelMetrics: map[string]models.ModelUsage{
				"reported": {AICredits: utils.Ptr(1.123456789)},
				"legacy":   {InputTokens: 1},
			},
		})
	})
	require.Contains(t, output, "AI Credits:")
	require.Contains(t, output, "0.000000001")
	require.Contains(t, output, "1.123456789")
	require.Contains(t, output, "n/a")
}
