package suggest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/validation"
	"gopkg.in/yaml.v3"
)

const defaultTimeoutSec = 120
const DefaultCaseCount = 3

// FocusCategory identifies the behavior area that generated test cases should target.
type FocusCategory string

const (
	FocusTriggers         FocusCategory = "triggers"
	FocusNegativeTriggers FocusCategory = "negative-triggers"
	FocusEdgeFixtures     FocusCategory = "edge-fixtures"
	FocusDoNotUseFor      FocusCategory = "do-not-use-for"
	FocusParameters       FocusCategory = "parameters"
)

// AllFocusCategories returns the supported focus category values.
func AllFocusCategories() []FocusCategory {
	return []FocusCategory{
		FocusTriggers,
		FocusNegativeTriggers,
		FocusEdgeFixtures,
		FocusDoNotUseFor,
		FocusParameters,
	}
}

// ParseFocusCategory validates a focus category. Empty focus means balanced generation.
func ParseFocusCategory(value string) (FocusCategory, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	for _, category := range AllFocusCategories() {
		if value == string(category) {
			return category, nil
		}
	}
	return "", fmt.Errorf("invalid focus %q: must be one of %s", value, focusCategoryList())
}

// Options configures suggestion generation.
type Options struct {
	SkillPath  string
	TimeoutSec int
	GraderDocs fs.FS // embedded grader documentation (optional)
	Count      int
	Focus      FocusCategory
}

