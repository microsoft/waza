package recommend

import (
	"math"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func makeOutcome(aggScore float64, successRate float64, stdDev float64, durationMs int64) *models.EvalOutcome {
	return &models.EvalOutcome{
		Digest: models.EvalDigest{
			AggregateScore: aggScore,
			SuccessRate:    successRate,
			StdDev:         stdDev,
			DurationMs:     durationMs,
		},
	}
}

func TestRecommend_TwoModels(t *testing.T) {
	engine := NewEngine()
	results := []ModelInput{
		{ModelID: "gpt-4o", Outcome: makeOutcome(7.2, 0.85, 1.2, 2300)},
		{ModelID: "claude-sonnet-4", Outcome: makeOutcome(8.5, 0.95, 0.8, 2600)},
	}

	rec := engine.Recommend(results)
	require.NotNil(t, rec)
	if rec.RecommendedModel != "claude-sonnet-4" {
		t.Errorf("expected claude-sonnet-4, got %s", rec.RecommendedModel)
	}
	if rec.HeuristicScore <= 0 {
		t.Errorf("expected positive heuristic score, got %f", rec.HeuristicScore)
	}
	if rec.WinnerMarginPct <= 0 {
		t.Errorf("expected positive margin, got %f", rec.WinnerMarginPct)
	}
	if len(rec.ModelScores) != 2 {
		t.Errorf("expected 2 model scores, got %d", len(rec.ModelScores))
	}
	if rec.ModelScores[0].Rank != 1 || rec.ModelScores[1].Rank != 2 {
		t.Errorf("expected ranks 1 and 2, got %d and %d", rec.ModelScores[0].Rank, rec.ModelScores[1].Rank)
	}
}

func TestRecommend_SingleModel(t *testing.T) {
	engine := NewEngine()
	results := []ModelInput{
		{ModelID: "gpt-4o", Outcome: makeOutcome(7.2, 0.85, 1.2, 2300)},
	}

	rec := engine.Recommend(results)
	if rec != nil {
		t.Errorf("expected nil recommendation for single model, got %+v", rec)
	}
}

func TestRecommend_AllNilOutcomes(t *testing.T) {
	engine := NewEngine()
	results := []ModelInput{
		{ModelID: "gpt-4o", Outcome: nil},
		{ModelID: "claude-sonnet-4", Outcome: nil},
	}

	rec := engine.Recommend(results)
	if rec != nil {
		t.Errorf("expected nil recommendation when all outcomes nil, got %+v", rec)
	}
}

func TestRecommend_OneNilOutcome(t *testing.T) {
	engine := NewEngine()
	results := []ModelInput{
		{ModelID: "gpt-4o", Outcome: makeOutcome(7.2, 0.85, 1.2, 2300)},
		{ModelID: "claude-sonnet-4", Outcome: nil},
	}

	rec := engine.Recommend(results)
	if rec != nil {
		t.Errorf("expected nil when fewer than 2 valid outcomes, got %+v", rec)
	}
}

func TestRecommend_TiedScores(t *testing.T) {
	engine := NewEngine()
	results := []ModelInput{
		{ModelID: "model-a", Outcome: makeOutcome(8.0, 0.90, 1.0, 2000)},
		{ModelID: "model-b", Outcome: makeOutcome(8.0, 0.90, 1.0, 2000)},
	}

	rec := engine.Recommend(results)
	require.NotNil(t, rec)
	// First in input order wins ties (stable sort)
	if rec.RecommendedModel != "model-a" {
		t.Errorf("expected model-a (first in order) for tie, got %s", rec.RecommendedModel)
	}
	if rec.WinnerMarginPct != 0 {
		t.Errorf("expected 0 margin for tie, got %f", rec.WinnerMarginPct)
	}
}

func TestRecommend_ThreeModels(t *testing.T) {
	engine := NewEngine()
	results := []ModelInput{
		{ModelID: "model-a", Outcome: makeOutcome(6.0, 0.70, 1.5, 3000)},
		{ModelID: "model-b", Outcome: makeOutcome(9.0, 0.95, 0.5, 2500)},
		{ModelID: "model-c", Outcome: makeOutcome(7.5, 0.85, 1.0, 2000)},
	}

	rec := engine.Recommend(results)
	require.NotNil(t, rec)
	if rec.RecommendedModel != "model-b" {
		t.Errorf("expected model-b as winner, got %s", rec.RecommendedModel)
	}
	if len(rec.ModelScores) != 3 {
		t.Errorf("expected 3 model scores, got %d", len(rec.ModelScores))
	}
	// Verify ranks are sequential
	for i, ms := range rec.ModelScores {
		if ms.Rank != i+1 {
			t.Errorf("expected rank %d, got %d for %s", i+1, ms.Rank, ms.ModelID)
		}
	}
}

func TestRecommend_WeightsApplied(t *testing.T) {
	engine := NewEngine()

	// model-a: higher aggregate score but slower and less consistent
	// model-b: lower aggregate but faster and more consistent
	results := []ModelInput{
		{ModelID: "model-a", Outcome: makeOutcome(9.0, 0.80, 2.0, 5000)},
		{ModelID: "model-b", Outcome: makeOutcome(7.0, 0.90, 0.5, 1000)},
	}

	rec := engine.Recommend(results)
	require.NotNil(t, rec)

	// Verify weights are set correctly
	if rec.Weights.AggregateScore != 0.40 {
		t.Errorf("expected aggregate weight 0.40, got %f", rec.Weights.AggregateScore)
	}
	if rec.Weights.PassRate != 0.30 {
		t.Errorf("expected pass rate weight 0.30, got %f", rec.Weights.PassRate)
	}
	if rec.Weights.Consistency != 0.20 {
		t.Errorf("expected consistency weight 0.20, got %f", rec.Weights.Consistency)
	}
	if rec.Weights.Speed != 0.10 {
		t.Errorf("expected speed weight 0.10, got %f", rec.Weights.Speed)
	}

	// Verify component scores exist for all models
	for _, ms := range rec.ModelScores {
		if _, ok := ms.Scores["aggregate_score_normalized"]; !ok {
			t.Errorf("missing aggregate_score_normalized for %s", ms.ModelID)
		}
		if _, ok := ms.Scores["pass_rate_normalized"]; !ok {
			t.Errorf("missing pass_rate_normalized for %s", ms.ModelID)
		}
		if _, ok := ms.Scores["consistency_normalized"]; !ok {
			t.Errorf("missing consistency_normalized for %s", ms.ModelID)
		}
		if _, ok := ms.Scores["speed_normalized"]; !ok {
			t.Errorf("missing speed_normalized for %s", ms.ModelID)
		}
	}
}

func TestNormalize_HigherBetter(t *testing.T) {
	all := []float64{2.0, 5.0, 8.0}

	// Min value → 0
	if v := normalizeHigherBetter(2.0, all); v != 0 {
		t.Errorf("expected 0 for min, got %f", v)
	}
	// Max value → 10
	if v := normalizeHigherBetter(8.0, all); v != 10 {
		t.Errorf("expected 10 for max, got %f", v)
	}
	// Mid value → 5
	if v := normalizeHigherBetter(5.0, all); v != 5 {
		t.Errorf("expected 5 for mid, got %f", v)
	}
}

func TestNormalize_LowerBetter(t *testing.T) {
	all := []float64{1.0, 3.0, 5.0}

	// Min value (best) → 10
	if v := normalizeLowerBetter(1.0, all); v != 10 {
		t.Errorf("expected 10 for min (best), got %f", v)
	}
	// Max value (worst) → 0
	if v := normalizeLowerBetter(5.0, all); v != 0 {
		t.Errorf("expected 0 for max (worst), got %f", v)
	}
	// Mid value → 5
	if v := normalizeLowerBetter(3.0, all); v != 5 {
		t.Errorf("expected 5 for mid, got %f", v)
	}
}

func TestNormalize_AllEqual(t *testing.T) {
	all := []float64{5.0, 5.0, 5.0}

	if v := normalizeHigherBetter(5.0, all); v != 5.0 {
		t.Errorf("expected 5.0 for all-equal higher-better, got %f", v)
	}
	if v := normalizeLowerBetter(5.0, all); v != 5.0 {
		t.Errorf("expected 5.0 for all-equal lower-better, got %f", v)
	}
}

func TestRecommend_ZeroScores(t *testing.T) {
	engine := NewEngine()
	results := []ModelInput{
		{ModelID: "model-a", Outcome: makeOutcome(0, 0, 0, 0)},
		{ModelID: "model-b", Outcome: makeOutcome(0, 0, 0, 0)},
	}

	rec := engine.Recommend(results)
	require.NotNil(t, rec)
	// Both should have equal scores; first in order wins
	if rec.RecommendedModel != "model-a" {
		t.Errorf("expected model-a for tied zero scores, got %s", rec.RecommendedModel)
	}
}

func TestRecommend_ComponentScoresInRange(t *testing.T) {
	engine := NewEngine()
	results := []ModelInput{
		{ModelID: "model-a", Outcome: makeOutcome(3.0, 0.60, 2.0, 5000)},
		{ModelID: "model-b", Outcome: makeOutcome(9.0, 0.95, 0.3, 1500)},
		{ModelID: "model-c", Outcome: makeOutcome(6.0, 0.80, 1.0, 3000)},
	}

	rec := engine.Recommend(results)
	require.NotNil(t, rec)

	for _, ms := range rec.ModelScores {
		for key, score := range ms.Scores {
			if score < 0 || score > 10 {
				t.Errorf("%s: %s score %.1f out of range [0, 10]", ms.ModelID, key, score)
			}
		}
		if ms.HeuristicScore < 0 || ms.HeuristicScore > 10 {
			t.Errorf("%s: heuristic score %.1f out of range [0, 10]", ms.ModelID, ms.HeuristicScore)
		}
	}
}

func TestRecommend_EmptyResults(t *testing.T) {
	engine := NewEngine()
	rec := engine.Recommend(nil)
	if rec != nil {
		t.Errorf("expected nil for empty results, got %+v", rec)
	}

	rec = engine.Recommend([]ModelInput{})
	if rec != nil {
		t.Errorf("expected nil for zero-length results, got %+v", rec)
	}
}

func TestRecommend_HeuristicScoreMax(t *testing.T) {
	// Weighted sum of components (each 0–10) with weights summing to 1.0
	// should never exceed 10.0
	engine := NewEngine()
	results := []ModelInput{
		{ModelID: "best", Outcome: makeOutcome(10.0, 1.0, 0.0, 100)},
		{ModelID: "worst", Outcome: makeOutcome(0.0, 0.0, 5.0, 10000)},
	}

	rec := engine.Recommend(results)
	require.NotNil(t, rec)
	for _, ms := range rec.ModelScores {
		if ms.HeuristicScore > 10.0 {
			t.Errorf("%s heuristic score %f exceeds 10.0", ms.ModelID, ms.HeuristicScore)
		}
	}
}

// TestRecommend_MarginCalculation verifies the margin percentage computation.
func TestRecommend_MarginCalculation(t *testing.T) {
	engine := NewEngine()
	// Use 3 models so normalization produces non-extreme values for runner-up
	results := []ModelInput{
		{ModelID: "model-a", Outcome: makeOutcome(9.0, 0.95, 0.5, 1000)},
		{ModelID: "model-b", Outcome: makeOutcome(7.0, 0.80, 1.0, 2000)},
		{ModelID: "model-c", Outcome: makeOutcome(5.0, 0.65, 1.5, 3000)},
	}

	rec := engine.Recommend(results)
	require.NotNil(t, rec)
	if rec.WinnerMarginPct <= 0 {
		t.Errorf("expected positive margin, got %f", rec.WinnerMarginPct)
	}
	if math.IsNaN(rec.WinnerMarginPct) || math.IsInf(rec.WinnerMarginPct, 0) {
		t.Errorf("margin is NaN or Inf: %f", rec.WinnerMarginPct)
	}
}

// TestRecommend_MarginZeroRunnerUp verifies margin is 0 when runner-up scores 0.
func TestRecommend_MarginZeroRunnerUp(t *testing.T) {
	engine := NewEngine()
	results := []ModelInput{
		{ModelID: "model-a", Outcome: makeOutcome(10.0, 1.0, 0.0, 1000)},
		{ModelID: "model-b", Outcome: makeOutcome(0.0, 0.0, 5.0, 10000)},
	}

	rec := engine.Recommend(results)
	require.NotNil(t, rec)
	// When runner-up score is 0, margin can't be computed as percentage
	if math.IsNaN(rec.WinnerMarginPct) || math.IsInf(rec.WinnerMarginPct, 0) {
		t.Errorf("margin should not be NaN or Inf, got %f", rec.WinnerMarginPct)
	}
}

// TestRecommend_DuplicateModelIDs verifies that duplicate ModelIDs in input
// produce distinct scores for each position rather than collapsing into one entry.
func TestRecommend_DuplicateModelIDs(t *testing.T) {
	engine := NewEngine()
	// Same ModelID but different outcomes — must not collapse.
	results := []ModelInput{
		{ModelID: "gpt-4o", Outcome: makeOutcome(9.0, 0.95, 0.5, 1000)},
		{ModelID: "gpt-4o", Outcome: makeOutcome(3.0, 0.40, 2.5, 5000)},
	}

	rec := engine.Recommend(results)
	require.NotNil(t, rec)
	if len(rec.ModelScores) != 2 {
		t.Fatalf("expected 2 model scores, got %d", len(rec.ModelScores))
	}
	// Scores must differ because the outcomes differ.
	if rec.ModelScores[0].HeuristicScore == rec.ModelScores[1].HeuristicScore {
		t.Errorf("duplicate ModelIDs with different outcomes should produce different scores, both got %.1f",
			rec.ModelScores[0].HeuristicScore)
	}
	// Winner should be the first entry (higher agg/pass/consistency/speed).
	if rec.ModelScores[0].HeuristicScore != 10.0 {
		t.Errorf("expected winner score 10.0 (best across all axes), got %.1f", rec.ModelScores[0].HeuristicScore)
	}
	if rec.ModelScores[1].HeuristicScore != 0.0 {
		t.Errorf("expected loser score 0.0 (worst across all axes), got %.1f", rec.ModelScores[1].HeuristicScore)
	}
}
