package tokens

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spboyer/waza/cmd/waza/tokens/internal"
	"github.com/spboyer/waza/internal/tokens"
	"github.com/spboyer/waza/internal/workspace"
	"github.com/spf13/cobra"
)

func newCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check [skill-name | paths...]",
		Short: "Check files against token limits",
		Long: `Check markdown files against token limits from .token-limits.json.

Paths may be files or directories (scanned recursively for .md/.mdx files).
A relative path is resolved from the working directory; an absolute path is
used as-is. When no path is given, the working directory is scanned.

If the first argument looks like a skill name (no path separators or file
extension), it is resolved via workspace detection to scope checking to that
skill's directory.

When no .token-limits.json is found, these defaults apply:

  defaults:
    SKILL.md              500 tokens
    references/**/*.md   1000 tokens
    docs/**/*.md         1500 tokens
    *.md                 2000 tokens

  overrides (not subject to the *.md default):
    README.md            3000 tokens
    CONTRIBUTING.md      2500 tokens`,
		Args: cobra.ArbitraryArgs,
		RunE: runCheck,
	}
	cmd.Flags().String("format", "table", "Output format: json | table")
	cmd.Flags().Bool("strict", false, "Exit with code 1 if any file exceeds its limit")
	cmd.Flags().Bool("quiet", false, "Suppress output when no limit is exceeded")
	return cmd
}

type checkResult struct {
	File     string `json:"file"`
	Tokens   int    `json:"tokens"`
	Limit    int    `json:"limit"`
	Pattern  string `json:"pattern,omitempty"`
	Exceeded bool   `json:"exceeded"`
}

type checkReport struct {
	Timestamp     string        `json:"timestamp"`
	TotalFiles    int           `json:"totalFiles"`
	ExceededCount int           `json:"exceededCount"`
	Results       []checkResult `json:"results"`
}

func runCheck(cmd *cobra.Command, args []string) error {
	format, err := cmd.Flags().GetString("format")
	if err != nil {
		return err
	}
	strict, err := cmd.Flags().GetBool("strict")
	if err != nil {
		return err
	}
	quiet, err := cmd.Flags().GetBool("quiet")
	if err != nil {
		return err
	}

	rootDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// If the first arg looks like a skill name (not a path), resolve via workspace
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
		args = nil // reset paths since we're scoping to skill dir
	}

	cfg, err := internal.LoadConfig(rootDir)
	if err != nil {
		return err
	}

	files, err := findMarkdownFiles(args, rootDir)
	if err != nil {
		return err
	}

	counter, err := tokens.NewCounter(tokens.TokenizerDefault)
	if err != nil {
		return err
	}
	var results []checkResult
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

		lr := internal.GetLimitForFile(r.Path, cfg)
		results = append(results, checkResult{
			File:     r.Path,
			Tokens:   r.Tokens,
			Limit:    lr.Limit,
			Pattern:  lr.Pattern,
			Exceeded: r.Tokens > lr.Limit,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Exceeded != results[j].Exceeded {
			return results[i].Exceeded
		}
		return results[i].File < results[j].File
	})

	output := ""
	switch format {
	case "json":
		output, err = checkJSON(results)
	case "table":
		output = checkTable(results)
	default:
		err = errors.New("invalid format: " + format)
	}
	if err != nil {
		return err
	}

	exceeded := countExceeded(results)
	if strict {
		if exceeded > 0 {
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			return errors.New(output)
		}
	}

	if !quiet {
		if _, err := fmt.Fprint(cmd.OutOrStdout(), output); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	}

	return nil
}

func countExceeded(results []checkResult) int {
	n := 0
	for _, r := range results {
		if r.Exceeded {
			n++
		}
	}
	return n
}

func checkTable(results []checkResult) string {
	if len(results) == 0 {
		return "No markdown files found."
	}

	var buf strings.Builder
	passed := 0
	var exceeded []checkResult
	for _, r := range results {
		if r.Exceeded {
			exceeded = append(exceeded, r)
		} else {
			passed++
		}
	}

	maxPath := len("File")
	tokW := len("Tokens")
	limW := len("Limit")
	for _, r := range results {
		if len(r.File) > maxPath {
			maxPath = len(r.File)
		}
		if w := len(strconv.Itoa(r.Tokens)); w > tokW {
			tokW = w
		}
		if w := len(strconv.Itoa(r.Limit)); w > limW {
			limW = w
		}
	}

	header := fmt.Sprintf("%-*s  %*s  %*s  Status", maxPath, "File", tokW, "Tokens", limW, "Limit")
	fmt.Fprintln(&buf, header)
	fmt.Fprintln(&buf, strings.Repeat("-", len(header)+10))

	for _, r := range results {
		status := "✅ OK"
		if r.Exceeded {
			status = "❌ EXCEEDED"
		}
		fmt.Fprintf(&buf, "%-*s  %*d  %*d  %s\n", maxPath, r.File, tokW, r.Tokens, limW, r.Limit, status)
	}

	fmt.Fprintln(&buf, strings.Repeat("-", len(header)+10))
	fmt.Fprintf(&buf, "\n%d/%d files within limits\n", passed, len(results))

	if len(exceeded) > 0 {
		fmt.Fprintf(&buf, "\n⚠️  %d file(s) exceed their token limits:\n", len(exceeded))
		for _, r := range exceeded {
			over := r.Tokens - r.Limit
			fmt.Fprintf(&buf, "   %s: %d tokens (%d over limit of %d)\n", r.File, r.Tokens, over, r.Limit)
		}
	}

	return buf.String()
}

func checkJSON(results []checkResult) (string, error) {
	report := checkReport{
		Timestamp:     nowISO(),
		TotalFiles:    len(results),
		ExceededCount: countExceeded(results),
		Results:       results,
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	err := enc.Encode(report)
	return buf.String(), err
}
