package skill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnmarshalText_Empty(t *testing.T) {
	var skill Skill
	require.Error(t, skill.UnmarshalText([]byte{}))
}

func TestUnmarshalText_NoFrontmatter(t *testing.T) {
	content := []byte(`
# No Frontmatter

This skill has no frontmatter, only body content.
`)

	var skill Skill
	require.NoError(t, skill.UnmarshalText(content))
	require.Empty(t, skill.Frontmatter.Name)
	require.Empty(t, skill.Frontmatter.Description)
	require.EqualValues(t, content, skill.Body)
}

func TestUnmarshalText_QuotedString(t *testing.T) {
	content := []byte(`---
name: "my-quoted-skill"
description: "A skill with a quoted description string"
---

# Quoted Skill
`)
	var skill Skill
	require.NoError(t, skill.UnmarshalText(content))
	require.Equal(t, "my-quoted-skill", skill.Frontmatter.Name)
	require.Equal(t, "A skill with a quoted description string", skill.Frontmatter.Description)
}

func TestUnmarshalText_MultilinePipe(t *testing.T) {
	content := []byte(`---
name: pipe-skill
description: |
  This is a multiline description
  that uses the pipe character.
  USE FOR: "testing multiline".
---

# Pipe Skill
`)
	var skill Skill
	require.NoError(t, skill.UnmarshalText(content))
	require.Equal(t, "pipe-skill", skill.Frontmatter.Name)
	require.Contains(t, skill.Frontmatter.Description, "multiline description")
	require.Contains(t, skill.Frontmatter.Description, "USE FOR:")
}

func TestUnmarshalText_PlainUnquoted(t *testing.T) {
	content := []byte(`---
name: plain-skill
description: A plain unquoted description for the skill
---

# Plain Skill
`)
	var skill Skill
	require.NoError(t, skill.UnmarshalText(content))
	require.Equal(t, "plain-skill", skill.Frontmatter.Name)
	require.Equal(t, "A plain unquoted description for the skill", skill.Frontmatter.Description)
}

func TestUnmarshalText_BodyPreserved(t *testing.T) {
	frontmatter := `---
name: body-test
description: A test skill
---`
	body := `
	# Body Test

This is the body content.

## Section

More content here.
`
	var skill Skill
	require.NoError(t, skill.UnmarshalText([]byte(frontmatter+body)))
	require.Equal(t, "body-test", skill.Frontmatter.Name)
	require.Equal(t, "A test skill", skill.Frontmatter.Description)
	require.Equal(t, body, skill.Body)
}

func TestUnmarshalText_TokensAndCharacters(t *testing.T) {
	content := []byte(`---
name: metrics-test
description: A test skill for metrics
---

# Metrics Test

Some body content here.
`)
	var skill Skill
	require.NoError(t, skill.UnmarshalText(content))
	require.Greater(t, skill.Tokens, 0, "tokens should be estimated")
	require.Equal(t, len(content), skill.Characters, "characters should match raw content length")
	require.Greater(t, skill.Lines, 0, "lines should be counted")
}

func TestMarshalText_RoundTrip(t *testing.T) {
	content := []byte(`---
name: round-trip
description: Original description
---

# Round Trip Skill

Body content stays the same.
`)
	var skill Skill
	require.NoError(t, skill.UnmarshalText(content))

	skill.Frontmatter.Description = "Updated description for round-trip test"

	data, err := skill.MarshalText()
	require.NoError(t, err)

	var updated Skill
	require.NoError(t, updated.UnmarshalText(data))
	require.Equal(t, "round-trip", updated.Frontmatter.Name)
	require.Equal(t, "Updated description for round-trip test", updated.Frontmatter.Description)
	require.Contains(t, updated.Body, "Body content stays the same")
}

func TestMarshalText_RoundTripViaFile(t *testing.T) {
	content := []byte(`---
name: round-trip
description: Original description
---

# Round Trip Skill

Body content stays the same.
`)
	path := filepath.Join(t.TempDir(), "SKILL.md")
	require.NoError(t, os.WriteFile(path, content, 0644))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var skill Skill
	require.NoError(t, skill.UnmarshalText(data))

	skill.Frontmatter.Description = "Updated description for round-trip test"

	out, err := skill.MarshalText()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0644))

	updatedData, err := os.ReadFile(path)
	require.NoError(t, err)

	var updated Skill
	require.NoError(t, updated.UnmarshalText(updatedData))
	require.Equal(t, "round-trip", updated.Frontmatter.Name)
	require.Equal(t, "Updated description for round-trip test", updated.Frontmatter.Description)
	require.Equal(t, skill.Body, updated.Body)
}

func TestParseFrontmatter_NoDelimiter(t *testing.T) {
	fm, _, _, body, err := parseFrontmatter("Just plain text")
	require.NoError(t, err)
	require.Empty(t, fm.Name)
	require.Contains(t, body, "Just plain text")
}

func TestParseFrontmatter_NoClosingDelimiter(t *testing.T) {
	_, _, _, _, err := parseFrontmatter("---\nname: test\nNo closing delimiter")
	require.Error(t, err)
}

func TestParseFrontmatter_ValidSimple(t *testing.T) {
	fm, _, _, body, err := parseFrontmatter("---\nname: test\n---\n\n# Body")
	require.NoError(t, err)
	require.Equal(t, "test", fm.Name)
	require.Contains(t, body, "# Body")
}

func TestMarshalText_PreservesExtraFrontmatterFields(t *testing.T) {
	content := []byte(`---
name: extra-test
description: Original description
owner: tools-team
tags:
  - utilities
  - scoring
---

# Extra Fields
`)
	var skill Skill
	require.NoError(t, skill.UnmarshalText(content))
	skill.Frontmatter.Description = "Updated description"

	data, err := skill.MarshalText()
	require.NoError(t, err)

	_, rawFrontmatter, _, _, err := parseFrontmatter(string(data))
	require.NoError(t, err)

	require.Equal(t, "tools-team", rawFrontmatter["owner"])
	require.Equal(t, []any{"utilities", "scoring"}, rawFrontmatter["tags"])
}
