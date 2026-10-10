package assurance

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/stretchr/testify/require"
)

const calibrationTestModel = "synthetic-model"
const calibrationTestRubric = "---\nname: synthetic\nversion: 1.0.0\nscale: pass-fail\ndescription: Synthetic test rubric.\n---\nOriginal reviewed rubric body.\n"

type calibrationHarness struct {
	t            *testing.T
	factories    int
	notices      int
	engines      []*calibrationTestEngine
	edit         func(*calibrationTestEngine, int)
	factoryError error
}

type calibrationTestEngine struct {
	t                        *testing.T
	observer                 func(execution.ExecutionDiagnostic) error
	initialized, executed    int
	shutdown                 int
	session                  string
	initError, shutdownError error
	shutdownDiagnostics      []execution.ExecutionDiagnostic
	initializeHook           func(context.Context) error
	shutdownHook             func(context.Context)
	usageHook                func()
	execute                  func(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error)
	usage                    *models.UsageStats
	accounting               execution.SessionUsageObservation
	events                   execution.SessionEventModelObservation
	finalized                bool
}

func (engine *calibrationTestEngine) Initialize(ctx context.Context) error {
	engine.initialized++
	require.NoError(engine.t, ctx.Err())
	if engine.initializeHook != nil {
		return engine.initializeHook(ctx)
	}
	return engine.initError
}

func (engine *calibrationTestEngine) Execute(ctx context.Context, request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	engine.executed++
	if engine.execute != nil {
		return engine.execute(ctx, request)
	}
	require.Equal(engine.t, calibrationTestModel, request.ModelID)
	require.Equal(engine.t, "low", request.ReasoningEffort)
	_, err := calibrationRequestIdentity(request)
	require.NoError(engine.t, err)
	require.Contains(engine.t, request.Message, "independent reviewed rubric override\n\n---\n\n## Task input\n stimulus \n")
	passed := strings.Contains(request.Message, "state: ready") && !strings.Contains(request.Message, "forbidden: yes")
	tool := 1
	if passed {
		tool = 0
	}
	_, err = request.Tools[tool].Handler(copilot.ToolInvocation{
		Arguments: map[string]any{"description": "actual native check", "reason": "deterministic verdict"},
	})
	require.NoError(engine.t, err)
	return &execution.ExecutionResponse{SessionID: engine.session, Success: true, ModelID: "response-is-not-model-evidence"}, nil
}

func (engine *calibrationTestEngine) Shutdown(ctx context.Context) error {
	engine.shutdown++
	require.NoError(engine.t, ctx.Err(), "shutdown must use an uncancelled bounded context")
	_, bounded := ctx.Deadline()
	require.True(engine.t, bounded)
	engine.finalized = true
	for _, diagnostic := range engine.shutdownDiagnostics {
		require.NoError(engine.t, engine.observer(diagnostic))
	}
	if engine.shutdownHook != nil {
		engine.shutdownHook(ctx)
	}
	return engine.shutdownError
}

func (engine *calibrationTestEngine) SessionUsage(id string) *models.UsageStats {
	require.True(engine.t, engine.finalized, "accounting must be read after shutdown")
	require.Equal(engine.t, engine.session, id)
	if engine.usageHook != nil {
		engine.usageHook()
	}
	return engine.usage
}

func (engine *calibrationTestEngine) SessionUsageObservation(id string) execution.SessionUsageObservation {
	require.True(engine.t, engine.finalized)
	require.Equal(engine.t, engine.session, id)
	return engine.accounting
}

func (engine *calibrationTestEngine) SessionEventModelObservation(id string) execution.SessionEventModelObservation {
	require.True(engine.t, engine.finalized)
	require.Equal(engine.t, engine.session, id)
	return engine.events
}

func (harness *calibrationHarness) factory(model string, observer func(execution.ExecutionDiagnostic) error) (CalibrationEngine, error) {
	harness.factories++
	require.Equal(harness.t, 1, harness.notices, "notice must precede construction")
	require.Equal(harness.t, calibrationTestModel, model)
	engine := &calibrationTestEngine{
		t: harness.t, observer: observer, session: "actual-observed-session",
		usage: &models.UsageStats{
			InputTokens: 3, OutputTokens: 2, AICredits: new(0.0),
			ModelMetrics: map[string]models.ModelUsage{model: {InputTokens: 3, OutputTokens: 2, AICredits: new(0.0)}},
		},
		accounting: execution.SessionUsageObservation{Source: "rpc", Complete: true, ModelAttributionComplete: true, Models: []string{model}},
		events:     execution.SessionEventModelObservation{Models: []string{model}, EventsObserved: 1, Complete: true},
	}
	if harness.edit != nil {
		harness.edit(engine, harness.factories)
	}
	harness.engines = append(harness.engines, engine)
	return engine, harness.factoryError
}

func calibrationFixture(t *testing.T) (CalibrateRequest, ReferenceDocument, string, *calibrationHarness) {
	t.Helper()
	verify, document, root := authoredVerificationFixture(t)
	verify.Calibrate = true
	verify.Spec.Graders[0].Kind = models.GraderKindPrompt
	verify.Spec.Graders[0].Parameters = models.PromptGraderParameters{
		Prompt: "independent reviewed rubric override", Rubric: "reviewed.md", Mode: models.PromptGraderModeIndependent,
	}
	verify.Spec.Config.JudgeReasoningEffort = "low"
	verify.Tasks["task"].Stimulus.Message = " stimulus "
	document.Calibration = &CalibrationPlan{Protocol: AgreementProtocol, Model: calibrationTestModel, MaxJudgeExecutions: 4}
	require.NoError(t, os.WriteFile(filepath.Join(root, "reviewed.md"), []byte(calibrationTestRubric), 0o600))
	for i := range document.Cases {
		document.Cases[i].Checks[0].RubricContentSHA256 = new(byteSHA256([]byte(calibrationTestRubric)))
	}
	calibrationRebind(t, &verify, &document)
	harness := &calibrationHarness{t: t}
	request := CalibrateRequest{VerifyRequest: verify, RubricRoot: verify.SnapshotRoot, EngineFactory: harness.factory}
	request.PaidCallNotice = func(plan CalibrationPlan, jobs int) error {
		harness.notices++
		require.Equal(t, *document.Calibration, plan)
		require.Equal(t, 4, jobs)
		require.Zero(t, harness.factories)
		return nil
	}
	return request, document, root, harness
}

func calibrationRebind(t *testing.T, request *VerifyRequest, document *ReferenceDocument) {
	t.Helper()
	config, err := evidence.JSONDigest(request.Spec)
	require.NoError(t, err)
	document.EvalResolvedConfigSHA256 = config.SHA256
	for ci := range document.Cases {
		candidate := &document.Cases[ci]
		task, err := evidence.JSONDigest(request.Tasks[candidate.TaskID])
		require.NoError(t, err)
		candidate.TaskDeclarationSHA256 = task.SHA256
		for i := range candidate.Checks {
			check := &candidate.Checks[i]
			declaration, err := preflight.ResolveGrader(check.Check, request.Tasks[candidate.TaskID], request.Spec)
			require.NoError(t, err)
			var native any = declaration.Config
			if declaration.Config == nil {
				native = declaration.Inline
			}
			digest, err := evidence.JSONDigest(native)
			require.NoError(t, err)
			check.GraderDeclarationSHA256 = digest.SHA256
		}
	}
	rebindSyntheticReview(t, request, *document)
}

func calibrationReplaceOutput(t *testing.T, document *ReferenceDocument, root string, index int, output *string) {
	t.Helper()
	candidate := &document.Cases[index]
	input, err := NewAuthoredOutput(document.ID, candidate.ID, candidate.TaskID, index+1, output)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, candidate.AuthoredInput.Path), input.Document(), 0o600))
	candidate.AuthoredInput.DocumentSHA256, candidate.ManifestSHA256 = byteSHA256(input.Document()), input.Manifest().SHA256
	if output != nil {
		reference, err := evidence.Reference(input.Manifest(), evidence.ReferenceInputArtifactID)
		require.NoError(t, err)
		reference.Pointer = "/output"
		for i := range candidate.Checks {
			candidate.Checks[i].Evidence = []models.EvidenceReference{reference}
		}
	}
}

func requireCalibrationReport(t *testing.T, report *Report) {
	t.Helper()
	require.NotNil(t, report)
	require.Equal(t, CalibratedReportVersion, report.SchemaVersion)
	require.Equal(t, CalibrationOperation, report.AssessmentMode)
	require.NotNil(t, report.Calibration.ExecutionLedger)
	_, err := ParseCalibratedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)
}

func calibrationPromptParameters(t *testing.T, config models.GraderConfig) models.PromptGraderParameters {
	t.Helper()
	parameters, ok := config.Parameters.(models.PromptGraderParameters)
	require.True(t, ok)
	return parameters
}

