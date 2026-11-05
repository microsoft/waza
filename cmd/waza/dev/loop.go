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

	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/scoring"
	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/workspace"
	"github.com/spf13/cobra"
)

type devConfig struct {
	SkillDir      string
	ModelID       string
	Copilot       bool
	Context       context.Context
	Target        scoring.AdherenceLevel
	MaxIterations int
	Auto          bool
	Out           io.Writer
	Err           io.Writer
	In            io.Reader
	Scorer        scoring.Scorer
}

func runDev(cmd *cobra.Command, args []string) error {
	// Load .waza.yaml defaults for dev flags.
	pcfg, err := projectconfig.Load(".")
	if err != nil || pcfg == nil {
		pcfg = projectconfig.New()
	}

	// Check for --scaffold-triggers first — it's a standalone mode.
	scaffoldTriggers, err := cmd.Flags().GetBool("scaffold-triggers")
	if err != nil {
		return err
	}
	if scaffoldTriggers {
		if len(args) == 0 {
			return errors.New("skill name or path required with --scaffold-triggers")
		}
		skillDir, err := resolveSkillDir(args[0], pcfg)
		if err != nil {
			return err
		}
		return runScaffoldTriggers(cmd, skillDir)
	}

	copilotMode, err := cmd.Flags().GetBool("copilot")
	if err != nil {
		return err
	}
	modelID, err := cmd.Flags().GetString("model")
	if err != nil {
		return err
	}
	allMode, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}
	filterStr, err := cmd.Flags().GetString("filter")
	if err != nil {
		return err
	}

	// Only load model from config when in copilot mode
	if copilotMode {
		if !cmd.Flags().Changed("model") && pcfg.Dev.Model != "" {
			modelID = pcfg.Dev.Model
		}
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

	if filterStr != "" && !allMode {
		return errors.New("--filter requires --all")
	}

	target := scoring.AdherenceMediumHigh
	maxIter := 5
	auto := false
	if !copilotMode {
		targetStr, targetErr := cmd.Flags().GetString("target")
		if targetErr != nil {
			return targetErr
		}
		if !cmd.Flags().Changed("target") && pcfg.Dev.Target != "" {
			targetStr = pcfg.Dev.Target
		}
		target, targetErr = scoring.ParseAdherenceLevel(targetStr)
		if targetErr != nil {
			return targetErr
		}

		maxIter, err = cmd.Flags().GetInt("max-iterations")
		if err != nil {
			return err
		}
		if !cmd.Flags().Changed("max-iterations") && pcfg.Dev.MaxIterations > 0 {
			maxIter = pcfg.Dev.MaxIterations
		}
		if maxIter < 1 {
			return errors.New("max-iterations must be at least 1")
		}
		auto, err = cmd.Flags().GetBool("auto")
		if err != nil {
			return err
		}
	}

	// Batch mode: --all or multiple skill name args
	if allMode || len(args) > 1 {
		return runDevBatch(cmd, args, allMode, filterStr, copilotMode, modelID, target, maxIter, auto)
	}

	// Single skill mode (original behavior)
	if len(args) == 0 {
		return errors.New("skill name or path required (or use --all)")
	}

	skillDir, err := resolveSkillDir(args[0], pcfg)
	if err != nil {
		return err
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
		Scorer:        &scoring.HeuristicScorer{},
	}

	if cfg.Copilot {
		return runDevCopilot(cfg)
	} else {
		return runDevLoop(cfg)
	}
}

// batchSkillResult holds before/after state for the batch summary table.
type batchSkillResult struct {
	Name         string
	BeforeLevel  scoring.AdherenceLevel
	AfterLevel   scoring.AdherenceLevel
	BeforeTokens int
	AfterTokens  int
	Err          error
}

