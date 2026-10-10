package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

const cliCalibrationModel = "synthetic-model"

type cliCalibrationFixture struct {
	spec, labels, review, rubric string
	document                     assurance.ReferenceDocument
}

func calibrationCLIHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeCalibrationCLIFixture(t *testing.T) *cliCalibrationFixture {
	t.Helper()
	specPath, labelsPath := writeAssuranceCommandFixture(t)
	dir := filepath.Dir(specPath)
	t.Chdir(dir)
	source, err := os.ReadFile(specPath)
	require.NoError(t, err)
	source = bytes.Replace(source, []byte("  executor: mock"), []byte("  executor: copilot-sdk"), 1)
	source = bytes.Replace(source, []byte("  model: harness"), []byte("  model: harness\n  judge_model: synthetic-model\n  judge_reasoning_effort: low"), 1)
	source = bytes.Replace(source, []byte("    type: file"), []byte("    type: prompt"), 1)
	source = bytes.Replace(source, []byte("      must_exist: [state.txt]"), []byte("      prompt: independent reviewed rubric override\n      rubric: reviewed.md\n      mode: independent"), 1)
	require.NoError(t, os.WriteFile(specPath, source, 0o600))
	spec, err := models.LoadEvalSpec(specPath)
	require.NoError(t, err)
	task, err := models.LoadTestCase(filepath.Join(dir, "task.yaml"))
	require.NoError(t, err)
	digest := func(value any) string {
		result, err := evidence.JSONDigest(value)
		require.NoError(t, err)
		return result.SHA256
	}
	rubric := []byte("---\nname: synthetic\nversion: 1.0.0\nscale: pass-fail\ndescription: Synthetic test rubric.\n---\nOriginal reviewed rubric body.\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "reviewed.md"), rubric, 0o600))
	inspection := preflight.Inspect(specPath, preflight.Options{})
	require.False(t, inspection.Failed(false), "%+v", inspection.Diagnostics)
	executable, err := assurance.CurrentExecutableSHA256()
	require.NoError(t, err)
	fixture := &cliCalibrationFixture{
		spec: specPath, labels: labelsPath, review: filepath.Join(dir, "review.json"), rubric: dir,
		document: assurance.ReferenceDocument{
			Kind: assurance.ReferenceKind, SchemaVersion: assurance.ReferenceVersion,
			ID: "synthetic-cli-corpus", Version: "1",
			EvalSourceSHA256: calibrationCLIHash(source), EvalResolvedConfigSHA256: digest(spec),
			ImplementationExecutableSHA256: new(executable),
			Domains:                        []assurance.DomainCriteria{{ID: "cli", MinimumCases: 4, MinimumAgreement: 1}},
			Calibration:                    &assurance.CalibrationPlan{Protocol: assurance.AgreementProtocol, Model: cliCalibrationModel, MaxJudgeExecutions: 4},
		},
	}
	for index, candidate := range []struct {
		id, output string
		class      assurance.CaseClassification
		passed     bool
	}{
		{"good", "state: ready\n", assurance.CaseGood, true},
		{"alternative", "note: alternate\nstate: ready\n", assurance.CaseAlternative, true},
		{"wrong", "state: wrong\n", assurance.CaseCriticalBad, false},
		{"forbidden", "state: ready\nforbidden: yes\n", assurance.CaseCriticalBad, false},
	} {
		input, err := assurance.NewAuthoredOutput(fixture.document.ID, candidate.id, task.TestID, index+1, new(candidate.output))
		require.NoError(t, err)
		name := candidate.id + ".json"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), input.Document(), 0o600))
		ref, err := evidence.Reference(input.Manifest(), evidence.ReferenceInputArtifactID)
		require.NoError(t, err)
		ref.Pointer = "/output"
		fixture.document.Cases = append(fixture.document.Cases, assurance.ReferenceCase{
			ID: candidate.id, ScenarioID: "state", Domain: "cli", Classification: candidate.class,
			TaskID: task.TestID, TaskDeclarationSHA256: digest(task),
			AuthoredInput:  &assurance.AuthoredSource{Path: name, DocumentSHA256: calibrationCLIHash(input.Document())},
			ManifestSHA256: input.Manifest().SHA256,
			Checks: []assurance.ReferenceCheck{{
				RequirementID: "state", Check: task.Requirements[0].Checks[0],
				GraderDeclarationSHA256: digest(spec.Graders[0]), RubricContentSHA256: new(calibrationCLIHash(rubric)),
				ExpectedPassed: new(candidate.passed), Evidence: []models.EvidenceReference{ref},
			}},
		})
	}
	fixture.save(t)
	return fixture
}

