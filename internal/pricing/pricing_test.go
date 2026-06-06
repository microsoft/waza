// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package pricing

import (
	"math"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestCompute_SDKPathPreferredWhenCostsReported(t *testing.T) {
	usage := &models.UsageStats{
		InputTokens:  100,
		OutputTokens: 50,
		ModelMetrics: map[string]models.ModelUsage{
			"claude-sonnet-4.6": {
				InputTokens:  100,
				OutputTokens: 50,
				RequestCost:  0.42,
			},
			"gpt-5": {
				InputTokens:  10,
				OutputTokens: 5,
				RequestCost:  0.08,
			},
		},
	}
	cost, src := Compute(usage)
	if src != SourceSDK {
		t.Fatalf("expected source %q, got %q", SourceSDK, src)
	}
	if !approxEqual(cost, 0.50) {
		t.Fatalf("expected sdk cost 0.50, got %v", cost)
	}
}

func TestCompute_TablePathPricesCacheTokens(t *testing.T) {
	// Sonnet 4 rates: in 3.00, out 15.00, cache_read 0.30, cache_write 3.75.
	// 1M each => 3 + 15 + 0.30 + 3.75 = 22.05
	usage := &models.UsageStats{
		ModelMetrics: map[string]models.ModelUsage{
			"claude-sonnet-4.6": {
				InputTokens:      1_000_000,
				OutputTokens:     1_000_000,
				CacheReadTokens:  1_000_000,
				CacheWriteTokens: 1_000_000,
			},
		},
	}
	cost, src := Compute(usage)
	if src != SourceTable {
		t.Fatalf("expected source %q, got %q", SourceTable, src)
	}
	if !approxEqual(cost, 22.05) {
		t.Fatalf("expected cost 22.05, got %v", cost)
	}
}

func TestCompute_TablePathMultiModelAggregation(t *testing.T) {
	// gpt-5: 500k in @ 1.25/M = 0.625, 100k out @ 10/M = 1.00 => 1.625
	// claude-haiku-4: 200k in @ 1/M = 0.20, 100k out @ 5/M = 0.50 => 0.70
	// total => 2.325
	usage := &models.UsageStats{
		ModelMetrics: map[string]models.ModelUsage{
			"gpt-5.1": {
				InputTokens:  500_000,
				OutputTokens: 100_000,
			},
			"claude-haiku-4.5": {
				InputTokens:  200_000,
				OutputTokens: 100_000,
			},
		},
	}
	cost, src := Compute(usage)
	if src != SourceTable {
		t.Fatalf("expected source %q, got %q", SourceTable, src)
	}
	if !approxEqual(cost, 2.325) {
		t.Fatalf("expected cost 2.325, got %v", cost)
	}
}

func TestCompute_UnknownModelFallsBackToEstimate(t *testing.T) {
	usage := &models.UsageStats{
		InputTokens:  100,
		OutputTokens: 200,
		ModelMetrics: map[string]models.ModelUsage{
			"some-future-model-x9": {
				InputTokens:  100,
				OutputTokens: 200,
			},
		},
	}
	cost, src := Compute(usage)
	if src != SourceEstimate {
		t.Fatalf("expected source %q, got %q", SourceEstimate, src)
	}
	if !approxEqual(cost, 300*FlatRatePerToken) {
		t.Fatalf("expected cost %v, got %v", 300*FlatRatePerToken, cost)
	}
}

func TestCompute_MixedKnownAndUnknownFallsBackToEstimate(t *testing.T) {
	// One known and one unknown model should fall through to the flat
	// estimate so we never silently undercharge the unknown half.
	usage := &models.UsageStats{
		InputTokens:  300,
		OutputTokens: 0,
		ModelMetrics: map[string]models.ModelUsage{
			"gpt-5":            {InputTokens: 100},
			"mystery-model-v2": {InputTokens: 200},
		},
	}
	cost, src := Compute(usage)
	if src != SourceEstimate {
		t.Fatalf("expected source %q, got %q", SourceEstimate, src)
	}
	if !approxEqual(cost, 300*FlatRatePerToken) {
		t.Fatalf("expected flat estimate, got %v", cost)
	}
}

func TestCompute_NoModelMetricsFallsBackToEstimate(t *testing.T) {
	usage := &models.UsageStats{
		InputTokens:  1000,
		OutputTokens: 500,
	}
	cost, src := Compute(usage)
	if src != SourceEstimate {
		t.Fatalf("expected source %q, got %q", SourceEstimate, src)
	}
	if !approxEqual(cost, 1500*FlatRatePerToken) {
		t.Fatalf("expected flat estimate, got %v", cost)
	}
}

func TestCompute_NilUsage(t *testing.T) {
	cost, src := Compute(nil)
	if src != SourceEstimate {
		t.Fatalf("expected estimate, got %q", src)
	}
	if cost != 0 {
		t.Fatalf("expected 0 cost, got %v", cost)
	}
}

func TestCompute_PrefersSDKEvenWhenTableWouldResolve(t *testing.T) {
	usage := &models.UsageStats{
		ModelMetrics: map[string]models.ModelUsage{
			"gpt-5": {
				InputTokens:  1_000_000,
				OutputTokens: 1_000_000,
				RequestCost:  99.99,
			},
		},
	}
	cost, src := Compute(usage)
	if src != SourceSDK {
		t.Fatalf("expected sdk, got %q", src)
	}
	if !approxEqual(cost, 99.99) {
		t.Fatalf("expected 99.99, got %v", cost)
	}
}

func TestLookupRates_PrefixMatching(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"claude-opus-4.6", "claude-opus-4"},
		{"claude-opus-4-20250514", "claude-opus-4"},
		{"claude-sonnet-4.6-fast", "claude-sonnet-4"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4"},
		{"claude-haiku-4.5", "claude-haiku-4"},
		{"gpt-5", "gpt-5"},
		{"gpt-5.1", "gpt-5"},
		{"gpt-5-codex", "gpt-5-codex"},
		{"gpt-5-codex-max", "gpt-5-codex"},
		{"gpt-5-codex-mini", "gpt-5-codex-mini"},
		{"gpt-5-mini", "gpt-5-mini"},
		{"gpt-4o-2024-08-06", "gpt-4o"},
		{"gpt-4o-mini-2024-07-18", "gpt-4o-mini"},
		{"GPT-4O", "gpt-4o"},
		{"  claude-sonnet-4.5  ", "claude-sonnet-4"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := LookupRates(tc.in)
			if !ok {
				t.Fatalf("expected match for %q", tc.in)
			}
			want, _ := LookupRates(tc.want)
			if got != want {
				t.Fatalf("for %q expected rates of %q (%+v), got %+v", tc.in, tc.want, want, got)
			}
		})
	}
}

