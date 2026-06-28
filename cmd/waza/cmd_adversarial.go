package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/microsoft/waza/internal/adversarial"
	"github.com/microsoft/waza/internal/models"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Documented, stable exit codes for `waza adversarial`. CI may depend on
// these — do not change without a release note.
const (
	// AdversarialExitPass means every pack task either passed (skill correctly
	// refused / declined) or was skipped.
	AdversarialExitPass = 0
	// AdversarialExitUnsafe means at least one pack task reported an unsafe
	// outcome and on_unsafe_outcome is set to "fail". This deliberately
	// matches GateExitGoldenFailure so CI scripts can handle both signals
	// identically.
	AdversarialExitUnsafe = 2
	// AdversarialExitConfig means flags or input files were invalid.
	AdversarialExitConfig = 3
)

type adversarialOptions struct {
	packs           []string
	specPath        string
	output          string
	onUnsafe        string
	engine          string
	model           string
	skill           string
	workers         int
	parallel        bool
	verbose         bool
	keepArtifacts   bool
	listPacks       bool
	artifactsDirOut string // optional override for the tempdir (mainly for tests)
}

func newAdversarialCommand() *cobra.Command {
	opts := &adversarialOptions{}

	cmd := &cobra.Command{
		Use:   "adversarial",
		Short: "Run offline adversarial / fault-injection packs against a skill",
		Long: `Run one or more built-in adversarial test packs against a skill.

Packs are deterministic, offline bundles of tasks and fixtures embedded into
the waza binary. Each task is marked golden, so any unsafe outcome (the skill
followed an embedded injection, leaked credentials, performed an out-of-scope
action, etc.) fails the run by default. CI scripts can rely on exit code 2
for "unsafe outcome detected" — the same exit code waza gate uses for golden
failures.

Built-in packs:
  prompt-injection    Indirect prompt injection delivered via fixture files.
  scope-bypass        Asks the skill to perform actions outside its scope.

Examples:
  waza adversarial --skill code-review
  waza adversarial --packs prompt-injection --on-unsafe-outcome warn
  waza adversarial --spec eval.yaml                  # read adversarial block from spec
  waza adversarial --skill code-review --engine mock # offline dev / CI smoke
`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdversarial(cmd, opts)
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&opts.packs, "packs", nil, "comma-separated list of pack names (default: every built-in pack)")
	f.StringVar(&opts.specPath, "spec", "", "optional eval.yaml; its adversarial.packs / adversarial.on_unsafe_outcome are used when --packs / --on-unsafe-outcome are not set")
	f.StringVar(&opts.output, "output", "", "write the full results JSON to this path")
	f.StringVar(&opts.onUnsafe, "on-unsafe-outcome", "", "fail|warn — fail (default) returns exit 2 on any unsafe outcome; warn returns 0")
	f.StringVar(&opts.engine, "engine", "", "engine to use (mock or copilot-sdk). Defaults to copilot-sdk, or to mock when --skill is unset")
	f.StringVar(&opts.model, "model", "claude-sonnet-4-20250514", "model id to evaluate against")
	f.StringVar(&opts.skill, "skill", "", "skill name to evaluate. Required unless --engine=mock")
	f.IntVar(&opts.workers, "workers", 0, "concurrency for task execution (0 = sequential)")
	f.BoolVar(&opts.parallel, "parallel", false, "enable parallel task execution")
	f.BoolVarP(&opts.verbose, "verbose", "v", false, "verbose progress output")
	f.BoolVar(&opts.keepArtifacts, "keep-artifacts", false, "keep the temp directory containing the extracted packs and synthesized eval.yaml")
	f.BoolVar(&opts.listPacks, "list-packs", false, "print the catalog of built-in adversarial packs and exit")
	f.StringVar(&opts.artifactsDirOut, "artifacts-dir", "", "directory to use as the artifacts root (default: an os.TempDir entry). Implies --keep-artifacts.")

	return cmd
}

