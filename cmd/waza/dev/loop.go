package dev

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spboyer/waza/internal/skill"
	"github.com/spboyer/waza/internal/workspace"
	"github.com/spf13/cobra"
)

type devConfig struct {
	SkillDir      string
	ModelID       string
	Copilot       bool
	Context       context.Context
	Target        AdherenceLevel
	MaxIterations int
	Auto          bool
	Out           io.Writer
	Err           io.Writer
	In            io.Reader
	Scorer        Scorer
}

func runDev(cmd *cobra.Command, args []string) error {
	copilotMode, err := cmd.Flags().GetBool("copilot")
	if err != nil {
		return err
	}
	modelID, err := cmd.Flags().GetString("model")
	if err != nil {
		return err
	}
	if copilotMode {
		if cmd.Flags().Changed("target") {
			return errors.New("--target is not valid with --copilot")
		}
		if cmd.Flags().Changed("max-iterations") {
			return errors.New("--max-iterations is not valid with --copilot")
		}
		if cmd.Flags().Changed("auto") {
			return errors.New("--auto is not valid with --copilot")
		}
	} else if cmd.Flags().Changed("model") {
		return errors.New("--model is valid only with --copilot")
	}

	target := AdherenceMediumHigh
	maxIter := 5
	auto := false
	if !copilotMode {
		targetStr, targetErr := cmd.Flags().GetString("target")
		if targetErr != nil {
			return targetErr
		}
		target, targetErr = ParseAdherenceLevel(targetStr)
		if targetErr != nil {
			return targetErr
		}

		maxIter, err = cmd.Flags().GetInt("max-iterations")
		if err != nil {
			return err
		}
		if maxIter < 1 {
			return errors.New("max-iterations must be at least 1")
		}
		auto, err = cmd.Flags().GetBool("auto")
		if err != nil {
			return err
		}
	}

	skillDir := ""
	if len(args) > 0 {
		skillDir = args[0]
	}

	// Try workspace detection if no explicit path given or arg looks like a skill name
	if skillDir == "" || !workspace.LooksLikePath(skillDir) {
		resolved := tryResolveSkillDir(skillDir)
		if resolved != "" {
			skillDir = resolved
		} else if skillDir == "" {
			skillDir = "."
		}
	}

	if !filepath.IsAbs(skillDir) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		skillDir = filepath.Join(wd, skillDir)
	}

	cfg := &devConfig{
		SkillDir:      skillDir,
		ModelID:       modelID,
		Copilot:       copilotMode,
		Context:       cmd.Context(),
		Target:        target,
		MaxIterations: maxIter,
		Auto:          auto,
		Out:           cmd.OutOrStdout(),
		Err:           cmd.ErrOrStderr(),
		In:            cmd.InOrStdin(),
		Scorer:        &HeuristicScorer{},
	}

	if cfg.Copilot {
		return runDevCopilot(cfg)
	} else {
		return runDevLoop(cfg)
	}
}

func runDevLoop(cfg *devConfig) error {
	if cfg.MaxIterations < 1 {
		return errors.New("max-iterations must be at least 1")
	}
	skillPath := filepath.Join(cfg.SkillDir, "SKILL.md")
	skill, err := readSkillFile(skillPath)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("no SKILL.md found in " + cfg.SkillDir)
	}
	if err != nil {
		return err
	}

	// Initial score for before/after comparison
	scorer := cfg.Scorer
	if scorer == nil {
		scorer = &HeuristicScorer{}
	}

	initialScore := scorer.Score(skill)
	initialTokens := skill.Tokens

	if initialScore.Level.AtLeast(cfg.Target) {
		DisplayScore(cfg.Out, skill, initialScore)
		DisplayTargetReached(cfg.Out, initialScore.Level)
		return nil
	}

	var currentScore *ScoreResult
	anyChanges := false

	for iter := 1; iter <= cfg.MaxIterations; iter++ {
		DisplayIterationHeader(cfg.Out, iter, cfg.MaxIterations)

		// Step 2: SCORE
		currentScore = scorer.Score(skill)
		DisplayScore(cfg.Out, skill, currentScore)

		// Step 3: CHECK — target reached?
		if currentScore.Level.AtLeast(cfg.Target) {
			DisplayTargetReached(cfg.Out, currentScore.Level)
			break
		}

		// Step 4: IMPROVE
		improved, err := improve(cfg, skill, currentScore)
		if err != nil {
			return fmt.Errorf("improving skill: %w", err)
		}
		if !improved {
			if _, err = fmt.Fprintln(cfg.Out, "\nNo improvements applied."); err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
			break
		}
		anyChanges = true

		skill, err = readSkillFile(skill.Path)
		if err != nil {
			return fmt.Errorf("re-loading skill after improvement: %w", err)
		}
		currentScore = scorer.Score(skill)
		if _, err := fmt.Fprintf(cfg.Out, "\n  Verified: score is now %s\n", currentScore.Level); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		// Check again after improvement
		if currentScore.Level.AtLeast(cfg.Target) {
			DisplayTargetReached(cfg.Out, currentScore.Level)
			break
		}

		if iter == cfg.MaxIterations {
			DisplayMaxIterations(cfg.Out, currentScore.Level)
		}
	}

	if anyChanges {
		if currentScore == nil {
			currentScore = initialScore
		}
		DisplaySummary(cfg.Out, skill.Frontmatter.Name, initialScore, currentScore, initialTokens, skill.Tokens)
	}

	return nil
}