func (fixture *cliCalibrationFixture) save(t *testing.T) {
	t.Helper()
	labels, err := json.Marshal(fixture.document)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(fixture.labels, labels, 0o600))
	// Deliberately synthetic review, not a human certification.
	review, err := json.Marshal(assurance.ReviewDocument{
		Kind: assurance.ReviewKind, SchemaVersion: assurance.ReferenceVersion, SourceID: "synthetic-source",
		SubjectID: fixture.document.ID, SubjectVersion: fixture.document.Version, LabelsSHA256: calibrationCLIHash(labels),
		State: assurance.ReviewReviewed, Reviewer: "synthetic-test-reviewer",
		ReviewedAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(fixture.review, review, 0o600))
}

func (fixture *cliCalibrationFixture) args(extra ...string) []string {
	return append([]string{fixture.spec, "--references", fixture.labels, "--review", fixture.review,
		"--accept-review-source", "synthetic-source", "--rubric-root", fixture.rubric}, extra...)
}

func calibrationCLIOptions() *assureCalibrateCommandOptions {
	return &assureCalibrateCommandOptions{
		Now: func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) },
	}
}

func calibrationCLIExit(t *testing.T, err error, want int) {
	t.Helper()
	if want == 0 {
		require.NoError(t, err)
		return
	}
	var exit *ExitCodeError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, want, exit.Code)
	require.NotContains(t, err.Error(), "secret-error")
}

type cliCalibrationEngine struct {
	t                    *testing.T
	mode                 string
	init, execute, close int
	finalized            bool
	cancel               context.CancelFunc
	observer             func(execution.ExecutionDiagnostic) error
}

func (engine *cliCalibrationEngine) Initialize(ctx context.Context) error {
	engine.init++
	if engine.mode == "init-error" {
		return errors.New("secret-error")
	}
	if engine.mode == "cancel-init" {
		engine.cancel()
		return ctx.Err()
	}
	return ctx.Err()
}

func (engine *cliCalibrationEngine) Execute(ctx context.Context, request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	engine.execute++
	require.True(engine.t, request.NoSkills)
	require.True(engine.t, request.EphemeralSession)
	require.True(engine.t, request.SkipWorkspaceCapture)
	require.Empty(engine.t, request.SessionID)
	require.Empty(engine.t, request.SkillPaths)
	require.Empty(engine.t, request.Instructions)
	require.Empty(engine.t, request.Context)
	require.Equal(engine.t, cliCalibrationModel, request.ModelID)
	require.Len(engine.t, request.Tools, 2)
	require.NotNil(engine.t, request.PermissionHandler)
	require.NotNil(engine.t, request.ToolPolicy)
	if engine.mode == "cancel-execute" {
		engine.cancel()
		return &execution.ExecutionResponse{SessionID: "synthetic-session"}, ctx.Err()
	}
	if engine.mode != "no-callback" {
		index := 1
		if strings.Contains(request.Message, "state: ready") && !strings.Contains(request.Message, "forbidden: yes") {
			index = 0
		}
		_, err := request.Tools[index].Handler(copilot.ToolInvocation{
			Arguments: map[string]any{"description": "native check", "reason": "synthetic deterministic verdict"},
		})
		require.NoError(engine.t, err)
	}
	if engine.mode == "execute-error" {
		return &execution.ExecutionResponse{SessionID: "synthetic-session"}, errors.New("secret-error")
	}
	return &execution.ExecutionResponse{SessionID: "synthetic-session", Success: engine.mode != "response-failure"}, nil
}

func (engine *cliCalibrationEngine) Shutdown(ctx context.Context) error {
	engine.close++
	engine.finalized = true
	require.NoError(engine.t, ctx.Err())
	_, deadline := ctx.Deadline()
	require.True(engine.t, deadline, "producer must own independent bounded cleanup")
	if engine.mode == "cancel-cleanup" {
		engine.cancel()
	}
	if engine.mode == "shutdown-diagnostic" {
		require.NoError(engine.t, engine.observer(execution.ExecutionDiagnostic{
			Stage: execution.StageClientStop, Code: execution.CodeCleanup, SessionID: "synthetic-session",
		}))
	}
	if engine.mode == "close-error" {
		return errors.New("secret-error")
	}
	return nil
}

func (engine *cliCalibrationEngine) SessionUsage(string) *models.UsageStats {
	require.True(engine.t, engine.finalized)
	if engine.mode == "cancel-accounting" {
		engine.cancel()
	}
	if engine.mode == "unknown-accounting" {
		return nil
	}
	credits := 0.0
	if engine.mode == "nonzero-accounting" {
		credits = 0.25
	}
	return &models.UsageStats{InputTokens: 3, OutputTokens: 2, AICredits: new(credits),
		ModelMetrics: map[string]models.ModelUsage{cliCalibrationModel: {InputTokens: 3, OutputTokens: 2, AICredits: new(credits)}}}
}

