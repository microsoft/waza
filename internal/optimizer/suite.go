package optimizer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
)

// Split is one disjoint, pinned set of task IDs.
type Split struct {
	Name    string   `json:"name"`
	Tags    []string `json:"tags"`
	TestIDs []string `json:"test_ids"`
}

// ConfigPin records the harness settings that must not change mid-run.
type ConfigPin struct {
	Executor        string `json:"executor"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	TrialsPerTask   int    `json:"trials_per_task"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
}

// SuitePin fixes the evaluation suite for the whole optimization run. It is
// recomputed before every evaluation; any drift fails with suite_changed.
type SuitePin struct {
	SpecPath      string    `json:"spec_path"`
	SpecDigest    string    `json:"spec_dir_digest"`
	ContextDir    string    `json:"context_dir,omitempty"`
	ContextDigest string    `json:"context_dir_digest,omitempty"`
	Config        ConfigPin `json:"config"`
	WazaVersion   string    `json:"waza_version"`
	Optimization  Split     `json:"optimization"`
	Qualification Split     `json:"qualification"`
}

// SuiteOptions selects the suite and its splits.
type SuiteOptions struct {
	SpecPath          string
	ContextDir        string
	Target            Target
	OptimizationTags  []string
	QualificationTags []string
	// ExcludeDirs are skipped when digesting the spec directory (for example
	// the optimizer output directory when it lives beside the suite).
	ExcludeDirs []string
	WazaVersion string
	ModelID     string
	Now         time.Time
}

