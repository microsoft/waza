package dev

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevLoop_LowSkillScored(t *testing.T) {
	// Create a Low-adherence skill in a temp directory.
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "test-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	skillContent := `---
name: test-skill
description: "A short description"
---

# Test Skill

Does things.
`
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644))

	skill, err := readSkillFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)

	result := (&HeuristicScorer{}).Score(skill)
	require.Equal(t, AdherenceLow, result.Level)
	require.NotEmpty(t, result.Issues)
}

func TestDevLoop_HighSkillPassesImmediately(t *testing.T) {
	// Create a High-adherence skill — the loop should pass without needing improvement.
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "good-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	skillContent := `---
name: good-skill
description: |
  **WORKFLOW SKILL** - Handle complex workflow operations involving multiple steps.
  USE FOR: "run workflow", "execute pipeline", "orchestrate tasks", "automate process".
  DO NOT USE FOR: simple one-off commands (use cli-runner), debugging (use debugger).
  INVOKES: task-runner for execution, config-parser for setup.
  FOR SINGLE OPERATIONS: Use task-runner directly for individual tasks.
---

# Good Skill

A well-documented skill.
`
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644))

	skill, err := readSkillFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)

	result := (&HeuristicScorer{}).Score(skill)
	require.Equal(t, AdherenceHigh, result.Level)
	require.True(t, result.Level.AtLeast(AdherenceMediumHigh),
		"High skill should pass MediumHigh target check")
}

func TestDevLoop_DisplayAfterScoring(t *testing.T) {
	// Verify that loading, scoring, and displaying works end-to-end.
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "display-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	skillContent := `---
name: display-skill
description: |
  Process various document formats including conversion, transformation, validation, and advanced formatting of structured data files.
  USE FOR: "convert document", "validate format", "transform data".
---

# Display Skill

Handles document processing.
`
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644))

	skill, err := readSkillFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)

	result := (&HeuristicScorer{}).Score(skill)
	require.Equal(t, AdherenceMedium, result.Level,
		"has triggers but no anti-triggers => Medium")

	var buf bytes.Buffer
	DisplayScore(&buf, skill, result)
	expected := `Skill: display-skill
Score: Medium
Tokens: 74
Description: 198 chars
Triggers: 3
Anti-triggers: 0
`
	require.Equal(t, expected, buf.String())
}

func TestDevLoop_ScoreProgressionPath(t *testing.T) {
	// Verify the scoring algorithm follows the documented progression:
	// Low → add triggers → Medium → add anti-triggers → Medium-High → add routing → High

	base := "This is a sufficiently long description for a skill that does many important things and processes data."
	// Pad to >= 150 chars
	for len(base) < 150 {
		base += " More details about the skill."
	}

	// Step 0: Long enough but no markers → Low (no triggers)
	skill := makeSkill("progress-skill", base)
	scorer := &HeuristicScorer{}
	r := scorer.Score(skill)
	require.Equal(t, AdherenceLow, r.Level, "no triggers => Low")

	// Step 1: Add triggers → Medium
	withTriggers := base + "\nUSE FOR: \"process data\", \"transform files\"."
	skill = makeSkill("progress-skill", withTriggers)
	r = scorer.Score(skill)
	require.Equal(t, AdherenceMedium, r.Level, "triggers but no anti => Medium")

	// Step 2: Add anti-triggers → Medium-High
	withAnti := withTriggers + "\nDO NOT USE FOR: deleting files (use file-manager)."
	skill = makeSkill("progress-skill", withAnti)
	r = scorer.Score(skill)
	require.Equal(t, AdherenceMediumHigh, r.Level, "triggers + anti => Medium-High")

	// Step 3: Add routing → High
	withRouting := "**WORKFLOW SKILL** - " + withAnti + "\nINVOKES: data-tools for processing."
	skill = makeSkill("progress-skill", withRouting)
	r = scorer.Score(skill)
	require.Equal(t, AdherenceHigh, r.Level, "triggers + anti + routing => High")
}

func TestDevLoop_RunDevLoop_AlreadyCompliant(t *testing.T) {
	// A High skill with a target of medium-high should exit on the first iteration.
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "compliant-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	skillContent := `---
name: compliant-skill
description: |
  **UTILITY SKILL** - A compliant skill that meets all criteria for scoring.
  USE FOR: "do thing a", "do thing b", "handle task c".
  DO NOT USE FOR: unrelated tasks (use other-tool).
  INVOKES: helper-tools for processing.
  FOR SINGLE OPERATIONS: Use helper-tools directly.
---

# Compliant Skill

Already meets Medium-High target.
`
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644))

	var buf bytes.Buffer
	cfg := &devConfig{
		SkillDir:      skillDir,
		Target:        AdherenceMediumHigh,
		MaxIterations: 1,
		Auto:          true,
		Out:           &buf,
		In:            &bytes.Buffer{},
	}

	err := runDevLoop(cfg)
	require.NoError(t, err)

	expected := `Skill: compliant-skill
Score: High
Tokens: 94
Description: 266 chars
Triggers: 3
Anti-triggers: 1

✅ Target adherence level High reached!
`
	require.Equal(t, expected, buf.String())
}

func TestDevLoop_RunDevLoop_MaxIterationsHit(t *testing.T) {
	// A Low skill with max-iterations=1 and auto mode should show timeout.
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "low-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	skillContent := `---
name: low-skill
description: "Short"
---

# Low Skill
`
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644))

	var buf bytes.Buffer
	cfg := &devConfig{
		SkillDir:      skillDir,
		Target:        AdherenceHigh, // very high target
		MaxIterations: 1,
		Auto:          true,
		Out:           &buf,
		In:            &bytes.Buffer{},
	}

	err := runDevLoop(cfg)
	require.NoError(t, err)

	expected := `
── Iteration 1/1 ──────────────────────────────────────────

Skill: low-skill
Score: Low
Tokens: 15
Description: 5 chars
Triggers: 0
Anti-triggers: 0

Issues:
  ❌ Description is 5 chars (need 150+)

📝 Suggested improvement (description-length):
────────────────────────────────────────
Short. Provides comprehensive support for common use cases and edge cases. Provides comprehensive support for common use cases and edge cases. Provides comprehensive support for common use cases and edge cases.
────────────────────────────────────────

  Verified: score is now Low

⏱️  Max iterations reached. Current level: Low
╔══════════════════════════════════════════════════════════════════╗
║  SENSEI SUMMARY: low-skill                                       ║
╠══════════════════════════════════════════════════════════════════╣
║  BEFORE                          AFTER                           ║
║  ──────                          ─────                           ║
║  Score: Low                      Score: Low                      ║
║  Tokens: 15                      Tokens: 66                      ║
║  Triggers: 0                     Triggers: 0                     ║
║  Anti-triggers: 0                Anti-triggers: 0                ║
║                                                                  ║
║  TOKEN STATUS: ✅ Under budget (66 < 500)                         ║
╚══════════════════════════════════════════════════════════════════╝
`
	require.Equal(t, expected, buf.String())
}

func TestDevConfig_Defaults(t *testing.T) {
	// Verify the NewCommand sets proper defaults via flags.
	cmd := NewCommand()
	require.NotNil(t, cmd)

	target, err := cmd.Flags().GetString("target")
	require.NoError(t, err)
	require.Equal(t, "medium-high", target)

	maxIter, err := cmd.Flags().GetInt("max-iterations")
	require.NoError(t, err)
	require.Equal(t, 5, maxIter)

	auto, err := cmd.Flags().GetBool("auto")
	require.NoError(t, err)
	require.False(t, auto)
}

func TestDevLoop_AutoProgressionToHigh(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "auto-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	skillContent := `---
name: auto-skill
description: "Short"
---

# Auto Skill

## Data Processing

## File Management

## Report Generation
`
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644))

	var buf bytes.Buffer
	cfg := &devConfig{
		SkillDir:      skillDir,
		Target:        AdherenceHigh,
		MaxIterations: 10,
		Auto:          true,
		Out:           &buf,
		In:            &bytes.Buffer{},
	}

	err := runDevLoop(cfg)
	require.NoError(t, err)

	expected := `
── Iteration 1/10 ──────────────────────────────────────────

Skill: auto-skill
Score: Low
Tokens: 31
Description: 5 chars
Triggers: 0
Anti-triggers: 0

Issues:
  ❌ Description is 5 chars (need 150+)

📝 Suggested improvement (description-length):
────────────────────────────────────────
Short. This skill handles data processing, file management, report generation. Provides comprehensive support for common use cases and edge cases. Provides comprehensive support for common use cases and edge cases.
────────────────────────────────────────

  Verified: score is now Low

── Iteration 2/10 ──────────────────────────────────────────

Skill: auto-skill
Score: Low
Tokens: 83
Description: 214 chars
Triggers: 0
Anti-triggers: 0

📝 Suggested improvement (triggers):
────────────────────────────────────────
USE FOR: auto-skill, data processing, file management, report generation, auto-skill help.
────────────────────────────────────────

  Verified: score is now Medium

── Iteration 3/10 ──────────────────────────────────────────

Skill: auto-skill
Score: Medium
Tokens: 106
Description: 305 chars
Triggers: 5
Anti-triggers: 0

📝 Suggested improvement (anti-triggers):
────────────────────────────────────────
DO NOT USE FOR: general coding questions unrelated to auto-skill, creating new projects from scratch.
────────────────────────────────────────

  Verified: score is now Medium-High

── Iteration 4/10 ──────────────────────────────────────────

Skill: auto-skill
Score: Medium-High
Tokens: 132
Description: 407 chars
Triggers: 5
Anti-triggers: 2

📝 Suggested improvement (routing-clarity):
────────────────────────────────────────
**UTILITY SKILL** INVOKES: built-in analysis tools. FOR SINGLE OPERATIONS: Use auto-skill directly for simple queries.
────────────────────────────────────────

  Verified: score is now High

✅ Target adherence level High reached!
╔══════════════════════════════════════════════════════════════════╗
║  SENSEI SUMMARY: auto-skill                                      ║
╠══════════════════════════════════════════════════════════════════╣
║  BEFORE                          AFTER                           ║
║  ──────                          ─────                           ║
║  Score: Low                      Score: High                     ║
║  Tokens: 31                      Tokens: 162                     ║
║  Triggers: 0                     Triggers: 5                     ║
║  Anti-triggers: 0                Anti-triggers: 2                ║
║                                                                  ║
║  TOKEN STATUS: ✅ Under budget (162 < 500)                        ║
╚══════════════════════════════════════════════════════════════════╝
`
	require.Equal(t, expected, buf.String())

	final, err := readSkillFile(filepath.Join(skillDir, "SKILL.md"))
	require.NoError(t, err)
	desc := final.Frontmatter.Description
	require.True(t, containsAny(desc, triggerPatterns), "should have triggers")
	require.True(t, containsAny(desc, antiTriggerPatterns), "should have anti-triggers")
	require.True(t, containsAny(desc, routingClarityPatterns), "should have routing clarity")
}

func TestDevLoop_NoSummaryWhenDeclined(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "declined-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	skillContent := `---
name: declined-skill
description: "Short"
---

# Declined Skill
`
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644))

	var buf bytes.Buffer
	cfg := &devConfig{
		SkillDir:      skillDir,
		Target:        AdherenceHigh,
		MaxIterations: 3,
		Auto:          false,
		Out:           &buf,
		In:            strings.NewReader("n\n"),
	}

	err := runDevLoop(cfg)
	require.NoError(t, err)

	expected := `
── Iteration 1/3 ──────────────────────────────────────────

Skill: declined-skill
Score: Low
Tokens: 17
Description: 5 chars
Triggers: 0
Anti-triggers: 0

Issues:
  ❌ Description is 5 chars (need 150+)

📝 Suggested improvement (description-length):
────────────────────────────────────────
Short. Provides comprehensive support for common use cases and edge cases. Provides comprehensive support for common use cases and edge cases. Provides comprehensive support for common use cases and edge cases.
────────────────────────────────────────
Apply this improvement? [y/N]
📝 Suggested improvement (triggers):
────────────────────────────────────────
USE FOR: declined-skill, declined-skill help, use declined-skill, how to declined-skill, declined-skill guide.
────────────────────────────────────────
Apply this improvement? [y/N]
No improvements applied.
`
	require.Equal(t, expected, buf.String())
}