// GeneratedFile is a single generated artifact.
type GeneratedFile struct {
	Path       string   `yaml:"path" json:"path"`
	Content    string   `yaml:"content" json:"content"`
	Confidence *float64 `yaml:"confidence,omitempty" json:"confidence,omitempty"`
	Rationale  string   `yaml:"rationale,omitempty" json:"rationale,omitempty"`
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
	skillFile, err := resolveSkillFile(opts.SkillPath)
	if err != nil {
		return nil, err
	}
	if opts.Count < 0 {
		return nil, fmt.Errorf("count must be at least 1, got %d", opts.Count)
	}
	if opts.Count == 0 {
		opts.Count = DefaultCaseCount
	}
	if _, err := ParseFocusCategory(string(opts.Focus)); err != nil {
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

	data := buildPromptData(sk, skillContent, opts.Count, opts.Focus)

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
	if opts.Count > 0 && len(suggestion.Tasks) > opts.Count {
		suggestion.Tasks = suggestion.Tasks[:opts.Count]
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
func buildPromptData(sk *skill.Skill, skillContent string, count int, focus FocusCategory) promptData {
	useFor, doNotUseFor := scaffold.ParseTriggerPhrases(sk.Frontmatter.Description)
	if count <= 0 {
		count = DefaultCaseCount
	}
	return promptData{
		SkillName:      orDefault(sk.Frontmatter.Name, filepath.Base(filepath.Dir(sk.Path))),
		Description:    strings.TrimSpace(sk.Frontmatter.Description),
		Triggers:       phrasesToText(useFor),
		AntiTriggers:   phrasesToText(doNotUseFor),
		ContentSummary: summarizeBody(sk.Body),
		GraderTypes:    "- " + strings.Join(AvailableGraderTypes(), "\n- "),
		SkillContent:   skillContent,
		Count:          count,
		Focus:          focusInstruction(focus),
	}
}

// BuildPrompt builds a single-pass LLM prompt (no grader docs).
// Retained for backward compatibility and tests.
func BuildPrompt(sk *skill.Skill, skillContent string) string {
	data := buildPromptData(sk, skillContent, DefaultCaseCount, "")
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
		if err := s.Validate(); err != nil {
			return nil, err
		}
		return &s, nil
	}

	if err := validateEvalYAML(normalized); err == nil {
		return &Suggestion{EvalYAML: normalized}, nil
	}

	return nil, errors.New("response is not valid suggestion YAML")
}

// WriteOptions configures how suggestions are applied to disk.
type WriteOptions struct {
	Force bool
}

// Validate checks generated eval and task YAML before any files are written.
func (s *Suggestion) Validate() error {
	if s == nil {
		return errors.New("suggestion is nil")
	}
	if err := validateEvalYAML(s.EvalYAML); err != nil {
		return err
	}
	seenPaths := map[string]bool{"eval.yaml": true}
	seenTaskIDs := make(map[string]string)
	for i, task := range s.Tasks {
		path, err := normalizeTaskPath(task.Path, fmt.Sprintf("tasks/task-%02d.yaml", i+1))
		if err != nil {
			return err
		}
		if seenPaths[path] {
			return fmt.Errorf("duplicate generated path: %s", path)
		}
		seenPaths[path] = true
		if err := validateGeneratedTask(task); err != nil {
			return fmt.Errorf("invalid generated task %d (%s): %w", i+1, orDefault(task.Path, "unnamed"), err)
		}
		id, err := generatedTaskID(task)
		if err != nil {
			return fmt.Errorf("invalid generated task %d (%s): %w", i+1, orDefault(task.Path, "unnamed"), err)
		}
		if previousPath, ok := seenTaskIDs[id]; ok {
			return fmt.Errorf("duplicate generated task id %q in %s and %s", id, previousPath, path)
		}
		seenTaskIDs[id] = path
	}
	for i, fixture := range s.Fixtures {
		path, err := normalizeFixturePath(fixture.Path, fmt.Sprintf("fixtures/fixture-%02d.txt", i+1))
		if err != nil {
			return err
		}
		if seenPaths[path] {
			return fmt.Errorf("duplicate generated path: %s", path)
		}
		seenPaths[path] = true
	}
	return nil
}

// WriteToDir writes suggested files to outputDir and returns written paths.
func (s *Suggestion) WriteToDir(outputDir string) ([]string, error) {
	return s.WriteToDirWithOptions(outputDir, WriteOptions{})
}

// WriteToDirWithOptions writes suggested files to outputDir and returns written paths.
func (s *Suggestion) WriteToDirWithOptions(outputDir string, opts WriteOptions) ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if err := s.validateNoConflicts(outputDir, opts); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating output directory: %w", err)
	}

	var written []string
	evalPath := filepath.Join(outputDir, "eval.yaml")
	evalYAML, writeEval, err := s.evalYAMLForApply(evalPath)
	if err != nil {
		return nil, err
	}
	if err := s.validateWritableTargets(outputDir, writeEval); err != nil {
		return nil, err
	}
	if opts.Force {
		if err := s.removeExistingTaskIDConflicts(outputDir); err != nil {
			return nil, err
		}
	}
	if writeEval {
		if err := writeGeneratedFile(outputDir, evalPath, string(evalYAML)); err != nil {
			return nil, fmt.Errorf("writing eval.yaml: %w", err)
		}
		written = append(written, evalPath)
	}

	for i, task := range s.Tasks {
		path, err := normalizeTaskPath(task.Path, fmt.Sprintf("tasks/task-%02d.yaml", i+1))
		if err != nil {
			return nil, err
		}
		target := filepath.Join(outputDir, path)
		if err := writeGeneratedFile(outputDir, target, task.Content); err != nil {
			return nil, err
		}
		written = append(written, target)
	}

	for i, fixture := range s.Fixtures {
		path, err := normalizeFixturePath(fixture.Path, fmt.Sprintf("fixtures/fixture-%02d.txt", i+1))
		if err != nil {
			return nil, err
		}
		target := filepath.Join(outputDir, path)
		if err := writeGeneratedFile(outputDir, target, fixture.Content); err != nil {
			return nil, err
		}
		written = append(written, target)
	}

	return written, nil
}

func (s *Suggestion) validateWritableTargets(outputDir string, writeEval bool) error {
	if writeEval {
		if err := ensureSafeGeneratedTarget(outputDir, filepath.Join(outputDir, "eval.yaml")); err != nil {
			return err
		}
	}
	for i, task := range s.Tasks {
		path, err := normalizeTaskPath(task.Path, fmt.Sprintf("tasks/task-%02d.yaml", i+1))
		if err != nil {
			return err
		}
		if err := ensureSafeGeneratedTarget(outputDir, filepath.Join(outputDir, path)); err != nil {
			return err
		}
	}
	for i, fixture := range s.Fixtures {
		path, err := normalizeFixturePath(fixture.Path, fmt.Sprintf("fixtures/fixture-%02d.txt", i+1))
		if err != nil {
			return err
		}
		if err := ensureSafeGeneratedTarget(outputDir, filepath.Join(outputDir, path)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Suggestion) evalYAMLForApply(evalPath string) ([]byte, bool, error) {
	info, statErr := os.Lstat(evalPath)
	if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("existing eval.yaml is a symlink; refusing to follow it")
	}
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, false, fmt.Errorf("checking existing eval.yaml: %w", statErr)
	}
	existing, err := os.ReadFile(evalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []byte(strings.TrimSpace(s.EvalYAML) + "\n"), true, nil
		}
		return nil, false, fmt.Errorf("reading existing eval.yaml: %w", err)
	}

	merged, changed, err := mergeEvalTasks(existing, []byte(s.EvalYAML))
	if err != nil {
		return nil, false, fmt.Errorf("merging eval.yaml: %w", err)
	}
	return merged, changed, nil
}