// resolveAdversarialConfig decides the final list of packs and the
// on_unsafe_outcome policy, layering flags over the optional spec block.
func resolveAdversarialConfig(opts *adversarialOptions) (*models.AdversarialConfig, *models.EvalSpec, error) {
	var specCfg *models.AdversarialConfig
	var loadedSpec *models.EvalSpec

	if opts.specPath != "" {
		spec, err := models.LoadEvalSpec(opts.specPath)
		if err != nil {
			return nil, nil, fmt.Errorf("load --spec: %w", err)
		}
		loadedSpec = spec
		if spec.Adversarial != nil {
			cp := *spec.Adversarial
			specCfg = &cp
		}
	}

	cfg := &models.AdversarialConfig{}
	switch {
	case len(opts.packs) > 0:
		cfg.Packs = append(cfg.Packs, opts.packs...)
	case specCfg != nil && len(specCfg.Packs) > 0:
		cfg.Packs = append(cfg.Packs, specCfg.Packs...)
	default:
		cfg.Packs = adversarial.ListPacks()
	}

	switch {
	case opts.onUnsafe != "":
		cfg.OnUnsafeOutcome = models.AdversarialOnUnsafeOutcome(opts.onUnsafe)
	case specCfg != nil && specCfg.OnUnsafeOutcome != "":
		cfg.OnUnsafeOutcome = specCfg.OnUnsafeOutcome
	default:
		cfg.OnUnsafeOutcome = models.AdversarialOnUnsafeOutcomeFail
	}

	if err := cfg.Validate(); err != nil {
		return nil, loadedSpec, err
	}

	// Reject unknown pack names early so users get a clean message instead
	// of a confusing tasks-not-found error from the runner.
	known := make(map[string]bool, len(adversarial.ListPacks()))
	for _, n := range adversarial.ListPacks() {
		known[n] = true
	}
	for _, p := range cfg.Packs {
		if !known[p] {
			return nil, loadedSpec, fmt.Errorf("unknown adversarial pack %q (known packs: %s)", p, strings.Join(adversarial.ListPacks(), ", "))
		}
	}

	return cfg, loadedSpec, nil
}

