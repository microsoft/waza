package dev

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spboyer/waza/internal/skill"
	"github.com/spf13/cobra"
)

type devConfig struct {
	SkillDir      string
	Target        AdherenceLevel
	MaxIterations int
	Auto          bool
	Out           io.Writer
	In            io.Reader
	Scorer        Scorer
	scan          *bufio.Scanner
}

func (c *devConfig) Scanner() *bufio.Scanner {
	if c.scan == nil {
		c.scan = bufio.NewScanner(c.In)
	}
	return c.scan
}

func runDev(cmd *cobra.Command, args []string) error {
	targetStr, err := cmd.Flags().GetString("target")
	if err != nil {
		return err
	}
	target, err := ParseAdherenceLevel(targetStr)
	if err != nil {
		return err
	}
	maxIter, err := cmd.Flags().GetInt("max-iterations")
	if err != nil {
		return err
	}
	if maxIter < 1 {
		return errors.New("max-iterations must be at least 1")
	}
	auto, err := cmd.Flags().GetBool("auto")
	if err != nil {
		return err
	}

	skillDir := "."
	if len(args) > 0 {
		skillDir = args[0]
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
		Target:        target,
		MaxIterations: maxIter,
		Auto:          auto,
		Out:           cmd.OutOrStdout(),
		In:            cmd.InOrStdin(),
		Scorer:        &HeuristicScorer{},
	}

	return runDevLoop(cfg)
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
	type step struct {
		section    string
		applicable bool
		suggest    func() string
		apply      func(string)
	}

	steps := []step{
		{
			section:    "description-length",
			applicable: score.DescriptionLen < 150,
			suggest:    func() string { return suggestExpandedDescription(skill) },
			apply:      func(s string) { skill.Frontmatter.Description = s },
		},
		{
			section:    "triggers",
			applicable: !score.HasTriggers,
			suggest:    func() string { return suggestTriggers(skill) },
			apply:      func(s string) { skill.Frontmatter.Description = appendSection(skill.Frontmatter.Description, s) },
		},
		{
			section:    "anti-triggers",
			applicable: !score.HasAntiTriggers && score.HasTriggers,
			suggest:    func() string { return suggestAntiTriggers(skill) },
			apply:      func(s string) { skill.Frontmatter.Description = appendSection(skill.Frontmatter.Description, s) },
		},
		{
			section:    "routing-clarity",
			applicable: !score.HasRoutingClarity && score.HasAntiTriggers,
			suggest:    func() string { return suggestRoutingClarity(skill) },
			apply:      func(s string) { skill.Frontmatter.Description = appendSection(skill.Frontmatter.Description, s) },
		},
	}

	for _, s := range steps {
		if !s.applicable {
			continue
		}
		suggestion := s.suggest()
		DisplayImprovement(cfg.Out, s.section, suggestion)
		if !cfg.Auto && !confirmApply(cfg) {
			continue
		}
		s.apply(suggestion)
		if err := writeSkillFile(skill); err != nil {
			return false, fmt.Errorf("applying %s: %w", s.section, err)
		}
		return true, nil
	}

	return false, nil
}

func confirmApply(cfg *devConfig) bool {
	return PromptConfirm(cfg.Scanner(), cfg.Out, "Apply this improvement?")
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
