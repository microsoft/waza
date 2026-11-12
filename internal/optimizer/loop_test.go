package optimizer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

// stubEvaluator scores a candidate by counting "+" in its files, keeping loop
// tests independent of the harness.
type stubEvaluator struct {
	mu    sync.Mutex
	calls []EvaluateRequest
	state map[string]string // split -> forced state
	hook  func(EvaluateRequest)
}

func (s *stubEvaluator) Evaluate(ctx context.Context, req EvaluateRequest) Evaluation {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	if s.hook != nil {
		s.hook(req)
	}
	ev := Evaluation{Split: req.Split.Name, Spend: Spend{Status: SpendUnavailable}}
	if cs := contextState(ctx); cs != "" {
		ev.State = cs
		return ev
	}
	if st := s.state[req.Split.Name]; st != "" {
		ev.State = st
		return ev
	}
	n := 0
	for _, f := range req.Files {
		n += strings.Count(f.Content, "+")
	}
	score := float64(n) / 10
	ev.State, ev.Score, ev.PassRate = StateScored, &score, &score
	for _, id := range req.Split.TestIDs {
		ev.Tasks = append(ev.Tasks, TaskFeedback{TestID: id, Status: "failed", Score: score, FailedGraders: []GraderFeedback{{Name: "g", Message: "need more +"}}})
	}
	return ev
}

func testPin() *SuitePin {
	return &SuitePin{
		SpecPath:      "/suite/eval.yaml",
		SpecDigest:    "sha256:" + strings.Repeat("a", 64),
		Config:        ConfigPin{Executor: "mock", Model: "mock"},
		Optimization:  Split{Name: SplitOptimization, Tags: []string{"opt"}, TestIDs: []string{"o1", "o2"}},
		Qualification: Split{Name: SplitQualification, Tags: []string{"qual"}, TestIDs: []string{"q1"}},
	}
}

func testConfig(t *testing.T, adapter Adapter, ev Evaluator) Config {
	t.Helper()
	return Config{
		Adapter:   adapter,
		Evaluator: ev,
		Pin:       testPin(),
		Target:    TargetContextOverlay,
		Seed:      []FileContent{{Path: "guide.md", Content: "seed\n"}},
		OutputDir: filepath.Join(t.TempDir(), "out"),
	}
}

func validateReportSchema(t *testing.T, outputDir string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(outputDir, "report.json"))
	require.NoError(t, err)
	validateAgainst(t, "optimizer-report-1.0.schema.json", schemas.OptimizerReportSchemaJSON, data)
}

func validateAgainst(t *testing.T, name, schemaJSON string, data []byte) {
	t.Helper()
	c := jsonschema.NewCompiler()
	for n, s := range map[string]string{
		"optimizer-capabilities-1.0.schema.json":     schemas.OptimizerCapabilitiesSchemaJSON,
		"optimizer-adapter-request-1.0.schema.json":  schemas.OptimizerAdapterRequestSchemaJSON,
		"optimizer-adapter-response-1.0.schema.json": schemas.OptimizerAdapterResponseSchemaJSON,
		"optimizer-report-1.0.schema.json":           schemas.OptimizerReportSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(s))
		require.NoError(t, err)
		require.NoError(t, c.AddResource("https://microsoft.github.io/waza/schemas/"+n, doc))
	}
	sch, err := c.Compile("https://microsoft.github.io/waza/schemas/" + name)
	require.NoError(t, err)
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
	require.NoError(t, err)
	require.NoError(t, sch.Validate(inst), string(data))
}

