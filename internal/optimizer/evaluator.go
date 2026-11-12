package optimizer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
)

// Evaluation states.
const (
	StateScored     = "scored"
	StateIncomplete = "evaluation_incomplete"
)

const maxGraderMessageBytes = 1000

// EvidenceRef points at the full harness outcome for one evaluation.
type EvidenceRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Spend reports measured usage or marks it unavailable. Unavailable is never
// rendered as zero.
type Spend struct {
	Status          string   `json:"status"`
	InputTokens     int      `json:"input_tokens,omitempty"`
	OutputTokens    int      `json:"output_tokens,omitempty"`
	PremiumRequests float64  `json:"premium_requests,omitempty"`
	AICredits       *float64 `json:"ai_credits,omitempty"`
}

// Spend statuses.
const (
	SpendMeasured    = "measured"
	SpendUnavailable = "unavailable"
	SpendPartial     = "partial"
)

// Evaluation is one harness run of one candidate on one split.
type Evaluation struct {
	Index       int            `json:"index"`
	CandidateID string         `json:"candidate_id"`
	Split       string         `json:"split"`
	State       string         `json:"state"`
	Score       *float64       `json:"score"`
	PassRate    *float64       `json:"pass_rate"`
	Tasks       []TaskFeedback `json:"tasks"`
	Evidence    *EvidenceRef   `json:"evidence"`
	Spend       Spend          `json:"spend"`
	DurationMs  int64          `json:"duration_ms"`
	Error       string         `json:"error,omitempty"`
}

// EvaluateRequest asks the evaluator to score one candidate on one split.
type EvaluateRequest struct {
	Index       int
	CandidateID string
	Files       []FileContent
	Split       Split
}

// Evaluator runs candidates through the harness. Failures are reported in
// Evaluation.State; err is reserved for programming errors.
type Evaluator interface {
	Evaluate(ctx context.Context, req EvaluateRequest) Evaluation
}

// EngineFactory builds a fresh engine for one evaluation.
type EngineFactory func(cfg models.Config) (execution.AgentEngine, error)

// HarnessEvaluator evaluates candidates with the unmodified orchestration
// runner: a fresh spec load, fresh engine, fresh materialized candidate and an
// exact task-ID filter per evaluation. Caching is never enabled.
type HarnessEvaluator struct {
	Pin           *SuitePin
	Target        Target
	EngineFactory EngineFactory
	OutputDir     string
	ExcludeDirs   []string
	WazaVersion   string
	Verbose       bool
	// ShutdownGrace and ShutdownTimeout bound engine shutdown; zero values
	// use the execution package defaults.
	ShutdownGrace   time.Duration
	ShutdownTimeout time.Duration
}

var errOptimizerDeadline = errors.New("optimizer deadline exceeded")

func contextState(ctx context.Context) string {
	if ctx.Err() == nil {
		return ""
	}
	if errors.Is(context.Cause(ctx), errOptimizerDeadline) {
		return CategoryTimeout
	}
	return CategoryCancelled
}