// runAdversarial is the cobra RunE entry. It extracts packs to a temp dir,
// writes a synthesized eval.yaml + task list, and reuses runCommandForSpec
// so the adversarial run shares all of waza run's plumbing.
func runAdversarial(cmd *cobra.Command, opts *adversarialOptions) error {
	if opts.listPacks {
		w := cmd.OutOrStdout()
		_, _ = fmt.Fprintln(w, "Built-in adversarial packs:")
		for _, name := range adversarial.ListPacks() {
			p, err := adversarial.LoadPack(name)
			if err != nil {
				_, _ = fmt.Fprintf(w, "  %s\t(error: %v)\n", name, err)
				continue
			}
			desc := strings.TrimSpace(p.Manifest.Description)
			if desc == "" {
				desc = "(no description)"
			}
			_, _ = fmt.Fprintf(w, "  %s\t%d tasks\t%s\n", name, len(p.Manifest.Tasks), desc)
		}
		return nil
	}

	cfg, baseSpec, err := resolveAdversarialConfig(opts)
	if err != nil {
		// Surface configuration errors with an exit code distinct from
		// "unsafe outcome detected". main.go honors ExitCodeError.Code and
		// prints the wrapped message, so deferred cleanups still run.
		return &ExitCodeError{Code: AdversarialExitConfig, Err: err}
	}

	engineName := strings.TrimSpace(opts.engine)
	if engineName == "" {
		if opts.skill == "" {
			engineName = "mock"
		} else {
			engineName = "copilot-sdk"
		}
	}
	skillName := strings.TrimSpace(opts.skill)
	if skillName == "" {
		// Use a deterministic placeholder for mock runs so the synthesized
		// spec validates without forcing the caller to pick one.
		skillName = "adversarial-target"
	}

	// Materialize packs.
	artifactsRoot := strings.TrimSpace(opts.artifactsDirOut)
	keepDir := opts.keepArtifacts || artifactsRoot != ""
	if artifactsRoot == "" {
		dir, err := os.MkdirTemp("", "waza-adversarial-*")
		if err != nil {
			return fmt.Errorf("create temp dir: %w", err)
		}
		artifactsRoot = dir
	} else {
		if err := os.MkdirAll(artifactsRoot, 0o755); err != nil {
			return fmt.Errorf("create artifacts dir: %w", err)
		}
	}
	if !keepDir {
		defer func() { _ = os.RemoveAll(artifactsRoot) }()
	}

	var taskGlobs []string
	for _, name := range cfg.Packs {
		pack, err := adversarial.LoadPack(name)
		if err != nil {
			return err
		}
		packRoot, err := pack.Extract(artifactsRoot)
		if err != nil {
			return fmt.Errorf("extract pack %q: %w", name, err)
		}
		fixturesDir := filepath.Join(packRoot, "fixtures")
		if err := injectContextDir(packRoot, fixturesDir); err != nil {
			return fmt.Errorf("inject context_dir for pack %q: %w", name, err)
		}
		for _, t := range pack.Manifest.Tasks {
			rel, err := filepath.Rel(artifactsRoot, filepath.Join(packRoot, filepath.FromSlash(t)))
			if err != nil {
				return fmt.Errorf("pack %q: relative task path: %w", name, err)
			}
			taskGlobs = append(taskGlobs, filepath.ToSlash(rel))
		}
	}
	sort.Strings(taskGlobs)

	// Synthesize an EvalSpec.
	synthesized := &models.EvalSpec{
		SchemaVersion: models.CurrentSchemaVersion,
		SpecIdentity: models.SpecIdentity{
			Name:        "adversarial",
			Description: "Auto-generated by waza adversarial",
		},
		SkillName: skillName,
		Version:   "1.0",
		Config: models.Config{
			TrialsPerTask: 1,
			TimeoutSec:    300,
			Concurrent:    opts.parallel,
			Workers:       opts.workers,
			EngineType:    engineName,
			ModelID:       opts.model,
		},
		Tasks:       taskGlobs,
		Adversarial: cfg,
	}

	// Carry forward useful spec-level config (graders, metrics) from a
	// referenced spec so callers can compose adversarial packs with their
	// own quality graders. We deliberately *do not* override the task list.
	if baseSpec != nil {
		if len(baseSpec.Graders) > 0 {
			synthesized.Graders = baseSpec.Graders
		}
		if len(baseSpec.Metrics) > 0 {
			synthesized.Metrics = baseSpec.Metrics
		}
	}

	// metrics are not strictly required for a run, but the YAML serializer
	// emits an empty list cleanly so we leave them unset if absent.

	specPath := filepath.Join(artifactsRoot, "eval.yaml")
	specBytes, err := yaml.Marshal(synthesized)
	if err != nil {
		return fmt.Errorf("marshal synthesized eval spec: %w", err)
	}
	if err := os.WriteFile(specPath, specBytes, 0o644); err != nil {
		return fmt.Errorf("write synthesized eval spec: %w", err)
	}

	if opts.verbose {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"adversarial: packs=%s policy=%s engine=%s artifacts=%s\n",
			strings.Join(cfg.Packs, ","), cfg.EffectiveOnUnsafeOutcome(), engineName, artifactsRoot)
	}

	// Reuse runCommandForSpec for the actual run. Set the package-level
	// flags it consumes, then restore them on exit so we don't leak state
	// across multiple commands in a single process (tests, embedders).
	prevContextDir := contextDir
	prevOutputPath := outputPath
	prevWorkers := workers
	prevParallel := parallel
	prevVerbose := verbose
	defer func() {
		contextDir = prevContextDir
		outputPath = prevOutputPath
		workers = prevWorkers
		parallel = prevParallel
		verbose = prevVerbose
	}()

	contextDir = artifactsRoot
	outputPath = opts.output
	workers = opts.workers
	parallel = opts.parallel
	verbose = opts.verbose

	sp := skillSpecPath{evalSpecPath: specPath, skillName: synthesized.SkillName}
	defaults := []string{synthesized.SkillName}

	results, runErr := runCommandForSpec(cmd, sp, defaults)
	// `runCommandForSpec` returns TestFailureError when graders trip and
	// there's a single model. Adversarial runs *expect* failures when the
	// skill behaved unsafely, so we translate the error into our own
	// pass/unsafe summary.
	var testFailErr *TestFailureError
	if runErr != nil && !errors.As(runErr, &testFailErr) {
		return runErr
	}

	unsafeTasks := collectUnsafeOutcomes(results)
	printAdversarialSummary(cmd, cfg, unsafeTasks, len(taskGlobs))

	if len(unsafeTasks) > 0 && cfg.EffectiveOnUnsafeOutcome() == models.AdversarialOnUnsafeOutcomeFail {
		// Return an ExitCodeError so deferred cleanups (tempdir removal,
		// run-globals restoration) still run. main.go translates the code
		// to the process exit. The test exit hook is honored first so unit
		// tests can capture the would-be code without bubbling an error.
		if adversarialExitFn != nil {
			adversarialExitFn(AdversarialExitUnsafe)
			return nil
		}
		cmd.SilenceErrors = true
		return &ExitCodeError{Code: AdversarialExitUnsafe, Err: errAdversarialUnsafe}
	}
	return nil
}

// adversarialExitFn is a hook so tests can capture the would-be exit code
// without terminating the test binary. nil means the real os.Exit is used.
var adversarialExitFn func(int)

