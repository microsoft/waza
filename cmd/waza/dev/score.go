package dev

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spboyer/waza/internal/skill"
)

// AdherenceLevel represents a skill's compliance level.
type AdherenceLevel string

const (
	AdherenceLow        AdherenceLevel = "Low"
	AdherenceMedium     AdherenceLevel = "Medium"
	AdherenceMediumHigh AdherenceLevel = "Medium-High"
	AdherenceHigh       AdherenceLevel = "High"

	tokenSoftLimit = 500
	tokenHardLimit = 5000
)

// adherenceRank maps levels to ordinals for comparison.
var adherenceRank = map[AdherenceLevel]int{
	AdherenceLow:        0,
	AdherenceMedium:     1,
	AdherenceMediumHigh: 2,
	AdherenceHigh:       3,
}

// String returns the string representation of the adherence level.
func (a AdherenceLevel) String() string {
	return string(a)
}

// AtLeast returns true if a is at or above the target level.
func (a AdherenceLevel) AtLeast(target AdherenceLevel) bool {
	return adherenceRank[a] >= adherenceRank[target]
}

// ParseAdherenceLevel converts a string flag value to an AdherenceLevel.
func ParseAdherenceLevel(s string) (AdherenceLevel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return AdherenceLow, nil
	case "medium":
		return AdherenceMedium, nil
	case "medium-high":
		return AdherenceMediumHigh, nil
	case "high":
		return AdherenceHigh, nil
	default:
		return AdherenceLow, fmt.Errorf("invalid adherence level %q: must be low, medium, medium-high, or high", s)
	}
}

// Issue represents a specific compliance problem found.
type Issue struct {
	Rule     string // rule identifier (e.g., "description-length")
	Message  string // human-readable description
	Severity string // "error" or "warning"
}

// ScoreResult holds the complete scoring output.
type ScoreResult struct {
	Level             AdherenceLevel
	Issues            []Issue
	DescriptionLen    int
	HasTriggers       bool
	HasAntiTriggers   bool
	HasRoutingClarity bool
	TriggerCount      int
	AntiTriggerCount  int
}

// Trigger phrase patterns (case-insensitive).
var triggerPatterns = []string{
	"use for:",
	"use this skill",
	"triggers:",
	"trigger phrases include",
}

// Anti-trigger phrase patterns (case-insensitive).
var antiTriggerPatterns = []string{
	"do not use for:",
	"not for:",
	"don't use this skill",
	"instead use",
}

// Routing clarity patterns (case-insensitive).
var routingClarityPatterns = []string{
	"invokes:",
	"for single operations:",
	"**workflow skill**",
	"**utility skill**",
	"**analysis skill**",
}

// Scorer evaluates a skill and returns a score.
type Scorer interface {
	Score(*skill.Skill) *ScoreResult
}

// HeuristicScorer scores skills using pattern-matching heuristics
// (trigger phrases, anti-triggers, routing clarity markers, etc.).
type HeuristicScorer struct{}

// Score implements Scorer.
func (HeuristicScorer) Score(sk *skill.Skill) *ScoreResult {
	result := &ScoreResult{}

	if sk == nil {
		result.Level = AdherenceLow
		result.Issues = append(result.Issues, Issue{
			Rule:     "nil-skill",
			Message:  "Skill is nil",
			Severity: "error",
		})
		return result
	}

	desc := sk.Frontmatter.Description
	name := sk.Frontmatter.Name
	trimmedDesc := strings.TrimSpace(desc)
	result.DescriptionLen = utf8.RuneCountInString(trimmedDesc)

	// Detect triggers
	result.HasTriggers = containsAny(trimmedDesc, triggerPatterns)
	result.TriggerCount = countPhrasesAfterPattern(trimmedDesc, "USE FOR:")

	// Detect anti-triggers
	result.HasAntiTriggers = containsAny(trimmedDesc, antiTriggerPatterns)
	result.AntiTriggerCount = countPhrasesAfterPattern(trimmedDesc, "DO NOT USE FOR:")

	// Detect routing clarity
	result.HasRoutingClarity = containsAny(trimmedDesc, routingClarityPatterns)

	// Validate name
	validateName(name, result)

	// Validate description length
	validateDescriptionLength(result.DescriptionLen, result)

	// Validate token budget
	if sk.Tokens > 0 {
		validateTokenBudget(sk.Tokens, result)
	}

	// Determine adherence level (algorithm from docs/sensei/scoring.md)
	result.Level = computeLevel(result)

	return result
}