func mergeEvalTasks(existingYAML []byte, generatedYAML []byte) ([]byte, bool, error) {
	if err := validateEvalYAML(string(existingYAML)); err != nil {
		return nil, false, err
	}
	if err := validateEvalYAML(string(generatedYAML)); err != nil {
		return nil, false, err
	}

	generatedTasks, err := evalTaskRefs(generatedYAML)
	if err != nil {
		return nil, false, err
	}
	if len(generatedTasks) == 0 {
		return existingYAML, false, nil
	}

	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(existingYAML)).Decode(&doc); err != nil {
		return nil, false, fmt.Errorf("parsing existing eval.yaml: %w", err)
	}
	root := mappingRoot(&doc)
	if root == nil {
		return nil, false, errors.New("existing eval.yaml must be a mapping")
	}

	tasksNode := mappingValue(root, "tasks")
	if tasksNode == nil {
		tasksNode = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "tasks"},
			tasksNode,
		)
	}
	if tasksNode.Kind != yaml.SequenceNode {
		return nil, false, errors.New("existing eval.yaml tasks must be a list")
	}

	existing := make(map[string]bool, len(tasksNode.Content))
	for _, item := range tasksNode.Content {
		if item.Kind == yaml.ScalarNode {
			existing[item.Value] = true
		}
	}

	changed := false
	for _, taskRef := range generatedTasks {
		if existing[taskRef] {
			continue
		}
		tasksNode.Content = append(tasksNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: taskRef})
		existing[taskRef] = true
		changed = true
	}
	if !changed {
		return existingYAML, false, nil
	}

	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		_ = encoder.Close()
		return nil, false, fmt.Errorf("encoding merged eval.yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, false, fmt.Errorf("encoding merged eval.yaml: %w", err)
	}
	if err := validateEvalYAML(out.String()); err != nil {
		return nil, false, err
	}
	return out.Bytes(), true, nil
}

