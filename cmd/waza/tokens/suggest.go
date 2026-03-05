package tokens

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/checks"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/spinner"
	"github.com/microsoft/waza/internal/tokens"
	"github.com/microsoft/waza/internal/workspace"
	"github.com/spf13/cobra"
)

const (
	maxDecorativeEmojis = 2
	tokensPerEmoji      = 2
	largeCodeBlockLines = 10
	tokensPerCodeLine   = 16
	largeTableRows      = 10
	tokensPerTableRow   = 12
	maxCopilotWorkers   = 4
)

var newChatEngine = func(modelID string) execution.AgentEngine {
	return execution.NewCopilotEngineBuilder(modelID, nil).Build()
}

func newSuggestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "suggest [skill-name | paths...]",
		Short: "Get optimization suggestions",
		Long: `Analyze markdown files for token optimization opportunities.

Paths may be files or directories (scanned recursively for .md/.mdx files).
A relative path is resolved from the working directory; an absolute path is
used as-is.

When no path is given, workspace detection determines what to analyze:
  - In a multi-skill workspace, each skill is analyzed with per-skill headers
  - In a single-skill workspace, that skill's directory is analyzed
  - Otherwise, the working directory is scanned

If the first argument looks like a skill name (no path separators or file
extension), it is resolved via workspace detection to scope suggestions to
that skill's directory.`,
		Args:          cobra.ArbitraryArgs,
		RunE:          runSuggest,
		SilenceErrors: true,
	}
	cmd.Flags().String("format", "text", "Output format: json | text")
	cmd.Flags().Int("min-savings", 10, "Minimum number of tokens a suggestion must save to be included in the output (applies only to non-Copilot suggestions)")
	cmd.Flags().Bool("copilot", false, "Get LLM-powered suggestions from the GitHub Copilot SDK")
	cmd.Flags().String("model", "gpt-5-mini (medium)", "Model to use for Copilot suggestions")
	return cmd
}

type suggestion struct {
	Line             int    `json:"line"`
	Issue            string `json:"issue"`
	Suggestion       string `json:"suggestion"`
	EstimatedSavings int    `json:"estimatedSavings,omitempty"`
}

type fileAnalysis struct {
	Skill              string       `json:"skill,omitempty"`
	File               string       `json:"file"`
	Tokens             int          `json:"tokens"`
	Characters         int          `json:"characters"`
	Lines              int          `json:"lines"`
	Suggestions        []suggestion `json:"suggestions,omitempty"`
	PotentialSavings   int          `json:"potentialSavings,omitempty"`
	CopilotSuggestions string       `json:"copilotSuggestions,omitempty"`
}

func runSuggest(cmd *cobra.Command, args []string) error {
	format, err := cmd.Flags().GetString("format")
	if err != nil {
		return err
	}
	if format != "text" && format != "json" {
		return fmt.Errorf("invalid format %q", format)
	}
	minSavings, err := cmd.Flags().GetInt("min-savings")
	if err != nil {
		return err
	}
	copilot, err := cmd.Flags().GetBool("copilot")
	if err != nil {
		return err
	}
	modelID, err := cmd.Flags().GetString("model")
	if err != nil {
		return err
	}
	if !copilot && cmd.Flags().Changed("model") {
		return errors.New("--model is valid only with --copilot")
	}

	rootDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	var filePaths []string
	workspaceRoot := rootDir

	if len(args) > 0 && workspace.LooksLikePath(args[0]) {
		// Explicit paths — use as-is (escape hatch)
		filePaths = args
	} else if len(args) > 0 {
		// Skill name — resolve via workspace with config options
		ctx, ctxErr := workspace.DetectContext(rootDir, ConfigDetectOptions()...)
		if ctxErr != nil {
			return fmt.Errorf("detecting workspace: %w", ctxErr)
		}
		si, findErr := workspace.FindSkill(ctx, args[0])
		if findErr != nil {
			return findErr
		}
		rootDir = si.Dir
	} else {
		// No args: workspace-aware mode
		ctx, ctxErr := workspace.DetectContext(rootDir, ConfigDetectOptions()...)
		if ctxErr == nil {
			switch ctx.Type {
			case workspace.ContextMultiSkill:
				return runSuggestBatch(cmd, ctx.Skills, format, minSavings, copilot, modelID, workspaceRoot)
			case workspace.ContextSingleSkill:
				rootDir = ctx.Skills[0].Dir
			}
		}
		// ContextNone or error: fall through to CWD scan
	}

	counter, err := tokens.NewCounter(tokens.TokenizerDefault)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	var engine execution.AgentEngine
	if copilot {
		engine = newChatEngine(modelID)

		if err := engine.Initialize(cmd.Context()); err != nil {
			return err
		}

		defer func() {
			if shutdownErr := engine.Shutdown(cmd.Context()); shutdownErr != nil {
				fmt.Fprintf(errOut, "⚠️  error shutting down Copilot engine: %v\n", shutdownErr) //nolint:errcheck
			}
		}()
	}

	wsPrefix := computeWorkspaceRelPrefix(workspaceRoot, rootDir)

	var analyses []fileAnalysis
	if copilot {
		stopSpinner := spinner.Start(errOut, "🤖 Analyzing with Copilot...")
		analyses, err = collectFileAnalyses(rootDir, filePaths, counter, engine, cmd, wsPrefix)
		stopSpinner()
	} else {
		analyses, err = collectFileAnalyses(rootDir, filePaths, counter, nil, cmd, wsPrefix)
	}
	if err != nil {
		return err
	}

	analyses = filterSuggestions(analyses, minSavings)

	if format == "json" {
		s, err := suggestionJSON(analyses)
		if err != nil {
			return err
		}
		fmt.Fprint(out, s) //nolint:errcheck
		return nil
	}
	fmt.Fprint(out, suggestionText(analyses)) //nolint:errcheck
	return nil
}

