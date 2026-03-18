package checks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/tokens"
)

// TokenLimitsChecker validates that markdown files are within configured token limits.
type TokenLimitsChecker struct {
	Config             TokenLimitsConfig // zero value triggers auto-loading from skill dir
	Paths              []string          // specific paths to check; nil scans skill dir
	Tokenizer          tokens.Tokenizer  // empty means use TokenizerDefault
	WorkspaceRelPrefix string            // skill dir relative to workspace root (e.g. "plugin/skills/azure-deploy")
}

// TokenLimitFileResult holds check results for a single file.
type TokenLimitFileResult struct {
	File     string
	Tokens   int
	Limit    int
	Pattern  string
	Exceeded bool
}

// TokenLimitsData holds the structured output of a token limits check.
type TokenLimitsData struct {
	Results       []TokenLimitFileResult
	TotalFiles    int
	ExceededCount int
}

var _ ComplianceChecker = (*TokenLimitsChecker)(nil)

func (c *TokenLimitsChecker) Name() string { return "token-limits" }

func (c *TokenLimitsChecker) Check(sk skill.Skill) (*CheckResult, error) {
	skillDir := filepath.Dir(sk.Path)

	cfg := c.Config
	if cfg.Defaults == nil {
		var err error
		cfg, err = LoadLimitsConfig(skillDir)
		if err != nil {
			return nil, fmt.Errorf("loading token limits config: %w", err)
		}
	}

	files, err := c.findFiles(skillDir)
	if err != nil {
		return nil, err
	}

	tokenizer := c.Tokenizer
	if tokenizer == "" {
		tokenizer = tokens.TokenizerDefault
	}
	counter, err := tokens.NewCounter(tokenizer)
	if err != nil {
		return nil, fmt.Errorf("creating token counter: %w", err)
	}

	var results []TokenLimitFileResult
	exceeded := 0
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f, err)
		}
		rel, err := filepath.Rel(skillDir, f)
		if err != nil {
			rel = f
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		count := counter.Count(string(content))
		lr := GetLimitForFile(rel, cfg, c.WorkspaceRelPrefix)
		isExceeded := count > lr.Limit
		if isExceeded {
			exceeded++
		}
		results = append(results, TokenLimitFileResult{
			File:     rel,
			Tokens:   count,
			Limit:    lr.Limit,
			Pattern:  lr.Pattern,
			Exceeded: isExceeded,
		})
	}

	summary := fmt.Sprintf("%d/%d files within limits", len(results)-exceeded, len(results))
	if exceeded > 0 {
		summary += fmt.Sprintf(" (%d exceeded)", exceeded)
	}

	return &CheckResult{
		Name:    c.Name(),
		Passed:  exceeded == 0,
		Summary: summary,
		Data: &TokenLimitsData{
			Results:       results,
			TotalFiles:    len(results),
			ExceededCount: exceeded,
		},
	}, nil
}

// Limits is a convenience wrapper that returns the typed data directly.
func (c *TokenLimitsChecker) Limits(sk skill.Skill) (*TokenLimitsData, error) {
	result, err := c.Check(sk)
	if err != nil {
		return nil, err
	}
	d, ok := result.Data.(*TokenLimitsData)
	if !ok {
		err = fmt.Errorf("unexpected data type: %T", result.Data)
	}
	return d, err
}

var excludedDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	"dist":         true,
	"coverage":     true,
}

func (c *TokenLimitsChecker) findFiles(rootDir string) ([]string, error) {
	paths := c.Paths
	if len(paths) == 0 {
		paths = []string{rootDir}
	}

	var result []string
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(rootDir, p)
		}

		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("stat %q: %w", p, err)
		}

		if !info.IsDir() {
			result = append(result, p)
			continue
		}

		err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && excludedDirs[d.Name()] {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				switch strings.ToLower(filepath.Ext(d.Name())) {
				case ".md", ".mdx":
					result = append(result, path)
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walking %q: %w", p, err)
		}
	}

	return result, nil
}
