package quality

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FormatTable renders the judge response as a CLI-friendly table.
func FormatTable(resp *JudgeResponse) string {
	var b strings.Builder

	nameWidth := len("DIMENSION")
	for _, d := range resp.Dimensions {
		if len(d.Name) > nameWidth {
			nameWidth = len(d.Name)
		}
	}

	header := fmt.Sprintf("%-*s  %-5s  %s", nameWidth, "DIMENSION", "SCORE", "FEEDBACK")
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", len(header)+10))
	b.WriteString("\n")

	for _, d := range resp.Dimensions {
		bar := scoreBar(d.Score)
		fmt.Fprintf(&b, "%-*s  %s  %s\n", nameWidth, d.Name, bar, d.Feedback)
	}

	b.WriteString(strings.Repeat("─", len(header)+10))
	b.WriteString("\n")
	fmt.Fprintf(&b, "Overall: %.1f/5.0\n", resp.OverallScore)
	if resp.Summary != "" {
		fmt.Fprintf(&b, "\n%s\n", resp.Summary)
	}

	return b.String()
}

// FormatJSON renders the judge response as indented JSON.
func FormatJSON(resp *JudgeResponse) (string, error) {
	data, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling judge response: %w", err)
	}
	return string(data), nil
}

// scoreBar renders a visual bar for a 1-5 score.
func scoreBar(score int) string {
	if score < 1 {
		score = 1
	}
	if score > 5 {
		score = 5
	}
	filled := strings.Repeat("█", score)
	empty := strings.Repeat("░", 5-score)
	return fmt.Sprintf("%s%s", filled, empty)
}
