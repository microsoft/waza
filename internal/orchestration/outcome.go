package orchestration

import (
	"math"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/statistics"
)

// ComputeTestStats computes aggregate statistics for a set of run results.
func ComputeTestStats(runs []models.RunResult) *models.TestStats {
	if len(runs) == 0 {
		return nil
	}

	passed := 0
	failed := 0
	errored := 0
	totalScore := 0.0
	totalWeightedScore := 0.0
	minScore := math.Inf(1)
	maxScore := math.Inf(-1)
	totalDuration := int64(0)
	scores := make([]float64, 0, len(runs))

	for _, run := range runs {
		score := run.ComputeRunScore()
		weightedScore := run.ComputeWeightedRunScore()
		totalScore += score
		totalWeightedScore += weightedScore
		scores = append(scores, score)

		if score < minScore {
			minScore = score
		}
		if score > maxScore {
			maxScore = score
		}

		switch run.Status {
		case models.StatusPassed:
			passed++
		case models.StatusFailed:
			failed++
		case models.StatusError:
			errored++
		case models.StatusSkipped:
			// skipped — don't count as passed or failed
		default:
			if run.AllValidationsPassed() {
				passed++
			} else {
				failed++
			}
		}

		totalDuration += run.DurationMs
	}

	stdDev := models.ComputeStdDev(scores)

	stats := &models.TestStats{
		PassRate:         float64(passed) / float64(len(runs)),
		PassedRuns:       passed,
		FailedRuns:       failed,
		ErrorRuns:        errored,
		TotalRuns:        len(runs),
		AvgScore:         totalScore / float64(len(runs)),
		AvgWeightedScore: totalWeightedScore / float64(len(runs)),
		MinScore:         minScore,
		MaxScore:         maxScore,
		StdDevScore:      stdDev,
		ScoreVariance:    stdDev * stdDev,
		AvgDurationMs:    totalDuration / int64(len(runs)),
	}

	stats.Flaky = stats.PassRate > 0 && stats.PassRate < 1
	if stats.Flaky {
		minorityOutcomes := min(passed, len(runs)-passed)
		stats.FlakinessPercent = (float64(minorityOutcomes) / float64(len(runs))) * 100
	}

	if len(runs) >= 2 {
		weightedScores := make([]float64, 0, len(runs))
		for _, run := range runs {
			weightedScores = append(weightedScores, run.ComputeWeightedRunScore())
		}

		ci := statistics.BootstrapCI(weightedScores, 0.95)
		stats.BootstrapCI = &ci
		stats.CI95Lo = ci.Lower
		stats.CI95Hi = ci.Upper

		sig := statistics.IsSignificant(ci)
		stats.IsSignificant = &sig
	}

	return stats
}

// BuildDigest computes an OutcomeDigest from test outcomes. durationMs is
// the total wall-clock duration to store in the digest. runsPerTest controls
// whether digest-level bootstrap CI is computed (requires > 1).
func BuildDigest(testOutcomes []models.TestOutcome, durationMs int64, runsPerTest int) models.OutcomeDigest {
	succeeded := 0
	failed := 0
	errors := 0
	skipped := 0

	for _, to := range testOutcomes {
		switch to.Status {
		case models.StatusPassed:
			succeeded++
		case models.StatusFailed:
			failed++
		case models.StatusError:
			errors++
		case models.StatusSkipped:
			skipped++
		}
	}

	totalTests := len(testOutcomes)
	successRate := 0.0
	if totalTests > 0 {
		successRate = float64(succeeded) / float64(totalTests)
	}

	aggregateScore := computeAggregateScore(testOutcomes)
	weightedScore := computeWeightedAggregateScore(testOutcomes)
	digestMin, digestMax, digestStdDev := computeDigestScoreStats(testOutcomes)
	groupStats := computeGroupStats(testOutcomes)

	digest := models.OutcomeDigest{
		TotalTests:     totalTests,
		Succeeded:      succeeded,
		Failed:         failed,
		Errors:         errors,
		Skipped:        skipped,
		SuccessRate:    successRate,
		AggregateScore: aggregateScore,
		WeightedScore:  weightedScore,
		MinScore:       digestMin,
		MaxScore:       digestMax,
		StdDev:         digestStdDev,
		DurationMs:     durationMs,
		Groups:         groupStats,
		Usage:          aggregateUsageFromOutcomes(testOutcomes),
	}

	if runsPerTest > 1 && len(testOutcomes) > 0 {
		perTestScores := make([]float64, 0, len(testOutcomes))
		for _, to := range testOutcomes {
			if to.Stats != nil {
				perTestScores = append(perTestScores, to.Stats.AvgWeightedScore)
			}
		}
		if len(perTestScores) >= 2 {
			ci := statistics.BootstrapCI(perTestScores, 0.95)
			sig := statistics.IsSignificant(ci)
			digest.Statistics = &models.StatisticalSummary{
				BootstrapCI:   ci,
				IsSignificant: sig,
			}
		}
	}

	return digest
}

