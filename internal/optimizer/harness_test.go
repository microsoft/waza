package optimizer

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

const exampleDir = "../../examples/optimizer"

func copySuite(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "suite")
	require.NoError(t, os.CopyFS(dst, os.DirFS(exampleDir)))
	return dst
}

func mockFactory(cfg models.Config) (execution.AgentEngine, error) {
	return execution.NewMockEngine(cfg.ModelID), nil
}

func harnessConfig(t *testing.T, suite string, adapter Adapter) Config {
	t.Helper()
	pin, err := PinSuite(SuiteOptions{
		SpecPath:          filepath.Join(suite, "eval.yaml"),
		Target:            TargetContextOverlay,
		OptimizationTags:  []string{"optimization"},
		QualificationTags: []string{"qualification"},
		WazaVersion:       "test",
	})
	require.NoError(t, err)
	seed, err := LoadSeed(filepath.Join(suite, "seed"), TargetContextOverlay)
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "out")
	return Config{
		Adapter: adapter, Pin: pin, Target: TargetContextOverlay, Seed: seed, OutputDir: out,
		Evaluator: &HarnessEvaluator{Pin: pin, Target: TargetContextOverlay, EngineFactory: mockFactory, OutputDir: out, WazaVersion: "test"},
	}
}

func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		tree[p] = string(data)
		return err
	}))
	return tree
}

func TestHarnessEndToEndAccepted(t *testing.T) {
	suite := copySuite(t)
	before := readTree(t, suite)
	fake, err := LoadFakeScript(filepath.Join(suite, "fake-script.json"))
	require.NoError(t, err)
	cfg := harnessConfig(t, suite, fake)

	r, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, OutcomeAccepted, r.Outcome, r.Failure)
	require.Equal(t, 0.5, *r.Qualification.SeedScore)
	require.Equal(t, 1.0, *r.Qualification.CandidateScore)
	require.Equal(t, before, readTree(t, suite), "the suite and fixtures are never modified")

	for _, ev := range r.Evaluations {
		require.NotNil(t, ev.Evidence, "every evaluation writes harness evidence")
		data, err := os.ReadFile(filepath.Join(cfg.OutputDir, ev.Evidence.Path))
		require.NoError(t, err)
		var outcome models.EvaluationOutcome
		require.NoError(t, json.Unmarshal(data, &outcome))
		split := cfg.Pin.Optimization
		if ev.Split == SplitQualification {
			split = cfg.Pin.Qualification
		}
		require.Len(t, outcome.TestOutcomes, len(split.TestIDs))
	}
	best := filepath.Join(cfg.OutputDir, "candidates", strings.TrimPrefix(r.BestCandidateID, "sha256:")[:16], "style-guide.md")
	require.FileExists(t, best)
	validateReportSchema(t, cfg.OutputDir)

	for _, req := range fake.Requests() {
		data, err := json.Marshal(req)
		require.NoError(t, err)
		for _, id := range cfg.Pin.Qualification.TestIDs {
			require.NotContains(t, string(data), id, "qualification tasks never reach the adapter")
		}
	}
}

// mutatingAdapter edits a task file before proposing, simulating a suite
// change during a run.
type mutatingAdapter struct {
	*FakeAdapter
	path string
}

func (m *mutatingAdapter) Propose(ctx context.Context, req ProposeRequest) (ProposeResponse, error) {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return ProposeResponse{}, err
	}
	if err := os.WriteFile(m.path, append(data, '\n'), 0o600); err != nil {
		return ProposeResponse{}, err
	}
	return m.FakeAdapter.Propose(ctx, req)
}

func TestHarnessDetectsSuiteChange(t *testing.T) {
	suite := copySuite(t)
	fake, err := LoadFakeScript(filepath.Join(suite, "fake-script.json"))
	require.NoError(t, err)
	cfg := harnessConfig(t, suite, &mutatingAdapter{FakeAdapter: fake, path: filepath.Join(suite, "tasks", "new-command.yaml")})
	r, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, OutcomeFailed, r.Outcome)
	require.Equal(t, CategorySuiteChanged, r.Failure.Category)
	require.Equal(t, 1, r.ExitCode())
}