func improve(cfg *devConfig, skill *skill.Skill, score *ScoreResult) (bool, error) {
	for _, s := range collectFrontmatterSuggestions(skill, score) {
		DisplayImprovement(cfg.Out, s.Section, s.Suggestion)
		if !cfg.Auto && !confirmApply(cfg) {
			continue
		}

		switch s.Section {
		case "description-length":
			skill.Frontmatter.Description = s.Suggestion
		default:
			skill.Frontmatter.Description = appendSection(skill.Frontmatter.Description, s.Suggestion)
		}

		if err := writeSkillFile(skill); err != nil {
			return false, fmt.Errorf("applying %s: %w", s.Section, err)
		}
		return true, nil
	}

	return false, nil
}

func confirmApply(cfg *devConfig) bool {
	return promptConfirm(cfg.In, cfg.Out, "Apply this improvement?")
}

type frontmatterSuggestion struct {
	Section    string
	Suggestion string
}

func collectFrontmatterSuggestions(skill *skill.Skill, score *ScoreResult) []frontmatterSuggestion {
	var suggestions []frontmatterSuggestion
	if score.DescriptionLen < 150 {
		suggestions = append(suggestions, frontmatterSuggestion{
			Section:    "description-length",
			Suggestion: suggestExpandedDescription(skill),
		})
	}
	if !score.HasTriggers {
		suggestions = append(suggestions, frontmatterSuggestion{
			Section:    "triggers",
			Suggestion: suggestTriggers(skill),
		})
	}
	if !score.HasAntiTriggers && score.HasTriggers {
		suggestions = append(suggestions, frontmatterSuggestion{
			Section:    "anti-triggers",
			Suggestion: suggestAntiTriggers(skill),
		})
	}
	if !score.HasRoutingClarity && score.HasAntiTriggers {
		suggestions = append(suggestions, frontmatterSuggestion{
			Section:    "routing-clarity",
			Suggestion: suggestRoutingClarity(skill),
		})
	}
	return suggestions
}

// suggestExpandedDescription builds a longer description from the skill body.
func suggestExpandedDescription(skill *skill.Skill) string {
	existing := strings.TrimSpace(skill.Frontmatter.Description)
	if existing == "" {
		existing = "Skill for " + skill.Frontmatter.Name + " functionality."
	}

	// Extract keywords from the body for context
	keywords := extractKeywords(skill.Body)
	if len(keywords) > 5 {
		keywords = keywords[:5]
	}

	var b strings.Builder
	b.WriteString(existing)
	if !strings.HasSuffix(existing, ".") {
		b.WriteString(".")
	}
	if len(keywords) > 0 {
		b.WriteString(" This skill handles ")
		b.WriteString(strings.Join(keywords, ", "))
		b.WriteString(".")
	}
	result := b.String()
	// May need multiple appends for very short descriptions
	for utf8.RuneCountInString(result) < 150 {
		result += " Provides comprehensive support for common use cases and edge cases."
	}
	return result
}

// suggestTriggers generates a USE FOR: block from skill content.
func suggestTriggers(skill *skill.Skill) string {
	keywords := extractKeywords(skill.Body)
	name := skill.Frontmatter.Name

	phrases := []string{name}
	for _, kw := range keywords {
		if len(phrases) >= 5 {
			break
		}
		phrases = append(phrases, kw)
	}
	fillers := []string{
		name + " help",
		"use " + name,
		"how to " + name,
		name + " guide",
	}
	for _, f := range fillers {
		if len(phrases) >= 5 {
			break
		}
		phrases = append(phrases, f)
	}

	return "USE FOR: " + strings.Join(phrases, ", ") + "."
}

// suggestAntiTriggers generates a DO NOT USE FOR: block.
func suggestAntiTriggers(skill *skill.Skill) string {
	return "DO NOT USE FOR: general coding questions unrelated to " + skill.Frontmatter.Name + ", creating new projects from scratch."
}

// suggestRoutingClarity generates routing clarity text.
func suggestRoutingClarity(skill *skill.Skill) string {
	return "**UTILITY SKILL** INVOKES: built-in analysis tools. FOR SINGLE OPERATIONS: Use " + skill.Frontmatter.Name + " directly for simple queries."
}

// appendSection adds a new section to the description, separated by a newline.
func appendSection(desc, section string) string {
	desc = strings.TrimRight(desc, " \t\n")
	return desc + "\n" + section
}

// extractKeywords pulls likely keywords from the body markdown.
func extractKeywords(body string) []string {
	var keywords []string
	seen := make(map[string]bool)

	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "## "); ok {
			kw := after
			kw = strings.TrimSpace(kw)
			lower := strings.ToLower(kw)
			if !seen[lower] && lower != "" {
				seen[lower] = true
				keywords = append(keywords, strings.ToLower(kw))
			}
		}
	}

	return keywords
}

func readSkillFile(path string) (*skill.Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading SKILL.md: %w", err)
	}
	var s skill.Skill
	if err := s.UnmarshalText(data); err != nil {
		return nil, err
	}
	s.Path = path
	return &s, nil
}

func writeSkillFile(skill *skill.Skill) error {
	data, err := skill.MarshalText()
	if err != nil {
		return err
	}
	return os.WriteFile(skill.Path, data, 0644)
}

// tryResolveSkillDir uses workspace detection to find a skill directory.
// If name is empty, returns the first detected skill's dir.
// If name is a skill name, returns that skill's dir.
// Returns empty string if detection fails.
func tryResolveSkillDir(name string) string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	ctx, err := workspace.DetectContext(wd)
	if err != nil || ctx.Type == workspace.ContextNone {
		return ""
	}

	if name == "" {
		if len(ctx.Skills) > 0 {
			return ctx.Skills[0].Dir
		}
		return ""
	}

	si, err := workspace.FindSkill(ctx, name)
	if err != nil {
		return ""
	}
	return si.Dir
}