func (engine *cliCalibrationEngine) SessionUsageObservation(string) execution.SessionUsageObservation {
	require.True(engine.t, engine.finalized)
	if engine.mode == "unknown-accounting" {
		return execution.SessionUsageObservation{}
	}
	return execution.SessionUsageObservation{Source: "rpc", Complete: true, ModelAttributionComplete: true, Models: []string{cliCalibrationModel}}
}

func (engine *cliCalibrationEngine) SessionEventModelObservation(string) execution.SessionEventModelObservation {
	require.True(engine.t, engine.finalized)
	model := cliCalibrationModel
	if engine.mode == "wrong-model" {
		model = "other-model"
	}
	return execution.SessionEventModelObservation{Models: []string{model}, Complete: true, EventsObserved: 1}
}

func TestAssureCalibrateNativeLifecycle(t *testing.T) {
	for _, mode := range []string{"pass", "no-callback", "response-failure", "init-error", "execute-error", "close-error",
		"factory-error-with-engine", "wrong-model", "unknown-accounting", "nonzero-accounting",
		"cancel-factory", "cancel-init", "cancel-execute", "cancel-cleanup", "cancel-accounting", "shutdown-diagnostic"} {
		t.Run(mode, func(t *testing.T) {
			fixture := writeCalibrationCLIFixture(t)
			opts := calibrationCLIOptions()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var output, notice bytes.Buffer
			var engines []*cliCalibrationEngine
			opts.EngineFactory = func(model string, observer func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error) {
				require.Contains(t, notice.String(), "acknowledgment=true")
				require.Contains(t, notice.String(), "N=4")
				require.Contains(t, notice.String(), "M=4")
				require.Contains(t, notice.String(), "not provider follow-up calls")
				require.Contains(t, notice.String(), "accounting is not zero")
				require.Equal(t, cliCalibrationModel, model)
				require.NotNil(t, observer)
				engine := &cliCalibrationEngine{t: t, mode: mode, cancel: cancel, observer: observer}
				engines = append(engines, engine)
				if mode == "cancel-factory" {
					cancel()
				}
				if mode == "factory-error-with-engine" {
					return engine, errors.New("secret-error")
				}
				return engine, nil
			}
			cmd := newAssureCalibrateCommandWithOptions(opts)
			cmd.SetContext(ctx)
			cmd.SetOut(&output)
			cmd.SetErr(&notice)
			path := filepath.Join(t.TempDir(), "report.json")
			cmd.SetArgs(fixture.args("--accept-paid-calls", "-o", path))
			err := cmd.Execute()
			require.NotEmpty(t, engines, "error=%v report=%s", err, output.String())
			for _, engine := range engines {
				require.Equal(t, 1, engine.close)
				if mode == "factory-error-with-engine" || mode == "cancel-factory" {
					require.Zero(t, engine.init)
					require.Zero(t, engine.execute)
				}
			}
			report, parseErr := assurance.ParseCalibratedReport(output.Bytes())
			require.NoError(t, parseErr)
			require.Equal(t, assurance.CalibratedReportVersion, report.SchemaVersion)
			require.NotNil(t, report.Calibration.ExecutionLedger)
			require.NotContains(t, output.String(), "synthetic-session")
			if mode == "shutdown-diagnostic" {
				require.Contains(t, output.String(), "cleanup_failed")
			}
			saved, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, output.Bytes(), saved)
			switch mode {
			case "pass", "nonzero-accounting":
				calibrationCLIExit(t, err, 0)
				require.Equal(t, assurance.AssessmentPassed, report.State)
				require.Len(t, *report.Calibration.ExecutionLedger, 4)
				require.NotNil(t, report.Calibration.Credits)
				if mode == "nonzero-accounting" {
					require.Equal(t, 1.0, *report.Calibration.Credits)
				} else {
					require.Zero(t, *report.Calibration.Credits)
				}
			case "unknown-accounting":
				require.Nil(t, report.Calibration.Credits)
				require.Nil(t, report.Calibration.Usage)
				require.NotEqual(t, assurance.AssessmentPassed, report.State)
				calibrationCLIExit(t, err, 1)
			case "no-callback":
				calibrationCLIExit(t, err, 1)
			default:
				calibrationCLIExit(t, err, 2)
			}
		})
	}
}

type calibrationBrokenWriter struct{ short bool }

func (writer calibrationBrokenWriter) Write([]byte) (int, error) {
	if writer.short {
		return 0, nil
	}
	return 0, errors.New("secret-error")
}

