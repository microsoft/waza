package specverify

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/microsoft/waza/internal/dataset"
	"github.com/microsoft/waza/internal/models"
	"gopkg.in/yaml.v3"
)

var wordRE = regexp.MustCompile(`[a-z0-9]+`)

// SemanticMatcher can opt in to LLM-assisted coverage after deterministic matching.
type SemanticMatcher interface {
	Matches(ctx context.Context, req Requirement, task TaskRef) (bool, string, error)
}

// Options controls verification behavior.
type Options struct {
	SkillPath     string
	EvalPath      string
	Semantic      bool
	JudgeModel    string
	FailThreshold int
	Matcher       SemanticMatcher
}

// Verify parses SKILL.md, loads eval.yaml tasks, and computes requirement coverage.
func Verify(ctx context.Context, opts Options) (*Report, error) {
	skillSpec, err := ParseSkillFile(opts.SkillPath)
	if err != nil {
		return nil, err
	}

	spec, err := models.LoadEvalSpec(opts.EvalPath)
	if err != nil {
		return nil, fmt.Errorf("loading eval spec: %w", err)
	}

	tasks, err := loadTaskRefs(spec, filepath.Dir(opts.EvalPath))
	if err != nil {
		return nil, err
	}

	report := &Report{
		SkillPath:  opts.SkillPath,
		EvalPath:   opts.EvalPath,
		Semantic:   opts.Semantic,
		JudgeModel: opts.JudgeModel,
		Summary: Summary{
			TotalRequirements: len(skillSpec.Requirements),
			FailThreshold:     opts.FailThreshold,
		},
		Coverage: make([]RequirementCoverage, 0, len(skillSpec.Requirements)),
	}

	for _, req := range skillSpec.Requirements {
		rc := RequirementCoverage{Requirement: req, CoveredBy: []CoveredBy{}}
		for _, task := range tasks {
			if ok, reason := deterministicMatch(req, task); ok {
				rc.CoveredBy = append(rc.CoveredBy, coveredBy(task, "deterministic", reason))
			}
		}
		if len(rc.CoveredBy) == 0 && opts.Semantic && opts.Matcher != nil {
			for _, task := range tasks {
				if req.Kind == RequirementDont && !isNegativeTask(task) {
					continue
				}
				ok, reason, err := opts.Matcher.Matches(ctx, req, task)
				if err != nil {
					return nil, err
				}
				if ok {
					rc.CoveredBy = append(rc.CoveredBy, coveredBy(task, "semantic", reason))
				}
			}
		}
		if len(rc.CoveredBy) > 0 {
			report.Summary.CoveredRequirements++
		}
		report.Coverage = append(report.Coverage, rc)
	}

	report.Summary.UncoveredRequirements = report.Summary.TotalRequirements - report.Summary.CoveredRequirements
	return report, nil
}

func loadTaskRefs(spec *models.EvalSpec, specDir string) ([]TaskRef, error) {
	if spec.TasksFrom != "" {
		return loadTasksFromDataset(spec, specDir)
	}

	taskFiles, err := spec.ResolveTestFiles(specDir)
	if err != nil {
		return nil, err
	}
	sort.Strings(taskFiles)
	if len(taskFiles) == 0 {
		return nil, fmt.Errorf("no task files matched patterns %v in %s", spec.Tasks, specDir)
	}

	tasks := make([]TaskRef, 0, len(taskFiles))
	for _, path := range taskFiles {
		tc, err := models.LoadTestCase(path)
		if err != nil {
			return nil, fmt.Errorf("loading task %s: %w", path, err)
		}
		if tc.Active != nil && !*tc.Active {
			continue
		}
		tasks = append(tasks, taskRefFromTestCase(path, tc))
	}
	return tasks, nil
}

func loadTasksFromDataset(spec *models.EvalSpec, specDir string) ([]TaskRef, error) {
	path := spec.TasksFrom
	if !filepath.IsAbs(path) {
		path = filepath.Join(specDir, path)
	}
	if filepath.Ext(path) == ".csv" {
		rows, err := loadCSVRows(path, spec.Range)
		if err != nil {
			return nil, fmt.Errorf("loading CSV dataset: %w", err)
		}
		tasks := make([]TaskRef, 0, len(rows))
		for i, row := range rows {
			tasks = append(tasks, taskRefFromCSVRow(path, i+1, row))
		}
		return tasks, nil
	}

	return nil, fmt.Errorf("spec verify supports YAML task files and CSV tasks_from datasets; unsupported tasks_from %q", spec.TasksFrom)
}

func loadCSVRows(path string, taskRange [2]int) ([]dataset.Row, error) {
	if taskRange != [2]int{} {
		if taskRange[0] <= 0 || taskRange[1] <= 0 {
			return nil, fmt.Errorf("invalid range: both values must be > 0, got [%d, %d]", taskRange[0], taskRange[1])
		}
		if taskRange[0] > taskRange[1] {
			return nil, fmt.Errorf("invalid range: start (%d) must be <= end (%d)", taskRange[0], taskRange[1])
		}
		return dataset.LoadCSVRange(path, taskRange[0], taskRange[1])
	}
	return dataset.LoadCSV(path)
}

func taskRefFromCSVRow(path string, rowNum int, row dataset.Row) TaskRef {
	id := fmt.Sprintf("row-%d", rowNum)
	if value := strings.TrimSpace(row["id"]); value != "" {
		id = value
	} else if value := strings.TrimSpace(row["name"]); value != "" {
		id = value
	}

	name := fmt.Sprintf("row-%d", rowNum)
	if value := strings.TrimSpace(row["name"]); value != "" {
		name = value
	}

	keys := make([]string, 0, len(row))
	for key := range row {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key+": "+row[key])
	}

	return TaskRef{
		ID:   id,
		Name: name,
		Path: fmt.Sprintf("%s#row-%d", path, rowNum),
		Text: strings.Join(values, "\n"),
	}
}

