package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	waza "github.com/spboyer/waza"
	"github.com/spboyer/waza/internal/dataset"
	"github.com/spboyer/waza/internal/execution"
	"github.com/spboyer/waza/internal/models"
	"github.com/spboyer/waza/internal/template"
	"github.com/spboyer/waza/internal/transcript"
	"github.com/spboyer/waza/internal/utils"
	"gopkg.in/yaml.v3"
)

const (
	maxSuggestionErrorLen        = 400
	maxSuggestionOutputLen       = 900
	maxSuggestionTraceEntryLen   = 500
	maxSuggestionToolSummaryLen  = 350
	maxSuggestionGraderDetailLen = 300
	maxSuggestionDebugLines      = 40
)

var (
	//go:embed eval_analysis_prompt.md
	evalAnalysisPrompt string
)

func generateEvalAnalysis(
	ctx context.Context,
	engine execution.AgentEngine,
	spec *models.BenchmarkSpec,
	specPath string,
	outcome *models.EvaluationOutcome,
	triggerResults []models.TriggerResult,
) (string, error) {
	failingTests := selectFailingTests(outcome.TestOutcomes)
	failedTriggers := selectFailedTriggerResults(triggerResults)
	if len(failingTests) == 0 && len(failedTriggers) == 0 {
		// Nothing failed, so there's nothing to suggest.
		return "", nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	resolvedSkillPaths := resolveSuggestionSkillPaths(spec, specPath)

	testDefinitions := map[string]string{}
	if len(failingTests) > 0 {
		var err error
		testDefinitions, err = loadTestDefinitionYAML(spec, specPath)
		if err != nil {
			return "", fmt.Errorf("loading test definitions: %w", err)
		}
	}

	if spec.Config.EngineType != "copilot-sdk" {
		return generateFakeSuggestionReport(spec, len(failingTests), len(failedTriggers)), nil
	}

	resources := loadSkillResources(resolvedSkillPaths)
	prompt := buildRunAnalysisPrompt(spec, failingTests, failedTriggers, testDefinitions)
	res, err := engine.Execute(ctx, &execution.ExecutionRequest{
		Message:    prompt,
		Timeout:    120 * time.Second,
		SkillPaths: resolvedSkillPaths,
		Resources:  resources,
	})
	if err != nil {
		return "", fmt.Errorf("getting Copilot suggestions: %w", err)
	}
	if res == nil {
		return "", errors.New("no suggestions from Copilot (empty response)")
	}

	writeSuggestionTranscript(prompt, res)

	report := strings.TrimSpace(res.FinalOutput)
	if report == "" {
		return "", buildNoSuggestionsError(res)
	}
	return report, nil
}

func buildNoSuggestionsError(res *execution.ExecutionResponse) error {
	trace := extractCopilotTrace(transcript.BuildFromSessionEvents(res.Events))
	if len(trace) == 0 {
		trace = summarizeSessionEventTypes(res.Events)
	}
	if len(trace) == 0 {
		return errors.New("no suggestions from Copilot (no session events captured)")
	}
	if len(trace) > maxSuggestionDebugLines {
		omitted := len(trace) - maxSuggestionDebugLines
		trace = append(trace[:maxSuggestionDebugLines], fmt.Sprintf("... %d additional event(s) omitted", omitted))
	}
	return fmt.Errorf("no suggestions from Copilot. Session transcript:\n- %s", strings.Join(trace, "\n- "))
}

func summarizeSessionEventTypes(events []copilot.SessionEvent) []string {
	if len(events) == 0 {
		return nil
	}
	lines := make([]string, 0, len(events))
	for i, evt := range events {
		lines = append(lines, fmt.Sprintf("event[%d]: %s", i+1, evt.Type))
	}
	return lines
}

func generateFakeSuggestionReport(spec *models.BenchmarkSpec, failedTests, failedTriggers int) string {
	totalFailures := failedTests + failedTriggers
	var b strings.Builder

	fmt.Fprintf(&b, "_Deterministic mock suggestion report (engine: `%s`)._\n\n", spec.Config.EngineType)
	fmt.Fprintf(&b, "1. Focus on resolving the %d failing case(s) from this run before broad skill changes.\n", totalFailures)
	if failedTests > 0 {
		fmt.Fprintf(&b, "2. Start with failing benchmark tasks (%d), aligning skill instructions to grader criteria and observed model/tool behavior.\n", failedTests)
	}
	if failedTriggers > 0 {
		fmt.Fprintf(&b, "3. Fix trigger routing for the %d failed trigger prompt(s), ensuring expected invoke/do-not-invoke behavior is explicit in the skill.\n", failedTriggers)
	}

	return b.String()
}

func resolveSuggestionSkillPaths(spec *models.BenchmarkSpec, specPath string) []string {
	specDir := filepath.Dir(specPath)
	paths := utils.ResolvePaths(spec.Config.SkillPaths, specDir)
	paths = append(paths, specDir)
	paths = append(paths, resolveEvaluatedSkillDirs(spec, specDir, paths)...)
	sort.Strings(paths)

	seen := make(map[string]bool, len(paths))
	unique := make([]string, 0, len(paths))
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		unique = append(unique, p)
	}
	return unique
}