func TestCalibrateActualNativePositiveCorpus(t *testing.T) {
	request, document, _, harness := calibrationFixture(t)
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentPassed, report.State)
	require.Equal(t, AssessmentPassed, report.Calibration.State)
	require.Equal(t, 4, report.Calibration.Executions)
	require.Equal(t, 4, report.Calibration.Samples)
	require.Equal(t, 4, harness.factories)
	require.Equal(t, 1, harness.notices)
	require.Equal(t, 12, report.Calibration.Usage.InputTokens)
	require.Equal(t, 0.0, *report.Calibration.Credits)
	require.False(t, report.Calibration.BillableCallsBounded)
	require.False(t, report.Calibration.CostBounded)
	require.False(t, report.Calibration.ConfidenceSupported)
	for i, observation := range report.Requirements[0].Observations {
		require.Equal(t, *document.Cases[i].Checks[0].ExpectedPassed, observation.Result.Passed)
		require.Equal(t, models.GraderKindPrompt, observation.Result.Type)
		require.Equal(t, float64(1-i/2), observation.Result.Score)
		require.True(t, *observation.Agreement)
		require.NotEmpty(t, observation.Result.Feedback)
		require.Nil(t, observation.Result.Details)
		entry := (*report.Calibration.ExecutionLedger)[i]
		require.Equal(t, AssessmentPassed, entry.State, "usable reject executions pass independently of the grade verdict")
		require.True(t, entry.Initialized)
		require.True(t, entry.Executed)
		require.Equal(t, 1, entry.Callbacks)
		require.Equal(t, []string{calibrationTestModel}, entry.EventModels)
		require.Equal(t, []string{calibrationTestModel}, entry.AccountingModels)
		require.Equal(t, 1, harness.engines[i].shutdown)
	}
	require.NotContains(t, string(marshalReferenceTest(t, report)), "actual-observed-session")
}

func TestCalibratePreSpendRejections(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*CalibrateRequest, *ReferenceDocument, string)
	}{
		{"not selected", func(r *CalibrateRequest, _ *ReferenceDocument, _ string) { r.Calibrate = false }},
		{"notice missing", func(r *CalibrateRequest, _ *ReferenceDocument, _ string) { r.PaidCallNotice = nil }},
		{"factory missing", func(r *CalibrateRequest, _ *ReferenceDocument, _ string) { r.EngineFactory = nil }},
		{"plan missing", func(_ *CalibrateRequest, d *ReferenceDocument, _ string) { d.Calibration = nil }},
		{"insufficient budget", func(_ *CalibrateRequest, d *ReferenceDocument, _ string) { d.Calibration.MaxJudgeExecutions = 3 }},
		{"automatic model unsupported", func(_ *CalibrateRequest, d *ReferenceDocument, _ string) { d.Calibration.Model = "auto" }},
		{"domain minimum", func(_ *CalibrateRequest, d *ReferenceDocument, _ string) { d.Domains[0].MinimumCases = 5 }},
		{"missing coverage", func(_ *CalibrateRequest, d *ReferenceDocument, _ string) { d.Cases = d.Cases[:3] }},
		{"source mismatch", func(r *CalibrateRequest, _ *ReferenceDocument, _ string) { r.EvalSource = []byte("not reviewed") }},
		{"missing executable", func(_ *CalibrateRequest, d *ReferenceDocument, _ string) { d.ImplementationExecutableSHA256 = nil }},
		{"executable mismatch", func(_ *CalibrateRequest, d *ReferenceDocument, _ string) {
			d.ImplementationExecutableSHA256 = new(strings.Repeat("a", 64))
		}},
		{"rubric root missing", func(r *CalibrateRequest, _ *ReferenceDocument, _ string) { r.RubricRoot = nil }},
		{"rubric missing", func(_ *CalibrateRequest, _ *ReferenceDocument, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, "reviewed.md")))
		}},
		{"rubric mismatch", func(_ *CalibrateRequest, _ *ReferenceDocument, root string) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "reviewed.md"), []byte("not reviewed"), 0o600))
		}},
		{"rubric digest missing", func(_ *CalibrateRequest, d *ReferenceDocument, _ string) {
			d.Cases[0].Checks[0].RubricContentSHA256 = nil
		}},
		{"authored missing", func(_ *CalibrateRequest, d *ReferenceDocument, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, d.Cases[0].AuthoredInput.Path)))
		}},
		{"authored profile incomplete", func(_ *CalibrateRequest, d *ReferenceDocument, root string) {
			calibrationReplaceOutput(t, d, root, 0, nil)
		}},
		{"authored digest mismatch", func(_ *CalibrateRequest, d *ReferenceDocument, _ string) {
			d.Cases[0].AuthoredInput.DocumentSHA256 = strings.Repeat("b", 64)
		}},
		{"duplicate stimulus", func(_ *CalibrateRequest, d *ReferenceDocument, root string) {
			calibrationReplaceOutput(t, d, root, 1, new("state: ready\n"))
		}},
		{"explicit model override", func(r *CalibrateRequest, _ *ReferenceDocument, _ string) {
			p := calibrationPromptParameters(t, r.Spec.Graders[0])
			p.Model = "not-reviewed-model"
			r.Spec.Graders[0].Parameters = p
		}},
		{"spec judge override", func(r *CalibrateRequest, _ *ReferenceDocument, _ string) {
			r.Spec.Config.JudgeModel = "not-reviewed-model"
		}},
		{"pairwise unsupported", func(r *CalibrateRequest, _ *ReferenceDocument, _ string) {
			p := calibrationPromptParameters(t, r.Spec.Graders[0])
			p.Mode = models.PromptGraderModePairwise
			r.Spec.Graders[0].Parameters = p
		}},
		{"continued session unsupported", func(r *CalibrateRequest, _ *ReferenceDocument, _ string) {
			p := calibrationPromptParameters(t, r.Spec.Graders[0])
			p.ContinueSession = true
			r.Spec.Graders[0].Parameters = p
		}},
		{"inline-only unsupported", func(r *CalibrateRequest, _ *ReferenceDocument, _ string) {
			p := calibrationPromptParameters(t, r.Spec.Graders[0])
			p.Rubric = ""
			r.Spec.Graders[0].Parameters = p
		}},
		{"builtin unsupported", func(r *CalibrateRequest, _ *ReferenceDocument, _ string) {
			p := calibrationPromptParameters(t, r.Spec.Graders[0])
			p.Rubric = "groundedness"
			r.Spec.Graders[0].Parameters = p
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, document, root, harness := calibrationFixture(t)
			test.mutate(&request, &document, root)
			calibrationRebind(t, &request.VerifyRequest, &document)
			report, err := Calibrate(t.Context(), request)
			require.NoError(t, err)
			requireCalibrationReport(t, report)
			require.NotEqual(t, AssessmentPassed, report.State)
			require.Zero(t, harness.factories)
			require.Zero(t, harness.notices)
			require.Empty(t, *report.Calibration.ExecutionLedger)
			require.Nil(t, report.Calibration.Usage)
			require.Nil(t, report.Calibration.Credits)
		})
	}
}

func TestCalibrateReviewAndNativeBindingsBeforeNotice(t *testing.T) {
	for _, test := range []string{"revoked", "unaccepted", "decision missing", "stale digest", "task mismatch", "grader mismatch", "selected check uncovered"} {
		t.Run(test, func(t *testing.T) {
			request, document, _, harness := calibrationFixture(t)
			switch test {
			case "revoked":
				request.Review.Decision.State = ReviewRevoked
			case "unaccepted":
				request.Acceptance.AcceptCurrentDecision = false
			case "decision missing":
				request.Review.Decision = nil
			case "stale digest":
				request.Review.Decision.LabelsDigest[0]++
			case "task mismatch":
				document.Cases[0].TaskDeclarationSHA256 = strings.Repeat("a", 64)
				rebindSyntheticReview(t, &request.VerifyRequest, document)
			case "grader mismatch":
				document.Cases[0].Checks[0].GraderDeclarationSHA256 = strings.Repeat("a", 64)
				rebindSyntheticReview(t, &request.VerifyRequest, document)
			case "selected check uncovered":
				request.Tasks["task"].Requirements = append(request.Tasks["task"].Requirements, models.Requirement{
					ID: "uncovered", Checks: []models.RequirementCheck{{Scope: "eval", Grader: "check"}},
				})
				calibrationRebind(t, &request.VerifyRequest, &document)
			}
			report, err := Calibrate(t.Context(), request)
			require.NoError(t, err)
			requireCalibrationReport(t, report)
			require.NotEqual(t, AssessmentPassed, report.State)
			require.Zero(t, harness.factories)
			require.Zero(t, harness.notices)
		})
	}
}

