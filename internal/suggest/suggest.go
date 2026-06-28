package suggest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/validation"
	"gopkg.in/yaml.v3"
)

const defaultTimeoutSec = 120

// FocusCategory steers the kinds of test cases the LLM should generate.
type FocusCategory string

const (
	FocusTriggers         FocusCategory = "triggers"
	FocusNegativeTriggers FocusCategory = "negative-triggers"
	FocusEdgeFixtures     FocusCategory = "edge-fixtures"
	FocusDoNotUseFor      FocusCategory = "do-not-use-for"
	FocusParameters       FocusCategory = "parameters"
)

// AvailableFocusCategories returns all supported --focus values.
func AvailableFocusCategories() []string {
	return []string{
		string(FocusTriggers),
		string(FocusNegativeTriggers),
		string(FocusEdgeFixtures),
		string(FocusDoNotUseFor),
		string(FocusParameters),
	}
}

// ValidateFocus returns nil if focus is empty or a known category.
func ValidateFocus(focus string) error {
	focus = strings.TrimSpace(focus)
	if focus == "" {
		return nil
	}
	for _, c := range AvailableFocusCategories() {
		if focus == c {
			return nil
		}
	}
	return fmt.Errorf("invalid --focus %q: must be one of %s", focus, strings.Join(AvailableFocusCategories(), ", "))
}

// Options configures suggestion generation.
type Options struct {
	SkillPath  string
	TimeoutSec int
	GraderDocs fs.FS // embedded grader documentation (optional)
	// Count is how many test cases to propose. <= 0 means "use model default".
	Count int
	// Focus narrows generation toward a category. Empty means "balanced".
	Focus string
}

// WriteOptions controls how a Suggestion is applied to disk.
type WriteOptions struct {
	// Force overwrites existing files and duplicate task ids when true.
	Force bool
	// EvalFile is the eval filename to write/preserve in outputDir.
	EvalFile string
	// TaskGlob is the configured task glob, relative to outputDir.
	TaskGlob string
	// TaskFileSuffix is the configured suffix for generated task files.
	TaskFileSuffix string
}

// GeneratedFile is a single generated artifact. For tasks, Confidence and
// Rationale carry per-case metadata that is shown in dry-run output but is
// *not* written into the task YAML file (which must satisfy the strict task
// schema).
type GeneratedFile struct {
	Path       string  `yaml:"path" json:"path"`
	Content    string  `yaml:"content" json:"content"`
	Confidence float64 `yaml:"confidence" json:"confidence"`
	Rationale  string  `yaml:"rationale" json:"rationale"`
}

// Suggestion is the structured output returned by the LLM.
type Suggestion struct {
	EvalYAML string          `yaml:"eval_yaml" json:"eval_yaml"`
	Tasks    []GeneratedFile `yaml:"tasks,omitempty" json:"tasks,omitempty"`
	Fixtures []GeneratedFile `yaml:"fixtures,omitempty" json:"fixtures,omitempty"`
}

// Generate runs the suggestion flow end-to-end.
// When opts.GraderDocs is set, uses a two-pass approach:
//
//	Pass 1: ask the LLM which grader types to use (lightweight)
//	Pass 2: provide detailed docs for those graders and generate eval YAML
//
// When opts.GraderDocs is nil, falls back to a single-pass prompt.
func Generate(ctx context.Context, engine execution.AgentEngine, opts Options) (*Suggestion, error) {
	if err := ValidateFocus(opts.Focus); err != nil {
		return nil, err
	}
	opts.Focus = strings.TrimSpace(opts.Focus)
	skillFile, err := resolveSkillFile(opts.SkillPath)
	if err != nil {
		return nil, err
	}

	skillContent, sk, err := loadSkill(skillFile)
	if err != nil {
		return nil, err
	}

	timeoutSec := opts.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = defaultTimeoutSec
	}

	data := buildPromptData(sk, skillContent)
	data.Count = opts.Count
	data.Focus = opts.Focus

	// Determine grader docs for the implementation prompt.
	var graderDocs string
	if opts.GraderDocs != nil {
		// Pass 1: select grader types
		selectionPrompt := renderSelectionPrompt(data)
		selectionCtx, cancelSelection := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
		resp, err := engine.Execute(selectionCtx, &execution.ExecutionRequest{
			Message: selectionPrompt,
		})
		cancelSelection()
		if err != nil {
			return nil, fmt.Errorf("grader selection: %w", err)
		}
		if resp == nil {
			return nil, errors.New("empty engine response during grader selection")
		}
		if err := engineResponseError(resp); err != nil {
			return nil, fmt.Errorf("grader selection: %w", err)
		}

		selected := parseGraderSelection(resp.FinalOutput)
		if len(selected) > 0 {
			graderDocs = LoadGraderDocs(opts.GraderDocs, selected)
		}
	}

	// Pass 2 (or single pass): generate eval YAML
	implPrompt := renderImplementationPrompt(data, graderDocs)
	implCtx, cancelImpl := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	resp, err := engine.Execute(implCtx, &execution.ExecutionRequest{
		Message: implPrompt,
	})
	cancelImpl()
	if err != nil {
		return nil, fmt.Errorf("getting suggestions: %w", err)
	}
	if resp == nil {
		return nil, errors.New("empty engine response")
	}
	if err := engineResponseError(resp); err != nil {
		return nil, fmt.Errorf("getting suggestions: %w", err)
	}

	suggestion, err := ParseResponse(resp.FinalOutput)
	if err != nil {
		return nil, fmt.Errorf("parsing suggest response: %w", err)
	}
	return suggestion, nil
}

