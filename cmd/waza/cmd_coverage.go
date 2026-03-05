package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/skill"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type coverageSkillRow struct {
	Skill    string   `json:"skill"`
	Tasks    int      `json:"tasks"`
	Graders  []string `json:"graders"`
	Coverage string   `json:"coverage"`
}

type coverageReport struct {
	TotalSkills int                `json:"total_skills"`
	Covered     int                `json:"covered"`
	Partial     int                `json:"partial"`
	Uncovered   int                `json:"uncovered"`
	CoveragePct float64            `json:"coverage_pct"`
	Skills      []coverageSkillRow `json:"skills"`
}

type evalSpecLite struct {
	Skill   string                `yaml:"skill"`
	Tasks   []string              `yaml:"tasks"`
	Graders []models.GraderConfig `yaml:"graders"`
}

func newCoverageCommand() *cobra.Command {
	var outputFormat string
	var searchPaths []string

	cmd := &cobra.Command{
		Use:   "coverage [root]",
		Short: "Generate an eval coverage grid for discovered skills",
		Long: `Generate an eval coverage grid showing which skills have eval coverage.

By default, this command scans:
  - skills/ and .github/skills for SKILL.md files
  - evals/ and skill directories for eval.yaml files

Use --path to add additional directories to scan for eval and skill files.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := "."
			if len(args) > 0 {
				root = args[0]
			}

			report, err := buildCoverageReport(root, searchPaths)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "text":
				renderCoverageText(cmd.OutOrStdout(), report)
			case "markdown":
				renderCoverageMarkdown(cmd.OutOrStdout(), report)
			case "json":
				if err := renderCoverageJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported format %q: must be text, markdown, or json", outputFormat)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFormat, "format", "f", "text", "Output format: text, markdown, or json")
	cmd.Flags().StringArrayVar(&searchPaths, "path", nil, "Additional directories to scan for skills/evals (repeatable)")
	return cmd
}

func buildCoverageReport(root string, discoverPaths []string) (*coverageReport, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving root path: %w", err)
	}
	if _, err := os.Stat(absRoot); err != nil {
		return nil, fmt.Errorf("invalid root path %q: %w", root, err)
	}

	skillPaths, err := discoverSkillFiles(absRoot, discoverPaths)
	if err != nil {
		return nil, err
	}
	if len(skillPaths) == 0 {
		return nil, fmt.Errorf("no SKILL.md files found under %s", absRoot)
	}

	evalBySkill := make(map[string][]string)
	tasksBySkill := make(map[string]int)
	gradersBySkill := make(map[string]map[string]struct{})
	var parseFailures []string

	evalPaths, err := discoverEvalFiles(absRoot, skillPaths, discoverPaths)
	if err != nil {
		return nil, err
	}

	for _, evalPath := range evalPaths {
		spec, parseErr := parseEvalSpec(evalPath)
		if parseErr != nil {
			parseFailures = append(parseFailures, fmt.Sprintf("%s (%v)", evalPath, parseErr))
			continue
		}
		skillName := strings.TrimSpace(spec.Skill)
		if skillName == "" {
			skillName = inferSkillNameFromEvalPath(evalPath)
		}
		if skillName == "" {
			continue
		}
		evalBySkill[skillName] = append(evalBySkill[skillName], evalPath)
		tasksBySkill[skillName] += len(spec.Tasks)
		if _, ok := gradersBySkill[skillName]; !ok {
			gradersBySkill[skillName] = make(map[string]struct{})
		}
		for _, g := range spec.Graders {
			kind := strings.TrimSpace(string(g.Kind))
			if kind != "" {
				gradersBySkill[skillName][kind] = struct{}{}
			}
		}
	}
	if len(parseFailures) > 0 {
		sort.Strings(parseFailures)
		return nil, fmt.Errorf("failed to parse %d eval files: %s", len(parseFailures), strings.Join(parseFailures, "; "))
	}

	skillNames := make([]string, 0, len(skillPaths))
	for name := range skillPaths {
		skillNames = append(skillNames, name)
	}
	sort.Strings(skillNames)

	report := &coverageReport{
		TotalSkills: len(skillNames),
		Skills:      make([]coverageSkillRow, 0, len(skillNames)),
	}

	for _, name := range skillNames {
		graderSet := gradersBySkill[name]
		graders := sortedKeys(graderSet)
		tasks := tasksBySkill[name]
		hasEval := len(evalBySkill[name]) > 0

		coverage := "❌ None"
		switch {
		case !hasEval:
			report.Uncovered++
		case tasks > 0 && len(graders) >= 2:
			coverage = "✅ Full"
			report.Covered++
		default:
			coverage = "⚠️ Partial"
			report.Partial++
		}

		report.Skills = append(report.Skills, coverageSkillRow{
			Skill:    name,
			Tasks:    tasks,
			Graders:  graders,
			Coverage: coverage,
		})
	}

	if report.TotalSkills > 0 {
		report.CoveragePct = float64(report.Covered+report.Partial) * 100 / float64(report.TotalSkills)
	}
	return report, nil
}

func discoverSkillFiles(root string, discoverPaths []string) (map[string]string, error) {
	searchRoots := []string{
		filepath.Join(root, "skills"),
		filepath.Join(root, ".github", "skills"),
	}
	for _, p := range discoverPaths {
		searchRoots = append(searchRoots, resolvePath(root, p))
	}

	found := make(map[string]string)
	seenPaths := make(map[string]struct{})

	for _, sr := range searchRoots {
		if !isDir(sr) {
			continue
		}
		err := filepath.WalkDir(sr, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
					return fs.SkipDir
				}
				return nil
			}
			if d.Name() != "SKILL.md" {
				return nil
			}
			absPath, _ := filepath.Abs(path)
			if _, ok := seenPaths[absPath]; ok {
				return nil
			}
			seenPaths[absPath] = struct{}{}
			skillName := parseSkillName(absPath)
			if skillName == "" {
				skillName = filepath.Base(filepath.Dir(absPath))
			}
			if _, exists := found[skillName]; !exists {
				found[skillName] = absPath
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walking skill directory %s: %w", sr, err)
		}
	}

	return found, nil
}

func discoverEvalFiles(root string, skillPaths map[string]string, discoverPaths []string) ([]string, error) {
	searchRoots := []string{filepath.Join(root, "evals")}
	for _, p := range discoverPaths {
		searchRoots = append(searchRoots, resolvePath(root, p))
	}

	candidates := make(map[string]struct{})

	for _, evalRoot := range searchRoots {
		if !isDir(evalRoot) {
			continue
		}
		if err := filepath.WalkDir(evalRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
					return fs.SkipDir
				}
				return nil
			}
			if d.Name() == "eval.yaml" || d.Name() == "eval.yml" {
				absPath, _ := filepath.Abs(path)
				candidates[absPath] = struct{}{}
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("walking eval directory %s: %w", evalRoot, err)
		}
	}

	for _, skillPath := range skillPaths {
		skillDir := filepath.Dir(skillPath)
		for _, rel := range []string{
			"eval.yaml", "eval.yml",
			filepath.Join("evals", "eval.yaml"), filepath.Join("evals", "eval.yml"),
			filepath.Join("tests", "eval.yaml"), filepath.Join("tests", "eval.yml"),
		} {
			p := filepath.Join(skillDir, rel)
			if isFile(p) {
				absPath, _ := filepath.Abs(p)
				candidates[absPath] = struct{}{}
			}
		}
	}

	evalPaths := make([]string, 0, len(candidates))
	for path := range candidates {
		evalPaths = append(evalPaths, path)
	}
	sort.Strings(evalPaths)
	return evalPaths, nil
}

func parseEvalSpec(evalPath string) (*evalSpecLite, error) {
	data, err := os.ReadFile(evalPath)
	if err != nil {
		return nil, err
	}
	var spec evalSpecLite
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func parseSkillName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var sk skill.Skill
	if err := sk.UnmarshalText(data); err != nil {
		return ""
	}
	return strings.TrimSpace(sk.Frontmatter.Name)
}

func inferSkillNameFromEvalPath(evalPath string) string {
	parent := filepath.Base(filepath.Dir(evalPath))
	switch parent {
	case "evals", "tests":
		return filepath.Base(filepath.Dir(filepath.Dir(evalPath)))
	default:
		return parent
	}
}

func renderCoverageText(w io.Writer, report *coverageReport) {
	fmt.Fprintln(w, "📊 Eval Coverage Grid")                                                                               //nolint:errcheck
	fmt.Fprintf(w, "Coverage: %.1f%% (%d/%d)\n\n", report.CoveragePct, report.Covered+report.Partial, report.TotalSkills) //nolint:errcheck

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Skill\tTasks\tGraders\tCoverage") //nolint:errcheck
	fmt.Fprintln(tw, "-----\t-----\t-------\t--------") //nolint:errcheck
	for _, row := range report.Skills {
		graders := "—"
		if len(row.Graders) > 0 {
			graders = strings.Join(row.Graders, ", ")
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", row.Skill, row.Tasks, graders, row.Coverage) //nolint:errcheck
	}
	_ = tw.Flush()
}

func renderCoverageMarkdown(w io.Writer, report *coverageReport) {
	fmt.Fprintln(w, "📊 Eval Coverage Grid")                   //nolint:errcheck
	fmt.Fprintln(w, "| Skill | Tasks | Graders | Coverage |") //nolint:errcheck
	fmt.Fprintln(w, "|-------|-------|---------|----------|") //nolint:errcheck
	for _, row := range report.Skills {
		graders := "—"
		if len(row.Graders) > 0 {
			graders = strings.Join(row.Graders, ", ")
		}
		fmt.Fprintf(w, "| %s | %d | %s | %s |\n", row.Skill, row.Tasks, graders, row.Coverage) //nolint:errcheck
	}
}

func renderCoverageJSON(w io.Writer, report *coverageReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func resolvePath(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
