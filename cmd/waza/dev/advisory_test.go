package dev

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spboyer/waza/internal/skill"
	"github.com/stretchr/testify/require"
)

func TestAdvisoryScorer_NilSkill(t *testing.T) {
	r := (AdvisoryScorer{}).Score(nil)
	require.NotNil(t, r)
	require.Empty(t, r.Advisories)
}

func TestAdvisoryScorer_ModuleCount(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantAdv bool
	}{
		{"2 modules — no warning", "\n## Module A\ntext\n## Module B\ntext\n", false},
		{"3 modules — no warning", "\n## A\n## B\n## C\n", false},
		{"4 modules — warning", "\n## A\n## B\n## C\n## D\n", true},
		{"5 modules with h3", "\n## A\n### B\n## C\n### D\n## E\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := &skill.Skill{Body: tt.body, Tokens: 1000}
			r := (AdvisoryScorer{}).Score(sk)
			found := hasAdvisory(r, "module-count")
			require.Equal(t, tt.wantAdv, found, "module-count advisory")
		})
	}
}

func TestAdvisoryScorer_Complexity(t *testing.T) {
	tests := []struct {
		name    string
		tokens  int
		wantAdv bool
	}{
		{"under limit", 2000, false},
		{"at limit", 2500, false},
		{"over limit", 2501, true},
		{"way over", 5000, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := &skill.Skill{Tokens: tt.tokens}
			r := (AdvisoryScorer{}).Score(sk)
			found := hasAdvisory(r, "complexity")
			require.Equal(t, tt.wantAdv, found, "complexity advisory")
		})
	}
}

func TestAdvisoryScorer_NegativeDeltaRisk(t *testing.T) {
	tests := []struct {
		name    string
		tokens  int
		wantAdv bool
	}{
		{"below range", 499, false},
		{"at lower bound", 500, true},
		{"in range", 650, true},
		{"at upper bound", 800, true},
		{"above range", 801, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := &skill.Skill{Tokens: tt.tokens}
			r := (AdvisoryScorer{}).Score(sk)
			found := hasAdvisory(r, "negative-delta-risk")
			require.Equal(t, tt.wantAdv, found, "negative-delta-risk advisory")
		})
	}
}

func TestAdvisoryScorer_ProceduralContent(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantAdv bool
	}{
		{"no steps", "Just some text\nno numbered items", false},
		{"2 steps — not enough", "1. First\n2. Second\n", false},
		{"3 steps — positive", "1. First\n2. Second\n3. Third\n", true},
		{"many steps", "1. A\n2. B\n3. C\n4. D\n5. E\n", true},
		{"indented steps", "  1. A\n  2. B\n  3. C\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := &skill.Skill{Body: tt.body, Tokens: 1000}
			r := (AdvisoryScorer{}).Score(sk)
			found := hasAdvisory(r, "procedural-content")
			require.Equal(t, tt.wantAdv, found, "procedural-content advisory")
			if found {
				adv := getAdvisory(r, "procedural-content")
				require.Equal(t, "positive", adv.Kind)
			}
		})
	}
}

func TestAdvisoryScorer_OverSpecificity(t *testing.T) {
	// Build body with N code blocks.
	makeBlocks := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString("```go\nfmt.Println(\"hi\")\n```\n")
		}
		return b.String()
	}

	tests := []struct {
		name    string
		blocks  int
		wantAdv bool
	}{
		{"10 blocks — fine", 10, false},
		{"50 blocks — fine", 50, false},
		{"51 blocks — warning", 51, true},
		{"100 blocks — warning", 100, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := &skill.Skill{Body: makeBlocks(tt.blocks), Tokens: 1000}
			r := (AdvisoryScorer{}).Score(sk)
			found := hasAdvisory(r, "over-specificity")
			require.Equal(t, tt.wantAdv, found, "over-specificity advisory")
		})
	}
}

func TestCountModules(t *testing.T) {
	require.Equal(t, 0, countModules("just text"))
	require.Equal(t, 2, countModules("## A\ntext\n## B\ntext"))
	require.Equal(t, 3, countModules("## A\n### B\n## C\n"))
	// h1 headings don't count
	require.Equal(t, 0, countModules("# Title\n"))
}

func TestCountNumberedSteps(t *testing.T) {
	require.Equal(t, 0, countNumberedSteps("no steps"))
	require.Equal(t, 3, countNumberedSteps("1. A\n2. B\n3. C"))
	require.Equal(t, 2, countNumberedSteps("  1. indented\n  2. also\n"))
}

func TestCountCodeBlocks(t *testing.T) {
	require.Equal(t, 0, countCodeBlocks("no blocks"))
	require.Equal(t, 1, countCodeBlocks("```\ncode\n```"))
	require.Equal(t, 2, countCodeBlocks("```go\na\n```\n```js\nb\n```"))
	// Odd fences count as partial
	require.Equal(t, 1, countCodeBlocks("```\ncode\n```\n```"))
}

func TestDisplayAdvisory(t *testing.T) {
	r := &AdvisoryResult{
		Advisories: []Advisory{
			{Check: "complexity", Message: "too complex", Kind: "warning"},
			{Check: "procedural-content", Message: "has steps", Kind: "positive"},
			{Check: "negative-delta-risk", Message: "sparse risk", Kind: "info"},
		},
	}
	var buf bytes.Buffer
	DisplayAdvisory(&buf, r)
	out := buf.String()
	require.Contains(t, out, "SkillsBench Advisory")
	require.Contains(t, out, "⚠️")
	require.Contains(t, out, "✅")
	require.Contains(t, out, "ℹ️")
	require.Contains(t, out, "too complex")
	require.Contains(t, out, "has steps")
}

func TestDisplayAdvisory_Empty(t *testing.T) {
	var buf bytes.Buffer
	DisplayAdvisory(&buf, &AdvisoryResult{})
	require.Empty(t, buf.String())
}

// helpers

func hasAdvisory(r *AdvisoryResult, check string) bool {
	for _, a := range r.Advisories {
		if a.Check == check {
			return true
		}
	}
	return false
}

func getAdvisory(r *AdvisoryResult, check string) Advisory {
	for _, a := range r.Advisories {
		if a.Check == check {
			return a
		}
	}
	return Advisory{}
}