func resolveEvaluatedSkillDirs(spec *models.BenchmarkSpec, specDir string, resolvedPaths []string) []string {
	if spec == nil || strings.TrimSpace(spec.SkillName) == "" {
		return nil
	}

	dirs := make([]string, 0)
	for _, base := range resolvedPaths {
		candidate := filepath.Join(base, spec.SkillName)
		if hasSkillFile(candidate) {
			dirs = append(dirs, candidate)
		}
	}

	parent := filepath.Dir(specDir)
	if parent != "" {
		candidate := filepath.Join(parent, "skills", spec.SkillName)
		if hasSkillFile(candidate) {
			dirs = append(dirs, candidate)
		}
	}

	return dirs
}

func hasSkillFile(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// maxResourceFileSize is the maximum size of a single file loaded as a resource
// for the suggestion workspace. Files larger than this are skipped.
const maxResourceFileSize = 100 * 1024 // 100 KB

// loadSkillResources walks each directory in paths and returns all text files
// as ResourceFile entries so they can be placed in the suggestion engine's
// workspace. Binary and oversized files are skipped.
func loadSkillResources(paths []string) []execution.ResourceFile {
	seen := make(map[string]bool)
	var resources []execution.ResourceFile

	for _, dir := range paths {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}

		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			if d.IsDir() {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}

			if !isTextFile(d.Name()) {
				return nil
			}

			fi, err := d.Info()
			if err != nil || fi.Size() > maxResourceFileSize || fi.Size() == 0 {
				return nil
			}

			rel, err := filepath.Rel(dir, path)
			if err != nil || rel == "." {
				return nil
			}
			// Use forward slashes for consistent workspace paths.
			rel = filepath.ToSlash(rel)
			if seen[rel] {
				return nil
			}

			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}

			seen[rel] = true
			resources = append(resources, execution.ResourceFile{
				Path:    rel,
				Content: string(content),
			})
			return nil
		})
	}
	return resources
}

// isTextFile returns true if the file extension looks like a text file that
// would be useful context for the suggesting LLM.
func isTextFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".md", ".yaml", ".yml", ".json", ".txt",
		".py", ".js", ".ts", ".go", ".sh", ".bash",
		".css", ".html", ".toml", ".cfg", ".ini",
		".xml", ".csv", ".sql", ".rb", ".rs", ".java",
		".c", ".h", ".cpp", ".hpp", ".cs", ".ps1":
		return true
	}
	// Also include extensionless files like SKILL.md patterns or Makefiles
	if ext == "" {
		base := strings.ToLower(name)
		switch base {
		case "makefile", "dockerfile", "license", "readme":
			return true
		}
	}
	return false
}