// collectFileAnalyses discovers and analyzes files in rootDir.
// When engine is non-nil, Copilot-based analysis is used; otherwise heuristic analysis.
func collectFileAnalyses(rootDir string, paths []string, counter tokens.Counter, engine execution.AgentEngine, cmd *cobra.Command, workspaceRelPrefix string) ([]fileAnalysis, error) {
	cfg, usedLegacy := resolveLimitsConfig(rootDir)
	if usedLegacy {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "⚠️  Using legacy .token-limits.json — consider moving limits to .waza.yaml\n")
	}
	checker := &checks.TokenLimitsChecker{
		Config:             cfg,
		Paths:              paths,
		WorkspaceRelPrefix: workspaceRelPrefix,
	}
	limitsData, err := checker.Limits(skill.Skill{Path: filepath.Join(rootDir, "SKILL.md")})
	if err != nil {
		return nil, err
	}

	limitsByFile := make(map[string]int, len(limitsData.Results))
	var files []string
	for _, r := range limitsData.Results {
		absPath := filepath.Join(rootDir, filepath.FromSlash(r.File))
		files = append(files, absPath)
		limitsByFile[r.File] = r.Limit
	}

	errOut := cmd.ErrOrStderr()
	var analyses []fileAnalysis

	if engine != nil {
		ctx, cancel := context.WithTimeout(cmd.Context(), 4*time.Minute)
		defer cancel()

		type result struct {
			analysis fileAnalysis
			err      error
		}
		ch := make(chan result, len(files))
		sem := make(chan struct{}, maxCopilotWorkers)

		var wg sync.WaitGroup
		for _, f := range files {
			wg.Add(1)
			go func(f string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				relPath, relErr := filepath.Rel(rootDir, f)
				if relErr != nil {
					ch <- result{err: fmt.Errorf("getting relative path for %s: %w", f, relErr)}
					return
				}
				if relPath == "" {
					relPath = f
				}
				b, readErr := os.ReadFile(f)
				if readErr != nil {
					ch <- result{err: fmt.Errorf("reading file %s: %w", f, readErr)}
					return
				}
				text := string(b)
				res, reportErr := copilotReport(ctx, engine, text)
				if reportErr != nil {
					ch <- result{err: fmt.Errorf("getting Copilot suggestions for %s: %w", f, reportErr)}
					return
				}
				r := countTokens(counter, text, relPath)
				ch <- result{analysis: fileAnalysis{
					File:               r.Path,
					Tokens:             r.Tokens,
					Characters:         r.Characters,
					Lines:              r.Lines,
					CopilotSuggestions: res,
				}}
			}(f)
		}

		go func() {
			wg.Wait()
			close(ch)
		}()

		for r := range ch {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if r.err != nil {
				fmt.Fprintf(errOut, "⚠️  %s\n", r.err) //nolint:errcheck
				continue
			}
			analyses = append(analyses, r.analysis)
		}
	} else {
		for _, f := range files {
			a, aErr := analyzeFile(counter, f, rootDir, limitsByFile)
			if aErr != nil {
				fmt.Fprintf(errOut, "⚠️  Error analyzing %s: %s\n", f, aErr) //nolint:errcheck
				continue
			}
			analyses = append(analyses, *a)
		}
	}

	sort.Slice(analyses, func(i, j int) bool {
		return analyses[i].File < analyses[j].File
	})

	return analyses, nil
}

