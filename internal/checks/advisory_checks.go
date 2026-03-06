package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/microsoft/waza/internal/skill"
)

// ModuleCountChecker counts .md files in the skill's references/ directory.
type ModuleCountChecker struct{}

var _ ComplianceChecker = (*ModuleCountChecker)(nil)

func (*ModuleCountChecker) Name() string { return "module-count" }

// ModuleCountData holds the structured output of a module count check.
type ModuleCountData struct {
	Status CheckStatus
	Count  int
}

// GetStatus implements StatusHolder.
func (d *ModuleCountData) GetStatus() CheckStatus { return d.Status }

func (*ModuleCountChecker) Check(sk skill.Skill) (*CheckResult, error) {
	count := 0
	if sk.Path != "" {
		refsDir := filepath.Join(filepath.Dir(sk.Path), "references")
		count = countMDFiles(refsDir)
	}

	var status CheckStatus
	var summary string
	passed := true

	switch {
	case count >= 4:
		status = StatusWarning
		summary = fmt.Sprintf("Found %d reference modules (4+ may have diminishing returns; consolidation recommended)", count)
		passed = false
	case count >= 2:
		status = StatusOptimal
		summary = fmt.Sprintf("Found %d reference modules (2-3 is optimal)", count)
	default:
		status = StatusOK
		summary = fmt.Sprintf("Found %d reference module(s)", count)
	}

	return &CheckResult{
		Name:    "module-count",
		Passed:  passed,
		Summary: summary,
		Data:    &ModuleCountData{Status: status, Count: count},
	}, nil
}

// countMDFiles recursively counts .md files in the given directory.
func countMDFiles(dir string) int {
	count := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			count++
		}
		return nil
	})
	return count
}

// ComplexityChecker classifies the skill based on token count and module count.
type ComplexityChecker struct{}

var _ ComplianceChecker = (*ComplexityChecker)(nil)

func (*ComplexityChecker) Name() string { return "complexity" }

// ComplexityData holds the structured output of a complexity check.
type ComplexityData struct {
	Status         CheckStatus
	Classification string // "compact", "detailed", or "comprehensive"
	TokenCount     int
	ModuleCount    int
}

// GetStatus implements StatusHolder.
func (d *ComplexityData) GetStatus() CheckStatus { return d.Status }

func (*ComplexityChecker) Check(sk skill.Skill) (*CheckResult, error) {
	tokens := sk.Tokens
	modules := 0
	if sk.Path != "" {
		refsDir := filepath.Join(filepath.Dir(sk.Path), "references")
		modules = countMDFiles(refsDir)
	}

	var status CheckStatus
	var classification string
	passed := true

	switch {
	case tokens > 500 || modules >= 4:
		status = StatusWarning
		classification = "comprehensive"
		passed = false
	case tokens >= 200 && modules >= 1 && modules <= 3:
		status = StatusOptimal
		classification = "detailed"
	default:
		status = StatusOK
		classification = "compact"
	}

	summary := fmt.Sprintf("Complexity: %s (%d tokens, %d modules)", classification, tokens, modules)

	return &CheckResult{
		Name:    "complexity",
		Passed:  passed,
		Summary: summary,
		Data:    &ComplexityData{Status: status, Classification: classification, TokenCount: tokens, ModuleCount: modules},
	}, nil
}

// NegativeDeltaRiskChecker scans SKILL.md content for patterns that degrade performance.
type NegativeDeltaRiskChecker struct{}

var _ ComplianceChecker = (*NegativeDeltaRiskChecker)(nil)

func (*NegativeDeltaRiskChecker) Name() string { return "negative-delta-risk" }

// NegativeDeltaRiskData holds the structured output.
type NegativeDeltaRiskData struct {
	Status CheckStatus
	Risks  []string
}

// GetStatus implements StatusHolder.
func (d *NegativeDeltaRiskData) GetStatus() CheckStatus { return d.Status }

