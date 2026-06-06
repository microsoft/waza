// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

// Package pricing computes a dollar cost for a run's token usage using, in
// order of preference:
//
//  1. The Copilot SDK's per-request cost (UsageStats.ModelMetrics.RequestCost),
//     when any model reports a non-zero cost.
//  2. A model rate table that prices input, output, cache-read, and cache-write
//     tokens per million for known model families, when every model in the
//     usage map can be resolved.
//  3. A flat fallback estimate of $0.00025 per token (the historical behavior),
//     when neither SDK data nor the rate table can price the usage.
//
// The Compute function returns the source so the dashboard can disclose how
// the number was derived.
package pricing

import (
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// SourceSDK indicates the cost was reported directly by the Copilot SDK.
const SourceSDK = "sdk"

// SourceTable indicates the cost was priced from the in-repo rate table.
const SourceTable = "table"

// SourceEstimate indicates the cost was computed from the flat-rate fallback.
const SourceEstimate = "estimate"

// SourceMixed is used by aggregators when underlying runs report different
// sources. It is not returned by Compute.
const SourceMixed = "mixed"

// FlatRatePerToken is the per-token rate used by the flat estimate fallback.
// This matches the historical behavior that the rate table replaces.
const FlatRatePerToken = 0.00025

// RateTableEffectiveDate is the date the embedded rate table reflects. It is
// surfaced in dashboard tooltips so users can judge staleness.
const RateTableEffectiveDate = "2025-01-01"

// Rates describes per-million-token prices for a model family in USD.
//
// Cache pricing follows Anthropic's published tiers (read = cheaper than
// input, write = more expensive than input). For providers without cache
// billing (e.g. OpenAI does not separately price cache writes), the relevant
// fields are left at zero.
type Rates struct {
	InputPer1M      float64
	OutputPer1M     float64
	CacheReadPer1M  float64
	CacheWritePer1M float64
	EffectiveDate   string
}

// Cost prices a single model's usage at these rates.
func (r Rates) Cost(u models.ModelUsage) float64 {
	const million = 1_000_000.0
	return float64(u.InputTokens)/million*r.InputPer1M +
		float64(u.OutputTokens)/million*r.OutputPer1M +
		float64(u.CacheReadTokens)/million*r.CacheReadPer1M +
		float64(u.CacheWriteTokens)/million*r.CacheWritePer1M
}

// modelRates maps a normalized model-family prefix to its rates. Lookups try
// an exact normalized match first, then fall back to the longest prefix match
// so date-stamped IDs like "claude-opus-4-20250514" resolve to "claude-opus-4".
//
// Pricing is best-effort published list pricing as of RateTableEffectiveDate;
// it is not pulled from the SDK at runtime. Update both values together.
var modelRates = map[string]Rates{
	// Anthropic Claude (Bedrock / Anthropic API list prices).
	"claude-opus-4":     {InputPer1M: 15.00, OutputPer1M: 75.00, CacheReadPer1M: 1.50, CacheWritePer1M: 18.75, EffectiveDate: RateTableEffectiveDate},
	"claude-sonnet-4":   {InputPer1M: 3.00, OutputPer1M: 15.00, CacheReadPer1M: 0.30, CacheWritePer1M: 3.75, EffectiveDate: RateTableEffectiveDate},
	"claude-haiku-4":    {InputPer1M: 1.00, OutputPer1M: 5.00, CacheReadPer1M: 0.10, CacheWritePer1M: 1.25, EffectiveDate: RateTableEffectiveDate},
	"claude-3-5-sonnet": {InputPer1M: 3.00, OutputPer1M: 15.00, CacheReadPer1M: 0.30, CacheWritePer1M: 3.75, EffectiveDate: RateTableEffectiveDate},
	"claude-3-5-haiku":  {InputPer1M: 0.80, OutputPer1M: 4.00, CacheReadPer1M: 0.08, CacheWritePer1M: 1.00, EffectiveDate: RateTableEffectiveDate},
	"claude-3-opus":     {InputPer1M: 15.00, OutputPer1M: 75.00, CacheReadPer1M: 1.50, CacheWritePer1M: 18.75, EffectiveDate: RateTableEffectiveDate},

	// OpenAI GPT-5 family. -mini and -codex-mini are cheaper; -codex matches
	// gpt-5. OpenAI does not bill cache writes separately.
	"gpt-5":            {InputPer1M: 1.25, OutputPer1M: 10.00, CacheReadPer1M: 0.125, EffectiveDate: RateTableEffectiveDate},
	"gpt-5-codex":      {InputPer1M: 1.25, OutputPer1M: 10.00, CacheReadPer1M: 0.125, EffectiveDate: RateTableEffectiveDate},
	"gpt-5-mini":       {InputPer1M: 0.25, OutputPer1M: 2.00, CacheReadPer1M: 0.025, EffectiveDate: RateTableEffectiveDate},
	"gpt-5-codex-mini": {InputPer1M: 0.25, OutputPer1M: 2.00, CacheReadPer1M: 0.025, EffectiveDate: RateTableEffectiveDate},

	// OpenAI legacy.
	"gpt-4o":      {InputPer1M: 2.50, OutputPer1M: 10.00, CacheReadPer1M: 1.25, EffectiveDate: RateTableEffectiveDate},
	"gpt-4o-mini": {InputPer1M: 0.15, OutputPer1M: 0.60, CacheReadPer1M: 0.075, EffectiveDate: RateTableEffectiveDate},
	"gpt-4-turbo": {InputPer1M: 10.00, OutputPer1M: 30.00, EffectiveDate: RateTableEffectiveDate},
	"gpt-4.1":     {InputPer1M: 2.00, OutputPer1M: 8.00, CacheReadPer1M: 0.50, EffectiveDate: RateTableEffectiveDate},
	"gpt-4":       {InputPer1M: 30.00, OutputPer1M: 60.00, EffectiveDate: RateTableEffectiveDate},

	// Google Gemini.
	"gemini-2.5-pro": {InputPer1M: 1.25, OutputPer1M: 10.00, CacheReadPer1M: 0.31, EffectiveDate: RateTableEffectiveDate},
	"gemini-2.5":     {InputPer1M: 1.25, OutputPer1M: 10.00, CacheReadPer1M: 0.31, EffectiveDate: RateTableEffectiveDate},
}

// normalizeModelID lowercases and trims a model ID so lookups are stable across
// the casing/whitespace variations seen in different transcripts.
func normalizeModelID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// LookupRates returns rates for a model ID. It first tries an exact normalized
// match, then walks the longest prefix down to the shortest looking for a
// known family. The second return value is false if no match is found.
//
// Examples:
//   - "claude-opus-4.6"          -> "claude-opus-4"
//   - "claude-opus-4-20250514"   -> "claude-opus-4"
//   - "claude-sonnet-4.6-fast"   -> "claude-sonnet-4"
//   - "gpt-5-codex-max"          -> "gpt-5-codex"
//   - "gpt-5.1"                  -> "gpt-5"
func LookupRates(modelID string) (Rates, bool) {
	id := normalizeModelID(modelID)
	if id == "" {
		return Rates{}, false
	}
	if r, ok := modelRates[id]; ok {
		return r, true
	}
	// Longest-prefix match. Require the boundary character right after the
	// prefix to be a non-alphanumeric separator (or end of string) so
	// "gpt-4" does not greedily match "gpt-40".
	best := ""
	for prefix := range modelRates {
		if !strings.HasPrefix(id, prefix) {
			continue
		}
		if len(id) > len(prefix) {
			next := id[len(prefix)]
			if (next >= 'a' && next <= 'z') || (next >= '0' && next <= '9') {
				continue
			}
		}
		if len(prefix) > len(best) {
			best = prefix
		}
	}
	if best != "" {
		return modelRates[best], true
	}
	return Rates{}, false
}

// Compute returns the dollar cost for a run's usage and the source of the
// number. The source is one of SourceSDK, SourceTable, or SourceEstimate.
//
// A nil or zero-token usage returns (0, SourceEstimate).
func Compute(usage *models.UsageStats) (float64, string) {
	if usage == nil {
		return 0, SourceEstimate
	}

	// Tier 1: SDK reported costs.
	var sdkTotal float64
	for _, m := range usage.ModelMetrics {
		sdkTotal += m.RequestCost
	}
	if sdkTotal > 0 {
		return sdkTotal, SourceSDK
	}

	// Tier 2: Rate table per model. Only return SourceTable when every
	// model with any tokens resolved against the rate table; otherwise the
	// number would mix priced and unpriced models silently.
	if len(usage.ModelMetrics) > 0 {
		var tableTotal float64
		allResolved := true
		anyTokens := false
		for modelID, m := range usage.ModelMetrics {
			modelTokens := m.InputTokens + m.OutputTokens + m.CacheReadTokens + m.CacheWriteTokens
			if modelTokens == 0 {
				continue
			}
			anyTokens = true
			rates, ok := LookupRates(modelID)
			if !ok {
				allResolved = false
				break
			}
			tableTotal += rates.Cost(m)
		}
		if allResolved && anyTokens {
			return tableTotal, SourceTable
		}
	}

	// Tier 3: Flat estimate.
	totalTokens := usage.InputTokens + usage.OutputTokens +
		usage.CacheReadTokens + usage.CacheWriteTokens
	return float64(totalTokens) * FlatRatePerToken, SourceEstimate
}

// CombineSources collapses per-run cost sources into a single label suitable
// for an aggregate (e.g. SummaryResponse). Empty / unknown sources are
// ignored; if no sources are provided it returns "". If all sources agree it
// returns that source. Otherwise it returns SourceMixed.
func CombineSources(sources []string) string {
	seen := ""
	for _, s := range sources {
		if s == "" {
			continue
		}
		if seen == "" {
			seen = s
			continue
		}
		if s != seen {
			return SourceMixed
		}
	}
	return seen
}