func TestPinSuiteAdmission(t *testing.T) {
	cases := map[string]struct {
		edit     func(t *testing.T, suite string)
		opts     func(*SuiteOptions)
		target   Target
		contains string
	}{
		"hooks":        {edit: appendFile("eval.yaml", "hooks:\n  before_run:\n    - command: echo hi\n"), contains: "hooks"},
		"baseline":     {edit: appendFile("eval.yaml", "baseline: true\n"), contains: "baseline"},
		"overlap":      {opts: func(o *SuiteOptions) { o.QualificationTags = []string{"qualification", "optimization"} }, contains: "both"},
		"empty split":  {opts: func(o *SuiteOptions) { o.QualificationTags = []string{"nope"} }, contains: "no active tasks"},
		"missing tags": {opts: func(o *SuiteOptions) { o.OptimizationTags = nil }, contains: "required"},
		"duplicate id": {edit: replaceInFile("tasks/bug-fix.yaml", "id: bug-fix", "id: deprecation"), contains: "duplicate"},
		"context dir":  {edit: appendFile("tasks/bug-fix.yaml", "context_dir: elsewhere\n"), contains: "context_dir"},
		"context fixture": {edit: replaceInFile("tasks/bug-fix.yaml", "inputs:\n", "inputs:\n  context:\n    fixture: fixtures/style-guide.md\n"),
			contains: "inputs.context.fixture"},
		"eval update snapshots": {edit: replaceInFile("eval.yaml", "graders:\n", "graders:\n  - type: diff\n    name: snap\n    config:\n      update_snapshots: true\n      expected_files:\n        - path: out.md\n          snapshot: snap.md\n"),
			contains: "update_snapshots"},
		"task update snapshots": {edit: appendFile("tasks/bug-fix.yaml", "  - name: snap\n    type: diff\n    config:\n      update_snapshots: true\n      expected_files:\n        - path: out.md\n          snapshot: snap.md\n"),
			contains: "update_snapshots"},
		"checkpoint update snapshots": {edit: appendFile("tasks/bug-fix.yaml", "checkpoints:\n  - after_turn: 1\n    graders:\n      - name: snap\n        type: diff\n        config:\n          update_snapshots: true\n          expected_files:\n            - path: out.md\n              snapshot: snap.md\n"),
			contains: "update_snapshots"},
		"relative schema file": {edit: replaceInFile("eval.yaml", "graders:\n", "graders:\n  - type: json_schema\n    name: shape\n    config:\n      schema_file: judge.json\n"),
			contains: "schema_file"},
		"relative rubric file": {edit: appendFile("tasks/bug-fix.yaml", "  - name: judge\n    type: prompt\n    config:\n      prompt: Grade it.\n      rubric: rubrics/custom.md\n"),
			contains: "rubric"},
		"relative trigger skill": {edit: appendFile("tasks/bug-fix.yaml", "  - name: trigger\n    type: trigger\n    config:\n      skill_path: skill/SKILL.md\n      mode: positive\n"),
			contains: "skill_path"},
		"external task file": {edit: func(t *testing.T, suite string) {
			shared := filepath.Join(filepath.Dir(suite), "shared")
			require.NoError(t, os.MkdirAll(shared, 0o755))
			data, err := os.ReadFile(filepath.Join(suite, "tasks", "bug-fix.yaml"))
			require.NoError(t, err)
			require.NoError(t, os.Remove(filepath.Join(suite, "tasks", "bug-fix.yaml")))
			require.NoError(t, os.WriteFile(filepath.Join(shared, "bug-fix.yaml"), data, 0o600))
			replaceInFile("eval.yaml", "  - \"tasks/*.yaml\"\n", "  - \"tasks/*.yaml\"\n  - \"../shared/*.yaml\"\n")(t, suite)
		}, contains: "outside the suite directory"},
		"parent path": {edit: replaceInFile("tasks/bug-fix.yaml", "    - path: style-guide.md", "    - path: ../outside.md"), contains: "outside the pinned"},
		"symlink": {edit: func(t *testing.T, suite string) {
			require.NoError(t, os.Symlink(filepath.Join(suite, "eval.yaml"), filepath.Join(suite, "link.yaml")))
		}, contains: "symlink"},
		"disabled skills": {edit: replaceInFile("eval.yaml", "  model: mock\n", "  model: mock\n  disabled_skills: [\"*\"]\n"),
			target: TargetSkillDirectory, contains: "disabled_skills"},
		"disabled candidate dir": {edit: replaceInFile("eval.yaml", "  model: mock\n", "  model: mock\n  disabled_skills: [\"skill\"]\n"),
			target: TargetSkillDirectory, contains: "disabled_skills"},
		"ambient skill": {edit: func(t *testing.T, _ string) {
			cwd := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(cwd, "SKILL.md"), []byte("---\nname: release-notes-writer\n---\nAmbient.\n"), 0o600))
			t.Chdir(cwd)
		}, target: TargetSkillDirectory, contains: "discoverable from the working directory"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			suite := copySuite(t)
			if tc.edit != nil {
				tc.edit(t, suite)
			}
			target := tc.target
			if target == "" {
				target = TargetContextOverlay
			}
			opts := SuiteOptions{SpecPath: filepath.Join(suite, "eval.yaml"), Target: target,
				OptimizationTags: []string{"optimization"}, QualificationTags: []string{"qualification"}}
			if tc.opts != nil {
				tc.opts(&opts)
			}
			_, err := PinSuite(opts)
			require.ErrorContains(t, err, tc.contains)
		})
	}
}

