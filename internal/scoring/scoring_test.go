package scoring

import (
	"os"
	"strings"
	"testing"

	"github.com/spboyer/waza/internal/skill"
	"github.com/spboyer/waza/internal/tokens"
	"github.com/stretchr/testify/require"
)

func mkSkill(name, description string) *skill.Skill {
	raw := "---\nname: " + name + "\ndescription: " + description + "\n---\n"
	return &skill.Skill{
		Frontmatter: skill.Frontmatter{Name: name, Description: description},
		RawContent:  raw,
		Tokens:      tokens.Estimate(raw),
		Characters:  len(raw),
		Lines:       strings.Count(raw, "\n") + 1,
	}
}

func readTestSkill(t *testing.T, path string) *skill.Skill {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var s skill.Skill
	require.NoError(t, s.UnmarshalText(data))
	s.Path = path
	return &s
}

func TestAdherenceLevel_AtLeast(t *testing.T) {
	tests := []struct {
		name   string
		level  AdherenceLevel
		target AdherenceLevel
		want   bool
	}{
		{"Low >= Low", AdherenceLow, AdherenceLow, true},
		{"Low >= Medium", AdherenceLow, AdherenceMedium, false},
		{"Low >= MediumHigh", AdherenceLow, AdherenceMediumHigh, false},
		{"Low >= High", AdherenceLow, AdherenceHigh, false},
		{"Medium >= Low", AdherenceMedium, AdherenceLow, true},
		{"Medium >= Medium", AdherenceMedium, AdherenceMedium, true},
		{"Medium >= MediumHigh", AdherenceMedium, AdherenceMediumHigh, false},
		{"MediumHigh >= Low", AdherenceMediumHigh, AdherenceLow, true},
		{"MediumHigh >= MediumHigh", AdherenceMediumHigh, AdherenceMediumHigh, true},
		{"MediumHigh >= High", AdherenceMediumHigh, AdherenceHigh, false},
		{"High >= Low", AdherenceHigh, AdherenceLow, true},
		{"High >= MediumHigh", AdherenceHigh, AdherenceMediumHigh, true},
		{"High >= High", AdherenceHigh, AdherenceHigh, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.level.AtLeast(tt.target))
		})
	}
}

func TestAdherenceLevel_Constants(t *testing.T) {
	require.Equal(t, AdherenceLevel("Low"), AdherenceLow)
	require.Equal(t, AdherenceLevel("Medium"), AdherenceMedium)
	require.Equal(t, AdherenceLevel("Medium-High"), AdherenceMediumHigh)
	require.Equal(t, AdherenceLevel("High"), AdherenceHigh)
}

