package dev

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spboyer/waza/internal/skill"
)

// SkillsBench-derived thresholds.
const (
	advisoryModuleMax       = 4    // 2-3 modules optimal; 4+ degrades
	advisoryComplexityLimit = 2500 // "comprehensive" hurts by -2.9pp
	advisorySparseMin       = 500  // below 800 tokens may be too sparse
	advisorySparseMax       = 800
	advisoryCodeBlockMax    = 50 // over-specificity threshold
)

// headingPattern matches Markdown ## or ### headings.
var headingPattern = regexp.MustCompile(`(?m)^#{2,3}\s+\S`)

// numberedStepPattern matches numbered workflow steps (e.g., "1. Do X").
var numberedStepPattern = regexp.MustCompile(`(?m)^\s*\d+\.\s+\S`)

// Advisory represents a single SkillsBench advisory finding.
type Advisory struct {
	Check   string // check identifier
	Message string // human-readable explanation
	Kind    string // "positive", "warning", or "info"
}

// AdvisoryResult holds all advisory findings.
type AdvisoryResult struct {
	Advisories []Advisory
}

// AdvisoryScorer runs SkillsBench research-backed checks.
type AdvisoryScorer struct{}

// Score evaluates a skill against SkillsBench research findings.
func (AdvisoryScorer) Score(sk *skill.Skill) *AdvisoryResult {
	r := &AdvisoryResult{}
	if sk == nil {
		return r
	}

	checkModuleCount(sk, r)
	checkComplexity(sk, r)
	checkSparseness(sk, r)
	checkProceduralContent(sk, r)
	checkOverSpecificity(sk, r)

	return r
}

// checkModuleCount warns when a skill references 4+ modules.
func checkModuleCount(sk *skill.Skill, r *AdvisoryResult) {
	count := countModules(sk.Body)
	if count >= advisoryModuleMax {
		r.Advisories = append(r.Advisories, Advisory{
			Check:   "module-count",
			Message: fmt.Sprintf("Skill has %d modules (2-3 optimal per SkillsBench; 4+ degrades performance)", count),
			Kind:    "warning",
		})
	}
}

// checkComplexity warns when token count exceeds the complexity threshold.
func checkComplexity(sk *skill.Skill, r *AdvisoryResult) {
	if sk.Tokens > advisoryComplexityLimit {
		r.Advisories = append(r.Advisories, Advisory{
			Check:   "complexity",
			Message: fmt.Sprintf("Skill is %d tokens (>%d is 'comprehensive' — hurts by -2.9pp per SkillsBench)", sk.Tokens, advisoryComplexityLimit),
			Kind:    "warning",
		})
	}
}

// checkSparseness flags skills in the 500-800 token range as potentially too sparse.
func checkSparseness(sk *skill.Skill, r *AdvisoryResult) {
	if sk.Tokens >= advisorySparseMin && sk.Tokens <= advisorySparseMax {
		r.Advisories = append(r.Advisories, Advisory{
			Check:   "negative-delta-risk",
			Message: fmt.Sprintf("Skill is %d tokens (500-800 range risks negative delta per SkillsBench)", sk.Tokens),
			Kind:    "info",
		})
	}
}

// checkProceduralContent detects numbered workflow steps (positive signal).
func checkProceduralContent(sk *skill.Skill, r *AdvisoryResult) {
	steps := countNumberedSteps(sk.Body)
	if steps >= 3 {
		r.Advisories = append(r.Advisories, Advisory{
			Check:   "procedural-content",
			Message: fmt.Sprintf("Skill has %d numbered steps (procedural content correlates with +18.8pp per SkillsBench)", steps),
			Kind:    "positive",
		})
	}
}

// checkOverSpecificity warns when a skill has too many code blocks.
func checkOverSpecificity(sk *skill.Skill, r *AdvisoryResult) {
	count := countCodeBlocks(sk.Body)
	if count > advisoryCodeBlockMax {
		r.Advisories = append(r.Advisories, Advisory{
			Check:   "over-specificity",
			Message: fmt.Sprintf("Skill has %d code blocks (>%d may be over-specific — too detailed can hurt)", count, advisoryCodeBlockMax),
			Kind:    "warning",
		})
	}
}

// countModules counts ## and ### headings in body as module indicators.
func countModules(body string) int {
	return len(headingPattern.FindAllString(body, -1))
}

// countNumberedSteps counts lines matching numbered list patterns.
func countNumberedSteps(body string) int {
	return len(numberedStepPattern.FindAllString(body, -1))
}

// countCodeBlocks counts fenced code blocks (``` delimiters).
func countCodeBlocks(body string) int {
	count := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			count++
		}
	}
	// Each block has an opening and closing fence.
	return count / 2
}