func TestRunAcceptsImprovedCandidateAndKeepsQualificationHidden(t *testing.T) {
	fake := NewFakeAdapter(
		FakeProposal{Files: map[string]string{"guide.md": "++\n"}, Rationale: "two"},
		FakeProposal{Files: map[string]string{"guide.md": "+\n"}},
		FakeProposal{Files: map[string]string{"guide.md": "++++\n"}},
	)
	ev := &stubEvaluator{}
	cfg := testConfig(t, fake, ev)
	r, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, OutcomeAccepted, r.Outcome, r.Failure)
	require.Equal(t, TerminationAdapterDone, r.Termination)
	require.Len(t, r.Candidates, 4)
	best := r.Candidates[3]
	require.Equal(t, best.ID, r.BestCandidateID)
	require.Equal(t, r.Candidates[1].ID, best.ParentID, "parent is the best incumbent, not the last proposal")
	require.Equal(t, "fake", best.Adapter)
	require.Equal(t, 0.4, *r.Qualification.CandidateScore)

	// Every evaluation records split and candidate; the qualification split
	// only appears after the adapter loop and never in adapter requests.
	for _, e := range r.Evaluations {
		require.NotEmpty(t, e.CandidateID)
		require.NotEmpty(t, e.Split)
	}
	require.Equal(t, SplitQualification, r.Evaluations[len(r.Evaluations)-1].Split)
	reqs := fake.Requests()
	require.Len(t, reqs, 4)
	for _, req := range reqs {
		data, err := json.Marshal(req)
		require.NoError(t, err)
		require.NotContains(t, string(data), "q1")
		require.NotContains(t, string(data), SplitQualification)
		require.NotContains(t, string(data), "two", "rationales are never fed back")
		validateAgainst(t, "optimizer-adapter-request-1.0.schema.json", "", data)
	}
	require.Equal(t, DefaultMaxEvaluations-ReservedQualificationEvaluations-1, reqs[0].Remaining.Evaluations)
	validateReportSchema(t, cfg.OutputDir)
	saved, err := os.ReadFile(filepath.Join(cfg.OutputDir, best.SavedPath, "guide.md"))
	require.NoError(t, err)
	require.Equal(t, "++++\n", string(saved))
}

func TestRunNoImprovementSkipsQualification(t *testing.T) {
	ev := &stubEvaluator{}
	cfg := testConfig(t, NewFakeAdapter(FakeProposal{Files: map[string]string{"guide.md": "+\n"}}), ev)
	cfg.MinImprovement = 0.1
	r, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, OutcomeNoImprovement, r.Outcome)
	require.False(t, r.Qualification.Performed)
	for _, c := range ev.calls {
		require.Equal(t, SplitOptimization, c.Split.Name)
	}
	_, err = os.Stat(filepath.Join(cfg.OutputDir, "evidence", SplitQualification))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRunRejectsHeldOutRegression(t *testing.T) {
	regress := &splitScoreEvaluator{stubEvaluator: &stubEvaluator{}}
	cfg := testConfig(t, NewFakeAdapter(FakeProposal{Files: map[string]string{"guide.md": "+\n"}}), regress)
	r, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, OutcomeRejected, r.Outcome)
	require.Equal(t, 0, r.ExitCode())
}

// splitScoreEvaluator inverts scores on the qualification split.
type splitScoreEvaluator struct{ *stubEvaluator }

func (s *splitScoreEvaluator) Evaluate(ctx context.Context, req EvaluateRequest) Evaluation {
	ev := s.stubEvaluator.Evaluate(ctx, req)
	if req.Split.Name == SplitQualification && ev.Score != nil {
		inv := 1 - *ev.Score
		ev.Score = &inv
	}
	return ev
}

func TestRunNotQualifiedWhenHeldOutIncomplete(t *testing.T) {
	ev := &stubEvaluator{state: map[string]string{SplitQualification: StateIncomplete}}
	cfg := testConfig(t, NewFakeAdapter(FakeProposal{Files: map[string]string{"guide.md": "+\n"}}), ev)
	r, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, OutcomeNotQualified, r.Outcome)
	require.Equal(t, 1, r.ExitCode())
}