// conflictingPathPhrases are phrases indicating conflicting procedure paths.
var conflictingPathPhrases = []string{
	"but alternatively",
	"however you can also",
	"another approach is",
}

// constraintKeywords are excessive-constraint indicators.
var constraintKeywords = []string{
	"must not",
	"never",
	"always",
	"forbidden",
	"prohibited",
}

// duplicateStepPattern matches "Step 1:" blocks; assumes content is lowercase
var duplicateStepPattern = regexp.MustCompile(`(?m)^step\s+1\s*:`)

func (*NegativeDeltaRiskChecker) Check(sk skill.Skill) (*CheckResult, error) {
	content := strings.ToLower(sk.RawContent)
	var risks []string

	// Check for conflicting procedure paths
	for _, phrase := range conflictingPathPhrases {
		if strings.Contains(content, phrase) {
			risks = append(risks, "conflicting procedure paths detected")
			break
		}
	}

	// Check for duplicate procedures (more than one "Step 1:" block)
	matches := duplicateStepPattern.FindAllString(content, -1)
	if len(matches) > 1 {
		risks = append(risks, fmt.Sprintf("duplicate procedures (%d 'Step 1:' blocks found)", len(matches)))
	}

	// Check for excessive constraints (more than 5 constraint keywords)
	constraintCount := 0
	for _, kw := range constraintKeywords {
		constraintCount += strings.Count(content, kw)
	}
	if constraintCount > 5 {
		risks = append(risks, fmt.Sprintf("excessive constraints (%d constraint keywords found)", constraintCount))
	}

	if len(risks) > 0 {
		return &CheckResult{
			Name:    "negative-delta-risk",
			Passed:  false,
			Summary: fmt.Sprintf("Negative delta risk patterns detected: %s", strings.Join(risks, "; ")),
			Data:    &NegativeDeltaRiskData{Status: StatusWarning, Risks: risks},
		}, nil
	}
	return &CheckResult{
		Name:    "negative-delta-risk",
		Passed:  true,
		Summary: "No negative delta risk patterns detected",
		Data:    &NegativeDeltaRiskData{Status: StatusOK},
	}, nil
}

// ProceduralContentChecker checks whether the description contains procedural language.
type ProceduralContentChecker struct{}

var _ ComplianceChecker = (*ProceduralContentChecker)(nil)

func (*ProceduralContentChecker) Name() string { return "procedural-content" }

// ProceduralContentData holds the structured output.
type ProceduralContentData struct {
	Status          CheckStatus
	HasActionVerbs  bool
	HasProcedureKWs bool
}

// GetStatus implements StatusHolder.
func (d *ProceduralContentData) GetStatus() CheckStatus { return d.Status }

var actionVerbs = []string{
	"process", "extract", "deploy", "configure", "analyze",
	"create", "build", "run", "execute", "validate",
	"check", "test", "install", "set up", "implement",
}

var procedureKeywords = []string{
	"step", "first", "then", "next", "finally",
	"workflow", "pipeline", "procedure", "when",
	"if…then", "if...then", "after", "before",
}

func (*ProceduralContentChecker) Check(sk skill.Skill) (*CheckResult, error) {
	desc := strings.ToLower(strings.TrimSpace(sk.Frontmatter.Description))

	hasAction := containsAnyWord(desc, actionVerbs)
	hasProc := containsAnyWord(desc, procedureKeywords)

	if !hasAction && !hasProc {
		return &CheckResult{
			Name:    "procedural-content",
			Passed:  false,
			Summary: "Description lacks procedural language (no action verbs or procedure keywords found)",
			Data:    &ProceduralContentData{Status: StatusWarning, HasActionVerbs: false, HasProcedureKWs: false},
		}, nil
	}
	return &CheckResult{
		Name:    "procedural-content",
		Passed:  true,
		Summary: "Description contains procedural language",
		Data:    &ProceduralContentData{Status: StatusOK, HasActionVerbs: hasAction, HasProcedureKWs: hasProc},
	}, nil
}