// PinSuite admits a suite for optimization and returns its pin. Admission is
// conservative: features that could leak state between candidates or splits,
// or that would bypass candidate materialization, are rejected.
func PinSuite(opts SuiteOptions) (*SuitePin, error) {
	if len(opts.OptimizationTags) == 0 || len(opts.QualificationTags) == 0 {
		return nil, newError(CategoryInvalidConfig, "--optimization-tag and --qualification-tag are both required")
	}
	specPath, err := filepath.Abs(opts.SpecPath)
	if err != nil {
		return nil, wrapError(CategoryInvalidConfig, err, "resolving spec path")
	}
	spec, err := models.LoadEvalSpecOffline(specPath)
	if err != nil {
		return nil, wrapError(CategoryInvalidSuite, err, "loading %s", opts.SpecPath)
	}
	specDir := filepath.Dir(specPath)
	if err := admitSpec(spec, opts.Target); err != nil {
		return nil, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	cases, err := orchestration.LoadSelectedTestCasesOffline(spec, specDir, now)
	if err != nil {
		return nil, wrapError(CategoryInvalidSuite, err, "loading tasks")
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		if strings.TrimSpace(tc.TestID) == "" {
			return nil, newError(CategoryInvalidSuite, "task %q has no id; optimizer splits require explicit task ids", tc.DisplayName)
		}
		if seen[tc.TestID] {
			return nil, newError(CategoryInvalidSuite, "duplicate task id %q", tc.TestID)
		}
		seen[tc.TestID] = true
		if err := admitTask(tc, opts.Target); err != nil {
			return nil, err
		}
	}
	opt := selectSplit(SplitOptimization, cases, opts.OptimizationTags)
	qual := selectSplit(SplitQualification, cases, opts.QualificationTags)
	if len(opt.TestIDs) == 0 {
		return nil, newError(CategoryInvalidSuite, "no active tasks match --optimization-tag %v", opts.OptimizationTags)
	}
	if len(qual.TestIDs) == 0 {
		return nil, newError(CategoryInvalidSuite, "no active tasks match --qualification-tag %v", opts.QualificationTags)
	}
	for _, id := range opt.TestIDs {
		if slices.Contains(qual.TestIDs, id) {
			return nil, newError(CategoryInvalidSuite, "task %q is in both the optimization and qualification splits", id)
		}
	}

	contextDir := opts.ContextDir
	if contextDir == "" {
		if def := filepath.Join(specDir, "fixtures"); dirExists(def) {
			contextDir = def
		}
	}
	if contextDir != "" {
		if contextDir, err = filepath.Abs(contextDir); err != nil {
			return nil, wrapError(CategoryInvalidConfig, err, "resolving context dir")
		}
	}
	if opts.Target == TargetContextOverlay && contextDir == "" {
		return nil, newError(CategoryInvalidConfig, "the context-overlay target needs a context directory (--context-dir or <spec-dir>/fixtures)")
	}
	if err := admitDependencies(spec, cases, specDir, contextDir); err != nil {
		return nil, err
	}
	if opts.Target == TargetSkillDirectory {
		if err := admitNoAmbientSkill(spec.SkillName); err != nil {
			return nil, err
		}
	}
	model := spec.Config.ModelID
	if opts.ModelID != "" {
		model = opts.ModelID
	}
	pin := &SuitePin{
		SpecPath: specPath,
		Config: ConfigPin{
			Executor:        spec.Config.EngineType,
			Model:           model,
			ReasoningEffort: spec.Config.ReasoningEffort,
			TrialsPerTask:   spec.Config.TrialsPerTask,
			TimeoutSeconds:  spec.Config.TimeoutSec,
		},
		WazaVersion:   opts.WazaVersion,
		ContextDir:    contextDir,
		Optimization:  opt,
		Qualification: qual,
	}
	if err := pin.digest(opts.ExcludeDirs); err != nil {
		return nil, err
	}
	return pin, nil
}

func admitSpec(spec *models.EvalSpec, target Target) error {
	h := spec.Hooks
	if len(h.BeforeRun)+len(h.AfterRun)+len(h.BeforeTask)+len(h.AfterTask) > 0 {
		return newError(CategoryInvalidSuite, "suites with hooks are not supported by the optimizer (hooks can carry state between candidates)")
	}
	if spec.Baseline {
		return newError(CategoryInvalidSuite, "baseline suites are not supported by the optimizer")
	}
	if spec.Scenario != "" {
		return newError(CategoryInvalidSuite, "scenario suites are not supported by the optimizer")
	}
	if target == TargetSkillDirectory && slices.Contains(spec.Config.DisabledSkills, "*") {
		return newError(CategoryInvalidSuite, "disabled_skills: [\"*\"] would evaluate every candidate without its skill; remove it for the skill-directory target")
	}
	for _, g := range spec.Graders {
		if updatesSnapshots(g.Parameters) {
			return newError(CategoryInvalidSuite, "grader %q sets update_snapshots, which can rewrite pinned suite files during optimization", g.Identifier)
		}
	}
	return nil
}

func updatesSnapshots(p models.GraderParameters) bool {
	switch v := p.(type) {
	case models.DiffGraderParameters:
		return v.UpdateSnapshots
	case *models.DiffGraderParameters:
		return v != nil && v.UpdateSnapshots
	case models.GenericGraderParameters:
		b, _ := v["update_snapshots"].(bool)
		return b
	}
	return false
}

// admitNoAmbientSkill rejects runs where a same-named skill is discoverable
// from the working directory: the runner and the Copilot executor search it
// before the candidate skill directory, so scores could reflect the ambient
// skill instead of the candidate.
func admitNoAmbientSkill(skillName string) error {
	if strings.TrimSpace(skillName) == "" {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return wrapError(CategoryInfrastructureFailure, err, "resolving working directory")
	}
	if execution.IsSkillAvailable([]string{cwd}, skillName) {
		return newError(CategoryInvalidSuite, "skill %q is discoverable from the working directory %s, which would shadow the candidate; run waza optimize from a directory without it", skillName, cwd)
	}
	return nil
}

func admitTask(tc *models.TestCase, target Target) error {
	graders := slices.Clone(tc.Validators)
	for _, cp := range tc.Checkpoints {
		graders = append(graders, cp.Graders...)
	}
	for _, g := range graders {
		if updatesSnapshots(g.Parameters) {
			return newError(CategoryInvalidSuite, "task %q grader %q sets update_snapshots, which can rewrite pinned suite files during optimization", tc.TestID, g.Identifier)
		}
	}
	switch target {
	case TargetSkillDirectory:
		if len(tc.SkillPaths) > 0 {
			return newError(CategoryInvalidSuite, "task %q sets skill_directories, which would bypass the candidate skill", tc.TestID)
		}
	case TargetContextOverlay:
		if tc.ContextRoot != "" {
			return newError(CategoryInvalidSuite, "task %q sets context_dir, which would bypass the candidate overlay", tc.TestID)
		}
		if len(tc.Stimulus.Repos) > 0 {
			return newError(CategoryInvalidSuite, "task %q clones repos, which would bypass the candidate overlay", tc.TestID)
		}
		if _, ok := tc.Stimulus.Metadata["fixture"]; ok {
			return newError(CategoryInvalidSuite, "task %q sets inputs.context.fixture, which is read from the suite directory rather than the candidate overlay", tc.TestID)
		}
	}
	return nil
}

// admitDependencies keeps every file the suite can read inside the pinned
// suite and context directories, so the pin digests cover them. Task files
// must live under the suite directory, and path-valued fields must not use
// ".." or absolute paths outside the pinned roots.
func admitDependencies(spec *models.EvalSpec, cases []*models.TestCase, specDir, contextDir string) error {
	roots := []string{specDir}
	if contextDir != "" {
		roots = append(roots, contextDir)
	}
	files, err := spec.ResolveTestFiles(specDir)
	if err != nil {
		return wrapError(CategoryInvalidSuite, err, "resolving task files")
	}
	for _, f := range files {
		if !withinAny(f, roots[:1]) {
			return newError(CategoryInvalidSuite, "task file %s is outside the suite directory, so it cannot be pinned", f)
		}
	}
	if err := checkPathFields("suite", spec, roots); err != nil {
		return err
	}
	for _, tc := range cases {
		if err := checkPathFields(fmt.Sprintf("task %q", tc.TestID), tc, roots); err != nil {
			return err
		}
	}
	return nil
}

func checkPathFields(owner string, v any, roots []string) error {
	data, err := json.Marshal(v)
	if err != nil {
		return wrapError(CategoryInvalidSuite, err, "inspecting %s", owner)
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return wrapError(CategoryInvalidSuite, err, "inspecting %s", owner)
	}
	var walk func(key string, node any) error
	walk = func(key string, node any) error {
		switch n := node.(type) {
		case map[string]any:
			for k, child := range n {
				if err := walk(k, child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range n {
				if err := walk(key, child); err != nil {
					return err
				}
			}
		case string:
			if isPathKey(key) && !pinnedPath(n, roots) {
				return newError(CategoryInvalidSuite, "%s field %q references %q outside the pinned suite and context directories", owner, key, n)
			}
		}
		return nil
	}
	return walk("", doc)
}

func isPathKey(key string) bool {
	switch key {
	case "path", "file", "files", "script", "fixture", "fixtures", "location":
		return true
	}
	for _, suffix := range []string{"_path", "_paths", "_file", "_files", "_dir", "_dirs", "_root"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

func pinnedPath(p string, roots []string) bool {
	if p == "" || strings.Contains(p, "://") {
		return true
	}
	if filepath.IsAbs(p) {
		return withinAny(p, roots)
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return false
		}
	}
	return true
}

func withinAny(p string, roots []string) bool {
	for _, root := range roots {
		if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func selectSplit(name string, cases []*models.TestCase, patterns []string) Split {
	ids := []string{}
	for _, tc := range cases {
		if matchesAnyTag(tc.Tags, patterns) {
			ids = append(ids, tc.TestID)
		}
	}
	sort.Strings(ids)
	tags := slices.Clone(patterns)
	sort.Strings(tags)
	return Split{Name: name, Tags: tags, TestIDs: ids}
}

func matchesAnyTag(tags, patterns []string) bool {
	for _, tag := range tags {
		for _, p := range patterns {
			if ok, err := filepath.Match(p, tag); err == nil && ok {
				return true
			}
		}
	}
	return false
}

func (p *SuitePin) digest(exclude []string) error {
	abs := make([]string, 0, len(exclude))
	for _, e := range exclude {
		if a, err := filepath.Abs(e); err == nil {
			abs = append(abs, filepath.Clean(a))
		}
	}
	skip := func(path string) bool { return slices.Contains(abs, filepath.Clean(path)) }
	var err error
	if p.SpecDigest, err = treeDigest(filepath.Dir(p.SpecPath), skip); err != nil {
		return wrapError(CategoryInvalidSuite, err, "digesting suite directory")
	}
	if p.ContextDir != "" {
		if p.ContextDigest, err = treeDigest(p.ContextDir, skip); err != nil {
			return wrapError(CategoryInvalidSuite, err, "digesting context directory")
		}
	}
	return nil
}

// Verify recomputes digests and fails with suite_changed on drift.
func (p *SuitePin) Verify(exclude []string) error {
	current := *p
	if err := current.digest(exclude); err != nil {
		return err
	}
	if current.SpecDigest != p.SpecDigest {
		return newError(CategorySuiteChanged, "suite directory changed during optimization")
	}
	if current.ContextDigest != p.ContextDigest {
		return newError(CategorySuiteChanged, "context directory changed during optimization")
	}
	return nil
}

// Digest identifies the whole pin.
func (p *SuitePin) Digest() string {
	data, _ := json.Marshal(p)
	return "sha256:" + sha256Hex(data)
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func (s Split) String() string {
	return fmt.Sprintf("%s(%d tasks)", s.Name, len(s.TestIDs))
}