func TestAssureCalibrateNativeRefusalAndNoticeFailure(t *testing.T) {
	for _, mode := range []string{"refusal", "explicit-false", "error", "short", "cancel-notice"} {
		t.Run(mode, func(t *testing.T) {
			fixture := writeCalibrationCLIFixture(t)
			opts := calibrationCLIOptions()
			factories := 0
			opts.EngineFactory = func(string, func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error) {
				factories++
				return nil, errors.New("must not construct")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cmd := newAssureCalibrateCommandWithOptions(opts)
			cmd.SetContext(ctx)
			var output, notice bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&notice)
			if mode == "error" || mode == "short" {
				cmd.SetErr(calibrationBrokenWriter{short: mode == "short"})
			}
			if mode == "cancel-notice" {
				cmd.SetErr(calibrationCancelWriter{cancel: cancel, writer: &notice})
			}
			// A prior automation input is never payment acknowledgment.
			cmd.SetIn(strings.NewReader("yes\n"))
			args := fixture.args()
			if mode == "explicit-false" {
				args = append(args, "--accept-paid-calls=false")
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			want := 2
			if mode == "refusal" || mode == "explicit-false" {
				want = 1
				require.Contains(t, notice.String(), "acknowledgment=false")
			}
			calibrationCLIExit(t, err, want)
			require.Zero(t, factories)
			report, parseErr := assurance.ParseCalibratedReport(output.Bytes())
			require.NoError(t, parseErr)
			require.Empty(t, *report.Calibration.ExecutionLedger)
			require.Nil(t, report.Calibration.Usage)
			require.Nil(t, report.Calibration.Credits)
		})
	}
}

type calibrationCancelWriter struct {
	cancel context.CancelFunc
	writer io.Writer
}

func (writer calibrationCancelWriter) Write(data []byte) (int, error) {
	writer.cancel()
	return writer.writer.Write(data)
}

func TestAssureCalibratePresenceHelpAndRegistration(t *testing.T) {
	root := newRootCommand()
	selected, _, err := root.Find([]string{"assure", "calibrate"})
	require.NoError(t, err)
	require.Equal(t, "calibrate", selected.Name())
	require.False(t, shouldRunUpdateCheck(selected, false))
	for _, args := range [][]string{{"--help"}, {"eval.yaml"}, {"eval.yaml", "--calibrate"}, {"eval.yaml", "--model", "other"}} {
		factories := 0
		opts := calibrationCLIOptions()
		opts.EngineFactory = func(string, func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error) {
			factories++
			return nil, nil
		}
		cmd := newAssureCalibrateCommandWithOptions(opts)
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetArgs(args)
		err := cmd.Execute()
		if args[0] == "--help" {
			require.NoError(t, err)
			require.Contains(t, output.String(), "--accept-paid-calls")
			require.Contains(t, output.String(), "default 10m0s")
		} else {
			require.Error(t, err)
		}
		require.Zero(t, factories)
		require.Equal(t, "false", cmd.Flags().Lookup("accept-paid-calls").DefValue)
		require.Nil(t, cmd.Flags().Lookup("calibrate"))
	}
}

func TestAssureCalibrateInputFailuresNeverReachProducer(t *testing.T) {
	for _, mode := range []string{"empty-references", "empty-review", "empty-source", "empty-rubric", "zero-timeout", "negative-timeout",
		"provider", "base-url", "missing-rubric", "rubric-file", "missing-labels", "bad-labels", "missing-review", "bad-review",
		"missing-eval", "duplicate-task", "cancel-before"} {
		t.Run(mode, func(t *testing.T) {
			fixture := writeCalibrationCLIFixture(t)
			opts := calibrationCLIOptions()
			called := 0
			opts.Calibrate = func(context.Context, assurance.CalibrateRequest) (*assurance.Report, error) {
				called++
				return nil, nil
			}
			args := fixture.args("--accept-paid-calls")
			switch mode {
			case "empty-references":
				args = append(args, "--references=")
			case "empty-review":
				args = append(args, "--review=")
			case "empty-source":
				args = append(args, "--accept-review-source=")
			case "empty-rubric":
				args = append(args, "--rubric-root=")
			case "zero-timeout":
				args = append(args, "--timeout=0")
			case "negative-timeout":
				args = append(args, "--timeout=-1s")
			case "provider":
				t.Setenv("COPILOT_PROVIDER_BASE_URL", "https://secret-error.invalid")
			case "base-url":
				t.Setenv("COPILOT_BASE_URL", "https://secret-error.invalid")
			case "missing-rubric":
				args = append(args, "--rubric-root", filepath.Join(fixture.rubric, "missing"))
			case "rubric-file":
				args = append(args, "--rubric-root", fixture.review)
			case "missing-labels":
				require.NoError(t, os.Remove(fixture.labels))
			case "bad-labels":
				require.NoError(t, os.WriteFile(fixture.labels, []byte("{}"), 0o600))
			case "missing-review":
				require.NoError(t, os.Remove(fixture.review))
			case "bad-review":
				require.NoError(t, os.WriteFile(fixture.review, []byte("{}"), 0o600))
			case "missing-eval":
				args[0] += ".missing"
			case "duplicate-task":
				source, err := os.ReadFile(fixture.spec)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(fixture.spec, bytes.Replace(source, []byte("tasks: [task.yaml]"), []byte("tasks: [task.yaml, task.yaml]"), 1), 0o600))
			}
			cmd := newAssureCalibrateCommandWithOptions(opts)
			if mode == "cancel-before" {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				cmd.SetContext(ctx)
			}
			cmd.SetArgs(args)
			calibrationCLIExit(t, cmd.Execute(), 2)
			require.Zero(t, called)
		})
	}
}

