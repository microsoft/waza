package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func callAggregateCLI(t *testing.T, directory string, inputs []manifestaggregate.Input) []byte {
	t.Helper()
	wire, err := json.Marshal(inputs)
	require.NoError(t, err)
	input := filepath.Join(t.TempDir(), "inputs.json")
	output := filepath.Join(t.TempDir(), "aggregate.json")
	require.NoError(t, os.WriteFile(input, wire, 0o600))
	command := newAggregateCommand()
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--store", directory, "--inputs", input, "-o", output})
	require.NoError(t, command.Execute())
	saved, err := os.ReadFile(output)
	require.NoError(t, err, "evaluated membership must be persisted")
	require.Equal(t, append(bytes.Clone(saved), '\n'), stdout.Bytes())
	return saved
}

func callAggregateAPI(t *testing.T, directory string, inputs []manifestaggregate.Input) []byte {
	t.Helper()
	status, body := postAggregateAPI(t, directory, inputs)
	require.Equal(t, http.StatusOK, status, string(body))
	return body
}

func postAggregateAPI(t *testing.T, directory string, inputs []manifestaggregate.Input) (int, []byte) {
	t.Helper()
	wire, err := json.Marshal(inputs)
	require.NoError(t, err)
	mux := http.NewServeMux()
	webapi.RegisterAggregateRoutesWithSources(mux, func() (manifestaggregate.Sources, func() error, error) {
		return manifestaggregate.OpenLocal(directory)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	response, err := server.Client().Post(server.URL+"/api/v1/aggregates", "application/json", bytes.NewReader(wire))
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, body
}

func TestAggregateNonzeroUsageDeduplicatesExecutionsAndCompatibleViewsInSavedReport(t *testing.T) {
	directory := t.TempDir()
	backend, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	first := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", nil, manifestfixture.RecordedNonzero(1))
	second := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", nil, manifestfixture.RecordedNonzero(2))
	require.NotEqual(t, first.Execution, second.Execution)
	inspection, err := evidencestate.Inspect(t.Context(), backend, first.Evidence)
	require.NoError(t, err)
	view := *inspection.Document
	view.Lineage = []evidencestate.Lineage{{Manifest: second.Manifest, Relationship: evidencestate.Aggregation}}
	otherView, err := evidencestate.Publish(t.Context(), backend, view)
	require.NoError(t, err)
	require.NotEqual(t, first.Evidence, otherView.EvidenceSHA256)
	require.NoError(t, backend.Close())
	firstInput := manifestaggregate.Input{Manifest: first.Manifest, Evidence: first.Evidence}
	inputs := []manifestaggregate.Input{
		{Manifest: first.Manifest, Evidence: otherView.EvidenceSHA256},
		{Manifest: second.Manifest, Evidence: second.Evidence},
		firstInput, firstInput,
	}
	saved := callAggregateCLI(t, directory, inputs)
	require.Equal(t, saved, callAggregateAPI(t, directory, inputs))
	report, err := manifestaggregate.Parse(saved)
	require.NoError(t, err)
	require.Equal(t, manifestaggregate.Counts{Members: 3, VerifiedManifests: 2, VerifiedExecutions: 2,
		MemberStates: map[string]int{"admitted": 3}, MemberOutcomes: map[string]int{"success": 3}}, report.Counts)
	require.Equal(t, "complete", report.Usage.State)
	require.Equal(t, 2, report.Usage.Executions, "denominator is distinct executions, not pairs/views")
	credits := 3.75
	require.Equal(t, &models.UsageStats{
		Turns: 3, InputTokens: 33, OutputTokens: 21, CacheReadTokens: 9, CacheWriteTokens: 6,
		PremiumRequests: 1.5, AICredits: &credits,
		ModelMetrics: map[string]models.ModelUsage{"fixture-model": {
			InputTokens: 33, OutputTokens: 21, CacheReadTokens: 9, CacheWriteTokens: 6,
			RequestCount: 6, RequestCost: .75, AICredits: &credits,
		}},
	}, report.Usage.Recorded, "a missing execution or double-counted compatible view must change the exact total")
}

func TestAggregateOversizedUsagePreservesSavedCLIAndAPIReport(t *testing.T) {
	directory := t.TempDir()
	backend, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	model := "escaped-model-" + strings.Repeat("\x01", (manifestaggregate.MaxReportBytes-512)/6)
	var largeUsage, smallUsage *models.UsageStats
	large := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", nil, func(report *runnerunit.Report) {
		manifestfixture.RecordedNonzero(1)(report)
		usage := report.Observation.Usage
		usage.ModelMetrics = map[string]models.ModelUsage{model: usage.ModelMetrics["fixture-model"]}
		wire, err := json.Marshal(usage)
		require.NoError(t, err)
		require.Less(t, len(model), manifestaggregate.MaxReportBytes, "character count is not a serialized-byte bound")
		require.Less(t, len(wire), manifestaggregate.MaxReportBytes, "usage alone fits; full report overhead must be measured")
		require.LessOrEqual(t, len(wire), artifactmanifest.MaxArtifactBytes, "native source remains within its supported profile")
		largeUsage = usage
		t.Logf("admitted recorded source usage: %d Go-escaped bytes", len(wire))
	})
	small := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", nil, func(report *runnerunit.Report) {
		manifestfixture.RecordedNonzero(2)(report)
		smallUsage = report.Observation.Usage
	})
	require.NoError(t, backend.Close())
	inputs := []manifestaggregate.Input{
		{Manifest: large.Manifest, Evidence: large.Evidence},
		{Manifest: small.Manifest, Evidence: small.Evidence},
	}
	var cli []byte
	for _, caller := range []struct {
		name string
		call func(*testing.T, string, []manifestaggregate.Input) []byte
	}{{"saved CLI", callAggregateCLI}, {"public API", callAggregateAPI}} {
		t.Run(caller.name, func(t *testing.T) {
			saved := caller.call(t, directory, inputs)
			report, err := manifestaggregate.Parse(saved)
			require.NoError(t, err)
			require.Equal(t, "complete", report.State, "all public sources were admitted; usage availability is separate")
			require.Equal(t, manifestaggregate.Counts{Members: 2, VerifiedManifests: 2, VerifiedExecutions: 2,
				MemberStates: map[string]int{"admitted": 2}, MemberOutcomes: map[string]int{"success": 2}}, report.Counts)
			require.Equal(t, "unavailable", report.Usage.State)
			require.Equal(t, 2, report.Usage.Executions, "withholding must not erase the eligible population")
			require.Nil(t, report.Usage.Recorded, "even the small neighbor must not become an unlabeled subset total")
			require.Equal(t, []string{"recorded_usage_report_size_unavailable"}, report.Usage.Diagnostics)
			require.NotContains(t, string(saved), "escaped-model-")
			require.LessOrEqual(t, len(saved), manifestaggregate.MaxReportBytes)
			candidate := *report
			candidate.Usage = manifestaggregate.Usage{
				State: "complete", Population: report.Usage.Population, Executions: 2, Diagnostics: []string{},
				Recorded: models.AggregateUsageStats([]*models.UsageStats{largeUsage, smallUsage}),
			}
			oversized, err := json.Marshal(candidate)
			require.NoError(t, err)
			require.Greater(t, len(oversized), manifestaggregate.MaxReportBytes,
				"actual full Go-escaped report, not usage/string size alone, exceeds the wire limit")
			t.Logf("full candidate report: %d bytes; retained report: %d bytes", len(oversized), len(saved))
			if caller.name == "saved CLI" {
				cli = saved
			} else if cli != nil {
				require.Equal(t, cli, saved)
			}
		})
	}
}