// containsAnyWord checks if the text contains any of the given terms.
func containsAnyWord(text string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(text, t) {
			return true
		}
	}
	return false
}

// OverSpecificityChecker detects hardcoded, instance-specific content.
type OverSpecificityChecker struct{}

var _ ComplianceChecker = (*OverSpecificityChecker)(nil)

func (*OverSpecificityChecker) Name() string { return "over-specificity" }

// OverSpecificityData holds the structured output.
type OverSpecificityData struct {
	Status     CheckStatus
	Categories []string
}

// GetStatus implements StatusHolder.
func (d *OverSpecificityData) GetStatus() CheckStatus { return d.Status }

// Patterns for over-specificity detection.
var (
	unixPathPrefixes   = []string{"/usr/", "/etc/", "/home/", "/var/", "/opt/"}
	windowsPathPattern = regexp.MustCompile(`(?i)[A-Z]:\\`)
	ipAddressPattern   = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	portPattern        = regexp.MustCompile(`:\d{4,5}\b`)
	// URL pattern: http(s)://something with a path, excluding known doc domains
	urlPattern = regexp.MustCompile(`https?://[^\s"'<>]+/[^\s"'<>]+`)
	docDomains = []string{"github.com", "arxiv.org", "docs.", "learn.microsoft.com"}
)

func (*OverSpecificityChecker) Check(sk skill.Skill) (*CheckResult, error) {
	content := strings.ToLower(sk.RawContent)
	var categories []string

	// Check absolute Unix paths
	for _, prefix := range unixPathPrefixes {
		if strings.Contains(content, prefix) {
			categories = append(categories, "absolute Unix paths")
			break
		}
	}

	// Check absolute Windows paths
	if windowsPathPattern.MatchString(content) {
		categories = append(categories, "absolute Windows paths")
	}

	// Check IP addresses
	if ipAddressPattern.MatchString(content) {
		categories = append(categories, "IP addresses")
	}

	// Check hardcoded URLs with paths (excluding doc domains)
	urls := urlPattern.FindAllString(content, -1)
	hasHardcodedURL := false
	for _, u := range urls {
		isDoc := false
		for _, domain := range docDomains {
			if strings.Contains(u, domain) {
				isDoc = true
				break
			}
		}
		if !isDoc {
			hasHardcodedURL = true
			break
		}
	}
	if hasHardcodedURL {
		categories = append(categories, "hardcoded URLs with paths")
	}

	// Check specific port numbers
	if portPattern.MatchString(content) {
		categories = append(categories, "specific port numbers")
	}

	if len(categories) > 0 {
		return &CheckResult{
			Name:    "over-specificity",
			Passed:  false,
			Summary: fmt.Sprintf("Over-specificity detected: %s", strings.Join(categories, ", ")),
			Data:    &OverSpecificityData{Status: StatusWarning, Categories: categories},
		}, nil
	}
	return &CheckResult{
		Name:    "over-specificity",
		Passed:  true,
		Summary: "No over-specificity patterns detected",
		Data:    &OverSpecificityData{Status: StatusOK},
	}, nil
}

// CrossModelDensityChecker checks description word count and action verb usage.
type CrossModelDensityChecker struct{}

var _ ComplianceChecker = (*CrossModelDensityChecker)(nil)

func (*CrossModelDensityChecker) Name() string { return "cross-model-density" }

// CrossModelDensityData holds the structured output.
type CrossModelDensityData struct {
	Status           CheckStatus
	WordCount        int
	HasActionVerb    bool
	ActionVerbIssues []string
}

// GetStatus implements StatusHolder.
func (d *CrossModelDensityData) GetStatus() CheckStatus { return d.Status }