// computeLevel applies the scoring algorithm. Description length and
// triggers/anti-triggers/routing determine the AdherenceLevel.
func computeLevel(r *ScoreResult) AdherenceLevel {
	if r.DescriptionLen < 150 || !r.HasTriggers {
		return AdherenceLow
	}
	if !r.HasAntiTriggers {
		return AdherenceMedium
	}
	if !r.HasRoutingClarity {
		return AdherenceMediumHigh
	}
	return AdherenceHigh
}

// containsAny returns true if text contains any of the patterns (case-insensitive).
func containsAny(text string, patterns []string) bool {
	lower := strings.ToLower(text)
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// countPhrasesAfterPattern counts comma-delimited phrases after pat in text.
func countPhrasesAfterPattern(text, pat string) int {
	lower := strings.ToLower(text)
	patLower := strings.ToLower(pat)
	idx := strings.Index(lower, patLower)
	if idx < 0 {
		return 0
	}
	after := text[idx+len(pat):]
	// Only count until the next major section marker or end of text.
	for _, stop := range []string{"DO NOT USE FOR:", "INVOKES:", "FOR SINGLE OPERATIONS:", "\n\n"} {
		if si := strings.Index(strings.ToUpper(after), strings.ToUpper(stop)); si >= 0 {
			after = after[:si]
		}
	}
	segments := strings.Split(after, ",")
	count := 0
	for _, segment := range segments {
		candidate := strings.TrimSpace(segment)
		candidate = strings.TrimRight(candidate, ".")
		candidate = strings.Trim(candidate, "\"'`")
		if candidate == "" {
			continue
		}
		count++
	}
	return count
}

// validateName checks name format rules and appends issues to result.
func validateName(name string, r *ScoreResult) {
	if name == "" {
		r.Issues = append(r.Issues, Issue{
			Rule:     "name-missing",
			Message:  "Frontmatter 'name' field is empty",
			Severity: "error",
		})
		return
	}
	if len(name) > 64 {
		r.Issues = append(r.Issues, Issue{
			Rule:     "name-too-long",
			Message:  fmt.Sprintf("Name is %d chars (max 64)", len(name)),
			Severity: "error",
		})
	}
	for _, c := range name {
		if !unicode.IsLower(c) && c != '-' && !unicode.IsDigit(c) {
			r.Issues = append(r.Issues, Issue{
				Rule:     "name-format",
				Message:  "Name must be lowercase letters, digits, and hyphens only",
				Severity: "error",
			})
			break
		}
	}
}

// validateDescriptionLength appends issues for description length thresholds.
func validateDescriptionLength(length int, r *ScoreResult) {
	if length < 150 {
		r.Issues = append(r.Issues, Issue{
			Rule:     "description-length",
			Message:  fmt.Sprintf("Description is %d chars (need 150+)", length),
			Severity: "error",
		})
	} else if length > 1024 {
		r.Issues = append(r.Issues, Issue{
			Rule:     "description-too-long",
			Message:  fmt.Sprintf("Description is %d chars (max 1024)", length),
			Severity: "warning",
		})
	}
}

// validateTokenBudget checks SKILL.md token counts against soft/hard limits.
func validateTokenBudget(tokenCount int, r *ScoreResult) {
	if tokenCount > tokenHardLimit {
		r.Issues = append(r.Issues, Issue{
			Rule:     "token-hard-limit",
			Message:  fmt.Sprintf("SKILL.md is %d tokens (hard limit %d)", tokenCount, tokenHardLimit),
			Severity: "error",
		})
	} else if tokenCount > tokenSoftLimit {
		r.Issues = append(r.Issues, Issue{
			Rule:     "token-soft-limit",
			Message:  fmt.Sprintf("SKILL.md is %d tokens (soft limit %d)", tokenCount, tokenSoftLimit),
			Severity: "warning",
		})
	}
}
