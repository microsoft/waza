package main

import (
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestAggregatePresentNativeCategoryRequiresObservedCount(t *testing.T) {
	directory := t.TempDir()
	zero := new(float64)
	published := aggregateOwnedCapture(t, directory, &models.UsageStats{
		AICredits: zero, ModelMetrics: map[string]models.ModelUsage{"recorded-mock": {AICredits: zero}},
	})
	backend, closeBackend, err := artifactmanifest.OpenBackend(filepath.Join(directory, ".waza-artifacts"), "")
	require.NoError(t, err)
	defer func() { require.NoError(t, closeBackend()) }()
	origin := published.Document.OriginManifest
	pairs := []manifestaggregate.EvidenceInput{{ManifestSHA256: origin, EvidenceSHA256: published.EvidenceSHA256}}
	report, err := manifestaggregate.Aggregate(t.Context(), backend, manifestaggregate.Request{Manifests: []string{origin}, Evidence: &pairs})
	require.NoError(t, err)
	candidate := &report.Evidence.Members[0].Candidates[0]
	require.Equal(t, evidencestate.Present, candidate.Usage.State)
	require.NotNil(t, candidate.Usage.Recorded)
	found := false
	for index := range candidate.Categories {
		if candidate.Categories[index].Category == evidencestate.Usage {
			require.NotNil(t, candidate.Categories[index].ObservedCount)
			candidate.Categories[index].ObservedCount = nil
			found = true
		}
	}
	require.True(t, found)
	delete(report.Evidence.CategoryStateCounts, evidencestate.Usage)
	_, err = manifestaggregate.Encode(report)
	require.Error(t, err, "native present usage cannot lose its observed count and merely vanish from tallies")
	require.ErrorContains(t, err, "observed_count")
	for index := range candidate.Categories {
		if candidate.Categories[index].Category == evidencestate.Usage {
			candidate.Categories[index].Verification = "recorded-degradation-only"
			candidate.Usage.Verification = "recorded-degradation-only"
			candidate.Usage.Recorded = nil
		}
	}
	_, err = manifestaggregate.Encode(report)
	require.Error(t, err, "recorded degradation cannot be retagged as present without a count")
}
