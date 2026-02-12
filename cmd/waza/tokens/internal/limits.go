package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	fallbackLimit    = 2000
	maxPatternLength = 500
)

// TokenLimitsConfig holds token limit configuration.
type TokenLimitsConfig struct {
	Description string         `json:"description,omitempty"`
	Defaults    map[string]int `json:"defaults"`
	Overrides   map[string]int `json:"overrides"`
}

// defaultLimits is the fallback configuration when no .token-limits.json exists.
var defaultLimits = TokenLimitsConfig{
	Defaults: map[string]int{
		"SKILL.md":           500,
		"references/**/*.md": 1000,
		"docs/**/*.md":       1500,
		"*.md":               2000,
	},
	Overrides: map[string]int{
		"README.md":       3000,
		"CONTRIBUTING.md": 2500,
	},
}

// LoadConfig unmarshals dir/.token-limits.json or returns [defaultLimits].
func LoadConfig(dir string) (TokenLimitsConfig, error) {
	path := filepath.Join(dir, ".token-limits.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultLimits, nil
		}
		return TokenLimitsConfig{}, fmt.Errorf("reading %q: %w", path, err)
	}
	cfg := TokenLimitsConfig{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("error parsing limits %q: %w", path, err)
	}
	if cfg.Defaults == nil {
		return cfg, errors.New(`missing or invalid "defaults" field in limits file: ` + path)
	}
	if cfg.Overrides == nil {
		cfg.Overrides = make(map[string]int)
	}
	return cfg, nil
}

// NormalizePath converts backslashes to forward slashes.
func NormalizePath(filePath string) string {
	return strings.ReplaceAll(filePath, `\`, "/")
}

// GlobToRegex converts a glob pattern to a compiled regex.
func GlobToRegex(pattern string) (*regexp.Regexp, error) {
	if len(pattern) > maxPatternLength {
		return nil, fmt.Errorf("pattern too long (max %d characters)", maxPatternLength)
	}

	re := regexp.QuoteMeta(pattern)
	// Undo quoting for our glob characters — order matters:
	// first handle ** (globstar), then *
	re = strings.ReplaceAll(re, `\*\*`, "{{GLOBSTAR}}")
	re = strings.ReplaceAll(re, `\*`, `[^/]*`)
	re = strings.ReplaceAll(re, "{{GLOBSTAR}}", `.*?`)

	if strings.Contains(pattern, "/") {
		return regexp.Compile(`^/?` + re + `$`)
	}
	return regexp.Compile(`(?:^|/)` + re + `$`)
}

// MatchesPattern checks whether filePath matches a glob pattern.
func MatchesPattern(filePath, pattern string) bool {
	normalized := NormalizePath(filePath)

	// Simple filename (no slashes, no wildcards) — match by suffix
	if !strings.Contains(pattern, "/") && !strings.Contains(pattern, "*") {
		return normalized == pattern || strings.HasSuffix(normalized, "/"+pattern)
	}

	re, err := GlobToRegex(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(normalized)
}

// patternSpecificity scores a glob pattern; higher means more specific.
func patternSpecificity(pattern string) int {
	score := 0

	if !strings.Contains(pattern, "*") {
		score += 10000
	}

	score += (strings.Count(pattern, "/")) * 100

	// Count single stars (not globstars)
	temp := strings.ReplaceAll(pattern, "**", "")
	score += strings.Count(temp, "*") * 10

	score -= strings.Count(pattern, "**") * 50
	score += len(pattern)

	return score
}

// LimitResult holds a resolved limit and the pattern that matched.
type LimitResult struct {
	Limit   int
	Pattern string
}

// GetLimitForFile determines the token limit for a file.
func GetLimitForFile(filePath string, cfg TokenLimitsConfig) LimitResult {
	normalized := NormalizePath(filePath)

	// Check overrides first (exact matches)
	for overridePath, limit := range cfg.Overrides {
		if normalized == overridePath || strings.HasSuffix(normalized, "/"+overridePath) {
			return LimitResult{Limit: limit, Pattern: overridePath}
		}
	}

	// Sort defaults by specificity (highest first)
	type entry struct {
		pattern string
		limit   int
	}
	entries := make([]entry, 0, len(cfg.Defaults))
	for p, l := range cfg.Defaults {
		entries = append(entries, entry{p, l})
	}
	sort.Slice(entries, func(i, j int) bool {
		return patternSpecificity(entries[i].pattern) > patternSpecificity(entries[j].pattern)
	})

	for _, e := range entries {
		if MatchesPattern(normalized, e.pattern) {
			return LimitResult{Limit: e.limit, Pattern: e.pattern}
		}
	}

	return LimitResult{Limit: fallbackLimit, Pattern: "none"}
}
