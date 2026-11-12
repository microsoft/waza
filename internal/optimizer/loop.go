package optimizer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Run outcomes.
const (
	OutcomeAccepted      = "accepted"
	OutcomeNoImprovement = "no_improvement"
	OutcomeRejected      = "rejected"
	OutcomeNotQualified  = "not_qualified"
	OutcomeFailed        = "failed"
	OutcomeIncomplete    = "incomplete"
)

// Termination reasons for the proposal loop.
const (
	TerminationAdapterDone      = "adapter_done"
	TerminationIterationBudget  = "iteration_budget"
	TerminationEvaluationBudget = "evaluation_budget"
	TerminationFailure          = "failure"
	TerminationCancelled        = "cancelled" //nolint:misspell // Stable external spelling.
	TerminationTimeout          = "timeout"
)

// Proposal states.
const (
	ProposalEvaluated = "evaluated"
	ProposalDuplicate = "duplicate"
	ProposalDone      = "done"
	ProposalRejected  = "rejected"
	ProposalFailed    = "failed"
)

// ReservedQualificationEvaluations are held back from the adapter budget to
// evaluate the seed and the best candidate on the held-out split.
const ReservedQualificationEvaluations = 2

// Defaults for unset budgets.
const (
	DefaultMaxIterations  = 5
	DefaultMaxEvaluations = 10
)

const maxRationaleBytes = 4000

// Config configures one optimization run.
type Config struct {
	Adapter   Adapter
	Evaluator Evaluator
	Pin       *SuitePin
	Target    Target
	Seed      []FileContent
	OutputDir string

	MaxIterations  int
	MaxEvaluations int
	// Deadline bounds the whole run, including qualification. Zero means none.
	Deadline       time.Duration
	MinImprovement float64

	// AllowPaidCalls is explicit consent. It is required whenever the
	// adapter declares paid calls, AdapterPaid is set, or ExecutorPaid is set.
	AllowPaidCalls bool
	AdapterPaid    bool
	ExecutorPaid   bool

	Now func() time.Time
}

// Failure explains a failed or incomplete run.
type Failure struct {
	Category string `json:"category"`
	Message  string `json:"message"`
}

// Budget records configured limits and their use.
type Budget struct {
	MaxIterations                    int   `json:"max_iterations"`
	MaxEvaluations                   int   `json:"max_evaluations"`
	ReservedQualificationEvaluations int   `json:"reserved_qualification_evaluations"`
	DeadlineMs                       int64 `json:"deadline_ms"`
	IterationsUsed                   int   `json:"iterations_used"`
	EvaluationsUsed                  int   `json:"evaluations_used"`
}

// Proposal records one adapter call.
type Proposal struct {
	Iteration   int    `json:"iteration"`
	State       string `json:"state"`
	CandidateID string `json:"candidate_id,omitempty"`
	Error       string `json:"error,omitempty"`
	Category    string `json:"category,omitempty"`
	Spend       Spend  `json:"spend"`
}

// Qualification records the held-out decision.
type Qualification struct {
	Performed       bool     `json:"performed"`
	SeedScore       *float64 `json:"seed_score"`
	CandidateScore  *float64 `json:"candidate_score"`
	SeedEvaluation  *int     `json:"seed_evaluation"`
	CandidateEval   *int     `json:"candidate_evaluation"`
	Decision        string   `json:"decision"`
	DecisionDetails string   `json:"details"`
}

// SpendSummary reports measured and unavailable usage. Waza never enforces a
// hard monetary cap; budgets bound calls and time only.
type SpendSummary struct {
	HardSpendCap bool  `json:"hard_spend_cap"`
	Evaluations  Spend `json:"evaluations"`
	Adapter      Spend `json:"adapter"`
	Total        Spend `json:"total"`
}

// Report is the versioned optimizer result. It is a separate artifact from
// run results; Lab does not render it.
type Report struct {
	Kind            string        `json:"kind"`
	SchemaVersion   string        `json:"schema_version"`
	ContractVersion string        `json:"contract_version"`
	Outcome         string        `json:"outcome"`
	Failure         *Failure      `json:"failure,omitempty"`
	Termination     string        `json:"termination"`
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      time.Time     `json:"finished_at"`
	Target          Target        `json:"target"`
	Adapter         *AdapterInfo  `json:"adapter,omitempty"`
	Negotiation     *Negotiation  `json:"negotiation,omitempty"`
	Suite           *SuitePin     `json:"suite"`
	SuiteDigest     string        `json:"suite_digest"`
	MinImprovement  float64       `json:"min_improvement"`
	Budget          Budget        `json:"budget"`
	PaidCalls       bool          `json:"paid_calls"`
	SeedCandidateID string        `json:"seed_candidate_id,omitempty"`
	BestCandidateID string        `json:"best_candidate_id,omitempty"`
	Candidates      []Candidate   `json:"candidates"`
	Proposals       []Proposal    `json:"proposals"`
	Evaluations     []Evaluation  `json:"evaluations"`
	Qualification   Qualification `json:"qualification"`
	Spend           SpendSummary  `json:"spend"`
}