func TestCalibrateNoticeRefusalAndNilEngine(t *testing.T) {
	request, _, _, harness := calibrationFixture(t)
	request.PaidCallNotice = func(CalibrationPlan, int) error { harness.notices++; return errors.New("private refusal") }
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, "paid_call_notice_refused", report.Reason)
	require.Zero(t, harness.factories)
	require.Empty(t, *report.Calibration.ExecutionLedger)
	require.NotContains(t, string(marshalReferenceTest(t, report)), "private refusal")

	request, _, _, harness = calibrationFixture(t)
	request.EngineFactory = func(string, func(execution.ExecutionDiagnostic) error) (CalibrationEngine, error) {
		harness.factories++
		return nil, nil
	}
	report, err = Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentError, report.State)
	require.Zero(t, report.Calibration.Executions)
	require.Nil(t, report.Calibration.Usage)
	require.Nil(t, report.Calibration.Credits)

	request, _, _, _ = calibrationFixture(t)
	request.EngineFactory = func(string, func(execution.ExecutionDiagnostic) error) (CalibrationEngine, error) {
		var absent *calibrationTestEngine
		return absent, nil
	}
	report, err = Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentError, report.State)
	require.Zero(t, report.Calibration.Executions)
}

func TestCalibrateFreezesBeforeNoticeAndFactory(t *testing.T) {
	for _, phase := range []string{"notice", "factory"} {
		t.Run(phase, func(t *testing.T) {
			request, document, root, harness := calibrationFixture(t)
			originalSpec, err := evidence.JSONDigest(request.Spec)
			require.NoError(t, err)
			mutate := func() {
				request.Tasks["task"].Stimulus.Message = "mutable hostile task"
				request.Tasks["task"].Expectation.MustInclude = []string{"must never run"}
				request.Tasks["task"].Validators = []models.ValidatorInline{{Identifier: "never", Kind: models.GraderKindProgram,
					Parameters: models.ProgramGraderParameters{Command: "never-execute"}}}
				request.Spec.Config.JudgeModel = "mutable hostile model"
				request.Spec.Graders[0].Parameters = models.PromptGraderParameters{Prompt: "mutable hostile rubric", Model: "hostile"}
				request.Review.Decision.State = ReviewRevoked
				require.NoError(t, os.WriteFile(filepath.Join(root, "reviewed.md"), []byte("mutable hostile source"), 0o600))
				for _, candidate := range document.Cases {
					require.NoError(t, os.WriteFile(filepath.Join(root, candidate.AuthoredInput.Path), []byte("mutable hostile output"), 0o600))
				}
			}
			notice := request.PaidCallNotice
			factory := request.EngineFactory
			if phase == "notice" {
				request.PaidCallNotice = func(plan CalibrationPlan, jobs int) error {
					require.NoError(t, notice(plan, jobs))
					mutate()
					plan.Model = "mutated by value"
					return nil
				}
			} else {
				request.EngineFactory = func(model string, observer func(execution.ExecutionDiagnostic) error) (CalibrationEngine, error) {
					mutate()
					return factory(model, observer)
				}
			}
			report, err := Calibrate(t.Context(), request)
			require.NoError(t, err)
			requireCalibrationReport(t, report)
			require.Equal(t, AssessmentPassed, report.State)
			require.True(t, report.Review.Eligible)
			require.Equal(t, originalSpec.SHA256, *report.Bindings[1].Actual)
			require.Equal(t, 4, harness.factories)
		})
	}
}

func TestCalibrateEmptyAndWhitespaceAreExact(t *testing.T) {
	request, document, root, harness := calibrationFixture(t)
	calibrationReplaceOutput(t, &document, root, 2, new(""))
	calibrationReplaceOutput(t, &document, root, 3, new(" \n\t"))
	calibrationRebind(t, &request.VerifyRequest, &document)
	var messages []string
	harness.edit = func(engine *calibrationTestEngine, _ int) {
		engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			messages = append(messages, req.Message)
			tool := 1
			if strings.Contains(req.Message, "state: ready") {
				tool = 0
			}
			_, err := req.Tools[tool].Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "exact finite input"}})
			return &execution.ExecutionResponse{SessionID: engine.session, Success: true}, err
		}
	}
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentPassed, report.State)
	require.True(t, strings.HasSuffix(messages[2], "## Candidate output\n\n"))
	require.True(t, strings.HasSuffix(messages[3], "## Candidate output\n \n\t\n"))
}

func TestCalibrateOperationalFailuresNeverBecomeCorrectBadPasses(t *testing.T) {
	for _, failure := range []string{"factory engine plus error", "initialize", "shutdown", "after callback execute", "response error", "observer", "unknown observer"} {
		t.Run(failure, func(t *testing.T) {
			request, _, _, harness := calibrationFixture(t)
			harness.edit = func(engine *calibrationTestEngine, index int) {
				if index != 3 {
					return
				}
				switch failure {
				case "factory engine plus error":
					harness.factoryError = errors.New("private factory payload")
				case "initialize":
					engine.initError = errors.New("private initialization payload")
				case "shutdown":
					engine.shutdownError = errors.New("private shutdown payload")
				default:
					engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
						_, err := req.Tools[1].Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "correct bad rejection"}})
						require.NoError(t, err)
						resp := &execution.ExecutionResponse{SessionID: engine.session, Success: true}
						switch failure {
						case "after callback execute":
							return resp, errors.New("private execute payload")
						case "response error":
							resp.ErrorMsg = "private response payload"
						case "observer":
							require.NoError(t, engine.observer(execution.ExecutionDiagnostic{Stage: execution.StageDisconnect, Code: execution.CodeCleanup, SessionID: "private-session"}))
						case "unknown observer":
							require.Error(t, engine.observer(execution.ExecutionDiagnostic{Stage: "private-stage", Code: "private-code"}))
						}
						return resp, nil
					}
				}
			}
			report, err := Calibrate(t.Context(), request)
			require.NoError(t, err)
			requireCalibrationReport(t, report)
			require.Equal(t, AssessmentError, report.State)
			entry := (*report.Calibration.ExecutionLedger)[2]
			require.Equal(t, AssessmentError, entry.State)
			require.NotEmpty(t, entry.Diagnostics)
			require.Equal(t, 1, harness.engines[2].shutdown)
			if failure == "factory engine plus error" || failure == "initialize" {
				require.False(t, entry.Executed)
				require.Zero(t, entry.Callbacks)
				require.Nil(t, entry.Usage)
			} else {
				require.Equal(t, 1, entry.Callbacks)
				require.True(t, entry.UsageComplete, "known accounting survives operational failure")
				require.Equal(t, 0.0, *entry.Credits)
			}
			require.NotContains(t, string(marshalReferenceTest(t, report)), "private")
		})
	}
}

func TestCalibrateMissingCallbacksCannotCertifyBadRejection(t *testing.T) {
	request, _, _, harness := calibrationFixture(t)
	harness.edit = func(engine *calibrationTestEngine, index int) {
		if index == 3 {
			engine.execute = func(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				return &execution.ExecutionResponse{SessionID: engine.session, Success: true, FinalOutput: "I reject the candidate"}, nil
			}
		}
	}
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentInsufficient, report.State)
	entry := (*report.Calibration.ExecutionLedger)[2]
	require.Equal(t, 0, entry.Callbacks)
	require.Equal(t, "grade_callback_missing", entry.Reason)
	require.Nil(t, report.Requirements[0].Observations[2].Agreement)
	require.Equal(t, 3, report.Calibration.Samples)
}

func TestCalibrateIndependentModelAndAccountingChannels(t *testing.T) {
	for _, failure := range []string{"no usage", "partial usage", "credits absent", "per-model credits absent", "NaN credits", "event missing", "events absent", "event invalid ID", "event conflict", "accounting conflict", "accounting projection", "accounting invalid ID", "source unknown", "negative tokens"} {
		t.Run(failure, func(t *testing.T) {
			request, _, _, harness := calibrationFixture(t)
			harness.edit = func(engine *calibrationTestEngine, index int) {
				if index != 3 {
					return
				}
				switch failure {
				case "no usage":
					engine.usage = nil
				case "partial usage":
					engine.accounting.Complete = false
				case "credits absent":
					engine.usage.AICredits = nil
				case "per-model credits absent":
					engine.usage.ModelMetrics[calibrationTestModel] = models.ModelUsage{}
				case "NaN credits":
					engine.usage.AICredits = new(math.NaN())
				case "event missing":
					engine.events.Complete = false
				case "events absent":
					engine.events.EventsObserved = 0
				case "event conflict":
					engine.events.Models = []string{"another-received-model"}
				case "event invalid ID":
					engine.events.Models = []string{""}
				case "accounting conflict":
					engine.accounting.Models = []string{"another-accounting-model"}
					engine.usage.ModelMetrics = map[string]models.ModelUsage{"another-accounting-model": {AICredits: new(0.0)}}
				case "accounting projection":
					engine.accounting.Models = []string{"not-the-metric-key"}
				case "accounting invalid ID":
					engine.accounting.Models = []string{""}
				case "source unknown":
					engine.accounting.Source = "private source"
				case "negative tokens":
					engine.usage.InputTokens = -1
				}
			}
			report, err := Calibrate(t.Context(), request)
			require.NoError(t, err)
			requireCalibrationReport(t, report)
			require.NotEqual(t, AssessmentPassed, report.State)
			entry := (*report.Calibration.ExecutionLedger)[2]
			require.Equal(t, 1, entry.Callbacks)
			if strings.HasPrefix(failure, "event") || failure == "accounting conflict" {
				require.True(t, entry.UsageComplete)
				require.NotNil(t, report.Calibration.Credits)
			} else {
				require.False(t, entry.UsageComplete)
				require.Nil(t, entry.Usage)
				require.Nil(t, report.Calibration.Credits)
				require.Equal(t, 9, report.Calibration.Usage.InputTokens, "aggregate must include missing entries, not claim complete totals")
			}
		})
	}
}

