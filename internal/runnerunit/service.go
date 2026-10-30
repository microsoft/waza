// Package runnerunit admits one captured work tuple and executes one attempt.
// Logical identities name admitted input; they do not attest execution.
package runnerunit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
)

// Report is an invocation-local selection/attempt receipt, not a persisted
// terminal result or evidence of cross-worker exactly-once execution.
type Report struct {
	Kind          string                      `json:"kind"`
	Version       string                      `json:"schema_version"`
	Status        string                      `json:"status"`
	Compatibility runnercontract.Response     `json:"compatibility"`
	Requested     *runnercontract.Selection   `json:"requested,omitempty"`
	Resolved      *runnercontract.Selection   `json:"resolved,omitempty"`
	Identity      *runneridentity.Identity    `json:"identity,omitempty"`
	Validation    string                      `json:"validation"`
	AdjacentWork  bool                        `json:"adjacent_work_selected"`
	Mode          string                      `json:"execution_mode,omitempty"`
	Calls         int                         `json:"engine_calls"`
	Observation   *models.RunResult           `json:"observation,omitempty"`
	Diagnostics   []runnercontract.Diagnostic `json:"diagnostics"`
}

func (r Report) ExitCode() int {
	if r.Status == "rejected" {
		if code := r.Compatibility.ExitCode(); code != 0 {
			return code
		}
		return 6
	}
	if r.Status == "execution_error" {
		return 2
	}
	if r.Observation != nil && r.Observation.Status == models.StatusFailed {
		return 1
	}
	return 0
}

type admitted struct {
	request *runnercontract.Request
	spec    *models.EvalSpec
	task    *models.TestCase
	input   *orchestration.ControlledInput
}

// Validate never constructs a runtime or reads input paths.
func Validate(data []byte) Report {
	report, _ := admit(data)
	return report
}

// Admission is adapted from frozen #779 service.go, excluding native lifecycle,
// persistence and terminalization surfaces.
func admit(data []byte) (Report, *admitted) {
	report := Report{
		Kind: "waza.runner-unit", Version: "1.0", Status: "rejected",
		Validation: "not_checked", Compatibility: runnercontract.Negotiate(data),
		Diagnostics: []runnercontract.Diagnostic{},
	}
	if report.Compatibility.Status != "compatible" {
		return report, nil
	}
	req, err := runnercontract.Parse(data)
	if err != nil {
		report.reject(err)
		return report, nil
	}
	report.Requested = &req.Selection
	input, err := resolve(req)
	if err != nil {
		report.reject(err)
		return report, nil
	}
	identity, err := runneridentity.DeriveJSON(data)
	if err != nil {
		report.reject(fmt.Errorf("deriving admitted input identity: %w", err))
		report.Diagnostics[0].Code = "invalid_identity"
		return report, nil
	}
	selected := req.Selection
	report.Resolved = &selected
	report.Identity = identity
	report.Status, report.Validation = "ready", "valid"
	return report, input
}

func (r *Report) reject(err error) {
	r.Status, r.Validation = "rejected", "invalid"
	// Reuse the negotiation boundary's diagnostic bound without inventing
	// terminal classifications or accepting an arbitrarily large error.
	r.Diagnostics = runnercontract.Rejected(err.Error()).Diagnostics
	r.Diagnostics[0].Code = "invalid_selection"
}

func resolve(req *runnercontract.Request) (*admitted, error) {
	if !slices.Contains(req.RequiredCapabilities, "exact-unit") ||
		!slices.Contains(req.RequiredCapabilities, "inline.mock-text") {
		return nil, fmt.Errorf("execution requires required_capabilities exact-unit and inline.mock-text")
	}
	spec, err := models.ParseCapturedEvalSpecOffline([]byte(req.Input.Eval), "eval.yaml")
	if err != nil {
		return nil, fmt.Errorf("captured eval: %w", err)
	}
	task, err := models.ParseCapturedTestCaseOffline([]byte(req.Input.Task), "task.yaml", func(string) ([]byte, error) {
		return nil, fmt.Errorf("inline.mock-text requires inputs.prompt; prompt_file cannot read host files")
	})
	if err != nil {
		return nil, fmt.Errorf("captured task: %w", err)
	}
	if !task.HasCapturedInlinePrompt() {
		return nil, fmt.Errorf("inline.mock-text requires an explicit inputs.prompt; an empty string is allowed")
	}
	if req.Selection.Shard != req.Input.Shard || req.Selection.Task != task.TestID {
		return nil, fmt.Errorf("selection conflicts with declared input shard/task")
	}
	attempts := spec.Config.MaxAttempts
	if attempts == 0 {
		attempts = 1
	}
	if req.Selection.Trial > spec.Config.TrialsPerTask || req.Selection.Attempt > attempts {
		return nil, fmt.Errorf("selection exceeds declared trials_per_task/max_attempts")
	}
	if task.Active != nil && !*task.Active {
		return nil, fmt.Errorf("selected task is disabled; explicitly enable it before dispatch")
	}
	if spec.Config.EngineType != "mock" {
		return nil, fmt.Errorf("only inline.mock-text is implemented; native/paid execution is unavailable even with allow_paid_execution")
	}
	if err := orchestration.ValidateInlineConfiguration(spec, task); err != nil {
		return nil, fmt.Errorf("inline.mock-text: %w; retain waza run for unsupported semantics", err)
	}
	if spec.TasksFrom != "" || spec.Range != [2]int{} || spec.Adversarial != nil ||
		len(spec.Inputs) != 0 || len(spec.Metrics) != 0 || len(task.Requirements) != 0 ||
		len(spec.Config.InstructionFiles) != 0 || len(task.InstructionFiles) != 0 ||
		task.ContextRoot != "" || task.Stimulus.WorkDir != "" ||
		len(task.Stimulus.Metadata) != 0 || len(task.Stimulus.Resources) != 0 ||
		len(task.Stimulus.Environment) != 0 {
		return nil, fmt.Errorf("inline.mock-text cannot consume CSV, inputs, metrics, requirements, context, files, workdir or instruction-file semantics; retain waza run")
	}
	prepared, err := orchestration.NewEvalRunner(config.NewEvalConfig(spec), nil).PrepareInlineInput(task)
	if err != nil {
		return nil, fmt.Errorf("preparing selected input: %w", err)
	}
	return &admitted{request: req, spec: spec, task: task, input: prepared}, nil
}

