package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/skill"
)

const defaultJudgeTimeout = 120

// JudgeOptions configures the quality judge.
type JudgeOptions struct {
	SkillPath  string
	TimeoutSec int
	Rubric     []Dimension // nil means use DefaultRubric()
}

// Judge runs an LLM-based quality assessment against a SKILL.md file.
func Judge(ctx context.Context, engine execution.AgentEngine, opts JudgeOptions) (*JudgeResponse, error) {
	skillFile, err := resolveSkillFile(opts.SkillPath)
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(skillFile)
	if err != nil {
		return nil, fmt.Errorf("reading SKILL.md: %w", err)
	}

	var sk skill.Skill
	if err := sk.UnmarshalText(content); err != nil {
		return nil, fmt.Errorf("parsing SKILL.md: %w", err)
	}

	rubric := opts.Rubric
	if rubric == nil {
		rubric = DefaultRubric()
	}

	timeoutSec := opts.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = defaultJudgeTimeout
	}

	prompt := BuildJudgePrompt(string(content), rubric)

	resp, err := engine.Execute(ctx, &execution.ExecutionRequest{
		Message: prompt,
		Timeout: time.Duration(timeoutSec) * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("judge execution: %w", err)
	}
	if resp == nil {
		return nil, errors.New("empty engine response from judge")
	}

	judgeResp, err := ParseJudgeResponse(resp.FinalOutput)
	if err != nil {
		return nil, fmt.Errorf("parsing judge response: %w", err)
	}

	issues := ValidateJudgeResponse(judgeResp, rubric)
	if len(issues) > 0 {
		return judgeResp, fmt.Errorf("judge response validation: %s", strings.Join(issues, "; "))
	}

	return judgeResp, nil
}

// BuildJudgePrompt constructs the system prompt for the LLM judge.
func BuildJudgePrompt(skillContent string, rubric []Dimension) string {
	var b strings.Builder
	b.WriteString("You are a skill quality judge. Evaluate the following SKILL.md content against each quality dimension.\n\n")
	b.WriteString("Score each dimension from 1 (poor) to 5 (excellent). Provide specific, actionable feedback for each.\n\n")
	b.WriteString("Quality dimensions:\n")
	for _, d := range rubric {
		fmt.Fprintf(&b, "- **%s**: %s\n", d.Name, d.Description)
	}
	b.WriteString("\nRespond with ONLY a JSON object in this exact format (no markdown fences, no extra text):\n")
	b.WriteString(`{
  "dimensions": [
    {"name": "<dimension_name>", "score": <1-5>, "feedback": "<specific feedback>"}
  ],
  "overall_score": <1.0-5.0>,
  "summary": "<overall assessment>"
}`)
	b.WriteString("\n\nImportant rules:\n")
	b.WriteString("- Include ALL dimensions listed above, in the same order\n")
	b.WriteString("- overall_score should be the weighted average of dimension scores\n")
	b.WriteString("- Keep feedback concise but actionable (1-2 sentences per dimension)\n")
	b.WriteString("- Be critical but fair — a score of 5 means exceptional quality\n\n")
	b.WriteString("SKILL.md content to evaluate:\n")
	b.WriteString("---\n")
	b.WriteString(skillContent)
	b.WriteString("\n---\n")
	return b.String()
}

// ParseJudgeResponse extracts a JudgeResponse from LLM output.
func ParseJudgeResponse(raw string) (*JudgeResponse, error) {
	cleaned := extractJSON(raw)
	if cleaned == "" {
		return nil, errors.New("no JSON found in judge response")
	}

	var resp JudgeResponse
	if err := json.Unmarshal([]byte(cleaned), &resp); err != nil {
		return nil, fmt.Errorf("invalid judge JSON: %w", err)
	}

	return &resp, nil
}

// extractJSON pulls a JSON object from potentially decorated LLM output.
func extractJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	// Try to find JSON in fenced code block
	if start := strings.Index(trimmed, "```"); start >= 0 {
		rest := trimmed[start+3:]
		// Skip language tag (e.g., ```json)
		if nl := strings.Index(rest, "\n"); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			return strings.TrimSpace(rest[:end])
		}
	}

	// Try to find bare JSON object
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return ""
	}
	end := strings.LastIndex(trimmed, "}")
	if end < start {
		return ""
	}
	return trimmed[start : end+1]
}

// resolveSkillFile resolves a path to a SKILL.md file.
func resolveSkillFile(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", errors.New("skill path is required")
	}

	resolved := input
	if !filepath.IsAbs(resolved) {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting working directory: %w", err)
		}
		resolved = filepath.Join(wd, resolved)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("skill path does not exist: %s", input)
		}
		return "", fmt.Errorf("checking skill path: %w", err)
	}

	if info.IsDir() {
		resolved = filepath.Join(resolved, "SKILL.md")
	}

	if filepath.Base(resolved) != "SKILL.md" {
		return "", fmt.Errorf("expected SKILL.md or skill directory, got %s", input)
	}
	if _, err := os.Stat(resolved); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no SKILL.md found in %s", input)
		}
		return "", fmt.Errorf("checking SKILL.md: %w", err)
	}
	return resolved, nil
}