// runSuggestBatch runs token suggestions for each skill in a multi-skill workspace.
func runSuggestBatch(cmd *cobra.Command, skills []workspace.SkillInfo, format string, minSavings int, copilot bool, modelID string, workspaceRoot string) error {
	counter, err := tokens.NewCounter(tokens.TokenizerDefault)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	var engine execution.AgentEngine
	if copilot {
		engine = newChatEngine(modelID)
		defer func() {
			if shutdownErr := engine.Shutdown(cmd.Context()); shutdownErr != nil {
				fmt.Fprintf(errOut, "⚠️  error shutting down Copilot engine: %v\n", shutdownErr) //nolint:errcheck
			}
		}()
	}

	var allAnalyses []fileAnalysis
	for i, si := range skills {
		if i > 0 && format != "json" {
			fmt.Fprintln(out) //nolint:errcheck
		}
		if format != "json" {
			fmt.Fprintf(out, "─── %s ───\n", si.Name) //nolint:errcheck
		}

		analyses, aErr := collectFileAnalyses(si.Dir, nil, counter, engine, cmd, computeWorkspaceRelPrefix(workspaceRoot, si.Dir))
		if aErr != nil {
			fmt.Fprintf(errOut, "  ⚠️  %s: %s\n", si.Name, aErr) //nolint:errcheck
			continue
		}
		analyses = filterSuggestions(analyses, minSavings)

		if format == "json" {
			for j := range analyses {
				analyses[j].Skill = si.Name
			}
			allAnalyses = append(allAnalyses, analyses...)
		} else {
			fmt.Fprint(out, suggestionText(analyses)) //nolint:errcheck
		}
	}

	if format == "json" {
		s, err := suggestionJSON(allAnalyses)
		if err != nil {
			return err
		}
		fmt.Fprint(out, s) //nolint:errcheck
	}
	return nil
}

// isDecorativeEmoji matches decorative emoji Unicode ranges.
func isDecorativeEmoji(r rune) bool {
	return (r >= 0x1F300 && r <= 0x1F9FF) || (r >= 0x2600 && r <= 0x26FF)
}

// countEmojis counts decorative emojis in text.
func countEmojis(text string) int {
	count := 0
	for _, r := range text {
		if isDecorativeEmoji(r) {
			count++
		}
	}
	return count
}

// findDuplicates finds adjacent repeated substrings of at least minLen
// characters (e.g. "abcabc"). It does not detect non-contiguous duplicates.
func findDuplicates(text string, minLen int) []string {
	var results []string
	n := len(text)
	if n < minLen*2 {
		return results
	}

	for i := 0; i <= n-minLen*2; i++ {
		for subLen := minLen; i+subLen*2 <= n; subLen++ {
			sub := text[i : i+subLen]
			reps := 1
			pos := i + subLen
			for pos+subLen <= n && text[pos:pos+subLen] == sub {
				reps++
				pos += subLen
			}
			if reps > 1 {
				fullMatch := text[i:pos]
				results = append(results, fullMatch)
				i = pos - 1
				break
			}
		}
	}
	return results
}