func runDevBatch(cmd *cobra.Command, args []string, allMode bool, filterStr string, copilotMode bool, modelID string, target scoring.AdherenceLevel, maxIter int, auto bool) error {
	pcfg, err := projectconfig.Load(".")
	if err != nil || pcfg == nil {
		pcfg = projectconfig.New()
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}
	ctx, err := workspace.DetectContext(wd, workspace.WithSkillsDir(pcfg.Paths.Skills), workspace.WithEvalsDir(pcfg.Paths.Evals), workspace.WithEvalFile(pcfg.Files.EvalFile))
	if err != nil {
		return fmt.Errorf("detecting workspace: %w", err)
	}
	if ctx.Type == workspace.ContextNone {
		return errors.New("no skills detected in workspace")
	}

	var skills []workspace.SkillInfo
	if allMode {
		skills = ctx.Skills
	} else {
		// Resolve each arg as a skill name
		for _, name := range args {
			si, findErr := workspace.FindSkill(ctx, name)
			if findErr != nil {
				return findErr
			}
			skills = append(skills, *si)
		}
	}

	if len(skills) == 0 {
		return errors.New("no skills found in workspace")
	}

	// Apply adherence level filter if specified
	if filterStr != "" {
		filterLevel, parseErr := scoring.ParseAdherenceLevel(filterStr)
		if parseErr != nil {
			return parseErr
		}
		scorer := &scoring.HeuristicScorer{}
		var filtered []workspace.SkillInfo
		for _, si := range skills {
			sk, readErr := readSkillFile(si.SkillPath)
			if readErr != nil {
				continue
			}
			result := scorer.Score(sk)
			if result.Level == filterLevel {
				filtered = append(filtered, si)
			}
		}
		skills = filtered
		if len(skills) == 0 {
			fprintf(cmd.OutOrStdout(), "No skills at %s adherence level.\n", filterLevel)
			return nil
		}
	}

	w := cmd.OutOrStdout()
	fprintf(w, "\n📦 Batch processing %d skill(s)\n", len(skills))
	fprintf(w, "═══════════════════════════════════════════════\n\n")

	var results []batchSkillResult

	for i, si := range skills {
		if i > 0 {
			fprintf(w, "\n")
		}
		fprintf(w, "─── [%d/%d] %s ───\n", i+1, len(skills), si.Name)

		cfg := &devConfig{
			SkillDir:      si.Dir,
			ModelID:       modelID,
			Copilot:       copilotMode,
			Context:       cmd.Context(),
			Target:        target,
			MaxIterations: maxIter,
			Auto:          auto,
			Out:           w,
			Err:           cmd.ErrOrStderr(),
			In:            cmd.InOrStdin(),
			Scorer:        &scoring.HeuristicScorer{},
		}

		// Capture before state
		sk, readErr := readSkillFile(filepath.Join(si.Dir, "SKILL.md"))
		if readErr != nil {
			results = append(results, batchSkillResult{Name: si.Name, Err: readErr})
			fprintf(w, "  ❌ Error: %s\n", readErr)
			continue
		}
		beforeScore := cfg.Scorer.Score(sk)
		beforeTokens := sk.Tokens

		// Run the loop
		var loopErr error
		if cfg.Copilot {
			loopErr = runDevCopilot(cfg)
		} else {
			loopErr = runDevLoop(cfg)
		}

		// Capture after state
		sk, readErr = readSkillFile(filepath.Join(si.Dir, "SKILL.md"))
		afterLevel := beforeScore.Level
		afterTokens := beforeTokens
		if readErr == nil {
			afterScore := cfg.Scorer.Score(sk)
			afterLevel = afterScore.Level
			afterTokens = sk.Tokens
		}

		results = append(results, batchSkillResult{
			Name:         si.Name,
			BeforeLevel:  beforeScore.Level,
			AfterLevel:   afterLevel,
			BeforeTokens: beforeTokens,
			AfterTokens:  afterTokens,
			Err:          loopErr,
		})

		if loopErr != nil {
			fprintf(w, "  ⚠️  Error processing %s: %s\n", si.Name, loopErr)
		}
	}

	// Print batch summary table
	DisplayBatchSummary(w, results)
	return nil
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
		scorer = &scoring.HeuristicScorer{}
	}

	initialScore := scorer.Score(skill)
	initialTokens := skill.Tokens

	if initialScore.Level.AtLeast(cfg.Target) {
		DisplayScore(cfg.Out, skill, initialScore)
		DisplayTargetReached(cfg.Out, initialScore.Level)
		return nil
	}

	var currentScore *scoring.ScoreResult
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

func improve(cfg *devConfig, skill *skill.Skill, score *scoring.ScoreResult) (bool, error) {
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

func collectFrontmatterSuggestions(skill *skill.Skill, score *scoring.ScoreResult) []frontmatterSuggestion {
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

// runScaffoldTriggers reads a skill's SKILL.md, parses trigger phrases from
// the description frontmatter, and writes tests/trigger_tests.yaml.
func runScaffoldTriggers(cmd *cobra.Command, skillDir string) error {
	// Read SKILL.md
	skillPath := filepath.Join(skillDir, "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return fmt.Errorf("reading SKILL.md: %w", err)
	}

	var sk skill.Skill
	if err := sk.UnmarshalText(data); err != nil {
		return fmt.Errorf("parsing SKILL.md: %w", err)
	}

	desc := sk.Frontmatter.Description
	if desc == "" {
		return errors.New("SKILL.md has no description field — cannot extract trigger phrases")
	}

	useFor, doNotUseFor := scaffold.ParseTriggerPhrases(desc)
	if len(useFor) == 0 && len(doNotUseFor) == 0 {
		return errors.New("no USE FOR or DO NOT USE FOR phrases found in description")
	}

	yaml := scaffold.TriggerTestsYAML(sk.Frontmatter.Name, useFor, doNotUseFor)

	// Write to tests/trigger_tests.yaml
	testsDir := filepath.Join(skillDir, "tests")
	if err := os.MkdirAll(testsDir, 0o755); err != nil {
		return fmt.Errorf("creating tests directory: %w", err)
	}

	outPath := filepath.Join(testsDir, "trigger_tests.yaml")
	if err := os.WriteFile(outPath, []byte(yaml), 0o644); err != nil {
		return fmt.Errorf("writing trigger_tests.yaml: %w", err)
	}

	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "✅ Scaffolded trigger tests from %s frontmatter\n", sk.Frontmatter.Name)
	_, _ = fmt.Fprintf(out, "   %d should-trigger prompts\n", len(useFor))
	_, _ = fmt.Fprintf(out, "   %d should-not-trigger prompts\n", len(doNotUseFor))
	_, _ = fmt.Fprintf(out, "   → %s\n", outPath)

	return nil
}
