package models_test

import (
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
	"github.com/stretchr/testify/require"
)

func TestModelCreditsRequireCompleteSessionAttribution(t *testing.T) {
	for _, tc := range []struct {
		name    string
		other   *models.UsageStats
		unknown bool
	}{
		{"missing session", nil, true},
		{"missing attribution", &models.UsageStats{InputTokens: 3}, true},
		{"known credits without attribution", &models.UsageStats{AICredits: utils.Ptr(1.0)}, true},
		{"different model missing credits", &models.UsageStats{ModelMetrics: map[string]models.ModelUsage{"other": {InputTokens: 3}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := models.AggregateUsageStats([]*models.UsageStats{
				{InputTokens: 2, AICredits: utils.Ptr(2.0), ModelMetrics: map[string]models.ModelUsage{"reported": {InputTokens: 2, AICredits: utils.Ptr(2.0)}}},
				tc.other,
			})
			require.NotNil(t, usage)
			require.Equal(t, 2, usage.ModelMetrics["reported"].InputTokens)
			if tc.unknown {
				require.Nil(t, usage.ModelMetrics["reported"].AICredits)
			} else {
				require.Equal(t, 2.0, *usage.ModelMetrics["reported"].AICredits)
				require.Nil(t, usage.ModelMetrics["other"].AICredits)
			}
		})
	}
}