func (h *HarnessEvaluator) Evaluate(ctx context.Context, req EvaluateRequest) Evaluation {
	start := time.Now()
	ev := Evaluation{Index: req.Index, CandidateID: req.CandidateID, Split: req.Split.Name, Spend: Spend{Status: SpendUnavailable}}
	fail := func(state string, err error) Evaluation {
		if cs := contextState(ctx); cs != "" {
			state = cs
		}
		ev.State = state
		if err != nil {
			ev.Error = err.Error()
		}
		ev.DurationMs = time.Since(start).Milliseconds()
		return ev
	}
	if cs := contextState(ctx); cs != "" {
		return fail(cs, context.Cause(ctx))
	}
	if err := h.Pin.Verify(h.ExcludeDirs); err != nil {
		return fail(categoryOrDefault(err, CategoryInfrastructureFailure), err)
	}
	outcome, err := h.run(ctx, req)
	if err != nil {
		return fail(categoryOrDefault(err, CategoryInfrastructureFailure), err)
	}
	if err := h.Pin.Verify(h.ExcludeDirs); err != nil {
		return fail(categoryOrDefault(err, CategoryInfrastructureFailure), err)
	}
	if outcome == nil {
		return fail(CategoryMissingEvidence, errors.New("harness returned no outcome"))
	}
	got := make([]string, 0, len(outcome.TestOutcomes))
	for _, to := range outcome.TestOutcomes {
		got = append(got, to.TestID)
	}
	sort.Strings(got)
	if !slices.Equal(got, req.Split.TestIDs) {
		return fail(CategoryMissingEvidence, fmt.Errorf("harness returned tasks %v, want %v", got, req.Split.TestIDs))
	}
	ref, err := h.writeEvidence(req, outcome)
	if err != nil {
		return fail(CategoryMissingEvidence, err)
	}
	ev.Evidence = ref
	ev.Tasks = taskFeedback(outcome)
	if u := outcome.Digest.Usage; u != nil {
		ev.Spend = Spend{Status: SpendMeasured, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, PremiumRequests: u.PremiumRequests, AICredits: u.AICredits}
	}
	d := outcome.Digest
	if d.Errors+d.Skipped > 0 || d.TotalTests != len(req.Split.TestIDs) {
		return fail(StateIncomplete, fmt.Errorf("%d errored and %d skipped of %d tasks", d.Errors, d.Skipped, d.TotalTests))
	}
	if n := graderEvaluationErrors(outcome); n > 0 {
		return fail(StateIncomplete, fmt.Errorf("%d grader results recorded evaluation errors", n))
	}
	score := d.AggregateScore
	pass := float64(d.Succeeded) / float64(d.TotalTests)
	ev.Score, ev.PassRate = &score, &pass
	ev.State = StateScored
	ev.DurationMs = time.Since(start).Milliseconds()
	return ev
}

func categoryOrDefault(err error, def string) string {
	if c := CategoryOf(err); c != "" {
		return c
	}
	return def
}