func TestAssureCalibrateNativeAdmissionFailures(t *testing.T) {
	for _, mode := range []string{"missing-plan", "wrong-model", "wrong-protocol", "ceiling", "coverage", "domain-minimum", "eval-binding",
		"executable-binding", "rubric-binding", "missing-rubric", "wrong-source", "unsupported-mode", "duplicate-stimulus",
		"goldens-empty", "goldens-null", "goldens-populated", "mixed-domain"} {
		t.Run(mode, func(t *testing.T) {
			fixture := writeCalibrationCLIFixture(t)
			args := fixture.args("--accept-paid-calls")
			switch mode {
			case "missing-plan":
				fixture.document.Calibration = nil
			case "wrong-model":
				fixture.document.Calibration.Model = "other-model"
			case "wrong-protocol":
				fixture.document.Calibration.Protocol = "unsupported"
			case "ceiling":
				fixture.document.Calibration.MaxJudgeExecutions = 1
			case "coverage":
				fixture.document.Cases = fixture.document.Cases[:3]
			case "domain-minimum":
				fixture.document.Domains[0].MinimumCases = 5
			case "duplicate-stimulus":
				candidate := &fixture.document.Cases[1]
				output := "state: ready\n"
				input, err := assurance.NewAuthoredOutput(fixture.document.ID, candidate.ID, candidate.TaskID, 2, &output)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(fixture.rubric, candidate.AuthoredInput.Path), input.Document(), 0o600))
				candidate.AuthoredInput.DocumentSHA256 = calibrationCLIHash(input.Document())
				candidate.ManifestSHA256 = input.Manifest().SHA256
				ref, err := evidence.Reference(input.Manifest(), evidence.ReferenceInputArtifactID)
				require.NoError(t, err)
				ref.Pointer = "/output"
				candidate.Checks[0].Evidence = []models.EvidenceReference{ref}
			case "goldens-empty", "goldens-null", "goldens-populated":
				path := filepath.Join(fixture.rubric, "reviewed.md")
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				goldens := "goldens: []\n"
				switch mode {
				case "goldens-null":
					goldens = "goldens: null\n"
				case "goldens-populated":
					goldens = "goldens:\n  - name: supplied-label\n    input: stimulus\n    output: 'state: ready'\n    expected: pass\n"
				}
				data = bytes.Replace(data, []byte("name: synthetic"), []byte(goldens+"name: synthetic"), 1)
				require.NoError(t, os.WriteFile(path, data, 0o600))
				for i := range fixture.document.Cases {
					fixture.document.Cases[i].Checks[0].RubricContentSHA256 = new(calibrationCLIHash(data))
				}
			case "mixed-domain":
				source, err := os.ReadFile(fixture.spec)
				require.NoError(t, err)
				source = bytes.Replace(source, []byte("metrics:"), []byte("  - name: mechanical\n    type: text\n    config:\n      contains: [ready]\nmetrics:"), 1)
				require.NoError(t, os.WriteFile(fixture.spec, source, 0o600))
				taskPath := filepath.Join(fixture.rubric, "task.yaml")
				taskBytes, err := os.ReadFile(taskPath)
				require.NoError(t, err)
				taskBytes = append(taskBytes, []byte("  - id: mechanical\n    category: outcome\n    description: Mechanical check.\n    checks:\n      - scope: eval\n        grader: mechanical\n")...)
				require.NoError(t, os.WriteFile(taskPath, taskBytes, 0o600))
				spec, err := models.LoadEvalSpec(fixture.spec)
				require.NoError(t, err)
				task, err := models.LoadTestCase(taskPath)
				require.NoError(t, err)
				specDigest, err := evidence.JSONDigest(spec)
				require.NoError(t, err)
				taskDigest, err := evidence.JSONDigest(task)
				require.NoError(t, err)
				decl, err := evidence.JSONDigest(spec.Graders[1])
				require.NoError(t, err)
				fixture.document.EvalSourceSHA256, fixture.document.EvalResolvedConfigSHA256 = calibrationCLIHash(source), specDigest.SHA256
				for i := range fixture.document.Cases {
					candidate := &fixture.document.Cases[i]
					candidate.TaskDeclarationSHA256 = taskDigest.SHA256
					candidate.Checks = append(candidate.Checks, assurance.ReferenceCheck{
						RequirementID: "mechanical", Check: models.RequirementCheck{Scope: "eval", Grader: "mechanical"},
						GraderDeclarationSHA256: decl.SHA256, ExpectedPassed: candidate.Checks[0].ExpectedPassed,
						Evidence: candidate.Checks[0].Evidence,
					})
				}
			case "eval-binding":
				fixture.document.EvalSourceSHA256 = strings.Repeat("b", 64)
			case "executable-binding":
				fixture.document.ImplementationExecutableSHA256 = new(strings.Repeat("b", 64))
			case "rubric-binding":
				fixture.document.Cases[0].Checks[0].RubricContentSHA256 = new(strings.Repeat("b", 64))
			case "missing-rubric":
				require.NoError(t, os.Remove(filepath.Join(fixture.rubric, "reviewed.md")))
			case "wrong-source":
				args = append(args, "--accept-review-source", "not-the-source")
			case "unsupported-mode":
				source, err := os.ReadFile(fixture.spec)
				require.NoError(t, err)
				source = bytes.Replace(source, []byte("mode: independent"), []byte("mode: continue_session"), 1)
				require.NoError(t, os.WriteFile(fixture.spec, source, 0o600))
				spec, err := models.LoadEvalSpec(fixture.spec)
				require.NoError(t, err)
				digest, err := evidence.JSONDigest(spec)
				require.NoError(t, err)
				fixture.document.EvalSourceSHA256, fixture.document.EvalResolvedConfigSHA256 = calibrationCLIHash(source), digest.SHA256
				decl, err := evidence.JSONDigest(spec.Graders[0])
				require.NoError(t, err)
				for i := range fixture.document.Cases {
					fixture.document.Cases[i].Checks[0].GraderDeclarationSHA256 = decl.SHA256
				}
			}
			fixture.save(t)
			opts := calibrationCLIOptions()
			factories := 0
			opts.EngineFactory = func(string, func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error) {
				factories++
				return nil, nil
			}
			cmd := newAssureCalibrateCommandWithOptions(opts)
			var output, notice bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&notice)
			cmd.SetArgs(args)
			require.Error(t, cmd.Execute())
			require.Zero(t, factories)
			require.Empty(t, notice.String())
		})
	}
}