func TestParseAdherenceLevel(t *testing.T) {
	tests := []struct {
		input   string
		want    AdherenceLevel
		wantErr bool
	}{
		{"low", AdherenceLow, false},
		{"medium", AdherenceMedium, false},
		{"medium-high", AdherenceMediumHigh, false},
		{"high", AdherenceHigh, false},
		{"LOW", AdherenceLow, false},
		{"Medium", AdherenceMedium, false},
		{"Medium-High", AdherenceMediumHigh, false},
		{"HIGH", AdherenceHigh, false},
		{"MEDIUM-HIGH", AdherenceMediumHigh, false},
		{"invalid", AdherenceLow, true},
		{"", AdherenceLow, true},
		{"mega-high", AdherenceLow, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseAdherenceLevel(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				require.Equal(t, AdherenceLow, got, "error case should return AdherenceLow")
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestHeuristicScorer(t *testing.T) {
	scorer := &HeuristicScorer{}
	longPrefix := strings.Repeat("This skill handles complex document workflows and validation steps. ", 3)
	tests := []struct {
		name              string
		description       string
		wantLevel         AdherenceLevel
		wantTriggers      bool
		wantAntiTrig      bool
		wantRouting       bool
		wantMinIssueCount int
	}{
		{
			name:              "low-short-description",
			description:       "Process PDF files for various tasks",
			wantLevel:         AdherenceLow,
			wantMinIssueCount: 1,
		},
		{
			name:              "low-empty-description",
			description:       "",
			wantLevel:         AdherenceLow,
			wantMinIssueCount: 1,
		},
		{
			name:              "low-whitespace-only",
			description:       "   \n\t  \n  ",
			wantLevel:         AdherenceLow,
			wantMinIssueCount: 1,
		},
		{
			name:        "low-no-triggers",
			description: strings.Repeat("This is a skill that does things. ", 5),
			wantLevel:   AdherenceLow,
		},
		{
			name: "medium-triggers-no-anti",
			description: longPrefix + `
USE FOR: "extract PDF text", "rotate PDF", "merge PDFs".`,
			wantLevel:    AdherenceMedium,
			wantTriggers: true,
		},
		{
			name: "medium-triggers-keyword",
			description: longPrefix + `
TRIGGERS: document conversion, format transformation, file processing.`,
			wantLevel:    AdherenceMedium,
			wantTriggers: true,
		},
		{
			name: "medium-high-full",
			description: longPrefix + `
USE FOR: "extract PDF text", "rotate PDF", "merge PDFs".
DO NOT USE FOR: creating PDFs (use document-creator).`,
			wantLevel:    AdherenceMediumHigh,
			wantTriggers: true,
			wantAntiTrig: true,
		},
		{
			name: "medium-high-not-for",
			description: longPrefix + `
USE FOR: "extract PDF text", "rotate PDF", "merge PDFs".
NOT FOR: creating PDFs from scratch. Instead use document-creator.`,
			wantLevel:    AdherenceMediumHigh,
			wantTriggers: true,
			wantAntiTrig: true,
		},
		{
			name: "high-with-routing",
			description: longPrefix + `
**WORKFLOW SKILL** - Process PDF files including text extraction.
USE FOR: "extract PDF text", "rotate PDF".
DO NOT USE FOR: creating PDFs (use document-creator).
INVOKES: pdf-tools MCP for extraction.
FOR SINGLE OPERATIONS: Use pdf-tools directly.`,
			wantLevel:    AdherenceHigh,
			wantTriggers: true,
			wantAntiTrig: true,
			wantRouting:  true,
		},
		{
			name: "high-utility-skill",
			description: longPrefix + `
**UTILITY SKILL** - Format and validate configuration files.
USE FOR: "format config", "validate yaml", "lint json".
DO NOT USE FOR: creating configs from scratch (use config-creator).
INVOKES: format-tools for validation.`,
			wantLevel:    AdherenceHigh,
			wantTriggers: true,
			wantAntiTrig: true,
			wantRouting:  true,
		},
		{
			name: "high-analysis-skill",
			description: longPrefix + `
**ANALYSIS SKILL** - Analyze code quality and patterns.
USE FOR: "review code", "check quality", "analyze patterns".
DO NOT USE FOR: fixing code (use code-fixer).
FOR SINGLE OPERATIONS: Use linter directly for simple checks.`,
			wantLevel:    AdherenceHigh,
			wantTriggers: true,
			wantAntiTrig: true,
			wantRouting:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := mkSkill("test-skill", tt.description)
			result := scorer.Score(sk)

			require.Equal(t, tt.wantLevel, result.Level,
				"expected %s but got %s", tt.wantLevel, result.Level)

			if tt.wantTriggers {
				require.True(t, result.HasTriggers, "expected triggers to be detected")
			}
			if tt.wantAntiTrig {
				require.True(t, result.HasAntiTriggers, "expected anti-triggers to be detected")
			}
			if tt.wantRouting {
				require.True(t, result.HasRoutingClarity, "expected routing clarity to be detected")
			}
			if tt.wantMinIssueCount > 0 {
				require.GreaterOrEqual(t, len(result.Issues), tt.wantMinIssueCount,
					"expected at least %d issues, got %d",
					tt.wantMinIssueCount, len(result.Issues))
			}
		})
	}
}

func TestContainsTriggers(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"USE FOR pattern", `USE FOR: "do thing"`, true},
		{"USE THIS SKILL", `USE THIS SKILL when you need to process files`, true},
		{"TRIGGERS pattern", `TRIGGERS: file processing, data extraction`, true},
		{"Trigger phrases include", `Trigger phrases include: "process data"`, true},
		{"no triggers", `This skill processes data`, false},
		{"empty", ``, false},
		{"use for lowercase", `use for: "do thing"`, true},
		{"triggers lowercase", `triggers: processing files`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, containsAny(tt.text, triggerPatterns))
		})
	}
}