func buildRunAnalysisPrompt(
	spec *models.BenchmarkSpec,
	failingTests []models.TestOutcome,
	failedTriggers []models.TriggerResult,
	testDefinitions map[string]string,
) string {
	var p strings.Builder
	p.WriteString(evalAnalysisPrompt)
	p.WriteString(buildGraderDocsSection(spec, failingTests))
	p.WriteString(buildFailingTestEvidence(spec, failingTests, testDefinitions))
	p.WriteString("\n\n")
	p.WriteString(buildFailedTriggerEvidence(failedTriggers))
	return p.String()
}

// buildGraderDocsSection collects grader types from the spec and from failed
// test outcomes, then returns the embedded documentation for each type so the
// suggestion model understands how graders work and how to fix failures.
func buildGraderDocsSection(spec *models.BenchmarkSpec, failingTests []models.TestOutcome) string {
	kinds := collectFailedGraderKinds(spec, failingTests)
	if len(kinds) == 0 {
		return ""
	}

	allDocs := waza.GraderDocs()
	if len(allDocs) == 0 {
		return ""
	}

	sorted := make([]string, 0, len(kinds))
	for k := range kinds {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	formatted := waza.FormatGraderDocs(allDocs, sorted)
	if formatted == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n## Grader reference\n\n")
	b.WriteString("Documentation for the grader types used in this evaluation:\n\n")
	b.WriteString(formatted)
	return b.String()
}

// collectFailedGraderKinds returns the set of grader kind strings from the
// spec's global graders and from any failed grader results in test outcomes.
func collectFailedGraderKinds(spec *models.BenchmarkSpec, failingTests []models.TestOutcome) map[string]bool {
	kinds := make(map[string]bool)

	// Include global graders from the spec.
	for _, g := range spec.Graders {
		if g.Kind != "" {
			kinds[string(g.Kind)] = true
		}
	}

	// Include grader types from failed validations in test outcomes.
	for _, to := range failingTests {
		for _, run := range to.Runs {
			for _, v := range run.Validations {
				if !v.Passed && v.Type != "" {
					kinds[string(v.Type)] = true
				}
			}
		}
	}

	return kinds
}

func buildFailingTestEvidence(
	spec *models.BenchmarkSpec,
	failingTests []models.TestOutcome,
	testDefinitions map[string]string,
) string {
	if len(failingTests) == 0 {
		return ""
	}

	var b strings.Builder
	if len(spec.Graders) > 0 {
		b.WriteString("### Global graders from eval.yaml\n\n")
		b.WriteString("```yaml\n")
		b.WriteString(strings.TrimSpace(marshalYAMLForPrompt(spec.Graders)))
		b.WriteString("\n```\n")
	}

	for _, to := range failingTests {
		fmt.Fprintf(&b, "\n### Test: %s (`%s`)\n", to.DisplayName, to.TestID)

		if def, ok := testDefinitions[to.TestID]; ok && strings.TrimSpace(def) != "" {
			b.WriteString("- Test definition:\n\n")
			b.WriteString("```yaml\n")
			b.WriteString(strings.TrimSpace(def))
			b.WriteString("\n```\n")
		} else {
			b.WriteString("- Test definition: unavailable\n")
		}

		for _, run := range to.Runs {
			fmt.Fprintf(&b, "\n#### Run %d (%s)\n", run.RunNumber, run.Status)

			if run.ErrorMsg != "" {
				fmt.Fprintf(&b, "- Error: %q\n", truncateForPrompt(run.ErrorMsg, maxSuggestionErrorLen))
			}
			if run.FinalOutput != "" {
				fmt.Fprintf(&b, "- Final output: %q\n", truncateForPrompt(run.FinalOutput, maxSuggestionOutputLen))
			}

			failedGraders := failedGraderFeedback(run.Validations)
			if len(failedGraders) > 0 {
				b.WriteString("- Failed grader outcomes:\n")
				for _, line := range failedGraders {
					fmt.Fprintf(&b, "  - %s\n", line)
				}
			}

			trace := extractCopilotTrace(run.Transcript)
			if len(trace) > 0 {
				b.WriteString("- Copilot responses and tool activity:\n")
				for _, line := range trace {
					fmt.Fprintf(&b, "  - %s\n", line)
				}
			} else {
				b.WriteString("- Copilot responses and tool activity: none captured\n")
			}
		}
	}

	return strings.TrimSpace(b.String())
}

func buildFailedTriggerEvidence(failedTriggers []models.TriggerResult) string {
	if len(failedTriggers) == 0 {
		return ""
	}

	var b strings.Builder
	for _, tr := range failedTriggers {
		fmt.Fprintf(&b, "### Prompt: %q\n", tr.Prompt)
		actual := ""
		expected := " not"
		if tr.ShouldTrigger {
			actual = " not"
			expected = ""
		}
		fmt.Fprintf(&b, "Given this prompt the agent should%s have used the skill, however it did%s.\n", expected, actual)
		if tr.ErrorMsg != "" {
			fmt.Fprintf(&b, "The agent returned this error: %q\n", truncateForPrompt(tr.ErrorMsg, maxSuggestionErrorLen))
		}

		trace := extractCopilotTrace(tr.Transcript)
		if len(trace) > 0 {
			b.WriteString("- Agent messages and tool usage:\n")
			for _, line := range trace {
				fmt.Fprintf(&b, "  - %s\n", line)
			}
		}

		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}

func selectFailingTests(testOutcomes []models.TestOutcome) []models.TestOutcome {
	failing := make([]models.TestOutcome, 0, len(testOutcomes))
	for _, to := range testOutcomes {
		if to.Status != models.StatusPassed {
			failing = append(failing, to)
		}
	}
	return failing
}

func selectFailedTriggerResults(results []models.TriggerResult) []models.TriggerResult {
	failed := make([]models.TriggerResult, 0, len(results))
	for _, r := range results {
		if r.ShouldTrigger != r.DidTrigger || strings.TrimSpace(r.ErrorMsg) != "" {
			failed = append(failed, r)
		}
	}
	return failed
}

func loadTestDefinitionYAML(spec *models.BenchmarkSpec, specPath string) (map[string]string, error) {
	testCases, err := loadTestCases(spec, specPath)
	if err != nil {
		return nil, err
	}

	defs := make(map[string]string, len(testCases))
	for _, tc := range testCases {
		if tc == nil || tc.TestID == "" {
			continue
		}
		if _, exists := defs[tc.TestID]; exists {
			continue
		}
		yml, err := yaml.Marshal(tc)
		if err != nil {
			return nil, fmt.Errorf("marshaling test case %s: %w", tc.TestID, err)
		}
		defs[tc.TestID] = strings.TrimSpace(string(yml))
	}
	return defs, nil
}

func loadTestCases(spec *models.BenchmarkSpec, specPath string) ([]*models.TestCase, error) {
	if spec.TasksFrom != "" {
		return loadTestCasesFromCSV(spec, specPath)
	}
	return loadTestCasesFromFiles(spec, specPath)
}

func loadTestCasesFromFiles(spec *models.BenchmarkSpec, specPath string) ([]*models.TestCase, error) {
	specDir := filepath.Dir(specPath)
	testFiles := make([]string, 0)
	for _, pattern := range spec.Tasks {
		fullPattern := filepath.Join(specDir, pattern)
		matches, err := filepath.Glob(fullPattern)
		if err != nil {
			return nil, err
		}
		testFiles = append(testFiles, matches...)
	}
	sort.Strings(testFiles)
	if len(testFiles) == 0 {
		return nil, fmt.Errorf("no test files matched patterns: %v in directory: %s", spec.Tasks, specDir)
	}

	testCases := make([]*models.TestCase, 0, len(testFiles))
	for _, path := range testFiles {
		tc, err := models.LoadTestCase(path)
		if err != nil {
			return nil, fmt.Errorf("failed to load test case %s: %w", path, err)
		}
		if tc.Active == nil || *tc.Active {
			testCases = append(testCases, tc)
		}
	}
	return testCases, nil
}

func loadTestCasesFromCSV(spec *models.BenchmarkSpec, specPath string) ([]*models.TestCase, error) {
	specDir := filepath.Dir(specPath)
	csvPath := spec.TasksFrom
	if !filepath.IsAbs(csvPath) {
		csvPath = filepath.Join(specDir, csvPath)
	}

	absSpecDir, err := filepath.Abs(specDir)
	if err != nil {
		return nil, fmt.Errorf("resolving spec directory: %w", err)
	}
	absCSVPath, err := filepath.Abs(csvPath)
	if err != nil {
		return nil, fmt.Errorf("resolving CSV path: %w", err)
	}
	if !strings.HasPrefix(absCSVPath, absSpecDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("tasks_from path %q escapes spec directory", spec.TasksFrom)
	}

	var rows []dataset.Row
	if spec.Range != [2]int{} {
		if spec.Range[0] <= 0 || spec.Range[1] <= 0 {
			return nil, fmt.Errorf("invalid range: both values must be > 0, got [%d, %d]", spec.Range[0], spec.Range[1])
		}
		if spec.Range[0] > spec.Range[1] {
			return nil, fmt.Errorf("invalid range: start (%d) must be <= end (%d)", spec.Range[0], spec.Range[1])
		}
		rows, err = dataset.LoadCSVRange(csvPath, spec.Range[0], spec.Range[1])
	} else {
		rows, err = dataset.LoadCSV(csvPath)
	}
	if err != nil {
		return nil, fmt.Errorf("loading CSV dataset: %w", err)
	}

	now := time.Now()
	baseCtx := &template.Context{
		JobID:     fmt.Sprintf("run-%d", now.Unix()),
		Timestamp: now.Format(time.RFC3339),
		Vars:      make(map[string]string),
	}
	for k, v := range spec.Inputs {
		baseCtx.Vars[k] = v
	}

	testCases := make([]*models.TestCase, 0, len(rows))
	for i, row := range rows {
		rowNum := i + 1
		testID := fmt.Sprintf("row-%d", rowNum)
		if v, ok := row["id"]; ok && v != "" {
			testID = v
		} else if v, ok := row["name"]; ok && v != "" {
			testID = v
		}

		displayName := fmt.Sprintf("row-%d", rowNum)
		if v, ok := row["name"]; ok && v != "" {
			displayName = v
		}

		rowCtx := &template.Context{
			JobID:     baseCtx.JobID,
			TaskName:  displayName,
			Iteration: 0,
			Attempt:   0,
			Timestamp: baseCtx.Timestamp,
			Vars:      make(map[string]string),
		}
		for k, v := range spec.Inputs {
			rowCtx.Vars[k] = v
		}
		for k, v := range row {
			rowCtx.Vars[k] = v
		}

		prompt := row["prompt"]
		if strings.Contains(prompt, "{{") {
			prompt, err = template.Render(prompt, rowCtx)
			if err != nil {
				return nil, fmt.Errorf("resolving prompt template for row %d: %w", rowNum, err)
			}
		}

		testCases = append(testCases, &models.TestCase{
			TestID:      testID,
			DisplayName: displayName,
			Stimulus: models.TestStimulus{
				Message: prompt,
			},
		})
	}
	return testCases, nil
}

func extractCopilotTrace(transcript []models.TranscriptEvent) []string {
	if len(transcript) == 0 {
		return nil
	}

	lines := make([]string, 0, len(transcript))
	for _, evt := range transcript {
		switch evt.Type {
		case copilot.AssistantMessage:
			if evt.Data.Content == nil {
				continue
			}
			msg := compactWhitespace(*evt.Data.Content)
			if msg == "" {
				continue
			}
			lines = append(lines, "agent: "+truncateForPrompt(msg, maxSuggestionTraceEntryLen))
		case copilot.SkillInvoked:
			if evt.Data.Message != nil {
				msg := compactWhitespace(*evt.Data.Message)
				if msg != "" {
					lines = append(lines, "skill invoked: "+truncateForPrompt(msg, maxSuggestionTraceEntryLen))
				}
			}
		case copilot.ToolExecutionStart:
			name := derefOr(evt.Data.ToolName, "<unknown>")
			args := marshalForPrompt(evt.Data.Arguments, maxSuggestionToolSummaryLen)
			if args == "" {
				lines = append(lines, fmt.Sprintf("tool start: %s", name))
				continue
			}
			lines = append(lines, fmt.Sprintf("tool start: %s args=%s", name, args))
		case copilot.ToolExecutionComplete, copilot.ToolExecutionPartialResult:
			parts := []string{"tool result:"}
			if evt.Data.ToolName != nil {
				parts = append(parts, "tool="+*evt.Data.ToolName)
			}
			if evt.Data.Success != nil {
				parts = append(parts, fmt.Sprintf("success=%t", *evt.Data.Success))
			}
			if evt.Data.Message != nil {
				msg := compactWhitespace(*evt.Data.Message)
				if msg != "" {
					parts = append(parts, "message="+truncateForPrompt(msg, maxSuggestionToolSummaryLen))
				}
			}
			if result := marshalForPrompt(evt.Data.Result, maxSuggestionToolSummaryLen); result != "" {
				parts = append(parts, "result="+result)
			}
			lines = append(lines, strings.Join(parts, " "))
		case copilot.ToolUserRequested:
			if evt.Data.Message == nil {
				continue
			}
			msg := compactWhitespace(*evt.Data.Message)
			if msg == "" {
				continue
			}
			lines = append(lines, "tool user request: "+truncateForPrompt(msg, maxSuggestionTraceEntryLen))
		}
	}

	return lines
}

func failedGraderFeedback(validations map[string]models.GraderResults) []string {
	if len(validations) == 0 {
		return nil
	}

	names := make([]string, 0, len(validations))
	for name := range validations {
		names = append(names, name)
	}
	sort.Strings(names)

	lines := make([]string, 0, len(names))
	for _, name := range names {
		v := validations[name]
		if v.Passed {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s (score=%.2f): %s", v.Name, v.Score, truncateForPrompt(v.Feedback, maxSuggestionGraderDetailLen)))
	}
	return lines
}

func marshalForPrompt(v any, maxLen int) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return truncateForPrompt(fmt.Sprintf("%v", v), maxLen)
	}
	return truncateForPrompt(compactWhitespace(string(b)), maxLen)
}

func marshalYAMLForPrompt(v any) string {
	b, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Sprintf("error marshaling yaml: %v", err)
	}
	return string(b)
}

