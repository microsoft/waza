package graders

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/skill"
)

const defaultTriggerThreshold = 0.6

// TriggerHeuristicGraderParams configures the trigger heuristic grader.
type TriggerHeuristicGraderParams struct {
	SkillPath string   `mapstructure:"skill_path"`
	Mode      string   `mapstructure:"mode"`
	Threshold *float64 `mapstructure:"threshold"`
}

type triggerHeuristicMode string

const (
	triggerModePositive triggerHeuristicMode = "positive"
	triggerModeNegative triggerHeuristicMode = "negative"
)

type triggerHeuristicGrader struct {
	name           string
	mode           triggerHeuristicMode
	threshold      float64
	skillPath      string
	keywords       map[string]struct{}
	triggerPhrases []string
}

func NewTriggerHeuristicGrader(name string, params TriggerHeuristicGraderParams) (*triggerHeuristicGrader, error) {
	if strings.TrimSpace(params.SkillPath) == "" {
		return nil, fmt.Errorf("trigger grader '%s' requires skill_path", name)
	}

	mode := triggerHeuristicMode(strings.ToLower(strings.TrimSpace(params.Mode)))
	switch mode {
	case triggerModePositive, triggerModeNegative:
	default:
		return nil, fmt.Errorf("trigger grader '%s' has invalid mode %q (must be positive or negative)", name, params.Mode)
	}

	threshold := defaultTriggerThreshold
	if params.Threshold != nil {
		threshold = *params.Threshold
	}
	if threshold < 0 || threshold > 1 {
		return nil, fmt.Errorf("trigger grader '%s' threshold must be between 0 and 1", name)
	}

	skillPath := resolveSkillPath(params.SkillPath)
	keywords, phrases, err := loadTriggerHeuristicData(skillPath)
	if err != nil {
		return nil, err
	}

	return &triggerHeuristicGrader{
		name:           name,
		mode:           mode,
		threshold:      threshold,
		skillPath:      skillPath,
		keywords:       keywords,
		triggerPhrases: phrases,
	}, nil
}

func (g *triggerHeuristicGrader) Name() string            { return g.name }
func (g *triggerHeuristicGrader) Kind() models.GraderKind { return models.GraderKindTrigger }

func (g *triggerHeuristicGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		if gradingContext == nil || gradingContext.TestCase == nil {
			return &models.GraderResults{
				Name:     g.name,
				Type:     models.GraderKindTrigger,
				Score:    0,
				Passed:   false,
				Feedback: "trigger grader requires test case prompt",
			}, nil
		}

		prompt := gradingContext.TestCase.Stimulus.Message
		score, phraseScore, matchedKeywords := g.scorePrompt(prompt)

		passed := score >= g.threshold
		if g.mode == triggerModeNegative {
			passed = score < g.threshold
		}

		var feedback string
		if g.mode == triggerModePositive {
			if passed {
				feedback = fmt.Sprintf("Prompt is trigger-aligned (score %.2f >= %.2f)", score, g.threshold)
			} else {
				feedback = fmt.Sprintf("Prompt is not trigger-aligned enough (score %.2f < %.2f)", score, g.threshold)
			}
		} else if passed {
			feedback = fmt.Sprintf("Prompt correctly treated as non-trigger (score %.2f < %.2f)", score, g.threshold)
		} else {
			feedback = fmt.Sprintf("Prompt appears trigger-aligned unexpectedly (score %.2f >= %.2f)", score, g.threshold)
		}

		return &models.GraderResults{
			Name:     g.name,
			Type:     models.GraderKindTrigger,
			Score:    score,
			Passed:   passed,
			Feedback: feedback,
			Details: map[string]any{
				"mode":             string(g.mode),
				"threshold":        g.threshold,
				"skill_path":       g.skillPath,
				"matched_keywords": matchedKeywords,
				"matched_count":    len(matchedKeywords),
				"keyword_count":    len(g.keywords),
				"phrase_score":     phraseScore,
			},
		}, nil
	})
}