func TestAssureCalibrateReportErrorAndSinkMatrix(t *testing.T) {
	for _, mode := range []string{"passed", "failed", "insufficient", "not-assessed", "invalid", "operational",
		"report-and-error", "nil-and-error", "nil-without-error", "stdout-error", "stdout-short", "file-error", "root-close", "encoding-error",
		"cancel-output", "cancel-close", "both-sinks-fail"} {
		t.Run(mode, func(t *testing.T) {
			fixture := writeCalibrationCLIFixture(t)
			opts := calibrationCLIOptions()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := 2
			state := assurance.AssessmentPassed
			switch mode {
			case "passed":
				want = 0
			case "failed":
				state, want = assurance.AssessmentFailed, 1
			case "insufficient":
				state, want = assurance.AssessmentInsufficient, 1
			case "not-assessed":
				state, want = assurance.AssessmentNotAssessed, 1
			case "invalid":
				state = assurance.AssessmentInvalid
			case "operational":
				state = assurance.AssessmentError
			}
			opts.Calibrate = func(ctx context.Context, request assurance.CalibrateRequest) (*assurance.Report, error) {
				require.True(t, request.Calibrate)
				require.True(t, request.Acceptance.AcceptCurrentDecision)
				require.Equal(t, "synthetic-source", request.Acceptance.SourceID)
				require.Equal(t, time.UTC, request.Now.Location())
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.InDelta(t, (10 * time.Minute).Seconds(), time.Until(deadline).Seconds(), 2)
				source, err := os.ReadFile(fixture.spec)
				require.NoError(t, err)
				require.Equal(t, source, request.EvalSource)
				report := &assurance.Report{Kind: assurance.ReportKind, SchemaVersion: assurance.CalibratedReportVersion, State: state}
				if mode == "report-and-error" {
					return report, errors.New("secret-error")
				}
				if mode == "nil-and-error" {
					return nil, errors.New("secret-error")
				}
				if mode == "nil-without-error" {
					return nil, nil
				}
				if mode == "encoding-error" {
					report.Calibration.Credits = new(math.NaN())
				}
				return report, nil
			}
			closed := 0
			opts.CloseRoot = func(root *os.Root) error {
				closed++
				err := root.Close()
				if mode == "root-close" {
					return errors.Join(err, errors.New("secret-error"))
				}
				if mode == "cancel-close" {
					cancel()
				}
				return err
			}
			cmd := newAssureCalibrateCommandWithOptions(opts)
			cmd.SetContext(ctx)
			var output bytes.Buffer
			cmd.SetOut(&output)
			if mode == "stdout-error" || mode == "stdout-short" || mode == "both-sinks-fail" {
				cmd.SetOut(calibrationBrokenWriter{short: mode == "stdout-short"})
			}
			if mode == "cancel-output" {
				cmd.SetOut(calibrationCancelWriter{cancel: cancel, writer: &output})
			}
			path := filepath.Join(t.TempDir(), "report.json")
			if mode == "file-error" || mode == "both-sinks-fail" {
				path = filepath.Join(t.TempDir(), "missing", "report.json")
			} else {
				require.NoError(t, os.WriteFile(path, []byte("old report"), 0o644))
			}
			cmd.SetArgs(fixture.args("-o", path))
			calibrationCLIExit(t, cmd.Execute(), want)
			require.Equal(t, 2, closed)
			switch mode {
			case "nil-and-error", "nil-without-error", "encoding-error":
				require.Empty(t, output.String())
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "old report", string(data))
			case "file-error":
				require.True(t, json.Valid(output.Bytes()))
			case "both-sinks-fail":
				require.Empty(t, output.String())
			default:
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.True(t, json.Valid(data))
				require.True(t, bytes.HasSuffix(data, []byte("\n")))
				stat, err := os.Stat(path)
				require.NoError(t, err)
				if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
					require.Equal(t, os.FileMode(0o600), stat.Mode().Perm())
				}
				if mode != "stdout-error" && mode != "stdout-short" {
					require.Equal(t, data, output.Bytes())
				}
			}
		})
	}
}

