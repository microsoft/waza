package checks

import (
	"fmt"

	"github.com/spboyer/waza/internal/scoring"
	"github.com/spboyer/waza/internal/skill"
)

// ComplianceScoreChecker validates skill frontmatter quality using heuristic scoring.
type ComplianceScoreChecker struct {
	Scorer           scoring.Scorer
	TokenLimit       int // overrides the default token hard limit when > 0
	WarningThreshold int // overrides the default token warning threshold when > 0
}

// ComplianceScoreData holds the structured output of a compliance score check.
type ComplianceScoreData struct {
	Score *scoring.ScoreResult
	Level scoring.AdherenceLevel
}

var _ ComplianceChecker = (*ComplianceScoreChecker)(nil)

func (c *ComplianceScoreChecker) Name() string { return "compliance-score" }

func (c *ComplianceScoreChecker) Check(sk skill.Skill) (*CheckResult, error) {
	scorer := c.Scorer
	if scorer == nil {
		scorer = &scoring.HeuristicScorer{TokenSoftLimit: c.WarningThreshold, TokenLimit: c.TokenLimit}
	}

	score := scorer.Score(&sk)

	return &CheckResult{
		Name:    c.Name(),
		Passed:  score.Level.AtLeast(scoring.AdherenceMediumHigh),
		Summary: fmt.Sprintf("Compliance Score: %s", score.Level),
		Data:    &ComplianceScoreData{Score: score, Level: score.Level},
	}, nil
}

// Score is a convenience wrapper that returns the typed data directly.
func (c *ComplianceScoreChecker) Score(sk skill.Skill) (*ComplianceScoreData, error) {
	result, err := c.Check(sk)
	if err != nil {
		return nil, err
	}
	d, ok := result.Data.(*ComplianceScoreData)
	if !ok {
		err = fmt.Errorf("unexpected data type: %T", result.Data)
	}
	return d, err
}