func TestRunExplicitFailures(t *testing.T) {
	malformedKind := &ProposeResponse{Kind: "nope", ContractVersion: ContractVersion}
	wrongParent := &ProposeResponse{Kind: AdapterResponseKind, ContractVersion: ContractVersion,
		Candidate: &ProposedCandidate{ParentID: "sha256:" + strings.Repeat("0", 64), Files: []FileContent{{Path: "guide.md", Content: "+"}}}}
	empty := &ProposeResponse{Kind: AdapterResponseKind, ContractVersion: ContractVersion}
	tests := []struct {
		name     string
		proposal FakeProposal
		category string
	}{
		{"adapter error", FakeProposal{Error: "boom"}, CategoryOptimizerFailure},
		{"malformed kind", FakeProposal{Raw: malformedKind}, CategoryMalformedProposal},
		{"parent mismatch", FakeProposal{Raw: wrongParent}, CategoryMalformedProposal},
		{"no candidate and not done", FakeProposal{Raw: empty}, CategoryMalformedProposal},
		{"path traversal", FakeProposal{Files: map[string]string{"../escape.md": "+"}}, CategoryUnsafeCandidatePath},
		{"absolute path", FakeProposal{Files: map[string]string{"/etc/passwd": "+"}}, CategoryUnsafeCandidatePath},
		{"git dir", FakeProposal{Files: map[string]string{".git/config": "+"}}, CategoryUnsafeCandidatePath},
		{"oversized", FakeProposal{Files: map[string]string{"guide.md": strings.Repeat("+", MaxCandidateFileBytes+1)}}, CategoryMalformedProposal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t, NewFakeAdapter(tt.proposal), &stubEvaluator{})
			r, err := Run(context.Background(), cfg)
			require.NoError(t, err)
			require.Equal(t, OutcomeFailed, r.Outcome)
			require.Equal(t, tt.category, r.Failure.Category, r.Failure.Message)
			require.Equal(t, TerminationFailure, r.Termination)
			require.Len(t, r.Evaluations, 1, "only the seed was evaluated")
			validateReportSchema(t, cfg.OutputDir)
		})
	}
}

func TestRunPreflightFailures(t *testing.T) {
	t.Run("unsupported version", func(t *testing.T) {
		fake := NewFakeAdapter()
		fake.Info.ContractVersions = []string{"2.0"}
		cfg := testConfig(t, fake, &stubEvaluator{})
		r, err := Run(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, CategoryUnsupportedVersion, r.Failure.Category)
		require.Empty(t, r.Evaluations)
	})
	t.Run("incompatible capability", func(t *testing.T) {
		fake := NewFakeAdapter()
		fake.Info.RequiredCapabilities = append(fake.Info.RequiredCapabilities, "trace.capture")
		ev := &stubEvaluator{}
		r, err := Run(context.Background(), testConfig(t, fake, ev))
		require.NoError(t, err)
		require.Equal(t, CategoryIncompatible, r.Failure.Category)
		require.Empty(t, ev.calls)
	})
	t.Run("consent required", func(t *testing.T) {
		fake := NewFakeAdapter()
		fake.Info.PaidCalls = true
		r, err := Run(context.Background(), testConfig(t, fake, &stubEvaluator{}))
		require.NoError(t, err)
		require.Equal(t, CategoryConsentRequired, r.Failure.Category)
	})
	t.Run("budget too small", func(t *testing.T) {
		cfg := testConfig(t, NewFakeAdapter(), &stubEvaluator{})
		cfg.MaxEvaluations = 2
		r, err := Run(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, CategoryInvalidConfig, r.Failure.Category)
	})
	t.Run("non-empty output dir", func(t *testing.T) {
		cfg := testConfig(t, NewFakeAdapter(), &stubEvaluator{})
		require.NoError(t, os.MkdirAll(cfg.OutputDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(cfg.OutputDir, "x"), nil, 0o600))
		_, err := Run(context.Background(), cfg)
		require.Equal(t, CategoryInvalidConfig, CategoryOf(err))
	})
	t.Run("seed incomplete", func(t *testing.T) {
		ev := &stubEvaluator{state: map[string]string{SplitOptimization: StateIncomplete}}
		r, err := Run(context.Background(), testConfig(t, NewFakeAdapter(), ev))
		require.NoError(t, err)
		require.Equal(t, OutcomeFailed, r.Outcome)
		require.Equal(t, StateIncomplete, r.Failure.Category)
	})
}