func (g *triggerHeuristicGrader) scorePrompt(prompt string) (score float64, phraseScore float64, matched []string) {
	promptTokens := tokenize(prompt)
	if len(promptTokens) == 0 {
		return 0, 0, nil
	}

	seen := make(map[string]bool)
	for _, token := range promptTokens {
		if _, ok := g.keywords[token]; ok && !seen[token] {
			seen[token] = true
			matched = append(matched, token)
		}
	}

	tokenScore := float64(len(matched)) / float64(len(promptTokens))
	phraseScore = g.bestPhraseScore(prompt)
	if phraseScore > tokenScore {
		return phraseScore, phraseScore, matched
	}
	return tokenScore, phraseScore, matched
}

func (g *triggerHeuristicGrader) bestPhraseScore(prompt string) float64 {
	if len(g.triggerPhrases) == 0 {
		return 0
	}

	promptLower := strings.ToLower(prompt)
	promptTokenSet := make(map[string]struct{})
	for _, token := range tokenize(prompt) {
		promptTokenSet[token] = struct{}{}
	}

	best := 0.0
	for _, phrase := range g.triggerPhrases {
		phraseLower := strings.ToLower(strings.TrimSpace(phrase))
		if phraseLower == "" {
			continue
		}
		if strings.Contains(promptLower, phraseLower) {
			return 1.0
		}

		phraseTokens := tokenize(phrase)
		if len(phraseTokens) == 0 {
			continue
		}

		hits := 0
		for _, token := range phraseTokens {
			if _, ok := promptTokenSet[token]; ok {
				hits++
			}
		}
		candidate := float64(hits) / float64(len(phraseTokens))
		if candidate > best {
			best = candidate
		}
	}
	return best
}

func loadTriggerHeuristicData(skillPath string) (map[string]struct{}, []string, error) {
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return nil, nil, fmt.Errorf("reading SKILL.md %s: %w", skillPath, err)
	}

	var sk skill.Skill
	if err := sk.UnmarshalText(data); err != nil {
		return nil, nil, fmt.Errorf("parsing SKILL.md %s: %w", skillPath, err)
	}

	description := sk.Frontmatter.Description
	if idx := strings.Index(strings.ToUpper(description), "DO NOT USE FOR:"); idx >= 0 {
		description = strings.TrimSpace(description[:idx])
	}

	keywords := map[string]struct{}{}
	for _, token := range tokenize(sk.Frontmatter.Name + " " + description + " " + sk.Body) {
		keywords[token] = struct{}{}
	}

	useFor, _ := scaffold.ParseTriggerPhrases(sk.Frontmatter.Description)
	phrases := make([]string, 0, len(useFor))
	for _, phrase := range useFor {
		if strings.TrimSpace(phrase.Prompt) == "" {
			continue
		}
		phrases = append(phrases, phrase.Prompt)
		for _, token := range tokenize(phrase.Prompt) {
			keywords[token] = struct{}{}
		}
	}

	if len(keywords) == 0 {
		return nil, nil, fmt.Errorf("SKILL.md %s does not contain usable keywords", skillPath)
	}

	return keywords, phrases, nil
}

func resolveSkillPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	return filepath.Join(cwd, path)
}

func tokenize(input string) []string {
	fields := strings.FieldsFunc(strings.ToLower(input), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})

	tokens := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if len(field) < 3 {
			continue
		}
		if _, isStopWord := stopWords[field]; isStopWord {
			continue
		}
		tokens = append(tokens, field)
	}
	return tokens
}

var stopWords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "this": {}, "that": {}, "from": {},
	"into": {}, "your": {}, "you": {}, "are": {}, "was": {}, "were": {}, "what": {},
	"when": {}, "where": {}, "how": {}, "why": {}, "not": {}, "can": {}, "should": {},
	"use": {}, "using": {}, "about": {}, "have": {}, "has": {}, "had": {}, "but": {},
	"all": {}, "any": {}, "too": {}, "out": {}, "get": {}, "let": {}, "help": {},
}
