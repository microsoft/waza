package recommend

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// ModelInput pairs a model identifier with its evaluation outcome.
// The outcome may be nil if the model run failed entirely.
type ModelInput struct {
	ModelID string
	Outcome *models.EvalOutcome
}

// Engine computes heuristic recommendations from multi-model evaluation outcomes.
type Engine struct {
	weights models.RecommendationWeights
}

// NewEngine creates a recommendation engine with default weights.
func NewEngine() *Engine {
	return &Engine{
		weights: models.RecommendationWeights{
			AggregateScore: 0.40,
			PassRate:       0.30,
			Consistency:    0.20,
			Speed:          0.10,
		},
	}
}

// Recommend computes a heuristic recommendation from a slice of model results.
// Returns nil if fewer than 2 models have non-nil outcomes.
func (e *Engine) Recommend(results []ModelInput) *models.Recommendation {
	// Filter to models with actual outcomes
	var valid []ModelInput
	for _, r := range results {
		if r.Outcome != nil {
			valid = append(valid, r)
		}
	}
	if len(valid) < 2 {
		return nil
	}

	scores := e.scoreModels(valid)

	winner := scores[0]
	runnerUp := scores[1]

	var margin float64
	if runnerUp.HeuristicScore > 0 {
		margin = ((winner.HeuristicScore - runnerUp.HeuristicScore) / runnerUp.HeuristicScore) * 100
	}

	reason := e.buildReason(winner, runnerUp, valid)

	return &models.Recommendation{
		RecommendedModel: winner.ModelID,
		HeuristicScore:   math.Round(winner.HeuristicScore*10) / 10,
		Reason:           reason,
		WinnerMarginPct:  math.Round(margin*10) / 10,
		Weights:          e.weights,
		ModelScores:      scores,
	}
}

// rawMetrics holds unnormalized values extracted from an outcome.
type rawMetrics struct {
	aggScore   float64
	passRate   float64
	stdDev     float64
	durationMs int64
}

// normalizedMetrics holds 0–10 normalized scores.
type normalizedMetrics struct {
	aggScore    float64
	passRate    float64
	consistency float64
	speed       float64
}

func (e *Engine) scoreModels(results []ModelInput) []models.ModelScore {
	metrics := e.extractMetrics(results)
	normalized := e.normalizeMetrics(metrics)

	scores := make([]models.ModelScore, len(results))
	for i := range results {
		norm := normalized[i]
		hScore := (norm.aggScore * e.weights.AggregateScore) +
			(norm.passRate * e.weights.PassRate) +
			(norm.consistency * e.weights.Consistency) +
			(norm.speed * e.weights.Speed)

		// Round to 1 decimal place
		hScore = math.Round(hScore*10) / 10

		scores[i] = models.ModelScore{
			ModelID:        results[i].ModelID,
			HeuristicScore: hScore,
			Scores: map[string]float64{
				"aggregate_score_normalized": math.Round(norm.aggScore*10) / 10,
				"pass_rate_normalized":       math.Round(norm.passRate*10) / 10,
				"consistency_normalized":     math.Round(norm.consistency*10) / 10,
				"speed_normalized":           math.Round(norm.speed*10) / 10,
			},
		}
	}

	// Sort descending by score; stable sort preserves input order for ties
	sort.SliceStable(scores, func(a, b int) bool {
		return scores[a].HeuristicScore > scores[b].HeuristicScore
	})
	for i := range scores {
		scores[i].Rank = i + 1
	}

	return scores
}

// extractMetrics returns metrics keyed by array index to avoid collisions
// when duplicate ModelIDs are present.
func (e *Engine) extractMetrics(results []ModelInput) map[int]rawMetrics {
	metrics := make(map[int]rawMetrics, len(results))
	for i, mr := range results {
		d := mr.Outcome.Digest
		metrics[i] = rawMetrics{
			aggScore:   d.AggregateScore,
			passRate:   d.SuccessRate * 100,
			stdDev:     d.StdDev,
			durationMs: d.DurationMs,
		}
	}
	return metrics
}

// normalizeMetrics scales each metric to a 0–10 range using min-max normalization.
// When all values are equal, all models receive 5.0 for that metric.
// Keyed by array index to handle duplicate ModelIDs correctly.
func (e *Engine) normalizeMetrics(metrics map[int]rawMetrics) map[int]normalizedMetrics {
	var aggScores, passRates, stdDevs, durations []float64
	for _, m := range metrics {
		aggScores = append(aggScores, m.aggScore)
		passRates = append(passRates, m.passRate)
		stdDevs = append(stdDevs, m.stdDev)
		durations = append(durations, float64(m.durationMs))
	}

	result := make(map[int]normalizedMetrics, len(metrics))
	for i, m := range metrics {
		result[i] = normalizedMetrics{
			aggScore:    normalizeHigherBetter(m.aggScore, aggScores),
			passRate:    normalizeHigherBetter(m.passRate, passRates),
			consistency: normalizeLowerBetter(m.stdDev, stdDevs),
			speed:       normalizeLowerBetter(float64(m.durationMs), durations),
		}
	}
	return result
}

// normalizeHigherBetter maps a value to 0–10 where higher raw values are better.
func normalizeHigherBetter(value float64, all []float64) float64 {
	minVal, maxVal := minMax(all)
	if maxVal == minVal {
		return 5.0
	}
	return ((value - minVal) / (maxVal - minVal)) * 10
}

// normalizeLowerBetter maps a value to 0–10 where lower raw values are better.
func normalizeLowerBetter(value float64, all []float64) float64 {
	minVal, maxVal := minMax(all)
	if maxVal == minVal {
		return 5.0
	}
	return ((maxVal - value) / (maxVal - minVal)) * 10
}

func minMax(values []float64) (float64, float64) {
	if len(values) == 0 {
		return 0, 0
	}
	mn, mx := values[0], values[0]
	for _, v := range values[1:] {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return mn, mx
}

func (e *Engine) buildReason(winner, runnerUp models.ModelScore, results []ModelInput) string {
	if winner.HeuristicScore == runnerUp.HeuristicScore {
		return fmt.Sprintf("Tied with %s; first in evaluation order selected", runnerUp.ModelID)
	}

	// Identify the strongest component advantage
	wAgg := winner.Scores["aggregate_score_normalized"]
	rAgg := runnerUp.Scores["aggregate_score_normalized"]
	wPass := winner.Scores["pass_rate_normalized"]
	rPass := runnerUp.Scores["pass_rate_normalized"]

	var parts []string

	if wAgg > rAgg {
		// Find raw scores for human-readable output
		for _, r := range results {
			if r.ModelID == winner.ModelID {
				parts = append(parts, fmt.Sprintf("Highest aggregate score: %.2f", r.Outcome.Digest.AggregateScore))
				break
			}
		}
	}
	if wPass > rPass {
		for _, r := range results {
			if r.ModelID == winner.ModelID {
				parts = append(parts, fmt.Sprintf("Pass rate: %.0f%%", r.Outcome.Digest.SuccessRate*100))
				break
			}
		}
	}

	if len(parts) == 0 {
		parts = append(parts, "Highest weighted score across all components")
	}

	return fmt.Sprintf("%s (weighted score: %.1f vs %s: %.1f)",
		strings.Join(parts, "; "), winner.HeuristicScore, runnerUp.ModelID, runnerUp.HeuristicScore)
}