func evalTaskRefs(raw []byte) ([]string, error) {
	var spec struct {
		Tasks []string `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("parsing eval tasks: %w", err)
	}
	return spec.Tasks, nil
}

func mappingRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		return doc.Content[0]
	}
	if doc.Kind == yaml.MappingNode {
		return doc
	}
	return nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Kind == yaml.ScalarNode && mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func (s *Suggestion) validateNoConflicts(outputDir string, opts WriteOptions) error {
	if opts.Force {
		return nil
	}

	existingTaskIDs, err := loadExistingTaskIDs(filepath.Join(outputDir, "tasks"))
	if err != nil {
		return err
	}

	var conflicts []string
	seenGeneratedIDs := make(map[string]string)
	seenGeneratedPaths := make(map[string]bool)
	for i, task := range s.Tasks {
		path, err := normalizeTaskPath(task.Path, fmt.Sprintf("tasks/task-%02d.yaml", i+1))
		if err != nil {
			return err
		}
		if seenGeneratedPaths[path] {
			return fmt.Errorf("duplicate generated path: %s", path)
		}
		seenGeneratedPaths[path] = true
		target := filepath.Join(outputDir, path)
		if _, err := os.Lstat(target); err == nil {
			conflicts = append(conflicts, fmt.Sprintf("- existing: %s\n+ generated: %s", path, path))
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking %s: %w", path, err)
		}
		id, err := generatedTaskID(task)
		if err != nil {
			return err
		}
		if previousPath, ok := seenGeneratedIDs[id]; ok {
			return fmt.Errorf("duplicate generated task id %q in %s and %s", id, previousPath, path)
		}
		seenGeneratedIDs[id] = path
		if existingPath, ok := existingTaskIDs[id]; ok {
			conflicts = append(conflicts, fmt.Sprintf("- existing task id %q: %s\n+ generated task id %q: %s", id, existingPath, id, path))
		}
	}

	for i, fixture := range s.Fixtures {
		path, err := normalizeFixturePath(fixture.Path, fmt.Sprintf("fixtures/fixture-%02d.txt", i+1))
		if err != nil {
			return err
		}
		if seenGeneratedPaths[path] {
			return fmt.Errorf("duplicate generated path: %s", path)
		}
		seenGeneratedPaths[path] = true
		target := filepath.Join(outputDir, path)
		if _, err := os.Lstat(target); err == nil {
			conflicts = append(conflicts, fmt.Sprintf("- existing: %s\n+ generated: %s", path, path))
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking %s: %w", path, err)
		}
	}

	if len(conflicts) > 0 {
		return fmt.Errorf("refusing to overwrite existing suggest output without --force:\n%s\nUse --force to overwrite conflicting files or task ids", strings.Join(conflicts, "\n"))
	}
	return nil
}

func (s *Suggestion) removeExistingTaskIDConflicts(outputDir string) error {
	existingTaskIDs, err := loadExistingTaskIDs(filepath.Join(outputDir, "tasks"))
	if err != nil {
		return err
	}
	for i, task := range s.Tasks {
		path, err := normalizeTaskPath(task.Path, fmt.Sprintf("tasks/task-%02d.yaml", i+1))
		if err != nil {
			return err
		}
		id, err := generatedTaskID(task)
		if err != nil {
			return err
		}
		existingPath, ok := existingTaskIDs[id]
		if !ok || existingPath == path {
			continue
		}
		target := filepath.Join(outputDir, existingPath)
		if err := ensureSafeGeneratedTarget(outputDir, target); err != nil {
			return err
		}
		if err := os.Remove(target); err != nil {
			return fmt.Errorf("removing existing task id %q at %s: %w", id, existingPath, err)
		}
	}
	return nil
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
	if errs := validation.ValidateEvalBytes([]byte(raw)); len(errs) > 0 {
		return fmt.Errorf("invalid eval_yaml: %s", strings.Join(errs, "; "))
	}
	var spec models.EvalSpec
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	decoder.KnownFields(true) // Strict parsing to catch unknown fields
	if err := decoder.Decode(&spec); err != nil {
		return fmt.Errorf("invalid eval_yaml: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("invalid eval_yaml: %w", err)
	}
	for _, taskRef := range spec.Tasks {
		if err := validateEvalTaskRef(taskRef); err != nil {
			return fmt.Errorf("invalid eval_yaml: tasks entry %q: %w", taskRef, err)
		}
	}
	for i, g := range spec.Graders {
		if g.Identifier == "" {
			return fmt.Errorf("invalid eval_yaml: grader[%d] is missing required 'name' field", i)
		}
		if g.Kind == "" {
			return fmt.Errorf("invalid eval_yaml: grader[%d] (%s) is missing required 'type' field", i, g.Identifier)
		}
	}
	return nil
}

func validateEvalTaskRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errors.New("must not be empty")
	}
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, `\`) {
		return errors.New("must be a relative path or glob")
	}
	if containsTraversalSegment(ref) {
		return errors.New("must not contain '..' segments")
	}
	return nil
}

func validateGeneratedTask(task GeneratedFile) error {
	if task.Confidence == nil {
		return errors.New("confidence is required")
	}
	if *task.Confidence < 0 || *task.Confidence > 1 {
		return fmt.Errorf("confidence must be between 0 and 1, got %g", *task.Confidence)
	}
	if strings.TrimSpace(task.Rationale) == "" {
		return errors.New("rationale is required")
	}
	if _, err := generatedTaskID(task); err != nil {
		return err
	}
	return nil
}

func generatedTaskID(task GeneratedFile) (string, error) {
	if strings.TrimSpace(task.Content) == "" {
		return "", errors.New("task content is required")
	}
	if errs := validation.ValidateTaskBytes([]byte(task.Content)); len(errs) > 0 {
		return "", fmt.Errorf("task schema validation failed: %s", strings.Join(errs, "; "))
	}
	var tc models.TestCase
	decoder := yaml.NewDecoder(bytes.NewReader([]byte(task.Content)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&tc); err != nil {
		return "", fmt.Errorf("parsing task YAML: %w", err)
	}
	if err := tc.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(tc.TestID) == "" {
		return "", errors.New("task id is required")
	}
	return tc.TestID, nil
}

func loadExistingTaskIDs(tasksDir string) (map[string]string, error) {
	ids := make(map[string]string)
	matches, err := filepath.Glob(filepath.Join(tasksDir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("scanning existing tasks: %w", err)
	}
	for _, match := range matches {
		info, err := os.Lstat(match)
		if err != nil {
			return nil, fmt.Errorf("checking existing task %s: %w", match, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("existing task %s is a symlink; refusing to follow it", match)
		}
		data, err := os.ReadFile(match)
		if err != nil {
			return nil, fmt.Errorf("reading existing task %s: %w", match, err)
		}
		id, err := generatedTaskID(GeneratedFile{Path: match, Content: string(data)})
		if err != nil {
			return nil, fmt.Errorf("existing task %s is invalid: %w", match, err)
		}
		rel, err := filepath.Rel(filepath.Dir(tasksDir), match)
		if err != nil {
			rel = match
		}
		ids[id] = rel
	}
	return ids, nil
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
	if containsTraversalSegment(clean) {
		return "", fmt.Errorf("invalid generated path: %s", path)
	}
	clean = filepath.Clean(clean)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("invalid generated path: %s", path)
	}
	return clean, nil
}

func normalizeTaskPath(path, fallback string) (string, error) {
	clean, err := normalizeGeneratedPath(path, fallback)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(filepath.ToSlash(clean), "tasks/") {
		return "", fmt.Errorf("invalid generated task path %q: must be under tasks/", path)
	}
	return clean, nil
}

func normalizeFixturePath(path, fallback string) (string, error) {
	clean, err := normalizeGeneratedPath(path, fallback)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(filepath.ToSlash(clean), "fixtures/") {
		return "", fmt.Errorf("invalid generated fixture path %q: must be under fixtures/", path)
	}
	return clean, nil
}

func containsTraversalSegment(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == '\\' || r == filepath.Separator
	}) {
		if seg == ".." {
			return true
		}
	}
	return false
}

func writeGeneratedFile(outputDir, path string, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", path, err)
	}
	if err := ensureSafeGeneratedTarget(outputDir, path); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func ensureSafeGeneratedTarget(outputDir, target string) error {
	rel, err := filepath.Rel(outputDir, target)
	if err != nil {
		return fmt.Errorf("checking generated path %s: %w", target, err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return fmt.Errorf("generated path escapes output directory: %s", target)
	}

	current := outputDir
	dir := filepath.Dir(rel)
	if dir != "." {
		for _, part := range strings.Split(dir, string(filepath.Separator)) {
			if part == "" || part == "." {
				continue
			}
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return fmt.Errorf("checking generated directory %s: %w", current, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("generated directory %s is a symlink; refusing to write through it", current)
			}
		}
	}

	info, err := os.Lstat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("checking generated file %s: %w", target, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("generated file %s is a symlink; refusing to overwrite it", target)
	}
	return nil
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func focusInstruction(focus FocusCategory) string {
	switch focus {
	case FocusTriggers:
		return "triggers: Focus all cases on positive trigger behavior from USE FOR phrases."
	case FocusNegativeTriggers:
		return "negative-triggers: Focus all cases on negative trigger boundaries where the skill should not activate."
	case FocusEdgeFixtures:
		return "edge-fixtures: Focus all cases on edge fixture inputs, unusual file contents, and boundary examples."
	case FocusDoNotUseFor:
		return "do-not-use-for: Focus all cases on explicit DO NOT USE FOR exclusions and refusal behavior."
	case FocusParameters:
		return "parameters: Focus all cases on parameter extraction, defaults, validation, and ambiguity."
	default:
		return "Balance cases across triggers, negative triggers, edge fixtures, DO NOT USE FOR exclusions, and parameters."
	}
}

func focusCategoryList() string {
	values := make([]string, 0, len(AllFocusCategories()))
	for _, category := range AllFocusCategories() {
		values = append(values, string(category))
	}
	return strings.Join(values, "|")
}