// ExitCode maps an outcome to a process exit code.
func (r *Report) ExitCode() int {
	switch r.Outcome {
	case OutcomeAccepted, OutcomeNoImprovement, OutcomeRejected:
		return 0
	}
	return 1
}

type runState struct {
	cfg        Config
	report     *Report
	files      map[string][]FileContent
	scores     map[string]float64
	history    []Feedback
	taskDetail bool
	optEvals   int
}

// Run executes the bounded optimize-evaluate loop and writes report.json to
// cfg.OutputDir. The returned error is non-nil only when the report itself
// cannot be produced; run failures are recorded in the report.
func Run(parent context.Context, cfg Config) (*Report, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = DefaultMaxIterations
	}
	if cfg.MaxEvaluations == 0 {
		cfg.MaxEvaluations = DefaultMaxEvaluations
	}
	if err := prepareOutputDir(cfg.OutputDir); err != nil {
		return nil, err
	}
	r := &Report{
		Kind:            ReportKind,
		SchemaVersion:   ContractVersion,
		ContractVersion: ContractVersion,
		StartedAt:       cfg.Now().UTC(),
		Target:          cfg.Target,
		Suite:           cfg.Pin,
		MinImprovement:  cfg.MinImprovement,
		Budget: Budget{
			MaxIterations:                    cfg.MaxIterations,
			MaxEvaluations:                   cfg.MaxEvaluations,
			ReservedQualificationEvaluations: ReservedQualificationEvaluations,
			DeadlineMs:                       cfg.Deadline.Milliseconds(),
		},
		Candidates:  []Candidate{},
		Proposals:   []Proposal{},
		Evaluations: []Evaluation{},
	}
	if cfg.Pin != nil {
		r.SuiteDigest = cfg.Pin.Digest()
	}
	s := &runState{cfg: cfg, report: r, files: map[string][]FileContent{}, scores: map[string]float64{}}

	ctx := parent
	if cfg.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeoutCause(parent, cfg.Deadline, errOptimizerDeadline)
		defer cancel()
	}
	s.execute(ctx)
	r.Spend = s.spend()
	r.FinishedAt = cfg.Now().UTC()
	if err := writeJSON(filepath.Join(cfg.OutputDir, "report.json"), r); err != nil {
		return r, err
	}
	return r, nil
}

func prepareOutputDir(dir string) error {
	if dir == "" {
		return newError(CategoryInvalidConfig, "an output directory is required")
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return os.MkdirAll(dir, 0o755)
	case err != nil:
		return wrapError(CategoryInvalidConfig, err, "reading output directory")
	case len(entries) > 0:
		return newError(CategoryInvalidConfig, "output directory %s is not empty; choose a new --output-dir", dir)
	}
	return nil
}

