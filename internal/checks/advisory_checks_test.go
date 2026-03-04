package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/skill"
	"github.com/stretchr/testify/require"
)

func TestModuleCountChecker(t *testing.T) {
	tmp := t.TempDir()
	refsDir := filepath.Join(tmp, "references")
	require.NoError(t, os.MkdirAll(refsDir, 0o755))

	tests := []struct {
		name    string
		mdCount int
		status  CheckStatus
		passed  bool
	}{
		{"zero modules", 0, StatusOK, true},
		{"one module", 1, StatusOK, true},
		{"two modules optimal", 2, StatusOptimal, true},
		{"three modules optimal", 3, StatusOptimal, true},
		{"four modules warning", 4, StatusWarning, false},
		{"five modules warning", 5, StatusWarning, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, _ := os.ReadDir(refsDir)
			for _, e := range entries {
				_ = os.Remove(filepath.Join(refsDir, e.Name()))
			}

			for i := range tt.mdCount {
				f := filepath.Join(refsDir, "ref"+string(rune('A'+i))+".md")
				require.NoError(t, os.WriteFile(f, []byte("# ref"), 0o644))
			}

			sk := skill.Skill{Path: filepath.Join(tmp, "SKILL.md")}
			checker := &ModuleCountChecker{}
			result, err := checker.Check(sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
			data, ok := result.Data.(*ModuleCountData)
			require.True(t, ok)
			require.Equal(t, tt.status, data.Status)
			require.Equal(t, tt.mdCount, data.Count)
		})
	}
}

func TestComplexityChecker(t *testing.T) {
	tests := []struct {
		name           string
		tokens         int
		mdCount        int
		classification string
		status         CheckStatus
		passed         bool
	}{
		{"compact", 100, 0, "compact", StatusOK, true},
		{"detailed", 300, 2, "detailed", StatusOptimal, true},
		{"comprehensive by tokens", 600, 1, "comprehensive", StatusWarning, false},
		{"comprehensive by modules", 300, 4, "comprehensive", StatusWarning, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			refsDir := filepath.Join(tmp, "references")
			require.NoError(t, os.MkdirAll(refsDir, 0o755))
			for i := range tt.mdCount {
				f := filepath.Join(refsDir, "ref"+string(rune('A'+i))+".md")
				require.NoError(t, os.WriteFile(f, []byte("# ref"), 0o644))
			}

			sk := skill.Skill{
				Tokens: tt.tokens,
				Path:   filepath.Join(tmp, "SKILL.md"),
			}
			checker := &ComplexityChecker{}
			result, err := checker.Check(sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
			data, ok := result.Data.(*ComplexityData)
			require.True(t, ok)
			require.Equal(t, tt.classification, data.Classification)
			require.Equal(t, tt.status, data.Status)
		})
	}
}

func TestNegativeDeltaRiskChecker(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		passed bool
		risks  int
	}{
		{
			name:   "clean content",
			raw:    "This is a normal skill description with no issues.",
			passed: true,
			risks:  0,
		},
		{
			name:   "conflicting paths",
			raw:    "Do X first. But alternatively you could do Y.",
			passed: false,
			risks:  1,
		},
		{
			name:   "duplicate step 1 blocks",
			raw:    "Step 1: Do this\nStep 2: Then this\nStep 1: Or start here",
			passed: false,
			risks:  1,
		},
		{
			name:   "excessive constraints",
			raw:    "You must not do A. Never do B. Always do C. It is forbidden to do D. Prohibited from E. You must not do F.",
			passed: false,
			risks:  1,
		},
		{
			name:   "multiple risks",
			raw:    "But alternatively try X.\nStep 1: first\nStep 1: second\nYou must not, never, always, forbidden, prohibited, must not do it.",
			passed: false,
			risks:  3,
		},
	}

	checker := &NegativeDeltaRiskChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := skill.Skill{RawContent: tt.raw}
			result, err := checker.Check(sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
			data, ok := result.Data.(*NegativeDeltaRiskData)
			require.True(t, ok)
			require.Len(t, data.Risks, tt.risks)
		})
	}
}

func TestProceduralContentChecker(t *testing.T) {
	tests := []struct {
		name   string
		desc   string
		passed bool
	}{
		{
			name:   "has action verb",
			desc:   "This skill extracts data from PDF files",
			passed: true,
		},
		{
			name:   "has procedure keyword",
			desc:   "A workflow for handling requests step by step",
			passed: true,
		},
		{
			name:   "no procedural language",
			desc:   "A general purpose tool for data",
			passed: false,
		},
		{
			name:   "empty description",
			desc:   "",
			passed: false,
		},
	}

	checker := &ProceduralContentChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := skill.Skill{
				Frontmatter: skill.Frontmatter{Description: tt.desc},
			}
			result, err := checker.Check(sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed, "desc: %q", tt.desc)
		})
	}
}

func TestOverSpecificityChecker(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		passed     bool
		categories int
	}{
		{
			name:       "clean content",
			raw:        "This skill processes markdown files and generates output.",
			passed:     true,
			categories: 0,
		},
		{
			name:       "unix path",
			raw:        "Files are stored in /usr/local/bin for access.",
			passed:     false,
			categories: 1,
		},
		{
			name:       "windows path",
			raw:        `Install to C:\Program Files\MyApp`,
			passed:     false,
			categories: 1,
		},
		{
			name:       "IP address",
			raw:        "Connect to 192.168.1.1 for the database.",
			passed:     false,
			categories: 1,
		},
		{
			name:       "hardcoded URL",
			raw:        "Download from https://example.com/releases/latest",
			passed:     false,
			categories: 1,
		},
		{
			name:       "doc URL allowed",
			raw:        "See https://github.com/owner/repo for details.",
			passed:     true,
			categories: 0,
		},
		{
			name:       "port number",
			raw:        "The server runs on :8080 by default.",
			passed:     false,
			categories: 1,
		},
		{
			name:       "multiple categories",
			raw:        "Use /home/user/app on 192.168.1.1:3000",
			passed:     false,
			categories: 3, // unix path, IP, port
		},
	}

	checker := &OverSpecificityChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := skill.Skill{RawContent: tt.raw}
			result, err := checker.Check(sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
			data, ok := result.Data.(*OverSpecificityData)
			require.True(t, ok)
			require.Len(t, data.Categories, tt.categories)
		})
	}
}