var commonActionVerbs = []string{
	"use", "when", "help", "enable", "provide", "support", "create",
	"build", "run", "execute", "analyze", "process", "extract", "deploy",
	"configure", "validate", "check", "test", "install", "implement",
}

func (*CrossModelDensityChecker) Check(sk skill.Skill) (*CheckResult, error) {
	desc := strings.TrimSpace(sk.Frontmatter.Description)
	if desc == "" {
		return &CheckResult{
			Name:    "cross-model-density",
			Passed:  true,
			Summary: "No description to check",
			Data:    &CrossModelDensityData{Status: StatusOK},
		}, nil
	}

	// Count words
	words := strings.Fields(desc)
	wordCount := len(words)

	// Check if first sentence leads with action verb (heuristic)
	firstSentence := strings.Split(desc, ".")[0]
	firstWord := ""
	firstSentenceWords := strings.Fields(firstSentence)
	if len(firstSentenceWords) > 0 {
		firstWord = strings.ToLower(strings.TrimSpace(firstSentenceWords[0]))
		firstWord = strings.TrimRight(firstWord, ":;,.!?")
	}
	hasActionVerb := false
	for _, verb := range commonActionVerbs {
		if firstWord == verb {
			hasActionVerb = true
			break
		}
	}

	var issues []string
	status := StatusOK
	passed := true

	if wordCount > 60 {
		issues = append(issues, fmt.Sprintf("word count is %d (>60 may reduce cross-model effectiveness)", wordCount))
		status = StatusWarning
		passed = false
	}

	if !hasActionVerb {
		issues = append(issues, "first sentence doesn't lead with action verb (reduces clarity)")
	}

	if len(issues) > 0 {
		return &CheckResult{
			Name:    "cross-model-density",
			Passed:  passed,
			Summary: fmt.Sprintf("Advisory 16: %s", strings.Join(issues, "; ")),
			Data:    &CrossModelDensityData{Status: status, WordCount: wordCount, HasActionVerb: hasActionVerb, ActionVerbIssues: issues},
		}, nil
	}

	return &CheckResult{
		Name:    "cross-model-density",
		Passed:  true,
		Summary: "Description density is optimal for cross-model use",
		Data:    &CrossModelDensityData{Status: StatusOptimal, WordCount: wordCount, HasActionVerb: hasActionVerb},
	}, nil
}

// BodyStructureChecker scans SKILL.md body for actionable structure.
type BodyStructureChecker struct{}

var _ ComplianceChecker = (*BodyStructureChecker)(nil)

func (*BodyStructureChecker) Name() string { return "body-structure" }

// BodyStructureData holds the structured output.
type BodyStructureData struct {
	Status           CheckStatus
	HasExamples      bool
	HasCodeBlocks    bool
	HasErrorHandling bool
	Findings         []string
}

// GetStatus implements StatusHolder.
func (d *BodyStructureData) GetStatus() CheckStatus { return d.Status }

var examplePatterns = []string{
	"## example", "### example", "**example", "for example:",
}

var errorHandlingPatterns = []string{
	"## error", "error handling", "## troubleshooting", "troubleshooting",
	"common issues", "known limitations",
	"warnings", "caveats", "note:", "important:",
}