func TestContainsAntiTriggers(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"DO NOT USE FOR", `DO NOT USE FOR: creating PDFs`, true},
		{"NOT FOR", `NOT FOR: editing forms`, true},
		{"Don't use this skill", `Don't use this skill for image editing`, true},
		{"Instead use", `Instead use document-creator for new PDFs`, true},
		{"no anti-triggers", `Process PDF files for various tasks`, false},
		{"empty", ``, false},
		{"do not use for lowercase", `do not use for: creating PDFs`, true},
		{"not for lowercase", `not for: editing forms`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, containsAny(tt.text, antiTriggerPatterns))
		})
	}
}

func TestContainsRoutingClarity(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"INVOKES", `INVOKES: pdf-tools MCP`, true},
		{"FOR SINGLE OPERATIONS", `FOR SINGLE OPERATIONS: Use tools directly`, true},
		{"WORKFLOW SKILL prefix", `**WORKFLOW SKILL** - Do workflows`, true},
		{"UTILITY SKILL prefix", `**UTILITY SKILL** - Utility operations`, true},
		{"ANALYSIS SKILL prefix", `**ANALYSIS SKILL** - Analyze things`, true},
		{"no routing", `Process PDF files`, false},
		{"empty", ``, false},
		{"invokes lowercase", `invokes: pdf-tools`, true},
		{"for single operations lowercase", `for single operations: use directly`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, containsAny(tt.text, routingClarityPatterns))
		})
	}
}

func TestCountPhrasesAfterPattern(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"three quoted triggers",
			`USE FOR: "extract PDF", "rotate PDF", "merge PDFs".`, 3},
		{"five quoted triggers",
			`USE FOR: "a", "b", "c", "d", "e".`, 5},
		{"no USE FOR line", `Just a description`, 0},
		{"USE FOR without quotes",
			`USE FOR: extracting text, rotating pages.`, 2},
		{"empty", ``, 0},
		{"mixed quoted and unquoted",
			`USE FOR: "extract PDF", rotate pages, "merge PDFs".`, 3},
		{"stops before DO NOT USE FOR",
			"USE FOR: \"a\", \"b\".\nDO NOT USE FOR: \"c\".", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, countPhrasesAfterPattern(tt.text, "USE FOR:"))
		})
	}
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantIssue bool
	}{
		{"valid lowercase", "pdf-processor", false},
		{"valid with hyphens", "my-cool-skill", false},
		{"valid single word", "tool", false},
		{"uppercase fails", "PDF-Processor", true},
		{"underscore fails", "pdf_processor", true},
		{"too long", strings.Repeat("a", 65), true},
		{"exactly 64", strings.Repeat("a", 64), false},
		{"empty fails", "", true},
		{"spaces fail", "my skill", true},
		{"numbers ok", "tool-v2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ScoreResult{}
			validateName(tt.input, r)
			if tt.wantIssue {
				require.NotEmpty(t, r.Issues, "expected validation issues for name %q", tt.input)
			} else {
				require.Empty(t, r.Issues, "expected no issues for name %q but got: %v", tt.input, r.Issues)
			}
		})
	}
}

func TestDescriptionLengthCategories(t *testing.T) {
	tests := []struct {
		name    string
		length  int
		wantLow bool
	}{
		{"under 150 — too short", 100, true},
		{"exactly 149 — too short", 149, true},
		{"exactly 150 — acceptable", 150, false},
		{"200 — ideal range", 200, false},
		{"500 — acceptable", 500, false},
		{"1024 — max", 1024, false},
		{"over 1024 — may warn", 1025, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc := strings.Repeat("x", tt.length)
			sk := mkSkill("test-skill", desc)
			result := (&HeuristicScorer{}).Score(sk)
			if tt.wantLow {
				require.Equal(t, AdherenceLow, result.Level,
					"description of length %d should score Low", tt.length)
			}
		})
	}
}

