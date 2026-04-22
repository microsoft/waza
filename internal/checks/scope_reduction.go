package checks

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/microsoft/waza/internal/skill"
)

// DefaultMinCapabilities is the minimum number of distinct capabilities
// before a scope-reduction warning is emitted.
const DefaultMinCapabilities = 2

// ScopeReductionChecker detects when a SKILL.md has suspiciously few
// capability signals, which may indicate that token-limit compression
// silently removed supported workflows.
type ScopeReductionChecker struct {
	// MinCapabilities overrides DefaultMinCapabilities when > 0.
	MinCapabilities int
}

var _ ComplianceChecker = (*ScopeReductionChecker)(nil)

func (c *ScopeReductionChecker) Name() string { return "scope-reduction" }

// ScopeReductionData holds the structured output.
type ScopeReductionData struct {
	Status            CheckStatus
	UseForCount       int
	HeadingCount      int
	StepSequences     int
	TotalCapabilities int
	Threshold         int
	Details           []string
}

// GetStatus implements StatusHolder.
func (d *ScopeReductionData) GetStatus() CheckStatus { return d.Status }

// useForPattern matches lines containing "USE FOR:" (case-insensitive).
// Captures everything after the colon.
var useForPattern = regexp.MustCompile(`(?im)(?:^|\n)\s*USE\s+FOR\s*:\s*(.+)`)

// doNotUseForPattern matches "DO NOT USE FOR:" or "NOT USE FOR:" lines to exclude.
var doNotUseForPattern = regexp.MustCompile(`(?i)(?:DO\s+)?NOT\s+USE\s+FOR\s*:`)

// headingL2Pattern matches level-2 headings (## Title).
var headingL2Pattern = regexp.MustCompile(`(?m)^##\s+\S`)

// stepSequencePattern detects the start of a numbered procedure (line
// beginning with "1."). Multiple independent sequences (separated by non-list
// content) count as distinct capabilities.
var stepSequencePattern = regexp.MustCompile(`(?m)^1\.\s+\S`)

func (c *ScopeReductionChecker) Check(sk skill.Skill) (*CheckResult, error) {
	threshold := c.MinCapabilities
	if threshold <= 0 {
		threshold = DefaultMinCapabilities
	}

	body := skillBodyContent(sk)
	content := sk.RawContent

	// 1. Count USE FOR items (from description or body)
	useForCount := countUseForItems(content)

	// 2. Count level-2 headings in the body
	headingCount := len(headingL2Pattern.FindAllString(body, -1))

	// 3. Count distinct numbered-step sequences
	stepSequences := len(stepSequencePattern.FindAllString(body, -1))

	// Total distinct capability signals — use the maximum of the three
	// indicators since they represent different facets of capability scope.
	total := maxInt(useForCount, headingCount, stepSequences)

	var details []string
	if useForCount > 0 {
		details = append(details, fmt.Sprintf("%d USE FOR item(s)", useForCount))
	}
	if headingCount > 0 {
		details = append(details, fmt.Sprintf("%d level-2 heading(s)", headingCount))
	}
	if stepSequences > 0 {
		details = append(details, fmt.Sprintf("%d numbered procedure(s)", stepSequences))
	}

	data := &ScopeReductionData{
		UseForCount:       useForCount,
		HeadingCount:      headingCount,
		StepSequences:     stepSequences,
		TotalCapabilities: total,
		Threshold:         threshold,
		Details:           details,
	}

	if total < threshold {
		data.Status = StatusWarning
		summary := fmt.Sprintf(
			"Low capability scope: %d signal(s) detected (minimum %d recommended) — possible token-limit compression loss",
			total, threshold,
		)
		return &CheckResult{
			Name:    "scope-reduction",
			Passed:  false,
			Summary: summary,
			Data:    data,
		}, nil
	}

	data.Status = StatusOK
	summary := fmt.Sprintf(
		"Capability scope: %d signal(s) detected (%s)",
		total, strings.Join(details, ", "),
	)
	return &CheckResult{
		Name:    "scope-reduction",
		Passed:  true,
		Summary: summary,
		Data:    data,
	}, nil
}

// countUseForItems parses "USE FOR:" lines and counts the comma-separated
// items within each match. Lines containing "DO NOT USE FOR" are excluded.
func countUseForItems(content string) int {
	matches := useForPattern.FindAllStringSubmatch(content, -1)
	count := 0
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		// Skip lines that are actually "DO NOT USE FOR:"
		fullMatch := m[0]
		if doNotUseForPattern.MatchString(fullMatch) {
			continue
		}
		items := strings.Split(m[1], ",")
		for _, item := range items {
			if strings.TrimSpace(item) != "" {
				count++
			}
		}
	}
	return count
}

func maxInt(a, b, c int) int {
	if b > a {
		a = b
	}
	if c > a {
		a = c
	}
	return a
}