func (*BodyStructureChecker) Check(sk skill.Skill) (*CheckResult, error) {
	content := strings.ToLower(skillBodyContent(sk))

	hasCodeBlocks := strings.Contains(content, "```")
	hasNumberedSteps := regexp.MustCompile(`(?m)^\s*\d+\.\s+`).MatchString(content)

	hasExamples := false
	for _, pattern := range examplePatterns {
		if strings.Contains(content, strings.ToLower(pattern)) {
			hasExamples = true
			break
		}
	}

	hasErrorHandling := false
	for _, pattern := range errorHandlingPatterns {
		if strings.Contains(content, pattern) {
			hasErrorHandling = true
			break
		}
	}

	var findings []string
	hasActionable := hasCodeBlocks || hasNumberedSteps

	if !hasActionable {
		findings = append(findings, "body lacks actionable instructions (no code blocks, numbered steps, or commands)")
	}
	if !hasExamples {
		findings = append(findings, "no examples section found")
	}
	if !hasErrorHandling {
		findings = append(findings, "no error handling or troubleshooting section found")
	}

	status := StatusOK
	passed := true

	if len(findings) > 0 {
		status = StatusWarning
		passed = false
	}

	summary := "Advisory 17: body structure quality"
	if len(findings) > 0 {
		summary = fmt.Sprintf("%s — %s", summary, strings.Join(findings, "; "))
	}

	return &CheckResult{
		Name:    "body-structure",
		Passed:  passed,
		Summary: summary,
		Data:    &BodyStructureData{Status: status, HasExamples: hasExamples, HasCodeBlocks: hasCodeBlocks, HasErrorHandling: hasErrorHandling, Findings: findings},
	}, nil
}

// ProgressiveDisclosureChecker flags large inline content.
type ProgressiveDisclosureChecker struct{}

var _ ComplianceChecker = (*ProgressiveDisclosureChecker)(nil)

func (*ProgressiveDisclosureChecker) Name() string { return "progressive-disclosure" }

// ProgressiveDisclosureData holds the structured output.
type ProgressiveDisclosureData struct {
	Status          CheckStatus
	BodyLines       int
	LargeCodeBlocks int
	Recommendations []string
}

// GetStatus implements StatusHolder.
func (d *ProgressiveDisclosureData) GetStatus() CheckStatus { return d.Status }

func (*ProgressiveDisclosureChecker) Check(sk skill.Skill) (*CheckResult, error) {
	content := skillBodyContent(sk)
	// Trim a single trailing newline so a 500-line file isn't reported as 501.
	trimmedContent := strings.TrimSuffix(content, "\n")
	lines := strings.Split(trimmedContent, "\n")
	bodyLines := len(lines)

	largeBlocks := 0
	inFence := false
	blockLines := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				if blockLines > 50 {
					largeBlocks++
				}
				inFence = false
				blockLines = 0
			} else {
				inFence = true
				blockLines = 0
			}
			continue
		}
		if inFence {
			blockLines++
		}
	}
	if inFence && blockLines > 50 {
		largeBlocks++
	}

	var recommendations []string
	status := StatusOK
	passed := true

	if bodyLines > 500 {
		recommendations = append(recommendations, fmt.Sprintf("SKILL.md body is %d lines (>500 lines reduces scannability; consider moving detail to references/)", bodyLines))
		status = StatusWarning
		passed = false
	}

	if largeBlocks > 0 {
		recommendations = append(recommendations, fmt.Sprintf("%d code block(s) exceed 50 lines (suggest moving to references/)", largeBlocks))
		status = StatusWarning
		passed = false
	}

	if len(recommendations) > 0 {
		return &CheckResult{
			Name:    "progressive-disclosure",
			Passed:  passed,
			Summary: fmt.Sprintf("Advisory 18: progressive disclosure — %s", strings.Join(recommendations, "; ")),
			Data:    &ProgressiveDisclosureData{Status: status, BodyLines: bodyLines, LargeCodeBlocks: largeBlocks, Recommendations: recommendations},
		}, nil
	}

	return &CheckResult{
		Name:    "progressive-disclosure",
		Passed:  true,
		Summary: "Content structure supports progressive disclosure",
		Data:    &ProgressiveDisclosureData{Status: StatusOK, BodyLines: bodyLines, LargeCodeBlocks: largeBlocks},
	}, nil
}

func skillBodyContent(sk skill.Skill) string {
	if sk.Body != "" {
		return sk.Body
	}
	if !strings.HasPrefix(sk.RawContent, "---") {
		return sk.RawContent
	}

	rest := sk.RawContent[3:]
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return sk.RawContent
	}
	return rest[idx+4:]
}