func appendFile(rel, text string) func(*testing.T, string) {
	return func(t *testing.T, suite string) {
		p := filepath.Join(suite, rel)
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p, append(data, []byte(text)...), 0o600))
	}
}

func replaceInFile(rel, old, repl string) func(*testing.T, string) {
	return func(t *testing.T, suite string) {
		p := filepath.Join(suite, rel)
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		// Windows checkouts may use CRLF line endings.
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		require.Contains(t, text, old, "edit anchor missing from %s", rel)
		require.NoError(t, os.WriteFile(p, []byte(strings.Replace(text, old, repl, 1)), 0o600))
	}
}

// skillEchoEngine answers with the candidate SKILL.md it was given, so a
// skill-directory candidate changes the score.
type skillEchoEngine struct{ *execution.MockEngine }

func (e skillEchoEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	resp, err := e.MockEngine.Execute(ctx, req)
	if err != nil || len(req.SkillPaths) == 0 {
		return resp, err
	}
	data, readErr := os.ReadFile(filepath.Join(req.SkillPaths[0], "SKILL.md"))
	if readErr != nil {
		return nil, readErr
	}
	resp.FinalOutput = string(data)
	return resp, nil
}

func skillTargetSuite(t *testing.T) (*SuitePin, []FileContent) {
	t.Helper()
	suite := copySuite(t)
	require.NoError(t, os.RemoveAll(filepath.Join(suite, "fixtures")))
	for _, f := range []string{"breaking-change", "bug-fix", "deprecation", "new-command", "security-fix"} {
		replaceInFile("tasks/"+f+".yaml", "  files:\n    - path: style-guide.md\n", "")(t, suite)
	}
	seedDir := filepath.Join(t.TempDir(), "skill")
	require.NoError(t, os.MkdirAll(seedDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, "SKILL.md"), []byte("---\nname: release-notes-writer\n---\nWrite release notes.\n"), 0o600))

	pin, err := PinSuite(SuiteOptions{SpecPath: filepath.Join(suite, "eval.yaml"), Target: TargetSkillDirectory,
		OptimizationTags: []string{"optimization"}, QualificationTags: []string{"qualification"}})
	require.NoError(t, err)
	seed, err := LoadSeed(seedDir, TargetSkillDirectory)
	require.NoError(t, err)
	return pin, seed
}

