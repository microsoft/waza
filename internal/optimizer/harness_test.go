package optimizer

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		contains string
	}{
		"hooks":        {edit: appendFile("eval.yaml", "hooks:\n  before_run:\n    - command: echo hi\n"), contains: "hooks"},
		"baseline":     {edit: appendFile("eval.yaml", "baseline: true\n"), contains: "baseline"},
		"overlap":      {opts: func(o *SuiteOptions) { o.QualificationTags = []string{"qualification", "optimization"} }, contains: "both"},
		"empty split":  {opts: func(o *SuiteOptions) { o.QualificationTags = []string{"nope"} }, contains: "no active tasks"},
		"missing tags": {opts: func(o *SuiteOptions) { o.OptimizationTags = nil }, contains: "required"},
		"duplicate id": {edit: replaceInFile("tasks/bug-fix.yaml", "id: bug-fix", "id: deprecation"), contains: "duplicate"},
		"context dir":  {edit: appendFile("tasks/bug-fix.yaml", "context_dir: elsewhere\n"), contains: "context_dir"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			suite := copySuite(t)
			if tc.edit != nil {
				tc.edit(t, suite)
			}
			opts := SuiteOptions{SpecPath: filepath.Join(suite, "eval.yaml"), Target: TargetContextOverlay,
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
		require.NoError(t, os.WriteFile(p, []byte(strings.Replace(string(data), old, repl, 1)), 0o600))
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

func TestHarnessSkillDirectoryTarget(t *testing.T) {
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
