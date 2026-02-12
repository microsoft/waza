package tokens

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spboyer/waza/cmd/waza/tokens/internal/tokens"
	"github.com/spf13/cobra"
)

func newCountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "count [paths...]",
		Short: "Count tokens in markdown files",
		Long: `Count tokens in markdown files.

Paths may be files or directories (scanned recursively for .md/.mdx files).
A relative path is resolved from the working directory; an absolute path is
used as-is. When no path is given, the working directory is scanned.`,
		Args: cobra.ArbitraryArgs,
		RunE: runCount,
	}
	cmd.Flags().String("format", "table", "Output format: json | table")
	cmd.Flags().String("sort", "path", "Sort table rows by: tokens | name | path")
	cmd.Flags().Int("min-tokens", 0, "Filter files with less than n tokens")
	cmd.Flags().Bool("no-total", false, "Hide total row in table output")
	return cmd
}

type countJSONOutput struct {
	GeneratedAt string                    `json:"generatedAt"`
	TotalTokens int                       `json:"totalTokens"`
	TotalFiles  int                       `json:"totalFiles"`
	Files       map[string]countFileEntry `json:"files"`
}

type countFileEntry struct {
	Tokens     int `json:"tokens"`
	Characters int `json:"characters"`
	Lines      int `json:"lines"`
}

func runCount(cmd *cobra.Command, args []string) error {
	format, err := cmd.Flags().GetString("format")
	if err != nil {
		return err
	}
	sortBy, err := cmd.Flags().GetString("sort")
	if err != nil {
		return err
	}
	minTokens, err := cmd.Flags().GetInt("min-tokens")
	if err != nil {
		return err
	}
	noTotal, err := cmd.Flags().GetBool("no-total")
	if err != nil {
		return err
	}
	if format == "json" {
		if cmd.Flags().Changed("sort") {
			return errors.New("--sort is only supported with table output")
		}
		if cmd.Flags().Changed("no-total") {
			return errors.New("--no-total is only supported with table output")
		}
	}

	rootDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}
	files, err := findMarkdownFiles(args, rootDir)
	if err != nil {
		return err
	}

	counter := tokens.NewEstimatingCounter()
	var results []FileResult
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("⚠️  Error reading %s: %w", f, err)
		}
		rel, err := filepath.Rel(rootDir, f)
		if err != nil {
			rel = f
		}
		r := countTokens(counter, string(content), rel)
		if r.Tokens >= minTokens {
			results = append(results, *r)
		}
	}

	sortResults(results, sortBy)

	out := cmd.OutOrStdout()
	if format == "json" {
		return outputCountJSON(out, results)
	}
	outputCountTable(out, results, !noTotal)
	return nil
}

func countTokens(counter tokens.Counter, text, relPath string) *FileResult {
	return &FileResult{
		Path:       filepath.Clean(relPath),
		Tokens:     counter.Count(text),
		Characters: len(text),
		Lines:      countLines(text),
	}
}

func sortResults(results []FileResult, by string) {
	sort.Slice(results, func(i, j int) bool {
		switch by {
		case "tokens":
			return results[i].Tokens > results[j].Tokens
		case "name":
			a := filepath.Base(results[i].Path)
			b := filepath.Base(results[j].Path)
			return strings.ToLower(a) < strings.ToLower(b)
		default:
			return results[i].Path < results[j].Path
		}
	})
}

func outputCountTable(w io.Writer, results []FileResult, showTotal bool) {
	if len(results) == 0 {
		fmt.Fprintln(w, "No markdown files found.")
		return
	}

	maxPath := 4
	for _, r := range results {
		if len(r.Path) > maxPath {
			maxPath = len(r.Path)
		}
	}

	header := fmt.Sprintf("%-*s  %8s  %8s  %6s", maxPath, "File", "Tokens", "Chars", "Lines")
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, strings.Repeat("-", len(header)))

	for _, r := range results {
		fmt.Fprintf(w, "%-*s  %8d  %8d  %6d\n", maxPath, r.Path, r.Tokens, r.Characters, r.Lines)
	}

	if showTotal {
		fmt.Fprintln(w, strings.Repeat("-", len(header)))
		var totalTokens, totalChars, totalLines int
		for _, r := range results {
			totalTokens += r.Tokens
			totalChars += r.Characters
			totalLines += r.Lines
		}
		fmt.Fprintf(w, "%-*s  %8d  %8d  %6d\n", maxPath, "Total", totalTokens, totalChars, totalLines)
		fmt.Fprintf(w, "\n%d file(s) scanned\n", len(results))
	}
}

func outputCountJSON(w io.Writer, results []FileResult) error {
	files := make(map[string]countFileEntry, len(results))
	totalTokens := 0
	for _, r := range results {
		totalTokens += r.Tokens
		files[r.Path] = countFileEntry{
			Tokens:     r.Tokens,
			Characters: r.Characters,
			Lines:      r.Lines,
		}
	}

	out := countJSONOutput{
		GeneratedAt: nowISO(),
		TotalTokens: totalTokens,
		TotalFiles:  len(results),
		Files:       files,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