func TestCalibrateAllMissingAccountingIsNull(t *testing.T) {
	request, _, _, harness := calibrationFixture(t)
	harness.edit = func(engine *calibrationTestEngine, _ int) { engine.usage = nil }
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentInsufficient, report.State)
	require.Nil(t, report.Calibration.Usage)
	require.Nil(t, report.Calibration.Credits)
}

func TestCalibrateThresholdAllowsOrdinaryDisagreementButNotCriticalAcceptance(t *testing.T) {
	for _, index := range []int{1, 3} {
		request, document, _, harness := calibrationFixture(t)
		document.Domains[0].MinimumAgreement = 0.75
		calibrationRebind(t, &request.VerifyRequest, &document)
		harness.edit = func(engine *calibrationTestEngine, ordinal int) {
			if ordinal != index {
				return
			}
			engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				tool := 1
				if index == 3 {
					tool = 0
				}
				_, err := req.Tools[tool].Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "actual disagreement"}})
				return &execution.ExecutionResponse{SessionID: engine.session, Success: true}, err
			}
		}
		report, err := Calibrate(t.Context(), request)
		require.NoError(t, err)
		requireCalibrationReport(t, report)
		require.Equal(t, 0.75, *report.Domains[0].Agreement)
		for _, entry := range *report.Calibration.ExecutionLedger {
			require.Equal(t, AssessmentPassed, entry.State)
		}
		if index == 1 {
			require.Equal(t, AssessmentPassed, report.State)
		} else {
			require.Equal(t, AssessmentFailed, report.State)
			require.Equal(t, "critical_false_acceptance", report.Reason)
		}
	}
}

func TestCalibrateCancellationFinalizesKnownAccounting(t *testing.T) {
	request, _, _, harness := calibrationFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	harness.edit = func(engine *calibrationTestEngine, index int) {
		if index != 3 {
			return
		}
		engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, err := req.Tools[1].Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "actual rejection before cancellation"}})
			require.NoError(t, err)
			cancel()
			return &execution.ExecutionResponse{SessionID: engine.session, Success: true}, ctx.Err()
		}
	}
	report, err := Calibrate(ctx, request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentError, report.State)
	require.Equal(t, 3, harness.factories)
	require.Equal(t, 3, report.Calibration.Executions)
	require.True(t, (*report.Calibration.ExecutionLedger)[2].UsageComplete)
	require.Equal(t, 1, harness.engines[2].shutdown)
	require.Nil(t, report.Calibration.Credits, "unexecuted admitted job keeps aggregate incomplete")
}

func TestCalibrateTaskScopeAndUnrelatedValidators(t *testing.T) {
	request, document, _, _ := calibrationFixture(t)
	task := request.Tasks["task"]
	config := request.Spec.Graders[0]
	task.Validators = []models.ValidatorInline{
		{Identifier: config.Identifier, Kind: config.Kind, Parameters: config.Parameters},
		{Identifier: "must-not-run", Kind: models.GraderKindProgram, Parameters: models.ProgramGraderParameters{Command: "does-not-exist"}},
	}
	task.Expectation.MustInclude = []string{"must-not-run"}
	task.Requirements[0].Checks[0].Scope = "task"
	for i := range document.Cases {
		document.Cases[i].Checks[0].Check.Scope = "task"
	}
	calibrationRebind(t, &request.VerifyRequest, &document)
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentPassed, report.State)
	require.Len(t, report.Requirements, 1)
}

func TestCalibrateFrozenJSONPreservesNativeParameterKinds(t *testing.T) {
	request, _, _, _ := calibrationFixture(t)
	request.Spec.Graders = append(request.Spec.Graders,
		models.GraderConfig{Identifier: "text", Kind: models.GraderKindText, Parameters: models.TextGraderParameters{Contains: []string{"finite"}}},
		models.GraderConfig{Identifier: "schema", Kind: models.GraderKindJSONSchema, Parameters: models.JSONSchemaGraderParameters{Schema: map[string]any{"const": json.Number("9007199254740993")}}},
	)
	frozen, err := freezeCalibrationRequest(request.VerifyRequest)
	require.NoError(t, err)
	require.NotSame(t, request.Spec, frozen.Spec)
	originalText, ok := request.Spec.Graders[1].Parameters.(models.TextGraderParameters)
	require.True(t, ok)
	frozenText, ok := frozen.Spec.Graders[1].Parameters.(models.TextGraderParameters)
	require.True(t, ok)
	originalText.Contains[0] = "changed"
	require.Equal(t, "finite", frozenText.Contains[0])
	originalSchema, ok := request.Spec.Graders[2].Parameters.(models.JSONSchemaGraderParameters)
	require.True(t, ok)
	frozenSchema, ok := frozen.Spec.Graders[2].Parameters.(models.JSONSchemaGraderParameters)
	require.True(t, ok)
	originalSchema.Schema["const"] = 1
	require.Equal(t, json.Number("9007199254740993"), frozenSchema.Schema["const"])
}

func TestCalibrateMultipleNativeCallbacksAreNotBillingCalls(t *testing.T) {
	request, _, _, harness := calibrationFixture(t)
	harness.edit = func(engine *calibrationTestEngine, _ int) {
		engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			tool := 1
			if strings.Contains(req.Message, "state: ready") && !strings.Contains(req.Message, "forbidden: yes") {
				tool = 0
			}
			for range 3 {
				_, err := req.Tools[tool].Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "actual repeated native callback"}})
				require.NoError(t, err)
			}
			return &execution.ExecutionResponse{SessionID: engine.session, Success: true}, nil
		}
	}
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentPassed, report.State)
	require.Equal(t, 4, report.Calibration.Executions)
	require.True(t, slices.ContainsFunc(*report.Calibration.ExecutionLedger, func(entry JudgeExecution) bool { return entry.Callbacks == 3 }))
	require.False(t, report.Calibration.BillableCallsBounded)
}

func TestCalibrateSeparateMechanicalDomainUsesActualStrictVerdicts(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		request, document, root, harness := calibrationFixture(t)
		request.Spec.Graders = append(request.Spec.Graders, models.GraderConfig{
			Identifier: "mechanical", Kind: models.GraderKindText,
			Parameters: models.TextGraderParameters{
				RegexMatch: []string{`(?m)^state:\s*ready$`}, RegexNotMatch: []string{`forbidden:\s*yes`},
			},
		})
		request.Tasks["task"].Requirements = append(request.Tasks["task"].Requirements, models.Requirement{
			ID: "mechanical", Checks: []models.RequirementCheck{{Scope: "eval", Grader: "mechanical"}},
		})
		document.Domains = append(document.Domains, DomainCriteria{ID: "mechanical", MinimumCases: 4, MinimumAgreement: 0.75})
		original := slices.Clone(document.Cases)
		for i, candidate := range original {
			candidate.ID = "mechanical-" + candidate.ID
			candidate.Domain = "mechanical"
			candidate.AuthoredInput = &AuthoredSource{Path: candidate.ID + ".json"}
			candidate.Checks = []ReferenceCheck{{
				RequirementID: "mechanical", Check: models.RequirementCheck{Scope: "eval", Grader: "mechanical"},
				ExpectedPassed: new(*original[i].Checks[0].ExpectedPassed),
			}}
			document.Cases = append(document.Cases, candidate)
			output := []string{"state: ready\n", "note: alternate\nstate: ready\n", "state: wrong\n", "state: ready\nforbidden: yes\n"}[i]
			if mismatch && i == 0 {
				output = "state: wrong\n"
			}
			calibrationReplaceOutput(t, &document, root, len(document.Cases)-1, new(output))
		}
		calibrationRebind(t, &request.VerifyRequest, &document)
		report, err := Calibrate(t.Context(), request)
		require.NoError(t, err)
		requireCalibrationReport(t, report)
		if mismatch {
			require.Equal(t, AssessmentFailed, report.State)
			require.Zero(t, harness.factories)
			require.Empty(t, *report.Calibration.ExecutionLedger)
			require.False(t, *report.Requirements[1].Observations[0].Agreement)
		} else {
			require.Equal(t, AssessmentPassed, report.State)
			require.Equal(t, 4, report.Calibration.Executions)
			require.Len(t, report.Requirements, 2)
			for _, observation := range report.Requirements[1].Observations {
				require.Equal(t, models.GraderKindText, observation.Result.Type)
				require.Equal(t, observation.ExpectedPassed, observation.Result.Passed)
				require.True(t, *observation.Agreement)
			}
		}
	}
}

