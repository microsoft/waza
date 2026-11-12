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
	if err := admitSpec(spec); err != nil {
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

func admitSpec(spec *models.EvalSpec) error {
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
	return nil
}

func admitTask(tc *models.TestCase, target Target) error {
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
	}
	return nil
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