func TestCalibrationAtomicReportFailurePreservesDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "existing-directory")
	require.NoError(t, os.Mkdir(destination, 0o700))
	marker := filepath.Join(destination, "old")
	require.NoError(t, os.WriteFile(marker, []byte("old"), 0o600))
	require.Error(t, writeCalibrationReport(destination, []byte("{}\n")))
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "old", string(data))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "staging must be removed")
}

func TestAssureCalibrateCommandsDoNotShareInjectionOrFlags(t *testing.T) {
	first := newAssureCalibrateCommandWithOptions(calibrationCLIOptions())
	second := newAssureCalibrateCommandWithOptions(calibrationCLIOptions())
	require.NoError(t, first.Flags().Set("accept-paid-calls", "true"))
	accepted, err := second.Flags().GetBool("accept-paid-calls")
	require.NoError(t, err)
	require.False(t, accepted)
	require.NoError(t, first.Flags().Set("timeout", "1s"))
	timeout, err := second.Flags().GetDuration("timeout")
	require.NoError(t, err)
	require.Equal(t, 10*time.Minute, timeout)
	parent := newAssureCommand()
	require.NotNil(t, parent.Flags().Lookup("calibrate"))
	require.Nil(t, parent.Flags().Lookup("accept-paid-calls"))
	child, _, err := parent.Find([]string{"calibrate"})
	require.NoError(t, err)
	require.IsType(t, &cobra.Command{}, child)
	require.Nil(t, child.Flags().Lookup("calibrate"))
}

func TestAssureCalibrateOwnedFactoryIsExactConstructionOnly(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	tree, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(file), "cmd_assure_calibrate.go"), nil, 0)
	require.NoError(t, err)
	builders, ownedClients, observers := 0, 0, 0
	ast.Inspect(tree, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				switch selector.Sel.Name {
				case "NewCopilotEngineBuilder":
					builders++
					require.Len(t, call.Args, 2)
					require.Equal(t, "model", call.Args[0].(*ast.Ident).Name)
				case "SharedClient", "Initialize", "Execute", "ListModels":
					t.Errorf("CLI factory must not initialize, execute, query models or share clients: %s", selector.Sel.Name)
				}
			}
		}
		if field, ok := node.(*ast.KeyValueExpr); ok {
			if key, ok := field.Key.(*ast.Ident); ok {
				switch key.Name {
				case "NewCopilotClient":
					ownedClients++
					value, ok := field.Value.(*ast.SelectorExpr)
					require.True(t, ok)
					require.Equal(t, "execution", value.X.(*ast.Ident).Name)
					require.Equal(t, "NewOwnedCopilotClient", value.Sel.Name)
				case "DiagnosticObserver":
					observers++
					require.Equal(t, "observer", field.Value.(*ast.Ident).Name)
				}
			}
		}
		return true
	})
	require.Equal(t, 1, builders)
	require.Equal(t, 1, ownedClients)
	require.Equal(t, 1, observers)
}