// Test #75: CrossModelDensityChecker
func TestCrossModelDensityChecker(t *testing.T) {
	tests := []struct {
		name          string
		description   string
		passed        bool
		status        CheckStatus
		wordCount     int
		hasActionVerb bool
	}{
		{
			name:          "optimal - short with action verb",
			description:   "Use this skill to process files quickly",
			passed:        true,
			status:        StatusOptimal,
			wordCount:     7,
			hasActionVerb: true,
		},
		{
			name:          "warning - over 60 words",
			description:   strings.Repeat("word ", 65),
			passed:        false,
			status:        StatusWarning,
			wordCount:     65,
			hasActionVerb: false,
		},
		{
			name:          "no action verb - still passes",
			description:   "This skill is useful for processing files",
			passed:        true,
			status:        StatusOK,
			wordCount:     7,
			hasActionVerb: false,
		},
		{
			name:        "empty description",
			description: "",
			passed:      true,
			status:      StatusOK,
		},
		{
			name:          "exactly 60 words - OK",
			description:   "Use " + strings.Repeat("word ", 59),
			passed:        true,
			status:        StatusOptimal,
			wordCount:     60,
			hasActionVerb: true,
		},
		{
			name:          "61 words - warning",
			description:   strings.Repeat("word ", 61),
			passed:        false,
			status:        StatusWarning,
			wordCount:     61,
			hasActionVerb: false,
		},
	}

	checker := &CrossModelDensityChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := skill.Skill{Frontmatter: skill.Frontmatter{Description: tt.description}}
			result, err := checker.Check(sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
			data, ok := result.Data.(*CrossModelDensityData)
			require.True(t, ok)
			require.Equal(t, tt.status, data.Status)
		})
	}
}

// Test #76: BodyStructureChecker
func TestBodyStructureChecker(t *testing.T) {
	tests := []struct {
		name       string
		rawContent string
		passed     bool
		status     CheckStatus
	}{
		{
			name: "complete structure",
			rawContent: `---
name: test
---
## Example
Here's an example:
` + "```bash\necho hello\n```" + `

## Troubleshooting
Common errors and how to fix them.
`,
			passed: true,
			status: StatusOK,
		},
		{
			name: "missing examples",
			rawContent: `---
name: test
---
` + "```bash\necho hello\n```" + `
## Error Handling
Handle errors carefully.
`,
			passed: false,
			status: StatusWarning,
		},
		{
			name: "missing error handling",
			rawContent: `---
name: test
---
## Example
` + "```bash\necho hello\n```",
			passed: false,
			status: StatusWarning,
		},
		{
			name: "no actionable instructions",
			rawContent: `---
name: test
---
Just plain text with no code blocks or numbered steps.
## Example
Another plain section.
## Note
Be careful.
`,
			passed: false,
			status: StatusWarning,
		},
		{
			name: "numbered steps count as actionable",
			rawContent: `---
name: test
---
1. First step
2. Second step
## Example
Steps example
## Important
Handle errors.
`,
			passed: true,
			status: StatusOK,
		},
	}

	checker := &BodyStructureChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := skill.Skill{RawContent: tt.rawContent}
			result, err := checker.Check(sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed, "result.Summary: %s", result.Summary)
			data, ok := result.Data.(*BodyStructureData)
			require.True(t, ok)
			require.Equal(t, tt.status, data.Status)
		})
	}
}

// Test #77: ProgressiveDisclosureChecker
func TestProgressiveDisclosureChecker(t *testing.T) {
	tests := []struct {
		name            string
		rawContent      string
		passed          bool
		status          CheckStatus
		expectBodyWarn  bool
		expectBlockWarn bool
	}{
		{
			name: "compact content",
			rawContent: `---
name: test
---
Short content with ` + "```bash\necho hello\n```",
			passed: true,
			status: StatusOK,
		},
		{
			name:           "body over 500 lines",
			rawContent:     strings.Repeat("line\n", 501),
			passed:         false,
			status:         StatusWarning,
			expectBodyWarn: true,
		},
		{
			name: "large code block",
			rawContent: `---
name: test
---
` + "```bash\n" + strings.Repeat("echo line\n", 55) + "```",
			passed:          false,
			status:          StatusWarning,
			expectBlockWarn: true,
		},
		{
			name: "code block exactly 50 lines - OK",
			rawContent: `---
name: test
---
` + "```bash\n" + strings.Repeat("echo line\n", 48) + "```",
			passed: true,
			status: StatusOK,
		},
		{
			name: "multiple small code blocks - OK",
			rawContent: `---
name: test
---
` + "```bash\necho 1\n```\n```bash\necho 2\n```",
			passed: true,
			status: StatusOK,
		},
	}

	checker := &ProgressiveDisclosureChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sk := skill.Skill{RawContent: tt.rawContent}
			result, err := checker.Check(sk)
			require.NoError(t, err)
			require.Equal(t, tt.passed, result.Passed)
			data, ok := result.Data.(*ProgressiveDisclosureData)
			require.True(t, ok)
			require.Equal(t, tt.status, data.Status)
		})
	}
}
