package tokens

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spboyer/waza/internal/projectconfig"
	"github.com/spboyer/waza/internal/tokens"
	"github.com/spboyer/waza/internal/workspace"
	"github.com/spf13/cobra"
)

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile [skill-name | path]",
		Short: "Structural analysis of SKILL.md files",
		Long: `Analyze the structure of SKILL.md files, reporting token count,
section count, code block count, and workflow step detection.

If the argument looks like a skill name (no path separators or file
extension), it is resolved via workspace detection.

Produces a one-line summary and optional warnings for each file.`,
		Args: cobra.ArbitraryArgs,
		RunE: runProfile,
	}
	cmd.Flags().String("format", "text", "Output format: json | text")
	cmd.Flags().String("tokenizer", "bpe", "Tokenizer to use: "+strings.Join(tokens.ValidTokenizers, " | "))
	return cmd
}

// SkillProfile holds structural analysis results for a single SKILL.md file.
type SkillProfile struct {
	Name          string   `json:"name"`
	Path          string   `json:"path"`
	Tokens        int      `json:"tokens"`
	Sections      int      `json:"sections"`
	CodeBlocks    int      `json:"codeBlocks"`
	WorkflowSteps int      `json:"workflowSteps"`
	DetailLevel   string   `json:"detailLevel"`
	Warnings      []string `json:"warnings,omitempty"`
}

const sectionWarningMinimum = 3

// tokenWarningThreshold returns the configured token warning threshold,
// falling back to 2500 if not set in .waza.yaml.
func tokenWarningThreshold() int {
	cfg, err := projectconfig.Load(".")
	if err != nil || cfg == nil {
		return projectconfig.DefaultTokenWarningThreshold
	}
	if cfg.Tokens.WarningThreshold <= 0 {
		return projectconfig.DefaultTokenWarningThreshold
	}
	return cfg.Tokens.WarningThreshold
}

var numberedStepRe = regexp.MustCompile(`(?m)^\s*\d+\.\s+`)

// analyzeSkillProfile performs structural analysis on SKILL.md content.
func analyzeSkillProfile(content string, relPath string, counter tokens.Counter) *SkillProfile {
	name := inferSkillName(relPath)
	toks := counter.Count(content)
	sections := countSections(content)
	codeBlocks := countCodeBlocks(content)
	steps := countWorkflowSteps(content)

	detail := "minimal"
	if sections >= 5 && codeBlocks >= 2 {
		detail = "detailed"
	} else if sections >= 3 {
		detail = "standard"
	}

	var warnings []string
	if steps == 0 {
		warnings = append(warnings, "no workflow steps detected")
	}
	threshold := tokenWarningThreshold()
	if toks > threshold {
		warnings = append(warnings, fmt.Sprintf("token count %d exceeds %d", toks, threshold))
	}
	if sections < sectionWarningMinimum {
		warnings = append(warnings, fmt.Sprintf("only %d sections (minimum recommended: %d)", sections, sectionWarningMinimum))
	}

	return &SkillProfile{
		Name:          name,
		Path:          relPath,
		Tokens:        toks,
		Sections:      sections,
		CodeBlocks:    codeBlocks,
		WorkflowSteps: steps,
		DetailLevel:   detail,
		Warnings:      warnings,
	}
}

// countSections counts markdown headings (lines starting with ## or deeper).
func countSections(content string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") ||
			strings.HasPrefix(trimmed, "#### ") || strings.HasPrefix(trimmed, "##### ") {
			n++
		}
	}
	return n
}

// countCodeBlocks counts fenced code blocks (``` delimiters).
func countCodeBlocks(content string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			n++
		}
	}
	return n / 2 // each block has opening and closing fence
}

// countWorkflowSteps counts numbered list items (e.g., "1. Step one").
func countWorkflowSteps(content string) int {
	return len(numberedStepRe.FindAllString(content, -1))
}

// inferSkillName extracts skill name from the file path.
func inferSkillName(relPath string) string {
	dir := filepath.Dir(relPath)
	if dir == "." || dir == "" {
		return strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
	}
	return filepath.Base(dir)
}

func runProfile(cmd *cobra.Command, args []string) error {
	format, err := cmd.Flags().GetString("format")
	if err != nil {
		return err
	}
	tokenizer, err := cmd.Flags().GetString("tokenizer")
	if err != nil {
		return err
	}
	if err = tokens.ValidateTokenizer(tokenizer); err != nil {
		return err
	}

	rootDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// If the first arg looks like a skill name, resolve via workspace
	if len(args) > 0 && !workspace.LooksLikePath(args[0]) {
		ctx, ctxErr := workspace.DetectContext(rootDir)
		if ctxErr != nil {
			return fmt.Errorf("detecting workspace: %w", ctxErr)
		}
		si, findErr := workspace.FindSkill(ctx, args[0])
		if findErr != nil {
			return findErr
		}
		rootDir = si.Dir
		args = nil
	}

	// Find SKILL.md files specifically
	files, err := findSkillFiles(args, rootDir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No SKILL.md files found.")
		return nil
	}

	counter, err := tokens.NewCounter(tokens.Tokenizer(tokenizer))
	if err != nil {
		return err
	}

	var profiles []*SkillProfile
	for _, f := range files {
		content, readErr := os.ReadFile(f)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", f, readErr)
		}
		rel, relErr := filepath.Rel(rootDir, f)
		if relErr != nil {
			rel = f
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		profiles = append(profiles, analyzeSkillProfile(string(content), rel, counter))
	}

	out := cmd.OutOrStdout()
	if format == "json" {
		return outputProfileJSON(out, profiles)
	}
	outputProfileText(out, profiles)
	return nil
}

// findSkillFiles finds SKILL.md files in the given paths or rootDir.
func findSkillFiles(paths []string, rootDir string) ([]string, error) {
	allMD, err := findMarkdownFiles(paths, rootDir)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, f := range allMD {
		base := strings.ToUpper(filepath.Base(f))
		if base == "SKILL.MD" || base == "SKILL.MDX" {
			result = append(result, f)
		}
	}
	return result, nil
}

func outputProfileText(w io.Writer, profiles []*SkillProfile) {
	for _, p := range profiles {
		detailMark := "✗"
		if p.DetailLevel == "detailed" {
			detailMark = "✓"
		}
		_, _ = fmt.Fprintf(w, "📊 %s: %s tokens (%s %s), %d sections, %d code blocks\n",
			p.Name, formatNumber(p.Tokens), p.DetailLevel, detailMark, p.Sections, p.CodeBlocks)
		for _, warn := range p.Warnings {
			_, _ = fmt.Fprintf(w, "   ⚠️  %s\n", warn)
		}
	}
}

func outputProfileJSON(w io.Writer, profiles []*SkillProfile) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if len(profiles) == 1 {
		return enc.Encode(profiles[0])
	}
	return enc.Encode(profiles)
}

// formatNumber adds comma separators to integers (e.g., 1722 → "1,722").
func formatNumber(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}