func engineResponseError(resp *execution.ExecutionResponse) error {
	if resp.Success {
		return nil
	}
	if msg := strings.TrimSpace(resp.ErrorMsg); msg != "" {
		return fmt.Errorf("engine execution failed: %s", msg)
	}
	return errors.New("engine execution failed")
}

// buildPromptData assembles the prompt data from a parsed skill.
func buildPromptData(sk *skill.Skill, skillContent string) promptData {
	useFor, doNotUseFor := scaffold.ParseTriggerPhrases(sk.Frontmatter.Description)
	return promptData{
		SkillName:      orDefault(sk.Frontmatter.Name, filepath.Base(filepath.Dir(sk.Path))),
		Description:    strings.TrimSpace(sk.Frontmatter.Description),
		Triggers:       phrasesToText(useFor),
		AntiTriggers:   phrasesToText(doNotUseFor),
		ContentSummary: summarizeBody(sk.Body),
		GraderTypes:    "- " + strings.Join(AvailableGraderTypes(), "\n- "),
		SkillContent:   skillContent,
	}
}

// BuildPrompt builds a single-pass LLM prompt (no grader docs).
// Retained for backward compatibility and tests.
func BuildPrompt(sk *skill.Skill, skillContent string) string {
	data := buildPromptData(sk, skillContent)
	return renderPrompt(data)
}

// parseGraderSelection extracts grader type names from the pass-1 response.
// Accepts either a YAML structure with a "graders" key or bare lines like "- code".
func parseGraderSelection(raw string) []string {
	normalized := strings.TrimSpace(extractYAML(raw))
	if normalized == "" {
		return nil
	}

	// Try structured YAML: { graders: [code, keyword, ...] }
	var structured struct {
		Graders []string `yaml:"graders"`
	}
	if err := yaml.Unmarshal([]byte(normalized), &structured); err == nil && len(structured.Graders) > 0 {
		return filterValidGraderTypes(structured.Graders)
	}

	// Try bare YAML list: [code, keyword, ...]
	var bare []string
	if err := yaml.Unmarshal([]byte(normalized), &bare); err == nil && len(bare) > 0 {
		return filterValidGraderTypes(bare)
	}

	// Try line-by-line: "- code\n- keyword\n..."
	var result []string
	for _, line := range strings.Split(normalized, "\n") {
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if t != "" {
			result = append(result, t)
		}
	}
	return filterValidGraderTypes(result)
}

// filterValidGraderTypes keeps only recognized grader type names.
func filterValidGraderTypes(types []string) []string {
	valid := make(map[string]bool)
	for _, t := range AvailableGraderTypes() {
		valid[t] = true
	}
	var result []string
	for _, t := range types {
		t = strings.TrimSpace(t)
		if valid[t] {
			result = append(result, t)
		}
	}
	return result
}

// AvailableGraderTypes returns supported grader kinds.
func AvailableGraderTypes() []string {
	return []string{
		string(models.GraderKindInlineScript),
		string(models.GraderKindPrompt),
		string(models.GraderKindText),
		string(models.GraderKindFile),
		string(models.GraderKindJSONSchema),
		string(models.GraderKindProgram),
		string(models.GraderKindBehavior),
		string(models.GraderKindActionSequence),
		string(models.GraderKindSkillInvocation),
		string(models.GraderKindTrigger),
		string(models.GraderKindDiff),
		string(models.GraderKindToolConstraint),
	}
}