func taskRefFromTestCase(path string, tc *models.TestCase) TaskRef {
	values := []string{
		tc.TestID,
		tc.DisplayName,
		tc.Summary,
		tc.Stimulus.Message,
		tc.Stimulus.MessageFile,
		tc.Stimulus.WorkDir,
	}
	values = append(values, tc.Tags...)
	values = append(values, tc.Expectation.MustInclude...)
	values = append(values, tc.Expectation.MustExclude...)
	values = append(values, tc.Expectation.MayInclude...)
	for _, outcome := range tc.Expectation.OutcomeSpecs {
		values = append(values, outcome.Category, fmt.Sprint(outcome.Value), outcome.Predicate)
	}
	values = append(values, tc.Expectation.BehaviorRules.MustUseTool...)
	values = append(values, tc.Expectation.BehaviorRules.ForbidTool...)
	metadataKeys := make([]string, 0, len(tc.Stimulus.Metadata))
	for key := range tc.Stimulus.Metadata {
		metadataKeys = append(metadataKeys, key)
	}
	sort.Strings(metadataKeys)
	for _, key := range metadataKeys {
		value := tc.Stimulus.Metadata[key]
		values = append(values, key, fmt.Sprint(value))
	}
	for _, resource := range tc.Stimulus.Resources {
		values = append(values, resource.Location, resource.Body)
	}
	for _, grader := range tc.Validators {
		values = append(values, grader.Identifier, string(grader.Kind), grader.Rubric)
		values = append(values, grader.Checks...)
		values = append(values, graderParametersText(grader.Parameters))
	}

	return TaskRef{
		ID:              fallbackTaskID(path, tc),
		Name:            tc.DisplayName,
		Path:            path,
		Text:            strings.Join(values, "\n"),
		ExpectedTrigger: tc.Expectation.ExpectedTrigger,
		Metadata:        tc.Stimulus.Metadata,
	}
}

func graderParametersText(params models.GraderParameters) string {
	if params == nil {
		return ""
	}
	data, err := yaml.Marshal(params)
	if err != nil {
		return fmt.Sprint(params)
	}
	return string(data)
}

func fallbackTaskID(path string, tc *models.TestCase) string {
	if strings.TrimSpace(tc.TestID) != "" {
		return strings.TrimSpace(tc.TestID)
	}
	if strings.TrimSpace(tc.DisplayName) != "" {
		return strings.TrimSpace(tc.DisplayName)
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

func deterministicMatch(req Requirement, task TaskRef) (bool, string) {
	taskText := normalizeText(task.Text)
	reqText := normalizeText(req.Text)
	if reqText == "" || taskText == "" {
		return false, ""
	}

	matched := strings.Contains(taskText, reqText)
	reason := "task text contains requirement phrase"
	if !matched && tokenCoverage(req.Text, taskText) {
		matched = true
		reason = "task text contains requirement keywords"
	}
	if !matched {
		return false, ""
	}
	if req.Kind == RequirementDont && !isNegativeTask(task) {
		return false, ""
	}
	return true, reason
}

func tokenCoverage(reqText, normalizedTask string) bool {
	tokens := significantTokens(reqText)
	if len(tokens) == 0 {
		return false
	}
	matches := 0
	for _, token := range tokens {
		if taskContainsToken(normalizedTask, token) {
			matches++
		}
	}
	if len(tokens) <= 2 {
		return matches == len(tokens)
	}
	return matches >= max(2, len(tokens)-1)
}

func taskContainsToken(normalizedTask, token string) bool {
	if strings.Contains(normalizedTask, token) {
		return true
	}
	if strings.HasSuffix(token, "s") && len(token) > 3 {
		return strings.Contains(normalizedTask, strings.TrimSuffix(token, "s"))
	}
	return false
}

func significantTokens(text string) []string {
	raw := wordRE.FindAllString(strings.ToLower(text), -1)
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, token := range raw {
		if len(token) < 3 || stopWords[token] || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "this": true, "that": true,
	"with": true, "from": true, "when": true, "what": true, "does": true,
	"use": true, "using": true, "into": true, "your": true, "you": true,
	"new": true, "not": true,
}

func isNegativeTask(task TaskRef) bool {
	if task.ExpectedTrigger != nil && !*task.ExpectedTrigger {
		return true
	}
	text := normalizeText(task.Text)
	for _, marker := range []string{"negative", "anti trigger", "should not trigger", "do not trigger", "forbidden skills"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func coveredBy(task TaskRef, mode, reason string) CoveredBy {
	return CoveredBy{
		TaskID:   task.ID,
		TaskName: task.Name,
		TaskPath: task.Path,
		Mode:     mode,
		Reason:   reason,
	}
}

func normalizeText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ParseSemanticResponse accepts a compact JSON or plain yes/no judge response.
func ParseSemanticResponse(raw string) (bool, string) {
	var structured struct {
		Covered bool   `json:"covered"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &structured); err == nil {
		return structured.Covered, strings.TrimSpace(structured.Reason)
	}
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(normalized, "yes") || strings.Contains(normalized, `"covered":true`) {
		return true, "semantic judge marked as covered"
	}
	return false, ""
}

func extractJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		return trimmed[start : end+1]
	}
	return trimmed
}