func TestLookupRates_NoMatch(t *testing.T) {
	if _, ok := LookupRates(""); ok {
		t.Fatal("empty model ID should not match")
	}
	if _, ok := LookupRates("totally-unknown-model"); ok {
		t.Fatal("unknown model ID should not match")
	}
	// Boundary check: gpt-4 should not greedily match "gpt-40-foo".
	if _, ok := LookupRates("gpt-40-future"); ok {
		t.Fatal("gpt-40-future must not match gpt-4 prefix")
	}
}

func TestCombineSources(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ""},
		{"all-empty", []string{"", ""}, ""},
		{"single-sdk", []string{SourceSDK}, SourceSDK},
		{"all-table", []string{SourceTable, SourceTable, SourceTable}, SourceTable},
		{"sdk-and-table", []string{SourceSDK, SourceTable}, SourceMixed},
		{"sdk-and-estimate", []string{SourceSDK, SourceEstimate}, SourceMixed},
		{"ignore-empties", []string{"", SourceTable, "", SourceTable}, SourceTable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CombineSources(tc.in)
			if got != tc.want {
				t.Fatalf("CombineSources(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRatesCost(t *testing.T) {
	r := Rates{InputPer1M: 1.0, OutputPer1M: 2.0, CacheReadPer1M: 0.5, CacheWritePer1M: 4.0}
	got := r.Cost(models.ModelUsage{
		InputTokens:      1_000_000,
		OutputTokens:     500_000,
		CacheReadTokens:  2_000_000,
		CacheWriteTokens: 250_000,
	})
	want := 1.0 + 1.0 + 1.0 + 1.0
	if !approxEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