// analyzeFile generates suggestions for a single file using simple heuristics.
func analyzeFile(counter tokens.Counter, filePath, rootDir string, limitsByFile map[string]int) (*fileAnalysis, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	rel, err := filepath.Rel(rootDir, filePath)
	if err != nil {
		rel = filePath
	}
	text := string(content)
	r := countTokens(counter, text, rel)
	lines := strings.Split(text, "\n")
	limit, ok := limitsByFile[r.Path]
	if !ok {
		limit = checks.FallbackLimit
	}

	var suggestions []suggestion

	// Check for excessive decorative emojis
	emojiCount := countEmojis(text)
	if emojiCount > maxDecorativeEmojis {
		excess := emojiCount - maxDecorativeEmojis
		suggestions = append(suggestions, suggestion{
			Line:             1,
			Issue:            fmt.Sprintf("Found %d emojis (%d over recommended %d)", emojiCount, excess, maxDecorativeEmojis),
			Suggestion:       "Remove decorative emojis that don't aid comprehension",
			EstimatedSavings: excess * tokensPerEmoji,
		})
	}

	// Check for large code blocks
	inCodeBlock := false
	codeBlockStart := 0
	codeBlockLines := 0

	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			if !inCodeBlock {
				inCodeBlock = true
				codeBlockStart = i + 1
				codeBlockLines = 0
			} else {
				inCodeBlock = false
				if codeBlockLines > largeCodeBlockLines {
					excess := codeBlockLines - largeCodeBlockLines
					suggestions = append(suggestions, suggestion{
						Line:             codeBlockStart,
						Issue:            fmt.Sprintf("Code block with %d lines (%d over %d)", codeBlockLines, excess, largeCodeBlockLines),
						Suggestion:       "Consider truncating example or moving to reference file",
						EstimatedSavings: excess * tokensPerCodeLine,
					})
				}
			}
		} else if inCodeBlock {
			codeBlockLines++
		}
	}

	// Check for large tables
	tableStart := -1
	tableRows := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
			if tableStart == -1 {
				tableStart = i + 1
			}
			tableRows++
		} else if tableStart != -1 {
			if tableRows > largeTableRows {
				excess := tableRows - largeTableRows
				suggestions = append(suggestions, suggestion{
					Line:             tableStart,
					Issue:            fmt.Sprintf("Table with %d rows (%d over %d)", tableRows, excess, largeTableRows),
					Suggestion:       "Consider summarizing or moving to reference file",
					EstimatedSavings: excess * tokensPerTableRow,
				})
			}
			tableStart = -1
			tableRows = 0
		}
	}
	if tableRows > largeTableRows {
		excess := tableRows - largeTableRows
		suggestions = append(suggestions, suggestion{
			Line:             tableStart,
			Issue:            fmt.Sprintf("Table with %d rows (%d over %d)", tableRows, excess, largeTableRows),
			Suggestion:       "Consider summarizing or moving to reference file",
			EstimatedSavings: excess * tokensPerTableRow,
		})
	}

	// Check for duplicate content
	for _, dup := range findDuplicates(text, 20) {
		savings := counter.Count(dup) / 2
		suggestions = append(suggestions, suggestion{
			Line:             1,
			Issue:            "Potential duplicate content detected",
			Suggestion:       "Remove redundant text or use references",
			EstimatedSavings: savings,
		})
	}

	// Check for excessive horizontal rules
	hrRegex := regexp.MustCompile(`(?m)^-{3,}$|^\*{3,}$|^_{3,}$`)
	hrMatches := hrRegex.FindAllString(text, -1)
	if len(hrMatches) > 3 {
		suggestions = append(suggestions, suggestion{
			Line:             1,
			Issue:            fmt.Sprintf("Found %d horizontal rules", len(hrMatches)),
			Suggestion:       "Reduce visual separators, use headings instead",
			EstimatedSavings: (len(hrMatches) - 3) * 2,
		})
	}

	// Check if file exceeds limit
	if r.Tokens > limit {
		suggestions = append(suggestions, suggestion{
			Line:             1,
			Issue:            fmt.Sprintf("File exceeds token limit (%d/%d)", r.Tokens, limit),
			Suggestion:       "Split content into multiple files or use reference documents",
			EstimatedSavings: 0,
		})
	}

	totalSavings := 0
	for _, s := range suggestions {
		totalSavings += s.EstimatedSavings
	}

	return &fileAnalysis{
		File:             r.Path,
		Tokens:           r.Tokens,
		Characters:       r.Characters,
		Lines:            r.Lines,
		Suggestions:      suggestions,
		PotentialSavings: totalSavings,
	}, nil
}

