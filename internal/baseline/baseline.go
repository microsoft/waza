package baseline

import (
	"math"

	"github.com/microsoft/waza/internal/models"
)

// BaselineResult pairs a task's baseline (no skill) and skill-enabled results
// with computed improvement metrics.
type BaselineResult struct {
	TaskName    string               `json:"task_name"`
	Baseline    *models.RunResult    `json:"baseline"`
	WithSkill   *models.RunResult    `json:"with_skill"`
	Improvement float64              `json:"improvement"`
	Breakdown   ImprovementBreakdown `json:"improvement_breakdown"`
}

// ImprovementBreakdown captures per-dimension deltas between baseline and skill runs.
// Positive values mean the skill run was better; negative means worse.
type ImprovementBreakdown struct {
	QualityDelta   float64 `json:"quality_delta"`
	TokenReduction float64 `json:"token_reduction"`
	TurnReduction  float64 `json:"turn_reduction"`
	TimeReduction  float64 `json:"time_reduction"`
	TaskCompletion float64 `json:"task_completion"`
}

// ComputeImprovement calculates the overall improvement score and per-dimension breakdown
// between a baseline run (no skill) and a skill-enabled run.
// Returns a value in [-1, 1] where positive means the skill helped.
func ComputeImprovement(baseline, withSkill *models.RunResult) (float64, ImprovementBreakdown) {
	breakdown := ImprovementBreakdown{}

	// Quality delta: difference in average grader scores
	baselineScore := baseline.ComputeRunScore()
	skillScore := withSkill.ComputeRunScore()
	breakdown.QualityDelta = skillScore - baselineScore

	// Token reduction: negative means fewer tokens (good)
	if baseline.SessionDigest.Usage != nil && withSkill.SessionDigest.Usage != nil {
		baseTok := float64(baseline.SessionDigest.Usage.InputTokens + baseline.SessionDigest.Usage.OutputTokens)
		skillTok := float64(withSkill.SessionDigest.Usage.InputTokens + withSkill.SessionDigest.Usage.OutputTokens)
		if baseTok > 0 {
			breakdown.TokenReduction = (skillTok - baseTok) / baseTok
		}
	}

	// Turn reduction: negative means fewer turns (good)
	if baseline.SessionDigest.Usage != nil && withSkill.SessionDigest.Usage != nil {
		baseTurns := float64(baseline.SessionDigest.Usage.Turns)
		skillTurns := float64(withSkill.SessionDigest.Usage.Turns)
		if baseTurns > 0 {
			breakdown.TurnReduction = (skillTurns - baseTurns) / baseTurns
		}
	}

	// Time reduction: negative means faster (good)
	if baseline.DurationMs > 0 {
		breakdown.TimeReduction = float64(withSkill.DurationMs-baseline.DurationMs) / float64(baseline.DurationMs)
	}

	// Task completion delta: 1 if skill passed but baseline didn't, -1 if opposite, 0 if same
	basePass := statusToCompletion(baseline.Status)
	skillPass := statusToCompletion(withSkill.Status)
	breakdown.TaskCompletion = skillPass - basePass

	// Overall improvement: weighted composite clamped to [-1, 1]
	improvement := computeComposite(breakdown)

	return improvement, breakdown
}

// ComputeFromOutcomes computes BaselineResults for paired TestOutcomes.
func ComputeFromOutcomes(withSkill, baseline *models.TestOutcome) *BaselineResult {
	if len(withSkill.Runs) == 0 || len(baseline.Runs) == 0 {
		return &BaselineResult{
			TaskName: withSkill.DisplayName,
		}
	}

	// Use first run from each for comparison
	improvement, breakdown := ComputeImprovement(&baseline.Runs[0], &withSkill.Runs[0])

	return &BaselineResult{
		TaskName:    withSkill.DisplayName,
		Baseline:    &baseline.Runs[0],
		WithSkill:   &withSkill.Runs[0],
		Improvement: improvement,
		Breakdown:   breakdown,
	}
}

func statusToCompletion(status models.Status) float64 {
	if status == models.StatusPassed {
		return 1.0
	}
	return 0.0
}

// computeComposite produces a [-1, 1] improvement score.
// Quality delta is the primary signal; efficiency metrics are secondary.
func computeComposite(b ImprovementBreakdown) float64 {
	// Quality is dominant (60%), efficiency savings split the rest
	// Token/turn/time reductions are inverted: negative reduction = improvement
	score := b.QualityDelta*0.6 +
		(-b.TokenReduction)*0.15 +
		(-b.TurnReduction)*0.1 +
		(-b.TimeReduction)*0.05 +
		b.TaskCompletion*0.1

	return math.Max(-1.0, math.Min(1.0, score))
}
