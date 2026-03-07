package tokens

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/microsoft/waza/internal/checks"
	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/workspace"
	"github.com/spf13/cobra"
)

func newCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check [skill-name | paths...]",
		Short: "Check files against token limits",
		Long: `Check markdown files against token limits.

Limits are resolved in priority order:
  1. .waza.yaml tokens.limits section (primary — workspace-level config)
  2. .token-limits.json in the skill directory (legacy fallback; deprecation warning shown)
  3. Built-in defaults when neither config exists

Paths may be files or directories (scanned recursively for .md/.mdx files).
A relative path is resolved from the working directory; an absolute path is
used as-is.

When no path is given, workspace detection determines what to check:
  - In a multi-skill workspace, each skill is checked with per-skill headers
  - In a single-skill workspace, that skill's directory is checked
  - Otherwise, the working directory is scanned

If the first argument looks like a skill name (no path separators or file
extension), it is resolved via workspace detection to scope checking to that
skill's directory.

Patterns support workspace-root-relative paths (e.g., plugin/skills/**/SKILL.md).
Configure tokens.warningThreshold and tokens.fallbackLimit in .waza.yaml.`,
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
	workspaceRoot := rootDir

	var paths []string

	if len(args) > 0 && workspace.LooksLikePath(args[0]) {
		// Explicit paths — use as-is (escape hatch)
		paths = args
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
				return runCheckBatch(cmd, ctx.Skills, format, strict, quiet, workspaceRoot)
			case workspace.ContextSingleSkill:
				rootDir = ctx.Skills[0].Dir
			}
		}
		// ContextNone or error: fall through to CWD scan
	}

	results, err := computeCheckResults(rootDir, paths, computeWorkspaceRelPrefix(workspaceRoot, rootDir), cmd.ErrOrStderr())
	if err != nil {
		return err
	}

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

// computeCheckResults runs the token limits checker and returns sorted results.
func computeCheckResults(rootDir string, paths []string, workspaceRelPrefix string, errW io.Writer) ([]checkResult, error) {
	cfg, usedLegacy := resolveLimitsConfig(rootDir)
	if usedLegacy {
		_, _ = fmt.Fprintf(errW, "⚠️  Using legacy .token-limits.json — consider moving limits to .waza.yaml\n")
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

	var results []checkResult
	for _, r := range limitsData.Results {
		results = append(results, checkResult{
			File:     r.File,
			Tokens:   r.Tokens,
			Limit:    r.Limit,
			Pattern:  r.Pattern,
			Exceeded: r.Exceeded,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Exceeded != results[j].Exceeded {
			return results[i].Exceeded
		}
		return results[i].File < results[j].File
	})

	return results, nil
}

// batchCheckReport wraps per-skill check results for JSON output.
type batchCheckReport struct {
	Timestamp string             `json:"timestamp"`
	Skills    []skillCheckReport `json:"skills"`
}

// skillCheckReport holds check results for a single skill in batch mode.
type skillCheckReport struct {
	Skill         string        `json:"skill"`
	TotalFiles    int           `json:"totalFiles"`
	ExceededCount int           `json:"exceededCount"`
	Results       []checkResult `json:"results,omitempty"`
	Error         string        `json:"error,omitempty"`
}

// runCheckBatch runs token limit checks for each skill in a multi-skill workspace.
func runCheckBatch(cmd *cobra.Command, skills []workspace.SkillInfo, format string, strict bool, quiet bool, workspaceRoot string) error {
	out := cmd.OutOrStdout()
	anyExceeded := false

	var skillReports []skillCheckReport

	for i, si := range skills {
		if format == "table" {
			if i > 0 && !quiet {
				fmt.Fprintln(out) //nolint:errcheck
			}
			if !quiet {
				fmt.Fprintf(out, "─── %s ───\n", si.Name) //nolint:errcheck
			}
		}

		results, err := computeCheckResults(si.Dir, nil, computeWorkspaceRelPrefix(workspaceRoot, si.Dir), cmd.ErrOrStderr())
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  ⚠️  %s: %s\n", si.Name, err) //nolint:errcheck
			if format == "json" {
				skillReports = append(skillReports, skillCheckReport{
					Skill: si.Name,
					Error: err.Error(),
				})
			}
			continue
		}

		exceeded := countExceeded(results)
		if exceeded > 0 {
			anyExceeded = true
		}

		switch format {
		case "json":
			skillReports = append(skillReports, skillCheckReport{
				Skill:         si.Name,
				TotalFiles:    len(results),
				ExceededCount: exceeded,
				Results:       results,
			})
		case "table":
			if !quiet {
				if _, wErr := fmt.Fprint(out, checkTable(results)); wErr != nil {
					return fmt.Errorf("writing output: %w", wErr)
				}
			}
		default:
			return errors.New("invalid format: " + format)
		}
	}

	if format == "json" {
		report := batchCheckReport{
			Timestamp: nowISO(),
			Skills:    skillReports,
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
		if _, err := fmt.Fprint(out, buf.String()); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	}

	if strict && anyExceeded {
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
		return errors.New("one or more skills have files exceeding token limits")
	}
	return nil
}