func compactWhitespace(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func truncateForPrompt(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func derefOr(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return *value
}

func displaySuggestionReport(w io.Writer, modelID, report string) {
	//nolint:errcheck
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "\n"+"═"+strings.Repeat("═", 54))
	_, _ = fmt.Fprintf(w, " SUGGESTIONS (%s)\n", modelID)
	_, _ = fmt.Fprintln(w, "═"+strings.Repeat("═", 54))
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, report)
	_, _ = fmt.Fprintln(w)
}

// writeSuggestionTranscript writes the suggestion session transcript to the
// transcript directory (if configured via --transcript-dir).
func writeSuggestionTranscript(prompt string, res *execution.ExecutionResponse) {
	if transcriptDir == "" || res == nil {
		return
	}

	now := time.Now()
	events := transcript.BuildFromSessionEvents(res.Events)

	status := models.StatusPassed
	if res.FinalOutput == "" || !res.Success {
		status = models.StatusFailed
	}

	t := &models.TaskTranscript{
		TaskID:      "waza-suggest",
		TaskName:    "suggestion-request",
		Status:      status,
		StartedAt:   now.Add(-time.Duration(res.DurationMs) * time.Millisecond),
		CompletedAt: now,
		DurationMs:  res.DurationMs,
		Prompt:      prompt,
		FinalOutput: res.FinalOutput,
		Transcript:  events,
		ErrorMsg:    res.ErrorMsg,
	}

	if _, err := transcript.Write(transcriptDir, t); err != nil {
		fmt.Fprintf(os.Stderr, "[WARN] Failed to write suggestion transcript: %v\n", err)
	}
}