// ParseResponse parses model YAML output into a Suggestion.
// Empty output is reported distinctly from malformed suggestion YAML.
func ParseResponse(raw string) (*Suggestion, error) {
	normalized := strings.TrimSpace(extractYAML(raw))
	if normalized == "" {
		return nil, errors.New("empty suggest response")
	}

	var s Suggestion
	decoder := yaml.NewDecoder(strings.NewReader(normalized))
	decoder.KnownFields(true)
	if err := decoder.Decode(&s); err == nil && strings.TrimSpace(s.EvalYAML) != "" {
		if err := validateEvalYAML(s.EvalYAML); err != nil {
			return nil, err
		}
		return &s, nil
	}

	if err := validateEvalYAML(normalized); err == nil {
		return &Suggestion{EvalYAML: normalized}, nil
	}

	return nil, errors.New("response is not valid suggestion YAML")
}

// WriteToDir writes suggested files to outputDir and returns written paths.
// Existing files are preserved unless opts.Force is true. Generated task
// YAML files are validated against the task schema before being written.
func (s *Suggestion) WriteToDir(outputDir string, opts WriteOptions) ([]string, error) {
	if err := validateEvalYAML(s.EvalYAML); err != nil {
		return nil, err
	}
	opts = opts.withDefaults()

	// Pre-flight: validate each task against the schema *and* check for
	// id collisions with existing tasks in outputDir.
	existingIDs, err := collectExistingTaskIDs(outputDir, opts.TaskGlob)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateExistingTaskIDs(outputDir, existingIDs); err != nil {
		return nil, err
	}

	type plannedTask struct {
		target string
		id     string
		body   []byte
	}
	planned := make([]plannedTask, 0, len(s.Tasks))
	seenIDs := make(map[string]string, len(s.Tasks))

	for i, task := range s.Tasks {
		path, err := normalizeGeneratedPath(task.Path, fallbackTaskPath(opts.TaskGlob, opts.TaskFileSuffix, i))
		if err != nil {
			return nil, err
		}
		if err := validateTaskMetadata(path, task); err != nil {
			return nil, err
		}
		body := []byte(strings.TrimSpace(task.Content) + "\n")
		if errs := validation.ValidateTaskBytes(body); len(errs) > 0 {
			return nil, fmt.Errorf("generated task %s failed schema validation: %s", path, strings.Join(errs, "; "))
		}
		id := extractTaskID(body)
		if id == "" {
			return nil, fmt.Errorf("generated task %s is missing required 'id' field", path)
		}
		if dup, ok := seenIDs[id]; ok {
			return nil, fmt.Errorf("generated tasks contain duplicate id %q (%s and %s)", id, dup, path)
		}
		seenIDs[id] = path

		target := filepath.Join(outputDir, path)
		if !opts.Force {
			if _, err := os.Stat(target); err == nil {
				diff, diffErr := buildOverwriteDiff(outputDir, target, body)
				if diffErr != nil {
					return nil, diffErr
				}
				return nil, fmt.Errorf("refusing to overwrite existing task file %s (use --force to override)\n%s", target, diff)
			}
			if existingPaths := existingIDs[id]; len(existingPaths) > 0 {
				existingPath := existingPaths[0]
				rel, _ := filepath.Rel(outputDir, existingPath)
				if rel == "" {
					rel = existingPath
				}
				rel = filepath.ToSlash(rel)
				diff, diffErr := buildOverwriteDiff(outputDir, existingPath, body)
				if diffErr != nil {
					return nil, diffErr
				}
				return nil, fmt.Errorf("refusing to overwrite task with existing id %q (already defined in %s; use --force to override)\n%s", id, rel, diff)
			}
		}
		planned = append(planned, plannedTask{target: target, id: id, body: body})
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating output directory: %w", err)
	}

	var written []string
	evalPath := filepath.Join(outputDir, opts.EvalFile)
	if _, err := os.Stat(evalPath); err == nil && !opts.Force {
		// Merge-safe: don't overwrite a curated eval file.
		// New task files will be picked up by its existing tasks: glob pattern.
	} else {
		if err := os.WriteFile(evalPath, []byte(strings.TrimSpace(s.EvalYAML)+"\n"), 0o644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", opts.EvalFile, err)
		}
		written = append(written, evalPath)
	}

	for _, pt := range planned {
		if err := os.MkdirAll(filepath.Dir(pt.target), 0o755); err != nil {
			return nil, fmt.Errorf("creating directory for %s: %w", pt.target, err)
		}
		if err := os.WriteFile(pt.target, pt.body, 0o644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", pt.target, err)
		}
		written = append(written, pt.target)
	}

	for i, fixture := range s.Fixtures {
		path, err := normalizeGeneratedPath(fixture.Path, fmt.Sprintf("fixtures/fixture-%02d.txt", i+1))
		if err != nil {
			return nil, err
		}
		target := filepath.Join(outputDir, path)
		if _, err := os.Stat(target); err == nil && !opts.Force {
			return nil, fmt.Errorf("refusing to overwrite existing fixture file %s (use --force to override)", target)
		}
		if err := writeGeneratedFile(target, fixture.Content); err != nil {
			return nil, err
		}
		written = append(written, target)
	}

	return written, nil
}