func TestValidateDescriptionLength(t *testing.T) {
	tests := []struct {
		name      string
		length    int
		wantIssue bool
		wantRule  string
	}{
		{"short — error", 50, true, "description-length"},
		{"min — no issue", 150, false, ""},
		{"ideal — no issue", 300, false, ""},
		{"over max — warning", 1100, true, "description-too-long"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ScoreResult{}
			validateDescriptionLength(tt.length, r)
			if tt.wantIssue {
				require.NotEmpty(t, r.Issues)
				require.Equal(t, tt.wantRule, r.Issues[0].Rule)
			} else {
				require.Empty(t, r.Issues)
			}
		})
	}
}

func TestValidateTokenBudget(t *testing.T) {
	tests := []struct {
		name     string
		tokens   int
		wantRule string
	}{
		{"under soft limit", 400, ""},
		{"over soft limit", 600, "token-soft-limit"},
		{"over hard limit", 6000, "token-hard-limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &ScoreResult{}
			validateTokenBudget(tt.tokens, TokenSoftLimit, TokenHardLimit, r)
			if tt.wantRule == "" {
				require.Empty(t, r.Issues)
			} else {
				require.NotEmpty(t, r.Issues)
				require.Equal(t, tt.wantRule, r.Issues[0].Rule)
			}
		})
	}
}

func TestHeuristicScorer_NilSkill(t *testing.T) {
	result := (&HeuristicScorer{}).Score(nil)
	require.NotNil(t, result)
	require.Equal(t, AdherenceLow, result.Level)
	require.NotEmpty(t, result.Issues)
}

func TestHeuristicScorer_DescriptionOverMax(t *testing.T) {
	desc := strings.Repeat("a", 1100) + "\nUSE FOR: \"thing\".\nDO NOT USE FOR: other.\nINVOKES: tools."
	sk := mkSkill("test-skill", desc)
	result := (&HeuristicScorer{}).Score(sk)
	require.NotNil(t, result)
	require.True(t, result.Level.AtLeast(AdherenceMediumHigh))
	var found bool
	for _, iss := range result.Issues {
		if iss.Rule == "description-too-long" {
			found = true
			require.Equal(t, "warning", iss.Severity)
		}
	}
	require.True(t, found, "expected a description-too-long warning issue")
}

func TestHeuristicScorer_RealCodeExplainer(t *testing.T) {
	sk := readTestSkill(t, "../../skills/code-explainer/SKILL.md")
	result := (&HeuristicScorer{}).Score(sk)
	require.Equal(t, AdherenceLow, result.Level,
		"code-explainer should be Low adherence (short desc, no triggers)")
}

func TestHeuristicScorer_RealWaza(t *testing.T) {
	sk := readTestSkill(t, "../../skills/waza/SKILL.md")
	result := (&HeuristicScorer{}).Score(sk)
	require.Equal(t, AdherenceHigh, result.Level,
		"waza skill should be High adherence (has triggers, anti-triggers, routing)")
	require.True(t, result.HasTriggers)
	require.True(t, result.HasAntiTriggers)
	require.True(t, result.HasRoutingClarity)
}

func TestHeuristicScorer_TestdataHigh(t *testing.T) {
	sk := readTestSkill(t, "../../cmd/waza/dev/testdata/high/SKILL.md")
	result := (&HeuristicScorer{}).Score(sk)
	require.Equal(t, AdherenceHigh, result.Level)
	require.True(t, result.HasTriggers)
	require.True(t, result.HasAntiTriggers)
	require.True(t, result.HasRoutingClarity)
}

func TestHeuristicScorer_TestdataValid(t *testing.T) {
	sk := readTestSkill(t, "../../cmd/waza/dev/testdata/valid/SKILL.md")
	result := (&HeuristicScorer{}).Score(sk)
	require.Equal(t, AdherenceMediumHigh, result.Level)
	require.True(t, result.HasTriggers)
	require.True(t, result.HasAntiTriggers)
	require.False(t, result.HasRoutingClarity)
}