func TestCalibrateMixedDomainAndCheckpointFailBeforeSpend(t *testing.T) {
	for _, mode := range []string{"mixed", "checkpoint"} {
		request, document, _, harness := calibrationFixture(t)
		task := request.Tasks["task"]
		if mode == "mixed" {
			request.Spec.Graders = append(request.Spec.Graders, models.GraderConfig{
				Identifier: "mechanical", Kind: models.GraderKindText, Parameters: models.TextGraderParameters{Contains: []string{"ready"}},
			})
			task.Requirements = append(task.Requirements, models.Requirement{
				ID: "mechanical", Checks: []models.RequirementCheck{{Scope: "eval", Grader: "mechanical"}},
			})
			for i := range document.Cases {
				check := document.Cases[i].Checks[0]
				check.RequirementID, check.Check.Grader, check.RubricContentSHA256 = "mechanical", "mechanical", nil
				document.Cases[i].Checks = append(document.Cases[i].Checks, check)
			}
		} else {
			config := request.Spec.Graders[0]
			task.Checkpoints = []models.Checkpoint{{
				AfterTurn: 1, Graders: []models.ValidatorInline{{Identifier: config.Identifier, Kind: config.Kind, Parameters: config.Parameters}},
			}}
			task.Requirements[0].Checks[0] = models.RequirementCheck{Scope: "checkpoint", Grader: "check", AfterTurn: 1}
			for i := range document.Cases {
				document.Cases[i].Checks[0].Check = task.Requirements[0].Checks[0]
			}
		}
		calibrationRebind(t, &request.VerifyRequest, &document)
		report, err := Calibrate(t.Context(), request)
		require.NoError(t, err)
		requireCalibrationReport(t, report)
		require.NotEqual(t, AssessmentPassed, report.State)
		require.Zero(t, harness.factories)
		require.Zero(t, harness.notices)
	}
}

func TestCalibrateUncoveredRequirementWithoutChecksFailsBeforeSpend(t *testing.T) {
	request, document, _, harness := calibrationFixture(t)
	request.Tasks["task"].Requirements = append(request.Tasks["task"].Requirements, models.Requirement{ID: "missing-checks"})
	calibrationRebind(t, &request.VerifyRequest, &document)
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.NotEqual(t, AssessmentPassed, report.State)
	require.Zero(t, harness.factories)
	require.Zero(t, harness.notices)
}

func TestCalibrateCeilingAndNoticeAreNotASpendingCap(t *testing.T) {
	request, document, _, harness := calibrationFixture(t)
	document.Calibration.MaxJudgeExecutions = 100
	calibrationRebind(t, &request.VerifyRequest, &document)
	request.PaidCallNotice = func(plan CalibrationPlan, admittedUniqueExecutions int) error {
		harness.notices++
		require.Equal(t, 100, plan.MaxJudgeExecutions)
		require.Equal(t, 4, admittedUniqueExecutions)
		return nil
	}
	harness.edit = func(engine *calibrationTestEngine, _ int) {
		engine.usage.AICredits = new(200.0)
		metric := engine.usage.ModelMetrics[calibrationTestModel]
		metric.AICredits = new(200.0)
		engine.usage.ModelMetrics[calibrationTestModel] = metric
	}
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentPassed, report.State)
	require.Equal(t, 4, report.Calibration.Executions)
	require.Equal(t, 100, report.Calibration.MaxJudgeExecutions)
	require.Equal(t, 800.0, *report.Calibration.Credits)
	require.False(t, report.Calibration.CostBounded)
}

func TestCalibrateContextCancellationBeforeConstruction(t *testing.T) {
	for _, phase := range []string{"before admission", "notice", "factory", "initialize"} {
		request, _, _, harness := calibrationFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		switch phase {
		case "before admission":
			cancel()
		case "notice":
			request.PaidCallNotice = func(CalibrationPlan, int) error { harness.notices++; cancel(); return nil }
		case "factory":
			factory := request.EngineFactory
			request.EngineFactory = func(model string, observer func(execution.ExecutionDiagnostic) error) (CalibrationEngine, error) {
				engine, err := factory(model, observer)
				cancel()
				return engine, err
			}
		default:
			harness.edit = func(engine *calibrationTestEngine, _ int) {
				engine.initError = context.Canceled
			}
		}
		report, err := Calibrate(ctx, request)
		require.NoError(t, err)
		requireCalibrationReport(t, report)
		require.NotEqual(t, AssessmentPassed, report.State)
		require.Zero(t, report.Calibration.Executions)
		if phase == "before admission" || phase == "notice" {
			require.Zero(t, harness.factories)
		}
		for _, engine := range harness.engines {
			require.Equal(t, 1, engine.shutdown)
			require.Zero(t, engine.executed)
		}
	}
}

func TestCalibrateOnlyOriginalRubricBodyWhenNoInlineOverride(t *testing.T) {
	request, document, _, harness := calibrationFixture(t)
	params := calibrationPromptParameters(t, request.Spec.Graders[0])
	params.Prompt = ""
	request.Spec.Graders[0].Parameters = params
	calibrationRebind(t, &request.VerifyRequest, &document)
	harness.edit = func(engine *calibrationTestEngine, _ int) {
		engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			require.True(t, strings.HasPrefix(req.Message, "Original reviewed rubric body.\n\n---\n"))
			tool := 1
			if strings.Contains(req.Message, "state: ready") && !strings.Contains(req.Message, "forbidden: yes") {
				tool = 0
			}
			_, err := req.Tools[tool].Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "original rubric body"}})
			return &execution.ExecutionResponse{SessionID: engine.session, Success: true}, err
		}
	}
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentPassed, report.State)
}

func TestCalibrateConstructionObserverFailureStillOwnsEngine(t *testing.T) {
	request, _, _, harness := calibrationFixture(t)
	factory := request.EngineFactory
	request.EngineFactory = func(model string, observer func(execution.ExecutionDiagnostic) error) (CalibrationEngine, error) {
		engine, err := factory(model, observer)
		require.NoError(t, observer(execution.ExecutionDiagnostic{Stage: execution.StageInitialize, Code: execution.CodeProvider}))
		return engine, err
	}
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentError, report.State)
	require.Zero(t, report.Calibration.Executions)
	for _, engine := range harness.engines {
		require.Zero(t, engine.initialized)
		require.Equal(t, 1, engine.shutdown)
	}
}

func TestCalibrateMissingResponseCannotBorrowRequestedOrResponseModel(t *testing.T) {
	for _, absent := range []bool{true, false} {
		request, _, _, harness := calibrationFixture(t)
		harness.edit = func(engine *calibrationTestEngine, index int) {
			if index != 3 {
				return
			}
			engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				_, err := req.Tools[1].Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "actual callback without session identity"}})
				if absent {
					return nil, err
				}
				return &execution.ExecutionResponse{ModelID: calibrationTestModel, Success: true}, err
			}
		}
		report, err := Calibrate(t.Context(), request)
		require.NoError(t, err)
		requireCalibrationReport(t, report)
		require.Equal(t, AssessmentInsufficient, report.State)
		entry := (*report.Calibration.ExecutionLedger)[2]
		require.Equal(t, 1, entry.Callbacks)
		require.Nil(t, entry.Usage)
		require.Empty(t, entry.EventModels)
		require.Empty(t, entry.AccountingModels)
		require.Nil(t, report.Calibration.Credits)
	}
}

func TestCalibratePostCallbackTypedDiagnosticIsPreserved(t *testing.T) {
	request, _, _, harness := calibrationFixture(t)
	harness.edit = func(engine *calibrationTestEngine, index int) {
		if index != 3 {
			return
		}
		engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, err := req.Tools[1].Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "actual callback before send failure"}})
			require.NoError(t, err)
			return &execution.ExecutionResponse{SessionID: engine.session}, errors.Join(
				&execution.DiagnosticError{Diagnostic: execution.ExecutionDiagnostic{
					Stage: execution.StageExecute, Code: execution.CodeSend, SessionID: "private-session",
				}}, errors.New("private-provider-payload"),
			)
		}
	}
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentError, report.State)
	entry := (*report.Calibration.ExecutionLedger)[2]
	require.Contains(t, entry.Diagnostics, JudgeDiagnostic{Stage: "execute", Code: "send_failed"})
	require.True(t, entry.UsageComplete)
	require.NotContains(t, string(marshalReferenceTest(t, report)), "private")
}