func validateTaskMetadata(path string, task GeneratedFile) error {
	if task.Confidence < 0 || task.Confidence > 1 {
		return fmt.Errorf("generated task %s has invalid confidence %.3f: must be between 0 and 1", path, task.Confidence)
	}
	if strings.TrimSpace(task.Rationale) == "" {
		return fmt.Errorf("generated task %s is missing required rationale metadata", path)
	}
	return nil
}

func (opts WriteOptions) withDefaults() WriteOptions {
	if strings.TrimSpace(opts.EvalFile) == "" {
		opts.EvalFile = projectconfig.DefaultEvalFile
	}
	if strings.TrimSpace(opts.TaskGlob) == "" {
		opts.TaskGlob = projectconfig.DefaultTaskGlob
	}
	if strings.TrimSpace(opts.TaskFileSuffix) == "" {
		opts.TaskFileSuffix = projectconfig.DefaultTaskFileSuffix
	}
	return opts
}

func fallbackTaskPath(taskGlob, taskFileSuffix string, index int) string {
	dir := filepath.Dir(taskGlob)
	if dir == "." || dir == "" {
		dir = "tasks"
	}
	suffix := strings.TrimSpace(taskFileSuffix)
	if suffix == "" {
		suffix = projectconfig.DefaultTaskFileSuffix
	}
	return filepath.Join(dir, fmt.Sprintf("task-%02d%s", index+1, suffix))
}

// collectExistingTaskIDs scans the configured task glob and returns task id ->
// file paths for collision detection. Missing directories are treated as empty.
func collectExistingTaskIDs(outputDir string, taskGlob string) (map[string][]string, error) {
	ids := make(map[string][]string)
	matches, err := filepath.Glob(filepath.Join(outputDir, taskGlob))
	if err != nil {
		return nil, fmt.Errorf("scanning tasks with glob %q: %w", taskGlob, err)
	}
	sort.Strings(matches)
	for _, full := range matches {
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("reading existing task %s for collision check: %w", full, err)
		}
		id := extractTaskID(data)
		if id != "" {
			ids[id] = append(ids[id], full)
		}
	}
	return ids, nil
}

func rejectDuplicateExistingTaskIDs(outputDir string, ids map[string][]string) error {
	var duplicateIDs []string
	for id, paths := range ids {
		if len(paths) > 1 {
			sort.Strings(paths)
			duplicateIDs = append(duplicateIDs, id)
		}
	}
	if len(duplicateIDs) == 0 {
		return nil
	}
	sort.Strings(duplicateIDs)
	var parts []string
	for _, id := range duplicateIDs {
		parts = append(parts, fmt.Sprintf("%q in %s", id, strings.Join(relPaths(outputDir, ids[id]), ", ")))
	}
	return fmt.Errorf("existing tasks contain duplicate id(s): %s", strings.Join(parts, "; "))
}

func relPaths(baseDir string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		rel, err := filepath.Rel(baseDir, path)
		if err != nil || rel == "" {
			rel = path
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

// extractTaskID pulls the top-level `id:` field from task YAML.
func extractTaskID(data []byte) string {
	var tc struct {
		ID string `yaml:"id"`
	}
	if err := yaml.Unmarshal(data, &tc); err != nil {
		return ""
	}
	return strings.TrimSpace(tc.ID)
}

func loadSkill(skillFile string) (string, *skill.Skill, error) {
	data, err := os.ReadFile(skillFile)
	if err != nil {
		return "", nil, fmt.Errorf("reading SKILL.md: %w", err)
	}
	var sk skill.Skill
	if err := sk.UnmarshalText(data); err != nil {
		return "", nil, fmt.Errorf("parsing SKILL.md: %w", err)
	}
	sk.Path = skillFile
	return string(data), &sk, nil
}

func resolveSkillFile(input string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", errors.New("skill path is required")
	}
	resolved := input
	if !filepath.IsAbs(resolved) {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getting working directory: %w", err)
		}
		resolved = filepath.Join(wd, resolved)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("skill path does not exist: %s", input)
		}
		return "", fmt.Errorf("checking skill path: %w", err)
	}

	if info.IsDir() {
		resolved = filepath.Join(resolved, "SKILL.md")
	}

	if filepath.Base(resolved) != "SKILL.md" {
		return "", fmt.Errorf("expected SKILL.md or skill directory, got %s", input)
	}
	if _, err := os.Stat(resolved); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no SKILL.md found in %s", input)
		}
		return "", fmt.Errorf("checking SKILL.md: %w", err)
	}
	return resolved, nil
}