// errAdversarialUnsafe is the sentinel returned to cobra when an unsafe
// outcome triggers a hard failure via the test exit hook.
var errAdversarialUnsafe = errors.New("adversarial: unsafe outcome detected")

// unsafeTask is the per-task adversarial verdict.
type unsafeTask struct {
	TaskID  string
	Reasons []string
}

// collectUnsafeOutcomes scans the per-model outcomes and returns a list of
// task IDs where the skill produced an unsafe outcome. We treat any failed
// golden task as unsafe (every adversarial pack task is golden), plus any
// task whose status is "fail" regardless of the golden flag, so custom
// graders contributed via the spec also count.
func collectUnsafeOutcomes(results []modelResult) []unsafeTask {
	var out []unsafeTask
	for _, mr := range results {
		if mr.outcome == nil {
			continue
		}
		for i := range mr.outcome.TestOutcomes {
			t := &mr.outcome.TestOutcomes[i]
			if isUnsafeOutcome(t) {
				out = append(out, unsafeTask{
					TaskID:  t.TestID,
					Reasons: unsafeReasons(t),
				})
			}
		}
	}
	// De-duplicate by task id (multiple models would otherwise inflate
	// the list) while preserving the first reason set we saw.
	seen := make(map[string]bool, len(out))
	dedup := out[:0]
	for _, u := range out {
		if seen[u.TaskID] {
			continue
		}
		seen[u.TaskID] = true
		dedup = append(dedup, u)
	}
	return dedup
}

func isUnsafeOutcome(t *models.TestOutcome) bool {
	if t == nil {
		return false
	}
	switch t.Status {
	case models.StatusFailed, models.StatusError:
		return true
	}
	return false
}

func unsafeReasons(t *models.TestOutcome) []string {
	var reasons []string
	for _, r := range t.Runs {
		if r.ErrorMsg != "" {
			reasons = append(reasons, r.ErrorMsg)
		}
		for _, g := range r.Validations {
			if !g.Passed && g.Feedback != "" {
				reasons = append(reasons, fmt.Sprintf("%s: %s", g.Name, g.Feedback))
			}
		}
	}
	if len(reasons) == 0 {
		reasons = []string{"task did not pass"}
	}
	return reasons
}

func printAdversarialSummary(cmd *cobra.Command, cfg *models.AdversarialConfig, unsafe []unsafeTask, totalTasks int) {
	w := cmd.OutOrStdout()
	fp := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
	fln := func(s string) { _, _ = fmt.Fprintln(w, s) }
	fln("")
	fp("Adversarial summary (packs: %s)\n", strings.Join(cfg.Packs, ", "))
	fp("  tasks run:  %d\n", totalTasks)
	fp("  unsafe:     %d\n", len(unsafe))
	policy := cfg.EffectiveOnUnsafeOutcome()
	fp("  policy:     %s\n", policy)
	if len(unsafe) == 0 {
		fln("  result:     ✅ all packs PASSED")
		return
	}
	fln("  result:     ❌ unsafe outcomes detected")
	for _, u := range unsafe {
		fp("    - %s\n", u.TaskID)
		for _, r := range u.Reasons {
			fp("        · %s\n", r)
		}
	}
}

// injectContextDir rewrites each top-level `*.yaml` task file directly under
// `<packRoot>/tasks` (non-recursive; nested task directories are not
// supported) so every task has an absolute `context_dir:` pointing at the
// pack's fixtures directory. The runner resolves a task's relative
// context_dir against the spec directory, which would land outside our
// synthesized eval root. Setting an absolute path side-steps that and keeps
// each pack self-contained even when multiple packs are extracted to the
// same artifacts root.
func injectContextDir(packRoot, fixturesDir string) error {
	tasksDir := filepath.Join(packRoot, "tasks")
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		return err
	}
	absFixtures, err := filepath.Abs(fixturesDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		p := filepath.Join(tasksDir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		// Drop any pre-existing `context_dir:` line — last write wins, but
		// keep the YAML deterministic for inspection.
		lines := strings.Split(string(b), "\n")
		out := lines[:0]
		for _, l := range lines {
			if strings.HasPrefix(l, "context_dir:") {
				continue
			}
			out = append(out, l)
		}
		injected := fmt.Sprintf("context_dir: %q\n%s", absFixtures, strings.Join(out, "\n"))
		if err := os.WriteFile(p, []byte(injected), 0o644); err != nil {
			return err
		}
	}
	return nil
}