func TestCalibrateExportActualOfflineReports(t *testing.T) {
	directory := os.Getenv("WAZA_CALIBRATION_REPORT_DIR")
	if directory == "" {
		t.Skip("set WAZA_CALIBRATION_REPORT_DIR to export actual offline API reports")
	}
	cwd, err := os.Getwd()
	require.NoError(t, err)
	worktree := filepath.Clean(filepath.Join(cwd, "..", ".."))
	relative, err := filepath.Rel(worktree, directory)
	require.NoError(t, err)
	require.True(t, filepath.IsLocal(relative))
	require.True(t, strings.HasPrefix(relative, ".cache"+string(filepath.Separator)), "exports must stay in this worktree's cache")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	write := func(name string, report *Report) {
		// Serialize the returned API report without changing claims, native
		// timing, provenance, accounting, or synthetic supplied-review metadata.
		data, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		data = append(data, '\n')
		require.NoError(t, root.WriteFile(name, data, 0o600))
		t.Logf("actual API report: %s sha256=%s", filepath.Join(directory, name), byteSHA256(data))
	}

	request, _, _, _ := calibrationFixture(t)
	positive, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, positive)
	require.Equal(t, AssessmentPassed, positive.State)
	require.Equal(t, 4, positive.Calibration.Executions)
	write("positive-calibrated-1.1.json", positive)

	request, _, _, harness := calibrationFixture(t)
	harness.edit = func(engine *calibrationTestEngine, index int) {
		if index != 3 {
			return
		}
		calibrationKnownBillingWithCleanup(engine, 2.5, true)
		engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, err := req.Tools[1].Handler(copilot.ToolInvocation{Arguments: map[string]any{
				"description": "actual native check", "reason": "correct bad rejection before operational failure",
			}})
			require.NoError(t, err)
			return &execution.ExecutionResponse{SessionID: engine.session, Success: false}, nil
		}
	}
	failure, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, failure)
	require.Equal(t, AssessmentError, failure.State)
	require.Equal(t, 4, failure.Calibration.Executions)
	require.True(t, (*failure.Calibration.ExecutionLedger)[2].UsageComplete)
	require.Equal(t, 1, (*failure.Calibration.ExecutionLedger)[2].Callbacks)
	require.Equal(t, 2.5, *failure.Calibration.Credits)
	require.Contains(t, (*failure.Calibration.ExecutionLedger)[2].Diagnostics, JudgeDiagnostic{Stage: "execute", Code: "execute_failed"})
	require.Contains(t, (*failure.Calibration.ExecutionLedger)[2].Diagnostics, JudgeDiagnostic{Stage: "client_stop", Code: "cleanup_failed"})
	require.Contains(t, (*failure.Calibration.ExecutionLedger)[2].Diagnostics, JudgeDiagnostic{Stage: "shutdown_delete", Code: "shutdown_rpc_failed"})
	write("operational-failure-calibrated-1.1.json", failure)

	controlRequest, _, _ := authoredVerificationFixture(t)
	control, err := Verify(t.Context(), controlRequest)
	require.NoError(t, err)
	require.Equal(t, ReferenceVersion, control.SchemaVersion)
	require.Equal(t, AssessmentPassed, control.State)
	require.Nil(t, control.Calibration.ExecutionLedger)
	write("verify-control-1.0.json", control)
}

func TestCalibrateRejectsReviewedRubricGoldensBeforeNotice(t *testing.T) {
	for _, test := range []struct{ name, fields string }{
		{"populated", "goldens:\n  - name: supplied-label\n    input: stimulus\n    output: 'state: ready'\n    expected: pass\n"},
		{"empty", "goldens: []\n"},
		{"null", "goldens: null\n"},
		{"implicit null", "goldens:\n"},
		{"tilde", "goldens: ~\n"},
		{"anchored empty", "goldens: &empty []\n"},
		{"aliased empty", "empty_goldens: &empty []\ngoldens: *empty\n"},
		{"aliased null", "empty_goldens: &empty null\ngoldens: *empty\n"},
		{"merged empty", "defaults: &reviewed {goldens: []}\n<<: *reviewed\n"},
		{"merged null", "defaults: &reviewed {goldens: null}\n<<: *reviewed\n"},
		{"merge sequence", "defaults: &reviewed {goldens: null}\n<<: [*reviewed]\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, document, root, harness := calibrationFixture(t)
			rubric := strings.Replace(calibrationTestRubric, "description: Synthetic test rubric.\n",
				"description: Synthetic test rubric.\n"+test.fields, 1)
			_, err := graders.ParseRubric([]byte(rubric))
			require.NoError(t, err, "the native rubric type admits this presence variant")
			require.NoError(t, os.WriteFile(filepath.Join(root, "reviewed.md"), []byte(rubric), 0o600))
			for i := range document.Cases {
				document.Cases[i].Checks[0].RubricContentSHA256 = new(byteSHA256([]byte(rubric)))
			}
			calibrationRebind(t, &request.VerifyRequest, &document)
			report, err := Calibrate(t.Context(), request)
			require.NoError(t, err)
			requireCalibrationReport(t, report)
			require.True(t, report.Review.Eligible)
			require.Equal(t, AssessmentNotAssessed, report.State)
			require.Equal(t, "rubric_goldens_unsupported", report.Reason)
			require.Equal(t, "verified", report.Requirements[0].Observations[0].Bindings[2].State)
			require.Equal(t, *document.Cases[0].Checks[0].RubricContentSHA256, *report.Requirements[0].Observations[0].Bindings[2].Actual)
			require.Zero(t, harness.notices)
			require.Zero(t, harness.factories)
			require.Empty(t, *report.Calibration.ExecutionLedger)
			require.Nil(t, report.Calibration.Usage)
		})
	}
}

func TestCalibrateGoldenPresenceReaderIsFrontmatterBounded(t *testing.T) {
	for _, test := range []struct {
		name, data string
		invalid    bool
	}{
		{"ordinary", calibrationTestRubric, false},
		{"body text", calibrationTestRubric + "\ngoldens: []\n", false},
		{"comment", strings.Replace(calibrationTestRubric, "name:", "# goldens: []\nname:", 1), false},
		{"description", strings.Replace(calibrationTestRubric, "description: Synthetic test rubric.", "description: 'The text goldens: [] is not a field.'", 1), false},
		{"missing opening", "name: synthetic\n", true},
		{"missing closing", "---\nname: synthetic\n", true},
		{"invalid YAML", "---\nname: [\n---\nbody", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			present, err := calibrationRubricGoldensPresent([]byte(test.data))
			if test.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.False(t, present)
		})
	}
}

func calibrationTwoJudgesFixture(t *testing.T, variation string, separateDomains, separateTasks bool) (CalibrateRequest, *calibrationHarness) {
	t.Helper()
	request, document, root, harness := calibrationFixture(t)
	config := request.Spec.Graders[0]
	config.Identifier = "other"
	params := calibrationPromptParameters(t, config)
	rubric := calibrationTestRubric
	switch variation {
	case "rubric":
		rubric = strings.Replace(rubric, "Original reviewed rubric body.", "A different reviewed rubric body.", 1)
		params.Rubric = "other.md"
	case "prompt":
		params.Prompt = "another reviewed inline override"
	case "reasoning":
		params.ReasoningEffort = "high"
	case "same identity":
		params.Rubric = "reviewed-alias.md"
		params.Model, params.ReasoningEffort = calibrationTestModel, "low"
	default:
		t.Fatalf("unknown test judging variation: %s", variation)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, params.Rubric), []byte(rubric), 0o600))
	config.Parameters = params
	request.Spec.Graders = append(request.Spec.Graders, config)
	requirement := models.Requirement{ID: "other", Checks: []models.RequirementCheck{{Scope: "eval", Grader: "other"}}}
	taskID := "task"
	if separateTasks {
		task := *request.Tasks["task"]
		task.TestID, task.Stimulus.Message = "other-task", " another task stimulus "
		task.Requirements = []models.Requirement{requirement}
		taskID = task.TestID
		request.Tasks[taskID] = &task
	} else {
		request.Tasks[taskID].Requirements = append(request.Tasks[taskID].Requirements, requirement)
	}
	domain := document.Cases[0].Domain
	if separateDomains {
		domain = "other"
		document.Domains = append(document.Domains, DomainCriteria{ID: domain, MinimumCases: 4, MinimumAgreement: 1})
	}
	original := slices.Clone(document.Cases)
	for i, candidate := range original {
		candidate.ID, candidate.TaskID, candidate.Domain = "other-"+candidate.ID, taskID, domain
		candidate.AuthoredInput = &AuthoredSource{Path: candidate.ID + ".json"}
		candidate.Checks = []ReferenceCheck{{
			RequirementID: "other", Check: requirement.Checks[0],
			ExpectedPassed:      new(*original[i].Checks[0].ExpectedPassed),
			RubricContentSHA256: new(byteSHA256([]byte(rubric))),
		}}
		document.Cases = append(document.Cases, candidate)
		output := []string{"state: ready\n", "note: alternate\nstate: ready\n", "state: wrong\n", "state: ready\nforbidden: yes\n"}[i]
		calibrationReplaceOutput(t, &document, root, len(document.Cases)-1, new(output))
	}
	document.Calibration.MaxJudgeExecutions = 8
	calibrationRebind(t, &request.VerifyRequest, &document)
	request.PaidCallNotice = func(plan CalibrationPlan, admittedUniqueExecutions int) error {
		harness.notices++
		require.Equal(t, calibrationTestModel, plan.Model)
		require.Equal(t, 8, plan.MaxJudgeExecutions)
		require.Equal(t, 8, admittedUniqueExecutions)
		require.Zero(t, harness.factories)
		return nil
	}
	harness.edit = func(engine *calibrationTestEngine, _ int) {
		engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, err := calibrationRequestIdentity(req)
			require.NoError(t, err)
			require.Equal(t, calibrationTestModel, req.ModelID)
			tool := 1
			if strings.Contains(req.Message, "state: ready") && !strings.Contains(req.Message, "forbidden: yes") {
				tool = 0
			}
			_, err = req.Tools[tool].Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "actual native domain check"}})
			return &execution.ExecutionResponse{SessionID: engine.session, Success: true}, err
		}
	}
	return request, harness
}