func extractYAML(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	start := strings.Index(trimmed, "```")
	if start < 0 {
		return trimmed
	}

	rest := trimmed[start+3:]
	if nl := strings.Index(rest, "\n"); nl >= 0 {
		rest = rest[nl+1:]
	}
	if end := strings.Index(rest, "```"); end >= 0 {
		return strings.TrimSpace(rest[:end])
	}

	return trimmed
}

func validateEvalYAML(raw string) error {
	var spec models.EvalSpec
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	decoder.KnownFields(true) // Strict parsing to catch unknown fields
	if err := decoder.Decode(&spec); err != nil {
		return fmt.Errorf("invalid eval_yaml: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("invalid eval_yaml: %w", err)
	}
	for i, g := range spec.Graders {
		if g.Identifier == "" {
			return fmt.Errorf("invalid eval_yaml: grader[%d] is missing required 'name' field", i)
		}
		if g.Kind == "" {
			return fmt.Errorf("invalid eval_yaml: grader[%d] (%s) is missing required 'type' field", i, g.Identifier)
		}
	}
	evalBody := []byte(strings.TrimSpace(raw) + "\n")
	if errs := validation.ValidateEvalBytes(evalBody); len(errs) > 0 {
		return fmt.Errorf("invalid eval_yaml schema: %s", strings.Join(errs, "; "))
	}
	return nil
}

func buildOverwriteDiff(outputDir string, existingPath string, proposed []byte) (string, error) {
	existing, err := os.ReadFile(existingPath)
	if err != nil {
		return "", fmt.Errorf("reading existing task for overwrite diff: %w", err)
	}
	rel, err := filepath.Rel(outputDir, existingPath)
	if err != nil || rel == "" {
		rel = existingPath
	}
	rel = filepath.ToSlash(rel)
	return simpleUnifiedDiff(rel, existing, proposed), nil
}

func simpleUnifiedDiff(path string, before []byte, after []byte) string {
	beforeLines := splitLinesForDiff(strings.TrimRight(string(before), "\n"))
	afterLines := splitLinesForDiff(strings.TrimRight(string(after), "\n"))
	var b strings.Builder
	fmt.Fprintf(&b, "diff:\n--- %s (existing)\n+++ %s (suggested)\n", path, path)
	max := len(beforeLines)
	if len(afterLines) > max {
		max = len(afterLines)
	}
	for i := 0; i < max; i++ {
		var oldLine, newLine string
		if i < len(beforeLines) {
			oldLine = beforeLines[i]
		}
		if i < len(afterLines) {
			newLine = afterLines[i]
		}
		switch {
		case i >= len(beforeLines):
			fmt.Fprintf(&b, "+%s\n", newLine)
		case i >= len(afterLines):
			fmt.Fprintf(&b, "-%s\n", oldLine)
		case oldLine == newLine:
			fmt.Fprintf(&b, " %s\n", oldLine)
		default:
			fmt.Fprintf(&b, "-%s\n+%s\n", oldLine, newLine)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func splitLinesForDiff(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func phrasesToText(phrases []scaffold.TriggerPhrase) string {
	if len(phrases) == 0 {
		return "none"
	}
	items := make([]string, 0, len(phrases))
	for _, p := range phrases {
		if strings.TrimSpace(p.Prompt) != "" {
			items = append(items, p.Prompt)
		}
	}
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

func summarizeBody(body string) string {
	lines := strings.Split(body, "\n")
	var highlights []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			highlights = append(highlights, trimmed)
			continue
		}
		if len(highlights) < 8 {
			highlights = append(highlights, trimmed)
		}
		if len(highlights) >= 8 {
			break
		}
	}
	if len(highlights) == 0 {
		return "No body content"
	}
	return strings.Join(highlights, " | ")
}

func normalizeGeneratedPath(path, fallback string) (string, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		clean = fallback
	}
	clean = filepath.Clean(clean)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("invalid generated path: %s", path)
	}
	return clean, nil
}

func writeGeneratedFile(path string, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
