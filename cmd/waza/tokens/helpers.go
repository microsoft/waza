package tokens

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/checks"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/workspace"
)

// FileResult holds token count results for a single file.
type FileResult struct {
	Path       string
	Tokens     int
	Characters int
	Lines      int
}

// nowISO returns the current time in ISO 8601 format.
func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// countLines returns the number of lines in s. An empty string has 0 lines.
// A trailing newline does not count as an additional line (matches wc -l behavior
// for files that end with a newline).
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

var excludedDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	"dist":         true,
	"coverage":     true,
}

// findMarkdownFiles takes a user-provided path (file or directory) and
// returns a list of markdown file paths. If paths is empty, scans rootDir.
func findMarkdownFiles(paths []string, rootDir string) ([]string, error) {
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

// ConfigDetectOptions loads .waza.yaml project config and returns workspace
// DetectOptions derived from the configured paths.
func ConfigDetectOptions() []workspace.DetectOption {
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	cfg, err := projectconfig.Load(wd)
	if err != nil {
		return nil
	}
	return []workspace.DetectOption{
		workspace.WithSkillsDir(cfg.Paths.Skills),
		workspace.WithEvalsDir(cfg.Paths.Evals),
	}
}

// resolveLimitsConfig returns a TokenLimitsConfig using .waza.yaml as the
// primary source and a flag indicating whether the legacy .token-limits.json
// was used as fallback. When usedLegacy is true the caller should emit a
// deprecation warning. Falls back to built-in defaults (via Check()) when
// neither source provides limits.
//
// Priority:
//  1. .waza.yaml tokens.limits (primary)
//  2. .token-limits.json (legacy fallback — caller emits deprecation warning)
//  3. Built-in defaults
func resolveLimitsConfig(skillDir string) (cfg checks.TokenLimitsConfig, usedLegacy bool) {
	// Primary: .waza.yaml tokens.limits
	pcfg, err := projectconfig.Load(skillDir)
	if err == nil && hasConfiguredTokenLimits(pcfg.Tokens.Limits) {
		defaults := pcfg.Tokens.Limits.Defaults
		if defaults == nil {
			defaults = make(map[string]int)
		}
		overrides := pcfg.Tokens.Limits.Overrides
		if overrides == nil {
			overrides = make(map[string]int)
		}
		return checks.TokenLimitsConfig{
			Defaults:  defaults,
			Overrides: overrides,
		}, false
	}

	// Legacy fallback: .token-limits.json — let Check() load it.
	if _, statErr := os.Stat(filepath.Join(skillDir, ".token-limits.json")); statErr == nil {
		return checks.TokenLimitsConfig{}, true
	}

	// Neither source has limits — Check() will apply built-in defaults.
	return checks.TokenLimitsConfig{}, false
}

func hasConfiguredTokenLimits(limits *projectconfig.TokenLimitsConfig) bool {
	if limits == nil {
		return false
	}
	return limits.Defaults != nil || limits.Overrides != nil
}

// computeWorkspaceRelPrefix returns the forward-slash-separated path from
// workspaceRoot to skillDir, or "" when they are the same or the relation
// cannot be computed.
func computeWorkspaceRelPrefix(workspaceRoot, skillDir string) string {
	if workspaceRoot == "" || skillDir == "" || workspaceRoot == skillDir {
		return ""
	}
	rel, err := filepath.Rel(workspaceRoot, skillDir)
	if err != nil || rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}