func TestCalibrateOneEffectiveJudgingIdentityPerPaidDomain(t *testing.T) {
	for _, variation := range []string{"rubric", "prompt", "reasoning"} {
		t.Run(variation, func(t *testing.T) {
			request, harness := calibrationTwoJudgesFixture(t, variation, false, false)
			report, err := Calibrate(t.Context(), request)
			require.NoError(t, err)
			requireCalibrationReport(t, report)
			require.Equal(t, AssessmentNotAssessed, report.State)
			require.Equal(t, "domain_judging_identity_mismatch", report.Reason)
			require.Zero(t, harness.notices)
			require.Zero(t, harness.factories)
			require.Empty(t, *report.Calibration.ExecutionLedger)
			require.Nil(t, report.Calibration.Credits)
		})
	}
}

func TestCalibrateSeparateReviewedPaidDomainsCanVaryJudgingIdentity(t *testing.T) {
	for _, variation := range []string{"rubric", "prompt", "reasoning"} {
		t.Run(variation, func(t *testing.T) {
			request, harness := calibrationTwoJudgesFixture(t, variation, true, false)
			report, err := Calibrate(t.Context(), request)
			require.NoError(t, err)
			requireCalibrationReport(t, report)
			require.Equal(t, AssessmentPassed, report.State)
			require.Equal(t, 8, report.Calibration.Executions)
			require.Equal(t, 8, harness.factories)
			require.Equal(t, 1, harness.notices)
			require.Len(t, report.Domains, 2)
			for _, domain := range report.Domains {
				require.Equal(t, AssessmentPassed, domain.State)
				require.Equal(t, 4, domain.ObservedCases)
			}
		})
	}
}

func TestCalibrateDomainIdentityExcludesTaskStimulusSelectorsAndPrivatePath(t *testing.T) {
	request, harness := calibrationTwoJudgesFixture(t, "same identity", false, true)
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentPassed, report.State)
	require.Equal(t, 8, report.Calibration.Executions)
	require.Len(t, report.Domains, 1)
	require.Equal(t, 8, harness.factories)
	require.Equal(t, 1, harness.notices)
}

func TestCalibrateDomainIdentityDoesNotReplaceStimulusDedup(t *testing.T) {
	request, harness := calibrationTwoJudgesFixture(t, "same identity", false, false)
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentInsufficient, report.State)
	require.Equal(t, "duplicate_judge_stimulus", report.Reason)
	require.Zero(t, harness.factories)
	require.Zero(t, harness.notices)
}

func calibrationKnownBillingWithCleanup(engine *calibrationTestEngine, credits float64, cleanupFailure bool) {
	engine.usage.AICredits = new(credits)
	metric := engine.usage.ModelMetrics[calibrationTestModel]
	metric.AICredits = new(credits)
	engine.usage.ModelMetrics[calibrationTestModel] = metric
	if cleanupFailure {
		engine.shutdownDiagnostics = []execution.ExecutionDiagnostic{
			{Stage: execution.StageDisconnect, Code: execution.CodeCleanup},
			{Stage: execution.StageEphemeralDelete, Code: execution.CodeCleanup},
		}
		engine.shutdownError = errors.Join(
			&execution.DiagnosticError{Diagnostic: execution.ExecutionDiagnostic{Stage: execution.StageClientStop, Code: execution.CodeCleanup}},
			&execution.DiagnosticError{Diagnostic: execution.ExecutionDiagnostic{Stage: execution.StageShutdownDelete, Code: execution.CodeShutdownRPC}},
			errors.New("private cleanup cause"),
		)
	}
}

func TestCalibrateResponseOnlyFailureCannotCertifyGoodOrBadCallback(t *testing.T) {
	for _, index := range []int{1, 3} {
		for _, billing := range []struct {
			name           string
			credits        float64
			cleanupFailure bool
		}{
			{"zero", 0, false}, {"nonzero", 2.5, false},
			{"zero with cleanup", 0, true}, {"nonzero with cleanup", 2.5, true},
		} {
			t.Run([]string{"good", "bad"}[index/2]+" "+billing.name, func(t *testing.T) {
				request, document, _, harness := calibrationFixture(t)
				harness.edit = func(engine *calibrationTestEngine, ordinal int) {
					if ordinal != index {
						return
					}
					calibrationKnownBillingWithCleanup(engine, billing.credits, billing.cleanupFailure)
					engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
						tool := 1
						if index == 1 {
							tool = 0
						}
						_, err := req.Tools[tool].Handler(copilot.ToolInvocation{Arguments: map[string]any{
							"description": "actual native check", "reason": "correct callback despite failed execution",
						}})
						require.NoError(t, err)
						return &execution.ExecutionResponse{SessionID: engine.session, Success: false}, nil
					}
				}

				report, err := Calibrate(t.Context(), request)
				require.NoError(t, err)
				requireCalibrationReport(t, report)
				require.Equal(t, AssessmentError, report.State)
				require.Equal(t, AssessmentError, report.Calibration.State)
				entry := (*report.Calibration.ExecutionLedger)[index-1]
				require.Equal(t, AssessmentError, entry.State)
				require.Contains(t, entry.Diagnostics, JudgeDiagnostic{Stage: "execute", Code: "execute_failed"})
				if billing.cleanupFailure {
					for _, diagnostic := range []JudgeDiagnostic{
						{Stage: "disconnect", Code: "cleanup_failed"},
						{Stage: "ephemeral_delete", Code: "cleanup_failed"},
						{Stage: "client_stop", Code: "cleanup_failed"},
						{Stage: "shutdown_delete", Code: "shutdown_rpc_failed"},
					} {
						require.Contains(t, entry.Diagnostics, diagnostic)
					}
				}
				require.True(t, entry.Initialized)
				require.True(t, entry.Executed)
				require.Equal(t, 1, entry.Callbacks)
				require.True(t, entry.UsageComplete)
				require.Equal(t, billing.credits, *entry.Credits)
				require.Equal(t, billing.credits, *entry.Usage.ModelMetrics[calibrationTestModel].AICredits)
				require.Equal(t, 1, harness.engines[index-1].shutdown)
				observation := report.Requirements[0].Observations[index-1]
				require.Equal(t, OperationalError, observation.State)
				require.Nil(t, observation.Agreement)
				require.NotNil(t, observation.Result)
				require.Equal(t, *document.Cases[index-1].Checks[0].ExpectedPassed, observation.Result.Passed)
				if index == 1 {
					require.Equal(t, graders.AllPromptsPassed, observation.Result.Feedback)
					require.Equal(t, 1.0, observation.Result.Score)
				} else {
					require.Contains(t, observation.Result.Feedback, "correct callback despite failed execution")
					require.Equal(t, 0.0, observation.Result.Score)
				}
				require.Nil(t, observation.Result.Details)
				require.Equal(t, billing.credits, *report.Calibration.Credits)
				require.NotContains(t, string(marshalReferenceTest(t, report)), "private cleanup cause")
			})
		}
	}
}