func TestHarnessSkillDirectoryTarget(t *testing.T) {
	pin, seed := skillTargetSuite(t)
	out := filepath.Join(t.TempDir(), "out")
	factory := func(cfg models.Config) (execution.AgentEngine, error) {
		return skillEchoEngine{execution.NewMockEngine(cfg.ModelID)}, nil
	}
	improved := "---\nname: release-notes-writer\n---\nWrite release notes. Call out every breaking change with migration steps, tell users to upgrade for security fixes, and show an example.\n"
	r, err := Run(context.Background(), Config{
		Adapter: NewFakeAdapter(FakeProposal{Files: map[string]string{"SKILL.md": improved}}),
		Pin:     pin, Target: TargetSkillDirectory, Seed: seed, OutputDir: out,
		Evaluator: &HarnessEvaluator{Pin: pin, Target: TargetSkillDirectory, EngineFactory: factory, OutputDir: out},
	})
	require.NoError(t, err)
	require.Equal(t, OutcomeAccepted, r.Outcome, r.Failure)
}

func TestHarnessSkillCandidateMustKeepSkillName(t *testing.T) {
	pin, seed := skillTargetSuite(t)
	out := filepath.Join(t.TempDir(), "out")
	factory := func(cfg models.Config) (execution.AgentEngine, error) {
		return skillEchoEngine{execution.NewMockEngine(cfg.ModelID)}, nil
	}
	renamed := "---\nname: something-else\n---\nWrite release notes. Call out every breaking change with migration steps, tell users to upgrade for security fixes, and show an example.\n"
	r, err := Run(context.Background(), Config{
		Adapter: NewFakeAdapter(FakeProposal{Files: map[string]string{"SKILL.md": renamed}}),
		Pin:     pin, Target: TargetSkillDirectory, Seed: seed, OutputDir: out,
		Evaluator: &HarnessEvaluator{Pin: pin, Target: TargetSkillDirectory, EngineFactory: factory, OutputDir: out},
	})
	require.NoError(t, err)
	require.Equal(t, OutcomeNoImprovement, r.Outcome, r.Failure)
	require.Equal(t, StateIncomplete, r.Evaluations[1].State)
	require.Contains(t, r.Evaluations[1].Error, "does not resolve as skill")
}

// TestAdaptersAreInterchangeable runs the fake, external-command, and
// reflect adapters against the same harness evaluator.
func TestAdaptersAreInterchangeable(t *testing.T) {
	reflectOut := "<<<WAZA-FILE path=\"style-guide.md\">>>\nFlag each breaking change with migration steps; tell users to upgrade; show an example.\n<<<WAZA-END>>>"
	adapters := map[string]func(t *testing.T) (Adapter, bool){
		"fake": func(*testing.T) (Adapter, bool) {
			return NewFakeAdapter(FakeProposal{Files: map[string]string{"style-guide.md": "breaking change migration upgrade example\n"}}), false
		},
		"command": func(t *testing.T) (Adapter, bool) { return helperAdapter(t, "plus", 0), true },
		"reflect": func(*testing.T) (Adapter, bool) {
			return NewReflectAdapter((&scriptedEngine{}).withOutput(reflectOut), "m"), true
		},
	}
	for name, mk := range adapters {
		t.Run(name, func(t *testing.T) {
			adapter, paid := mk(t)
			cfg := harnessConfig(t, copySuite(t), adapter)
			cfg.AllowPaidCalls = paid
			cfg.AdapterPaid = paid
			cfg.MaxIterations = 2
			r, err := Run(context.Background(), cfg)
			require.NoError(t, err)
			require.Contains(t, []string{OutcomeAccepted, OutcomeNoImprovement}, r.Outcome, r.Failure)
			require.Equal(t, 0, r.ExitCode())
			require.NotEmpty(t, r.Proposals)
		})
	}
}

func TestGraderEvaluationErrorsCountsFinalAndCheckpointResults(t *testing.T) {
	broken := models.GraderResults{Details: map[string]any{"evaluation_errors": map[int]string{0: "boom"}}}
	clean := models.GraderResults{Details: map[string]any{"evaluation_errors": map[int]string{}}}
	outcome := &models.EvaluationOutcome{TestOutcomes: []models.TestOutcome{{Runs: []models.RunResult{{
		Validations: map[string]models.GraderResults{"final": broken, "ok": clean},
		Checkpoints: []models.CheckpointOutcome{{Validations: map[string]models.GraderResults{"cp": broken}}},
	}}}}}
	require.Equal(t, 2, graderEvaluationErrors(outcome))
}