func computeAggregateScore(testOutcomes []models.TestOutcome) float64 {
	if len(testOutcomes) == 0 {
		return 0.0
	}
	totalScore := 0.0
	for _, to := range testOutcomes {
		if to.Stats != nil {
			totalScore += to.Stats.AvgScore
		}
	}
	return totalScore / float64(len(testOutcomes))
}

func computeWeightedAggregateScore(testOutcomes []models.TestOutcome) float64 {
	if len(testOutcomes) == 0 {
		return 0.0
	}
	totalScore := 0.0
	for _, to := range testOutcomes {
		if to.Stats != nil {
			totalScore += to.Stats.AvgWeightedScore
		}
	}
	return totalScore / float64(len(testOutcomes))
}

func computeDigestScoreStats(testOutcomes []models.TestOutcome) (float64, float64, float64) {
	if len(testOutcomes) == 0 {
		return 0.0, 0.0, 0.0
	}
	scores := make([]float64, 0, len(testOutcomes))
	minScore := 1.0
	maxScore := 0.0
	for _, to := range testOutcomes {
		s := 0.0
		if to.Stats != nil {
			s = to.Stats.AvgScore
		}
		scores = append(scores, s)
		if s < minScore {
			minScore = s
		}
		if s > maxScore {
			maxScore = s
		}
	}
	return minScore, maxScore, models.ComputeStdDev(scores)
}

func computeGroupStats(outcomes []models.TestOutcome) []models.GroupStats {
	type accumulator struct {
		passed     int
		total      int
		scoreTotal float64
		scoreCount int
	}

	groups := make(map[string]*accumulator)
	var order []string

	for _, to := range outcomes {
		if to.Group == "" {
			continue
		}
		acc, exists := groups[to.Group]
		if !exists {
			acc = &accumulator{}
			groups[to.Group] = acc
			order = append(order, to.Group)
		}
		acc.total++
		if to.Status == models.StatusPassed {
			acc.passed++
		}
		if to.Stats != nil {
			acc.scoreTotal += to.Stats.AvgScore
			acc.scoreCount++
		}
	}

	if len(groups) == 0 {
		return nil
	}

	result := make([]models.GroupStats, 0, len(order))
	for _, name := range order {
		acc := groups[name]
		avg := 0.0
		if acc.scoreCount > 0 {
			avg = acc.scoreTotal / float64(acc.scoreCount)
		}
		result = append(result, models.GroupStats{
			Name:     name,
			Passed:   acc.passed,
			Total:    acc.total,
			AvgScore: avg,
		})
	}
	return result
}

func aggregateUsageFromOutcomes(testOutcomes []models.TestOutcome) *models.UsageStats {
	var allUsage []*models.UsageStats
	for _, to := range testOutcomes {
		for _, run := range to.Runs {
			if run.SessionDigest.Usage != nil {
				allUsage = append(allUsage, run.SessionDigest.Usage)
			}
		}
	}
	return models.AggregateUsageStats(allUsage)
}

// RegradeOutcome produces a new EvaluationOutcome by replacing test outcomes
// in the original with the graded ones and recomputing stats and digest.
func RegradeOutcome(original *models.EvaluationOutcome, gradedOutcomes []models.TestOutcome, judgeModel string) *models.EvaluationOutcome {
	for i := range gradedOutcomes {
		gradedOutcomes[i].Stats = ComputeTestStats(gradedOutcomes[i].Runs)
	}

	setup := original.Setup
	if judgeModel != "" {
		setup.JudgeModel = judgeModel
	}

	runsPerTest := setup.RunsPerTest
	if runsPerTest <= 0 {
		runsPerTest = 1
	}

	return &models.EvaluationOutcome{
		RunID:        original.RunID,
		SkillTested:  original.SkillTested,
		BenchName:    original.BenchName,
		Timestamp:    original.Timestamp,
		Setup:        setup,
		Digest:       BuildDigest(gradedOutcomes, original.Digest.DurationMs, runsPerTest),
		Measures:     make(map[string]models.MeasureResult),
		TestOutcomes: gradedOutcomes,
		Metadata:     original.Metadata,
	}
}