func (s *runState) fail(category string, err error) {
	if s.report.Outcome == "" {
		s.report.Outcome = OutcomeFailed
		if category == CategoryCancelled || category == CategoryTimeout {
			s.report.Outcome = OutcomeIncomplete
		}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.report.Failure = &Failure{Category: category, Message: msg}
	if s.report.Termination == "" {
		switch category {
		case CategoryCancelled:
			s.report.Termination = TerminationCancelled
		case CategoryTimeout:
			s.report.Termination = TerminationTimeout
		default:
			s.report.Termination = TerminationFailure
		}
	}
}

func (s *runState) execute(ctx context.Context) {
	cfg := s.cfg
	switch {
	case cfg.Adapter == nil || cfg.Evaluator == nil || cfg.Pin == nil:
		s.fail(CategoryInvalidConfig, errors.New("adapter, evaluator and suite pin are required"))
		return
	case cfg.MaxIterations < 1:
		s.fail(CategoryInvalidConfig, errors.New("--max-iterations must be at least 1"))
		return
	case cfg.MaxEvaluations < ReservedQualificationEvaluations+1:
		s.fail(CategoryInvalidConfig, fmt.Errorf("--max-evaluations must be at least %d (seed plus %d reserved qualification evaluations)", ReservedQualificationEvaluations+1, ReservedQualificationEvaluations))
		return
	case cfg.MinImprovement < 0:
		s.fail(CategoryInvalidConfig, errors.New("--min-improvement must not be negative"))
		return
	}

	// Consent known before the adapter runs is checked before Describe so no
	// external process starts without it.
	s.report.PaidCalls = cfg.AdapterPaid || cfg.ExecutorPaid
	if s.report.PaidCalls && !cfg.AllowPaidCalls {
		s.fail(CategoryConsentRequired, errors.New("this run can make paid model calls; pass --allow-paid-calls to consent"))
		return
	}
	info, err := cfg.Adapter.Describe(ctx)
	if err != nil {
		s.fail(s.categorize(ctx, err, CategoryOptimizerFailure), err)
		return
	}
	s.report.Adapter = &info
	neg, err := Negotiate(info, cfg.Target)
	if err != nil {
		s.fail(categoryOrDefault(err, CategoryIncompatible), err)
		return
	}
	s.report.Negotiation = &neg
	s.taskDetail = slices.Contains(neg.SelectedCapabilities, CapFeedbackTaskResults)
	s.report.PaidCalls = s.report.PaidCalls || info.PaidCalls
	if s.report.PaidCalls && !cfg.AllowPaidCalls {
		s.fail(CategoryConsentRequired, errors.New("this run can make paid model calls; pass --allow-paid-calls to consent"))
		return
	}

	seedFiles, err := validateFiles(cfg.Seed, cfg.Target)
	if err != nil {
		s.fail(categoryOrDefault(err, CategoryInvalidConfig), fmt.Errorf("seed: %w", err))
		return
	}
	seed := s.addCandidate(seedFiles, "", OriginSeed, 0, "", info)
	s.report.SeedCandidateID = seed.ID
	seedEval := s.evaluate(ctx, seed.ID, cfg.Pin.Optimization)
	if seedEval.State != StateScored {
		s.fail(seedEval.State, fmt.Errorf("seed evaluation %s: %s", seedEval.State, seedEval.Error))
		return
	}
	s.recordFeedback(seedEval, "", 0)
	best := seed.ID

	for iter := 1; iter <= cfg.MaxIterations; iter++ {
		if cs := contextState(ctx); cs != "" {
			s.fail(cs, context.Cause(ctx))
			return
		}
		remainingEvals := cfg.MaxEvaluations - ReservedQualificationEvaluations - s.optEvals
		if remainingEvals <= 0 {
			s.report.Termination = TerminationEvaluationBudget
			break
		}
		s.report.Budget.IterationsUsed = iter
		next, stop := s.iterate(ctx, iter, best, remainingEvals, info)
		if s.report.Failure != nil {
			return
		}
		if next != "" {
			best = next
		}
		if stop {
			s.report.Termination = TerminationAdapterDone
			break
		}
	}

	if s.report.Termination == "" {
		s.report.Termination = TerminationIterationBudget
	}
	s.report.BestCandidateID = best
	if best == seed.ID || s.scores[best] <= s.scores[seed.ID]+cfg.MinImprovement {
		s.report.BestCandidateID = seed.ID
		s.report.Outcome = OutcomeNoImprovement
		s.report.Qualification.Decision = "skipped"
		s.report.Qualification.DecisionDetails = "no candidate beat the seed on the optimization split by more than min_improvement"
		return
	}
	s.qualify(ctx, seed.ID, best)
}

// iterate performs one adapter call. It returns a new incumbent ID (or "")
// and whether the adapter asked to stop.
func (s *runState) iterate(ctx context.Context, iter int, best string, remainingEvals int, info AdapterInfo) (string, bool) {
	cfg := s.cfg
	req := ProposeRequest{
		Kind:            AdapterRequestKind,
		ContractVersion: ContractVersion,
		Operation:       OperationPropose,
		Iteration:       iter,
		Target:          cfg.Target,
		Parent:          ParentCandidate{ID: best, Score: floatPtr(s.scores[best]), Files: slices.Clone(s.files[best])},
		History:         s.historyCopy(),
		Remaining:       Remaining{Iterations: cfg.MaxIterations - iter + 1, Evaluations: remainingEvals},
	}
	if dl, ok := ctx.Deadline(); ok {
		req.Remaining.DeadlineUnixMs = dl.UnixMilli()
	}
	resp, err := cfg.Adapter.Propose(ctx, req)
	prop := Proposal{Iteration: iter, Spend: usageSpend(resp.Usage)}
	record := func(state, category string, err error) {
		prop.State, prop.Category = state, category
		if err != nil {
			prop.Error = err.Error()
		}
		s.report.Proposals = append(s.report.Proposals, prop)
	}
	if err != nil {
		cat := s.categorize(ctx, err, CategoryOptimizerFailure)
		record(ProposalFailed, cat, err)
		s.fail(cat, fmt.Errorf("iteration %d: %w", iter, err))
		return "", false
	}
	if err := validateResponse(resp, best); err != nil {
		cat := categoryOrDefault(err, CategoryMalformedProposal)
		record(ProposalRejected, cat, err)
		s.fail(cat, fmt.Errorf("iteration %d: %w", iter, err))
		return "", false
	}
	if resp.Candidate == nil {
		record(ProposalDone, "", nil)
		return "", true
	}
	files, err := validateFiles(resp.Candidate.Files, cfg.Target)
	if err != nil {
		cat := categoryOrDefault(err, CategoryMalformedProposal)
		record(ProposalRejected, cat, err)
		s.fail(cat, fmt.Errorf("iteration %d: %w", iter, err))
		return "", false
	}
	id, _ := candidateIdentity(cfg.Target, files)
	prop.CandidateID = id
	if _, dup := s.files[id]; dup {
		record(ProposalDuplicate, "", nil)
		return "", resp.Done
	}
	s.addCandidate(files, best, OriginAdapter, iter, truncateText(resp.Candidate.Rationale, maxRationaleBytes), info)
	record(ProposalEvaluated, "", nil)
	ev := s.evaluate(ctx, id, cfg.Pin.Optimization)
	switch ev.State {
	case StateScored:
		s.recordFeedback(ev, best, iter)
		if s.scores[id] > s.scores[best] {
			return id, resp.Done
		}
	case StateIncomplete:
		s.recordFeedback(ev, best, iter)
	default:
		s.fail(ev.State, fmt.Errorf("iteration %d evaluation %s: %s", iter, ev.State, ev.Error))
	}
	return "", resp.Done
}

func validateResponse(resp ProposeResponse, parentID string) error {
	if resp.Kind != AdapterResponseKind {
		return newError(CategoryMalformedProposal, "response kind %q, want %q", resp.Kind, AdapterResponseKind)
	}
	if resp.ContractVersion != ContractVersion {
		return newError(CategoryUnsupportedVersion, "response contract_version %q, want %q", resp.ContractVersion, ContractVersion)
	}
	if resp.Candidate == nil {
		if !resp.Done {
			return newError(CategoryMalformedProposal, "response has neither a candidate nor done=true")
		}
		return nil
	}
	if resp.Candidate.ParentID != parentID {
		return newError(CategoryMalformedProposal, "candidate parent_id %q does not match the offered parent %q", resp.Candidate.ParentID, parentID)
	}
	return nil
}

func (s *runState) qualify(ctx context.Context, seedID, bestID string) {
	q := &s.report.Qualification
	q.Performed = true
	split := s.cfg.Pin.Qualification
	seedEval := s.evaluate(ctx, seedID, split)
	q.SeedEvaluation = intPtr(seedEval.Index)
	q.SeedScore = seedEval.Score
	if seedEval.State != StateScored {
		s.notQualified(seedEval)
		return
	}
	candEval := s.evaluate(ctx, bestID, split)
	q.CandidateEval = intPtr(candEval.Index)
	q.CandidateScore = candEval.Score
	if candEval.State != StateScored {
		s.notQualified(candEval)
		return
	}
	if *candEval.Score >= *seedEval.Score {
		s.report.Outcome = OutcomeAccepted
		q.Decision = OutcomeAccepted
		q.DecisionDetails = fmt.Sprintf("held-out score %.4f did not regress from seed %.4f", *candEval.Score, *seedEval.Score)
		return
	}
	s.report.Outcome = OutcomeRejected
	q.Decision = OutcomeRejected
	q.DecisionDetails = fmt.Sprintf("held-out score %.4f regressed from seed %.4f", *candEval.Score, *seedEval.Score)
}

func (s *runState) notQualified(ev Evaluation) {
	q := &s.report.Qualification
	q.Decision = OutcomeNotQualified
	q.DecisionDetails = fmt.Sprintf("qualification evaluation %d was %s", ev.Index, ev.State)
	switch ev.State {
	case StateIncomplete:
		s.report.Outcome = OutcomeNotQualified
	default:
		s.fail(ev.State, fmt.Errorf("qualification evaluation %s: %s", ev.State, ev.Error))
	}
}

func (s *runState) addCandidate(files []FileContent, parent, origin string, iter int, rationale string, info AdapterInfo) Candidate {
	id, digests := candidateIdentity(s.cfg.Target, files)
	c := Candidate{
		ID: id, ParentID: parent, Origin: origin, Iteration: iter,
		Target: s.cfg.Target, Files: digests, Rationale: rationale,
	}
	if origin == OriginAdapter {
		c.Adapter, c.AdapterVersion = info.Name, info.Version
	}
	rel := filepath.ToSlash(filepath.Join("candidates", strings.TrimPrefix(id, "sha256:")[:16]))
	dir := filepath.Join(s.cfg.OutputDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(dir, 0o755); err == nil && materialize(dir, files) == nil {
		c.SavedPath = rel
	}
	s.files[id] = files
	s.report.Candidates = append(s.report.Candidates, c)
	return c
}

func (s *runState) evaluate(ctx context.Context, id string, split Split) Evaluation {
	idx := len(s.report.Evaluations) + 1
	ev := s.cfg.Evaluator.Evaluate(ctx, EvaluateRequest{Index: idx, CandidateID: id, Files: s.files[id], Split: split})
	ev.Index, ev.CandidateID, ev.Split = idx, id, split.Name
	if ev.Tasks == nil {
		ev.Tasks = []TaskFeedback{}
	}
	s.report.Evaluations = append(s.report.Evaluations, ev)
	s.report.Budget.EvaluationsUsed = len(s.report.Evaluations)
	if split.Name == SplitOptimization {
		s.optEvals++
		if ev.State == StateScored && ev.Score != nil {
			s.scores[id] = *ev.Score
		}
	}
	return ev
}

// recordFeedback appends optimization-split feedback. Qualification
// evaluations never reach this path.
func (s *runState) recordFeedback(ev Evaluation, parent string, iter int) {
	if ev.Split != SplitOptimization {
		return
	}
	fb := Feedback{CandidateID: ev.CandidateID, ParentID: parent, Iteration: iter, Split: ev.Split, State: ev.State, Score: ev.Score, PassRate: ev.PassRate, Tasks: []TaskFeedback{}}
	if s.taskDetail {
		fb.Tasks = slices.Clone(ev.Tasks)
	}
	s.history = append(s.history, fb)
}

func (s *runState) historyCopy() []Feedback {
	out := make([]Feedback, len(s.history))
	for i, fb := range s.history {
		fb.Tasks = slices.Clone(fb.Tasks)
		out[i] = fb
	}
	return out
}

func (s *runState) categorize(ctx context.Context, err error, def string) string {
	if cs := contextState(ctx); cs != "" {
		return cs
	}
	return categoryOrDefault(err, def)
}

func (s *runState) spend() SpendSummary {
	evals := make([]Spend, 0, len(s.report.Evaluations))
	for _, ev := range s.report.Evaluations {
		evals = append(evals, ev.Spend)
	}
	adapter := make([]Spend, 0, len(s.report.Proposals))
	for _, p := range s.report.Proposals {
		adapter = append(adapter, p.Spend)
	}
	return SpendSummary{
		HardSpendCap: false,
		Evaluations:  sumSpend(evals),
		Adapter:      sumSpend(adapter),
		Total:        sumSpend(slices.Concat(evals, adapter)),
	}
}

func usageSpend(u *Usage) Spend {
	if u == nil {
		return Spend{Status: SpendUnavailable}
	}
	return Spend{Status: SpendMeasured, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, PremiumRequests: u.PremiumRequests, AICredits: u.AICredits}
}

// sumSpend totals measured parts. Any unavailable part makes the total
// partial; credits are summed only when every measured part reports them.
func sumSpend(parts []Spend) Spend {
	if len(parts) == 0 {
		return Spend{Status: SpendUnavailable}
	}
	total := Spend{}
	measured, credits, allCredits := 0, 0.0, true
	for _, p := range parts {
		if p.Status != SpendMeasured {
			continue
		}
		measured++
		total.InputTokens += p.InputTokens
		total.OutputTokens += p.OutputTokens
		total.PremiumRequests += p.PremiumRequests
		if p.AICredits == nil {
			allCredits = false
		} else {
			credits += *p.AICredits
		}
	}
	switch {
	case measured == 0:
		return Spend{Status: SpendUnavailable}
	case measured < len(parts):
		total.Status = SpendPartial
	default:
		total.Status = SpendMeasured
	}
	if allCredits {
		total.AICredits = &credits
	}
	return total
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func floatPtr(f float64) *float64 { return &f }
func intPtr(i int) *int           { return &i }