// EngineFactory must return a non-nil concrete *execution.MockEngine.
// Wrappers and other implementations are rejected without lifecycle calls.
type EngineFactory func(models.Config) (execution.AgentEngine, error)

// Service has no ambient configuration or mutable invocation state. Factory is
// supplied only by embedding hosts/tests; it cannot broaden admission.
type Service struct {
	factory EngineFactory
}

func NewService(factory EngineFactory) *Service {
	if factory == nil {
		factory = func(cfg models.Config) (execution.AgentEngine, error) {
			return execution.NewMockEngine(cfg.ModelID), nil
		}
	}
	return &Service{factory: factory}
}

type onceEngine struct {
	execution.AgentEngine
	calls int
}

func (e *onceEngine) Execute(ctx context.Context, request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	if e.calls != 0 {
		return nil, fmt.Errorf("exact-unit forbids a second engine call")
	}
	e.calls++
	response, err := e.AgentEngine.Execute(ctx, request)
	if err == nil && response != nil && !response.Success {
		return response, fmt.Errorf("engine execution failed: %s", response.ErrorMsg)
	}
	return response, err
}

// Run invokes shared negotiation and exact admission before constructing a
// fresh runtime. Each explicit repeat executes again; there is no deduplication.
func (s *Service) Run(ctx context.Context, data []byte) (report Report) {
	report, input := admit(data)
	if input == nil {
		return report
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(input.request.TimeoutMS)*time.Millisecond)
	defer cancel()
	if err := ctx.Err(); err != nil {
		report.executionError(err)
		return report
	}
	engine, err := s.factory(input.spec.Config)
	if err != nil {
		report.executionError(fmt.Errorf("constructing runtime: %w", err))
		return report
	}
	mock, ok := engine.(*execution.MockEngine)
	if !ok || mock == nil {
		report.executionError(fmt.Errorf("admitting runtime: inline.mock-text requires a non-nil concrete *execution.MockEngine; factory returned %T", engine))
		return report
	}
	report.Mode = "mock"
	if err := ctx.Err(); err != nil {
		report.executionError(err)
		return report
	}
	return execute(ctx, report, input, mock)
}

func (r *Report) executionError(err error) {
	r.Status = "execution_error"
	diagnostic := runnercontract.Rejected(err.Error()).Diagnostics[0]
	diagnostic.Code = "execution_error"
	r.Diagnostics = append(r.Diagnostics, diagnostic)
}

// execute owns lifecycle only after Run admits the concrete mock runtime.
func execute(ctx context.Context, report Report, input *admitted, engine execution.AgentEngine) (result Report) {
	result = report
	observed := &onceEngine{AgentEngine: engine}
	defer func() {
		shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if err := engine.Shutdown(shutdownCtx); err != nil {
			result.executionError(fmt.Errorf("runtime shutdown: %w", err))
		}
		result.Calls = observed.calls
	}()
	if err := engine.Initialize(ctx); err != nil {
		result.executionError(fmt.Errorf("initializing runtime: %w", err))
		return result
	}
	runner := orchestration.NewEvalRunner(config.NewEvalConfig(input.spec), observed)
	run, err := runner.ExecuteInlineAttempt(ctx, input.task, input.input, input.request.Selection.Trial, input.request.Selection.Attempt)
	result.Observation = run
	switch {
	case ctx.Err() != nil:
		result.executionError(ctx.Err())
	case err != nil:
		result.executionError(err)
	case run == nil:
		result.executionError(fmt.Errorf("execution returned no observation"))
	case run.Status != models.StatusPassed && run.Status != models.StatusFailed:
		result.executionError(errors.New("execution did not produce a graded observation: " + run.ErrorMsg))
	default:
		result.Status = "completed"
	}
	return result
}