func (h *HarnessEvaluator) run(ctx context.Context, req EvaluateRequest) (*models.EvaluationOutcome, error) {
	spec, err := models.LoadEvalSpecOffline(h.Pin.SpecPath)
	if err != nil {
		return nil, wrapError(CategorySuiteChanged, err, "reloading suite")
	}
	spec.Config.ModelID = h.Pin.Config.Model
	if got := (ConfigPin{spec.Config.EngineType, spec.Config.ModelID, spec.Config.ReasoningEffort, spec.Config.TrialsPerTask, spec.Config.TimeoutSec}); got != h.Pin.Config {
		return nil, newError(CategorySuiteChanged, "suite config changed during optimization")
	}
	workdir, err := os.MkdirTemp("", "waza-optimizer-candidate-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	fixtureDir := h.Pin.ContextDir
	switch h.Target {
	case TargetSkillDirectory:
		skillDir := filepath.Join(workdir, "skill")
		if err := os.Mkdir(skillDir, 0o755); err != nil {
			return nil, err
		}
		if err := materialize(skillDir, req.Files); err != nil {
			return nil, err
		}
		spec.Config.SkillPaths = []string{skillDir}
		if len(spec.Config.FilteredSkillPaths()) != 1 {
			return nil, newError(CategoryInvalidSuite, "the suite's disabled_skills would remove the candidate skill directory")
		}
		if err := admitNoAmbientSkill(spec.SkillName); err != nil {
			return nil, err
		}
	case TargetContextOverlay:
		fixtureDir = filepath.Join(workdir, "context")
		if err := os.Mkdir(fixtureDir, 0o755); err != nil {
			return nil, err
		}
		if err := copyTree(h.Pin.ContextDir, fixtureDir); err != nil {
			return nil, err
		}
		if err := materialize(fixtureDir, req.Files); err != nil {
			return nil, err
		}
	default:
		return nil, newError(CategoryInvalidConfig, "unknown target %q", h.Target)
	}
	if fixtureDir == "" {
		fixtureDir = filepath.Join(workdir, "empty-context")
		if err := os.Mkdir(fixtureDir, 0o755); err != nil {
			return nil, err
		}
	}
	cfg := config.NewEvalConfig(spec,
		config.WithSpecDir(filepath.Dir(h.Pin.SpecPath)),
		config.WithFixtureDir(fixtureDir),
		config.WithVerbose(h.Verbose),
	)
	engine, err := h.EngineFactory(spec.Config)
	if err != nil {
		return nil, wrapError(CategoryInfrastructureFailure, err, "creating engine")
	}
	runner := orchestration.NewEvalRunner(cfg, engine,
		orchestration.WithExactTestIDs(req.Split.TestIDs...),
		orchestration.WithWazaVersion(h.WazaVersion),
	)
	outcome, runErr := runner.RunBenchmark(ctx)
	shutdownErr := execution.ShutdownBounded(ctx, engine, h.shutdownGrace(), h.shutdownTimeout())
	if runErr != nil {
		return nil, wrapError(CategoryInfrastructureFailure, errors.Join(runErr, shutdownErr), "running harness")
	}
	if shutdownErr != nil {
		return nil, wrapError(CategoryInfrastructureFailure, shutdownErr, "shutting down engine")
	}
	// Shutdown can publish final provider usage; refresh it before scoring
	// spend or writing evidence.
	execution.UpdateOutcomeUsage(outcome, engine)
	return outcome, nil
}

func (h *HarnessEvaluator) shutdownGrace() time.Duration {
	if h.ShutdownGrace > 0 {
		return h.ShutdownGrace
	}
	return execution.DefaultShutdownGrace
}

func (h *HarnessEvaluator) shutdownTimeout() time.Duration {
	if h.ShutdownTimeout > 0 {
		return h.ShutdownTimeout
	}
	return execution.DefaultShutdownTimeout
}

func (h *HarnessEvaluator) writeEvidence(req EvaluateRequest, outcome *models.EvaluationOutcome) (*EvidenceRef, error) {
	data, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding evidence: %w", err)
	}
	rel := filepath.ToSlash(filepath.Join("evidence", req.Split.Name, fmt.Sprintf("%03d-%s.json", req.Index, shortID(req.CandidateID))))
	full := filepath.Join(h.OutputDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return nil, fmt.Errorf("creating evidence directory: %w", err)
	}
	if err := os.WriteFile(full, data, 0o600); err != nil {
		return nil, fmt.Errorf("writing evidence: %w", err)
	}
	return &EvidenceRef{Path: rel, SHA256: sha256Hex(data)}, nil
}

// graderEvaluationErrors counts final and checkpoint grader results that
// recorded structured evaluation errors (for example an inline-script
// exception) without failing the task with a Go error.
func graderEvaluationErrors(outcome *models.EvaluationOutcome) int {
	count := func(results map[string]models.GraderResults) int {
		n := 0
		for _, g := range results {
			if v, ok := g.Details["evaluation_errors"]; ok && !isEmptyValue(v) {
				n++
			}
		}
		return n
	}
	n := 0
	for _, to := range outcome.TestOutcomes {
		for _, run := range to.Runs {
			n += count(run.Validations)
			for _, cp := range run.Checkpoints {
				n += count(cp.Validations)
			}
		}
	}
	return n
}

func isEmptyValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.String:
		return rv.Len() == 0
	}
	return false
}

// taskFeedback summarizes outcomes as bounded, adapter-safe feedback.
func taskFeedback(outcome *models.EvaluationOutcome) []TaskFeedback {
	out := make([]TaskFeedback, 0, len(outcome.TestOutcomes))
	for _, to := range outcome.TestOutcomes {
		tf := TaskFeedback{TestID: to.TestID, Status: string(to.Status), FailedGraders: []GraderFeedback{}}
		if to.Stats != nil {
			tf.Score = to.Stats.AvgScore
		}
		seen := map[string]bool{}
		for _, run := range to.Runs {
			names := make([]string, 0, len(run.Validations))
			for name := range run.Validations {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				g := run.Validations[name]
				if g.Passed || seen[name] {
					continue
				}
				seen[name] = true
				tf.FailedGraders = append(tf.FailedGraders, GraderFeedback{Name: name, Message: truncateText(g.Feedback, maxGraderMessageBytes)})
			}
		}
		out = append(out, tf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TestID < out[j].TestID })
	return out
}