// filterSuggestions removes suggestions below minSavings and recalculates
// per-file totals. Files left with no suggestions (and no Copilot output)
// are dropped entirely.
func filterSuggestions(analyses []fileAnalysis, minSavings int) []fileAnalysis {
	var out []fileAnalysis
	for _, a := range analyses {
		filtered := a
		var kept []suggestion
		for _, s := range a.Suggestions {
			if s.EstimatedSavings >= minSavings || s.EstimatedSavings == 0 {
				kept = append(kept, s)
			}
		}
		filtered.Suggestions = kept
		fileSavings := 0
		for _, s := range kept {
			fileSavings += s.EstimatedSavings
		}
		filtered.PotentialSavings = fileSavings
		if len(kept) > 0 || filtered.CopilotSuggestions != "" {
			out = append(out, filtered)
		}
	}
	return out
}

func suggestionText(analyses []fileAnalysis) string {
	if len(analyses) == 0 {
		return "✅ No optimization suggestions found.\n"
	}

	var buf strings.Builder
	totalSavings := 0
	for _, analysis := range analyses {
		totalSavings += analysis.PotentialSavings

		fmt.Fprintf(&buf, "\n📄 %s (%d tokens)\n", analysis.File, analysis.Tokens)
		fmt.Fprintln(&buf, strings.Repeat("-", 60))

		for _, s := range analysis.Suggestions {
			savings := ""
			if s.EstimatedSavings > 0 {
				savings = fmt.Sprintf(" (~%d tokens)", s.EstimatedSavings)
			}
			fmt.Fprintf(&buf, "  Line %d: %s\n", s.Line, s.Issue)
			fmt.Fprintf(&buf, "    💡 %s%s\n", s.Suggestion, savings)
		}

		if analysis.CopilotSuggestions != "" {
			fmt.Fprintln(&buf, "\n"+wrapText(analysis.CopilotSuggestions, 120))
		} else if analysis.PotentialSavings > 0 {
			fmt.Fprintf(&buf, "\n  Total potential savings: ~%d tokens\n", analysis.PotentialSavings)
		}
	}

	summary := fmt.Sprintf("\n📊 Summary: %d file(s) with suggestions", len(analyses))
	if totalSavings > 0 {
		summary += fmt.Sprintf(", ~%d potential token savings", totalSavings)
	}
	fmt.Fprintln(&buf, summary)
	return buf.String()
}

func suggestionJSON(analyses []fileAnalysis) (string, error) {
	totalSavings := 0
	for _, a := range analyses {
		totalSavings += a.PotentialSavings
	}

	out := struct {
		Timestamp             string         `json:"timestamp"`
		Analyses              []fileAnalysis `json:"analyses"`
		TotalPotentialSavings int            `json:"totalPotentialSavings,omitempty"`
	}{
		Timestamp:             nowISO(),
		Analyses:              analyses,
		TotalPotentialSavings: totalSavings,
	}

	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	err := enc.Encode(out)
	return buf.String(), err
}

//go:embed suggestion_prompt.md
var suggestionPrompt string

// wrapText word-wraps each paragraph in text to width columns. Lines that
// start with whitespace or are part of markdown structure (lists, headings,
// code fences) are kept as-is.
func wrapText(text string, width int) string {
	var out strings.Builder
	inCodeBlock := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			out.WriteString(trimmed)
			out.WriteByte('\n')
			continue
		}
		if inCodeBlock || len(trimmed) <= width || trimmed == "" ||
			strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "  ") ||
			strings.HasPrefix(trimmed, "\t") ||
			strings.HasPrefix(trimmed, "- ") ||
			strings.HasPrefix(trimmed, "* ") ||
			strings.HasPrefix(trimmed, "|") {
			out.WriteString(trimmed)
			out.WriteByte('\n')
			continue
		}
		words := strings.Fields(trimmed)
		col := 0
		for i, w := range words {
			wl := utf8.RuneCountInString(w)
			if i > 0 && col+1+wl > width {
				out.WriteByte('\n')
				col = 0
			} else if i > 0 {
				out.WriteByte(' ')
				col++
			}
			out.WriteString(w)
			col += wl
		}
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n")
}

func copilotReport(ctx context.Context, engine execution.AgentEngine, content string) (string, error) {
	res, err := engine.Execute(ctx, &execution.ExecutionRequest{
		Message: suggestionPrompt + content,
		Timeout: 60 * time.Second,
	})
	if err != nil {
		return "", err
	}
	return res.FinalOutput, nil
}
