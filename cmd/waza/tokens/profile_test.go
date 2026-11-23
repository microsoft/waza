package tokens

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile_TextFormat(t *testing.T) {
	cmd := newProfileCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetArgs([]string{"testdata/profile"})
	require.NoError(t, cmd.Execute())

	output := out.String()
	require.Contains(t, output, "📊 profile")
	require.Contains(t, output, "sections")
	require.Contains(t, output, "code blocks")
	require.Contains(t, output, "detailed ✓")
	require.NotContains(t, output, "⚠️  token count")
}

func TestProfile_JSONFormat(t *testing.T) {
	cmd := newProfileCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetArgs([]string{"--format", "json", "testdata/profile"})
	require.NoError(t, cmd.Execute())

	var result SkillProfile
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))

	require.Equal(t, "profile", result.Name)
	require.Greater(t, result.Tokens, 0)
	require.Equal(t, 8, result.Sections)
	require.Equal(t, 4, result.CodeBlocks) // bash, json, plain, yaml
	require.Equal(t, 4, result.WorkflowSteps)
	require.Equal(t, "detailed", result.DetailLevel)
}

func TestProfile_NoSkillFile(t *testing.T) {
	dir := t.TempDir()
	// Write a non-SKILL markdown file
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hello"), 0644))

	cmd := newProfileCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetArgs([]string{dir})
	require.NoError(t, cmd.Execute())

	require.Contains(t, out.String(), "No SKILL.md files found")
}

func TestProfile_WarningNoSteps(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: no-steps\ndescription: test\n---\n\n## Section One\n\n## Section Two\n\n## Section Three\n\nNo numbered steps here.\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644))

	cmd := newProfileCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetArgs([]string{dir})
	require.NoError(t, cmd.Execute())

	output := out.String()
	require.Contains(t, output, "no workflow steps detected")
}

func TestProfile_WarningHighTokens(t *testing.T) {
	dir := t.TempDir()
	// Create a file with lots of content to exceed 500 tokens
	var b bytes.Buffer
	b.WriteString("---\nname: big-skill\ndescription: test\n---\n\n")
	for i := 0; i < 100; i++ {
		b.WriteString("## Section\n\nLorem ipsum dolor sit amet, consectetur adipiscing elit. ")
		b.WriteString("Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. ")
		b.WriteString("Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris.\n\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), b.Bytes(), 0644))

	cmd := newProfileCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetArgs([]string{dir})
	require.NoError(t, cmd.Execute())

	require.Contains(t, out.String(), "token count")
	require.Contains(t, out.String(), "exceeds 500")
}

func TestProfile_WarningFewSections(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: sparse\ndescription: test\n---\n\n## Only One\n\n1. Step one\n2. Step two\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644))

	cmd := newProfileCmd()
	out := new(bytes.Buffer)
	cmd.SetOut(out)
	cmd.SetArgs([]string{dir})
	require.NoError(t, cmd.Execute())

	require.Contains(t, out.String(), "only 1 sections")
}

func TestAnalyzeSkillProfile_Sections(t *testing.T) {
	content := "# Title\n\n## Section 1\n\nContent.\n\n## Section 2\n\n### Subsection\n\nMore.\n"
	p := analyzeSkillProfile(content, "skills/my-skill/SKILL.md", &mockCounter{tokens: 100})
	require.Equal(t, 3, p.Sections) // ## Section 1, ## Section 2, ### Subsection
	require.Equal(t, "my-skill", p.Name)
}

func TestAnalyzeSkillProfile_CodeBlocks(t *testing.T) {
	content := "## Example\n\n```bash\necho hello\n```\n\n```json\n{}\n```\n"
	p := analyzeSkillProfile(content, "SKILL.md", &mockCounter{tokens: 50})
	require.Equal(t, 2, p.CodeBlocks)
}

func TestAnalyzeSkillProfile_WorkflowSteps(t *testing.T) {
	content := "## Steps\n\n1. First step\n2. Second step\n3. Third step\n"
	p := analyzeSkillProfile(content, "SKILL.md", &mockCounter{tokens: 30})
	require.Equal(t, 3, p.WorkflowSteps)
}

func TestCountSections(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"no sections", "Just text\nmore text", 0},
		{"h1 only", "# Title\n\nContent", 0},
		{"h2 sections", "## One\n\n## Two\n\n## Three", 3},
		{"mixed levels", "## One\n### Two\n#### Three", 3},
		{"hash in content", "Not a ## heading\n## Real heading", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, countSections(tt.content))
		})
	}
}

func TestCountCodeBlocks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"none", "No code here", 0},
		{"one block", "```\ncode\n```", 1},
		{"two blocks", "```bash\necho\n```\n\n```go\nfmt\n```", 2},
		{"with language", "```python\nprint('hi')\n```", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, countCodeBlocks(tt.content))
		})
	}
}

func TestCountWorkflowSteps(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{"none", "No steps here", 0},
		{"numbered", "1. First\n2. Second\n3. Third", 3},
		{"with indent", "  1. Indented step", 1},
		{"mixed", "- Bullet\n1. Numbered\n- Another bullet\n2. Another numbered", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, countWorkflowSteps(tt.content))
		})
	}
}

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1,000"},
		{1722, "1,722"},
		{12345, "12,345"},
		{1000000, "1,000,000"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			require.Equal(t, tt.want, formatNumber(tt.n))
		})
	}
}

func TestInferSkillName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"SKILL.md", "SKILL"},
		{"skills/my-skill/SKILL.md", "my-skill"},
		{"path/to/cool-skill/SKILL.md", "cool-skill"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			require.Equal(t, tt.want, inferSkillName(tt.path))
		})
	}
}

// mockCounter is a test helper that returns a fixed token count.
type mockCounter struct {
	tokens int
}

func (m *mockCounter) Count(_ string) int {
	return m.tokens
}

func TestProfileRejectsInvalidFormat(t *testing.T) {
	cmd := newProfileCmd()
	cmd.SetArgs([]string{"testdata/count", "--format", "bad"})
	require.ErrorContains(t, cmd.Execute(), "invalid format")
}