func TestCalibrateCancellationAtOwnedCallbackBoundaries(t *testing.T) {
	for _, phase := range []string{
		"before admission", "notice nil", "notice refusal", "factory", "factory error",
		"initialize", "initialize error", "execute", "cleanup", "accounting",
	} {
		t.Run(phase, func(t *testing.T) {
			request, document, _, harness := calibrationFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch phase {
			case "before admission":
				cancel()
			case "notice nil", "notice refusal":
				request.PaidCallNotice = func(CalibrationPlan, int) error {
					harness.notices++
					cancel()
					if phase == "notice refusal" {
						return errors.New("private notice refusal")
					}
					return nil
				}
			case "factory", "factory error":
				factory := request.EngineFactory
				request.EngineFactory = func(model string, observer func(execution.ExecutionDiagnostic) error) (CalibrationEngine, error) {
					engine, err := factory(model, observer)
					owned, ok := engine.(*calibrationTestEngine)
					require.True(t, ok)
					calibrationKnownBillingWithCleanup(owned, 2.5, true)
					cancel()
					if phase == "factory error" {
						return owned, errors.New("private factory failure")
					}
					return owned, err
				}
			default:
				harness.edit = func(engine *calibrationTestEngine, index int) {
					target := 1
					if phase == "cleanup" || phase == "accounting" {
						target = 4
					}
					if index != target {
						return
					}
					calibrationKnownBillingWithCleanup(engine, 2.5, true)
					switch phase {
					case "initialize", "initialize error":
						engine.initializeHook = func(context.Context) error {
							cancel()
							if phase == "initialize error" {
								return errors.New("private initialization failure")
							}
							return nil
						}
					case "execute":
						engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
							_, err := req.Tools[0].Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "actual callback before parent cancellation"}})
							require.NoError(t, err)
							cancel()
							return &execution.ExecutionResponse{SessionID: engine.session, Success: true}, nil
						}
					case "cleanup":
						engine.shutdownHook = func(cleanupCtx context.Context) {
							require.NoError(t, cleanupCtx.Err())
							cancel()
							require.NoError(t, cleanupCtx.Err(), "parent cancellation must not cancel owned cleanup")
						}
					case "accounting":
						engine.usageHook = cancel
					}
				}
			}
			report, err := Calibrate(ctx, request)
			require.NoError(t, err)
			requireCalibrationReport(t, report)
			require.Equal(t, AssessmentError, report.State)
			require.Equal(t, AssessmentError, report.Calibration.State)
			expectedFactories, expectedExecutions := 1, 0
			switch phase {
			case "before admission", "notice nil", "notice refusal":
				expectedFactories = 0
			case "execute":
				expectedExecutions = 1
			case "cleanup", "accounting":
				expectedFactories, expectedExecutions = 4, 4
			}
			require.Equal(t, expectedFactories, harness.factories)
			require.Equal(t, expectedExecutions, report.Calibration.Executions)
			for _, engine := range harness.engines {
				require.Equal(t, 1, engine.shutdown)
			}
			if expectedFactories > 0 {
				target := expectedFactories - 1
				entry := (*report.Calibration.ExecutionLedger)[target]
				require.Equal(t, AssessmentError, entry.State)
				for _, diagnostic := range []JudgeDiagnostic{
					{Stage: "disconnect", Code: "cleanup_failed"},
					{Stage: "ephemeral_delete", Code: "cleanup_failed"},
					{Stage: "client_stop", Code: "cleanup_failed"},
					{Stage: "shutdown_delete", Code: "shutdown_rpc_failed"},
				} {
					require.Contains(t, entry.Diagnostics, diagnostic)
				}
				if expectedExecutions > 0 {
					require.True(t, entry.UsageComplete)
					require.Equal(t, 2.5, *entry.Credits)
					require.Contains(t, entry.Diagnostics, JudgeDiagnostic{Stage: "execute", Code: "execute_failed"})
				} else {
					require.False(t, entry.Executed)
					require.Nil(t, entry.Usage)
				}
				if phase == "factory error" {
					require.Contains(t, entry.Diagnostics, JudgeDiagnostic{Stage: "initialize", Code: "provider_failed"})
					require.Zero(t, harness.engines[0].initialized)
				}
			}
			if phase == "cleanup" || phase == "accounting" {
				require.Equal(t, 2.5, *report.Calibration.Credits, "complete accounting survives cancellation during final cleanup")
			}
			_, err = ReadDocument(context.WithoutCancel(ctx), request.SnapshotRoot, document.Cases[0].AuthoredInput.Path, maxSnapshotBytes)
			require.NoError(t, err, "borrowed snapshot root remains caller-owned")
			_, err = ReadDocument(context.WithoutCancel(ctx), request.RubricRoot, "reviewed.md", maxSnapshotBytes)
			require.NoError(t, err, "borrowed rubric root remains caller-owned")
		})
	}
}

func TestCalibrateObserverIsScopedAndSealedPerInvocation(t *testing.T) {
	for _, currentFailure := range []bool{false, true} {
		request, _, _, harness := calibrationFixture(t)
		harness.edit = func(engine *calibrationTestEngine, index int) {
			if index != 2 {
				return
			}
			require.Error(t, harness.engines[0].observer(execution.ExecutionDiagnostic{Stage: execution.StageExecute, Code: execution.CodeSend}),
				"previous observer must already be closed during the next factory")
			engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
				diagnostic := execution.ExecutionDiagnostic{Stage: execution.StageExecute, Code: execution.CodeSend}
				require.Error(t, harness.engines[0].observer(diagnostic), "completed observer cannot affect another owned invocation")
				if currentFailure {
					require.NoError(t, engine.observer(diagnostic))
				}
				_, err := req.Tools[0].Handler(copilot.ToolInvocation{Arguments: map[string]any{"reason": "actual scoped callback"}})
				return &execution.ExecutionResponse{SessionID: engine.session, Success: true}, err
			}
		}
		report, err := Calibrate(t.Context(), request)
		require.NoError(t, err)
		requireCalibrationReport(t, report)
		require.Equal(t, AssessmentPassed, (*report.Calibration.ExecutionLedger)[0].State)
		require.Empty(t, (*report.Calibration.ExecutionLedger)[0].Diagnostics)
		if currentFailure {
			require.Equal(t, AssessmentError, report.State)
			require.Contains(t, (*report.Calibration.ExecutionLedger)[1].Diagnostics, JudgeDiagnostic{Stage: "execute", Code: "send_failed"})
		} else {
			require.Equal(t, AssessmentPassed, report.State)
		}
		before := marshalReferenceTest(t, report)
		require.Error(t, harness.engines[3].observer(execution.ExecutionDiagnostic{Stage: execution.StageClientStop, Code: execution.CodeCleanup}))
		require.Equal(t, before, marshalReferenceTest(t, report), "completed callbacks cannot mutate returned evidence")
	}
}

func TestCalibrateCompletedNativeHandlersCannotMutateEvidence(t *testing.T) {
	request, _, _, harness := calibrationFixture(t)
	var retained func(copilot.ToolInvocation) (copilot.ToolResult, error)
	harness.edit = func(engine *calibrationTestEngine, index int) {
		if index == 2 {
			require.NotNil(t, retained)
			_, err := retained(copilot.ToolInvocation{Arguments: map[string]any{"reason": "late fabricated verdict"}})
			require.Error(t, err)
		}
		if index != 1 {
			return
		}
		engine.execute = func(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			retained = req.Tools[0].Handler
			_, err := retained(copilot.ToolInvocation{Arguments: map[string]any{"reason": "actual initial native verdict"}})
			return &execution.ExecutionResponse{SessionID: engine.session, Success: true}, err
		}
	}
	report, err := Calibrate(t.Context(), request)
	require.NoError(t, err)
	requireCalibrationReport(t, report)
	require.Equal(t, AssessmentPassed, report.State)
	require.Equal(t, 1, (*report.Calibration.ExecutionLedger)[0].Callbacks)
	before := marshalReferenceTest(t, report)
	_, err = retained(copilot.ToolInvocation{Arguments: map[string]any{"reason": "late returned-report mutation"}})
	require.Error(t, err)
	require.Equal(t, before, marshalReferenceTest(t, report))
}

func TestCalibrateExecutionEvidenceSurvivesReportErrors(t *testing.T) {
	for _, failure := range []string{"contract", "encoding"} {
		t.Run(failure, func(t *testing.T) {
			request, _, _, harness := calibrationFixture(t)
			harness.edit = func(engine *calibrationTestEngine, _ int) {
				if failure == "encoding" {
					calibrationKnownBillingWithCleanup(engine, 1e308, false)
				} else {
					engine.usage.InputTokens = 9007199254740991
					metric := engine.usage.ModelMetrics[calibrationTestModel]
					metric.InputTokens = 9007199254740991
					engine.usage.ModelMetrics[calibrationTestModel] = metric
				}
			}
			report, err := Calibrate(t.Context(), request)
			require.Error(t, err)
			require.NotNil(t, report)
			require.Equal(t, AssessmentError, report.State)
			require.Equal(t, AssessmentError, report.Calibration.State)
			require.Equal(t, 4, report.Calibration.Executions)
			require.Len(t, *report.Calibration.ExecutionLedger, 4)
			for _, entry := range *report.Calibration.ExecutionLedger {
				require.True(t, entry.Executed)
				require.True(t, entry.UsageComplete)
				require.NotNil(t, entry.Usage)
				require.Equal(t, 1, entry.Callbacks)
			}
			for _, observation := range report.Requirements[0].Observations {
				require.NotNil(t, observation.Result)
			}
		})
	}
}
