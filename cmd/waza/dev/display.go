package dev

import (
	"fmt"
	"io"
	"strings"

	"github.com/spboyer/waza/internal/skill"
)

const boxWidth = 66

func fprintf(w io.Writer, format string, a ...any) {
	if _, err := fmt.Fprintf(w, format, a...); err != nil {
		panic("error writing output: " + err.Error())
	}
}

func fprintln(w io.Writer, a ...any) {
	if _, err := fmt.Fprintln(w, a...); err != nil {
		panic("error writing output: " + err.Error())
	}
}

// DisplayIterationHeader shows iteration progress.
func DisplayIterationHeader(w io.Writer, iteration, maxIterations int) {
	fprintf(w, "\n── Iteration %d/%d ──────────────────────────────────────────\n\n", iteration, maxIterations)
}

// DisplayScore shows the current score with issues.
func DisplayScore(w io.Writer, sk *skill.Skill, score *ScoreResult) {
	name := sk.Frontmatter.Name
	fprintf(w, "Skill: %s\n", name)
	fprintf(w, "Score: %s\n", score.Level)
	fprintf(w, "Tokens: %d\n", sk.Tokens)
	fprintf(w, "Description: %d chars\n", score.DescriptionLen)
	fprintf(w, "Triggers: %d\n", score.TriggerCount)
	fprintf(w, "Anti-triggers: %d\n", score.AntiTriggerCount)

	if len(score.Issues) > 0 {
		fprintf(w, "\n")
		DisplayIssues(w, score.Issues)
	}
}

// DisplayIssues lists all issues found.
func DisplayIssues(w io.Writer, issues []Issue) {
	fprintf(w, "Issues:\n")
	for _, iss := range issues {
		icon := "⚠️"
		if iss.Severity == "error" {
			icon = "❌"
		}
		fprintf(w, "  %s %s\n", icon, iss.Message)
	}
}

// DisplaySummary shows before/after comparison box.
func DisplaySummary(w io.Writer, skillName string, before, after *ScoreResult, beforeTokens, afterTokens int) {
	top := "╔" + strings.Repeat("═", boxWidth) + "╗"
	mid := "╠" + strings.Repeat("═", boxWidth) + "╣"
	bot := "╚" + strings.Repeat("═", boxWidth) + "╝"

	fprintln(w, top)
	fprintln(w, boxLine(fmt.Sprintf("SENSEI SUMMARY: %s", skillName)))
	fprintln(w, mid)
	fprintln(w, boxLine("BEFORE                          AFTER"))
	fprintln(w, boxLine("──────                          ─────"))
	fprintln(w, boxLine(fmt.Sprintf("Score: %-24s Score: %s", before.Level, after.Level)))
	fprintln(w, boxLine(fmt.Sprintf("Tokens: %-23d Tokens: %d", beforeTokens, afterTokens)))
	fprintln(w, boxLine(fmt.Sprintf("Triggers: %-21d Triggers: %d", before.TriggerCount, after.TriggerCount)))
	fprintln(w, boxLine(fmt.Sprintf("Anti-triggers: %-16d Anti-triggers: %d", before.AntiTriggerCount, after.AntiTriggerCount)))
	fprintln(w, boxLine(""))

	tokenStatus := fmt.Sprintf("TOKEN STATUS: ✅ Under budget (%d < %d)", afterTokens, tokenSoftLimit)
	if afterTokens > tokenSoftLimit {
		tokenStatus = fmt.Sprintf("TOKEN STATUS: ⚠️ Over soft limit (%d > %d)", afterTokens, tokenSoftLimit)
	}
	if afterTokens > tokenHardLimit {
		tokenStatus = fmt.Sprintf("TOKEN STATUS: ❌ Over hard limit (%d > %d)", afterTokens, tokenHardLimit)
	}
	fprintln(w, boxLine(tokenStatus))
	fprintln(w, bot)
}

// boxLine pads text inside box borders (║ ... ║).
func boxLine(text string) string {
	maxText := boxWidth - 2
	text = truncateText(text, maxText)
	padding := boxWidth - 2 - len([]rune(text))
	if padding < 0 {
		padding = 0
	}
	return "║  " + text + strings.Repeat(" ", padding) + "║"
}

func truncateText(text string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}

// DisplayImprovement shows a suggested improvement.
func DisplayImprovement(w io.Writer, section, suggestion string) {
	fprintf(w, "\n📝 Suggested improvement (%s):\n", section)
	fprintln(w, "────────────────────────────────────────")
	fprintln(w, suggestion)
	fprintln(w, "────────────────────────────────────────")
}

// DisplayTargetReached shows success message.
func DisplayTargetReached(w io.Writer, level AdherenceLevel) {
	fprintf(w, "\n✅ Target adherence level %s reached!\n", level)
}

// DisplayMaxIterations shows timeout message.
func DisplayMaxIterations(w io.Writer, currentLevel AdherenceLevel) {
	fprintf(w, "\n⏱️  Max iterations reached. Current level: %s\n", currentLevel)
}