// stalledShutdownEngine never finishes Shutdown on its own.
type stalledShutdownEngine struct{ *execution.MockEngine }

func (stalledShutdownEngine) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	select {} // ignores cancellation, like a wedged client
}

func TestHarnessEvaluatorBoundsStalledShutdown(t *testing.T) {
	suite := copySuite(t)
	cfg := harnessConfig(t, suite, NewFakeAdapter())
	h, ok := cfg.Evaluator.(*HarnessEvaluator)
	require.True(t, ok)
	h.EngineFactory = func(c models.Config) (execution.AgentEngine, error) {
		return stalledShutdownEngine{execution.NewMockEngine(c.ModelID)}, nil
	}
	h.ShutdownGrace, h.ShutdownTimeout = 10*time.Millisecond, 10*time.Millisecond
	require.NoError(t, os.MkdirAll(cfg.OutputDir, 0o755))
	start := time.Now()
	ev := h.Evaluate(context.Background(), EvaluateRequest{Index: 1, CandidateID: "sha256:" + strings.Repeat("a", 64), Files: cfg.Seed, Split: cfg.Pin.Optimization})
	require.Equal(t, CategoryInfrastructureFailure, ev.State, ev.Error)
	require.Contains(t, ev.Error, "shutting down engine")
	require.NotNil(t, ev.Evidence, "the completed outcome is kept as evidence")
	require.Nil(t, ev.Score)
	require.Less(t, time.Since(start), 20*time.Second)
}

// TestHarnessAmbientSkillAfterPinIsSchemaValid covers an evaluation-time
// invalid_suite failure and checks the report still matches its schema.
func TestHarnessAmbientSkillAfterPinIsSchemaValid(t *testing.T) {
	pin, seed := skillTargetSuite(t)
	cwd := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cwd, "SKILL.md"), []byte("---\nname: release-notes-writer\n---\nAmbient.\n"), 0o600))
	t.Chdir(cwd)
	out := filepath.Join(t.TempDir(), "out")
	r, err := Run(context.Background(), Config{
		Adapter: NewFakeAdapter(), Pin: pin, Target: TargetSkillDirectory, Seed: seed, OutputDir: out,
		Evaluator: &HarnessEvaluator{Pin: pin, Target: TargetSkillDirectory, OutputDir: out,
			EngineFactory: func(cfg models.Config) (execution.AgentEngine, error) {
				return execution.NewMockEngine(cfg.ModelID), nil
			}},
	})
	require.NoError(t, err)
	require.Equal(t, OutcomeFailed, r.Outcome)
	require.Equal(t, CategoryInvalidSuite, r.Failure.Category)
	require.Equal(t, CategoryInvalidSuite, r.Evaluations[0].State)
	validateReportSchema(t, out)
}

// TestPinSuiteRejectsIndependentJudgeUnderMock pins the invariant paid-call
// consent relies on: evidence_context prompt graders call a separate Copilot
// judge, so a suite can only use them with the copilot-sdk executor, which
// already requires --allow-paid-calls.
func TestPinSuiteRejectsIndependentJudgeUnderMock(t *testing.T) {
	suite := copySuite(t)
	appendFile("tasks/bug-fix.yaml", "  - name: judge\n    type: prompt\n    config:\n      prompt: Grade it.\n      evidence_context:\n        files: [notes.md]\n")(t, suite)
	_, err := PinSuite(SuiteOptions{SpecPath: filepath.Join(suite, "eval.yaml"), Target: TargetContextOverlay,
		OptimizationTags: []string{"optimization"}, QualificationTags: []string{"qualification"}})
	require.ErrorContains(t, err, "evidence_context requires executor copilot-sdk")
	require.Equal(t, CategoryInvalidSuite, CategoryOf(err))
}