func TestRunBudgetsAndDuplicates(t *testing.T) {
	t.Run("evaluation budget reserves qualification", func(t *testing.T) {
		props := []FakeProposal{}
		for i := 1; i <= 5; i++ {
			props = append(props, FakeProposal{Files: map[string]string{"guide.md": strings.Repeat("+", i)}})
		}
		ev := &stubEvaluator{}
		cfg := testConfig(t, NewFakeAdapter(props...), ev)
		cfg.MaxEvaluations = 4
		r, err := Run(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, TerminationEvaluationBudget, r.Termination)
		require.Equal(t, OutcomeAccepted, r.Outcome)
		require.Len(t, ev.calls, 4)
		require.Equal(t, 4, r.Budget.EvaluationsUsed)
	})
	t.Run("iteration budget and duplicate", func(t *testing.T) {
		fake := NewFakeAdapter(
			FakeProposal{Files: map[string]string{"guide.md": "seed\n"}},
			FakeProposal{Files: map[string]string{"guide.md": "+"}},
			FakeProposal{Files: map[string]string{"guide.md": "++"}},
		)
		ev := &stubEvaluator{}
		cfg := testConfig(t, fake, ev)
		cfg.MaxIterations = 2
		r, err := Run(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, TerminationIterationBudget, r.Termination)
		require.Equal(t, ProposalDuplicate, r.Proposals[0].State)
		require.Equal(t, 2, r.Budget.IterationsUsed)
		require.Len(t, fake.Requests(), 2)
		require.Equal(t, 1, fake.Requests()[1].Remaining.Iterations)
	})
}

func TestRunDeadlineAndCancel(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		cfg := testConfig(t, NewFakeAdapter(FakeProposal{Hang: true}), &stubEvaluator{})
		cfg.Deadline = 50 * time.Millisecond
		r, err := Run(context.Background(), cfg)
		require.NoError(t, err)
		require.Equal(t, OutcomeIncomplete, r.Outcome)
		require.Equal(t, CategoryTimeout, r.Failure.Category)
		require.Equal(t, TerminationTimeout, r.Termination)
		validateReportSchema(t, cfg.OutputDir)
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ev := &stubEvaluator{}
		ev.hook = func(EvaluateRequest) { cancel() }
		r, err := Run(ctx, testConfig(t, NewFakeAdapter(), ev))
		require.NoError(t, err)
		require.Equal(t, OutcomeIncomplete, r.Outcome)
		require.Equal(t, CategoryCancelled, r.Failure.Category)
	})
}

func TestRunSpendAccounting(t *testing.T) {
	credits := 1.5
	fake := NewFakeAdapter(FakeProposal{Files: map[string]string{"guide.md": "+"}, Usage: &Usage{InputTokens: 10, OutputTokens: 5, AICredits: &credits}})
	r, err := Run(context.Background(), testConfig(t, fake, &stubEvaluator{}))
	require.NoError(t, err)
	require.False(t, r.Spend.HardSpendCap)
	require.Equal(t, SpendUnavailable, r.Spend.Evaluations.Status)
	require.Equal(t, SpendPartial, r.Spend.Adapter.Status)
	require.Equal(t, 10, r.Spend.Adapter.InputTokens)
	require.Equal(t, SpendPartial, r.Spend.Total.Status)
	require.Equal(t, 1.5, *r.Spend.Total.AICredits, "partial totals sum only the measured parts")
}

func TestRunWithoutTaskResultsCapabilityHidesTaskDetail(t *testing.T) {
	fake := NewFakeAdapter(FakeProposal{Files: map[string]string{"guide.md": "+"}})
	fake.Info.OptionalCapabilities = []string{CapTargetContextOverlay}
	_, err := Run(context.Background(), testConfig(t, fake, &stubEvaluator{}))
	require.NoError(t, err)
	for _, req := range fake.Requests() {
		for _, fb := range req.History {
			require.Empty(t, fb.Tasks)
		}
	}
}

func TestErrorCategoryOf(t *testing.T) {
	err := wrapError(CategoryTimeout, errors.New("x"), "y")
	require.Equal(t, CategoryTimeout, CategoryOf(err))
	require.Empty(t, CategoryOf(errors.New("plain")))
}

func TestRunFailsWhenCandidateCannotBeSaved(t *testing.T) {
	ev := &stubEvaluator{}
	cfg := testConfig(t, NewFakeAdapter(FakeProposal{Files: map[string]string{"guide.md": "+\n"}}), ev)
	ev.hook = func(req EvaluateRequest) {
		// Replace the candidates directory with a file after the seed is saved.
		dir := filepath.Join(cfg.OutputDir, "candidates")
		require.NoError(t, os.RemoveAll(dir))
		require.NoError(t, os.WriteFile(dir, nil, 0o600))
	}
	report, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, OutcomeFailed, report.Outcome)
	require.Equal(t, CategoryInfrastructureFailure, report.Failure.Category)
	require.Len(t, report.Evaluations, 1, "an unsaved candidate must not be evaluated")
	require.Equal(t, ProposalFailed, report.Proposals[0].State)
}