func TestAssureCalibrateLegacyRemainsOfflineVersionOne(t *testing.T) {
	for _, legacyFlag := range []bool{false, true} {
		spec, labels := writeAssuranceCommandFixture(t)
		parent := newAssureCommand()
		factories := 0
		opts := calibrationCLIOptions()
		opts.EngineFactory = func(string, func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error) {
			factories++
			return nil, errors.New("must not construct")
		}
		child, _, err := parent.Find([]string{"calibrate"})
		require.NoError(t, err)
		parent.RemoveCommand(child)
		parent.AddCommand(newAssureCalibrateCommandWithOptions(opts))
		var output, notice bytes.Buffer
		parent.SetOut(&output)
		parent.SetErr(&notice)
		args := []string{spec, "--references", labels}
		if legacyFlag {
			args = append(args, "--calibrate")
		}
		parent.SetArgs(args)
		calibrationCLIExit(t, parent.Execute(), 1)
		require.Zero(t, factories)
		var report assurance.Report
		require.NoError(t, json.Unmarshal(output.Bytes(), &report))
		require.Equal(t, "1.0", report.SchemaVersion)
		require.Nil(t, report.Calibration.ExecutionLedger)
		require.Zero(t, report.Calibration.Executions)
		require.Nil(t, report.Calibration.Credits)
		if legacyFlag {
			require.Contains(t, notice.String(), "Calibration unavailable: no paid calls")
		} else {
			require.Empty(t, notice.String())
		}
	}
}

func TestAssureCalibrateSignalAndTimeoutAreCommandLocal(t *testing.T) {
	for _, mode := range []string{"signal", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			fixture := writeCalibrationCLIFixture(t)
			opts := calibrationCLIOptions()
			signals, stopped, producers := 0, 0, 0
			opts.SignalContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
				signals++
				child, cancel := context.WithCancel(ctx)
				if mode == "signal" {
					cancel()
				}
				return child, func() { stopped++; cancel() }
			}
			opts.Calibrate = func(context.Context, assurance.CalibrateRequest) (*assurance.Report, error) {
				producers++
				return nil, nil
			}
			cmd := newAssureCalibrateCommandWithOptions(opts)
			cmd.SetArgs(fixture.args("--timeout=1ns"))
			calibrationCLIExit(t, cmd.Execute(), 2)
			require.Equal(t, 1, signals)
			require.Equal(t, 1, stopped)
			require.Zero(t, producers)
		})
	}
}

type calibrationHookWriter struct {
	hook   func()
	writer io.Writer
}

func (writer calibrationHookWriter) Write(data []byte) (int, error) {
	writer.hook()
	return writer.writer.Write(data)
}

func TestAssureCalibrateRechecksRedirectBeforeFactory(t *testing.T) {
	fixture := writeCalibrationCLIFixture(t)
	opts := calibrationCLIOptions()
	factories := 0
	opts.EngineFactory = func(string, func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error) {
		factories++
		return nil, nil
	}
	cmd := newAssureCalibrateCommandWithOptions(opts)
	var output, notice bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(calibrationHookWriter{
		hook: func() { t.Setenv("COPILOT_PROVIDER_BASE_URL", "https://secret-error.invalid") }, writer: &notice,
	})
	cmd.SetArgs(fixture.args("--accept-paid-calls"))
	calibrationCLIExit(t, cmd.Execute(), 2)
	require.Zero(t, factories)
	report, err := assurance.ParseCalibratedReport(output.Bytes())
	require.NoError(t, err)
	require.NotEmpty(t, *report.Calibration.ExecutionLedger)
	require.Nil(t, report.Calibration.Credits)
}

func TestAssureCalibrateReservedTokenAndParentFlagIndependence(t *testing.T) {
	fixture := writeCalibrationCLIFixture(t)
	opts := calibrationCLIOptions()
	factories := 0
	opts.EngineFactory = func(string, func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error) {
		factories++
		return nil, nil
	}
	parent := newAssureCommand()
	child, _, err := parent.Find([]string{"calibrate"})
	require.NoError(t, err)
	parent.RemoveCommand(child)
	parent.AddCommand(newAssureCalibrateCommandWithOptions(opts))
	var output, notice bytes.Buffer
	parent.SetOut(&output)
	parent.SetErr(&notice)
	parent.SetArgs(append([]string{"calibrate"}, fixture.args()...))
	calibrationCLIExit(t, parent.Execute(), 1)
	require.Zero(t, factories)
	report, err := assurance.ParseCalibratedReport(output.Bytes())
	require.NoError(t, err)
	require.Empty(t, *report.Calibration.ExecutionLedger)
	require.Contains(t, notice.String(), "acknowledgment=false")

	source, err := os.ReadFile(fixture.spec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.rubric, "calibrate"), source, 0o600))
	parent = newAssureCommand()
	output.Reset()
	notice.Reset()
	parent.SetOut(&output)
	parent.SetErr(&notice)
	parent.SetArgs([]string{"./calibrate", "--references", fixture.labels})
	calibrationCLIExit(t, parent.Execute(), 1)
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.Equal(t, "1.0", report.SchemaVersion)
	require.Empty(t, notice.String())
}
