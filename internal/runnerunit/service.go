// Package runnerunit admits one captured work tuple and executes one attempt.
// Logical identities name admitted input; they do not attest execution.
package runnerunit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
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
	binding       *runnerterminal.Binding
	failure       error
	boundary      string
	localOutcome  *models.EvaluationOutcome
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

// AdmittedBinding returns the caller binding Submit would use for data, or
// false when the request is not admitted.
func AdmittedBinding(data []byte) (artifactmanifest.Binding, bool) {
	report, input := admit(data)
	if input == nil || report.binding.Identity == nil {
		return artifactmanifest.Binding{}, false
	}
	return artifactmanifest.Binding{Identity: *report.binding.Identity, Selection: report.binding.Selection,
		ExecutionMode: report.binding.ExecutionMode, GraderIDs: slices.Clone(report.binding.GraderIDs)}, true
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
	mode := input.spec.Config.EngineType
	if mode == "copilot-sdk" {
		mode = "copilot"
	}
	report.binding = &runnerterminal.Binding{
		Selection: selected, Identity: identity, GraderIDs: graderIDs(input),
		ExecutionMode: mode,
	}
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
	mockProfile := slices.Contains(req.RequiredCapabilities, "inline.mock-text")
	copilotProfile := slices.Contains(req.RequiredCapabilities, "inline.copilot-text")
	if !slices.Contains(req.RequiredCapabilities, "exact-unit") || mockProfile == copilotProfile {
		return nil, fmt.Errorf("execution requires exact-unit and exactly one required profile: inline.mock-text or inline.copilot-text")
	}
	profile, engineType := "inline.mock-text", "mock"
	if copilotProfile {
		profile, engineType = "inline.copilot-text", "copilot-sdk"
	}
	spec, err := models.ParseCapturedEvalSpecOffline([]byte(req.Input.Eval), "eval.yaml")
	if err != nil {
		return nil, fmt.Errorf("captured eval: %w", err)
	}
	if copilotProfile && spec.CapturedMetricsExplicitNull() {
		return nil, fmt.Errorf("inline.copilot-text does not accept explicit metrics: null; omit the field or provide an array")
	}
	task, err := models.ParseCapturedTestCaseOffline([]byte(req.Input.Task), "task.yaml", func(string) ([]byte, error) {
		return nil, fmt.Errorf("%s requires inputs.prompt; prompt_file cannot read host files", profile)
	})
	if err != nil {
		return nil, fmt.Errorf("captured task: %w", err)
	}
	if !task.HasCapturedInlinePrompt() {
		return nil, fmt.Errorf("%s requires an explicit inputs.prompt; an empty string is allowed", profile)
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
	if spec.Config.EngineType != engineType {
		return nil, fmt.Errorf("%s requires config.executor: %s", profile, engineType)
	}
	if copilotProfile && (!req.AllowPaidExecution || strings.TrimSpace(spec.Config.ModelID) == "") {
		return nil, fmt.Errorf("inline.copilot-text requires allow_paid_execution: true and an explicit nonblank config.model")
	}
	if err := orchestration.ValidateInlineConfiguration(spec, task); err != nil {
		return nil, fmt.Errorf("%s: %w; retain waza run for unsupported semantics", profile, err)
	}
	if spec.TasksFrom != "" || spec.Range != [2]int{} || spec.Adversarial != nil ||
		len(spec.Inputs) != 0 || len(task.Requirements) != 0 ||
		len(spec.Config.InstructionFiles) != 0 || len(task.InstructionFiles) != 0 ||
		task.ContextRoot != "" || task.Stimulus.WorkDir != "" ||
		len(task.Stimulus.Metadata) != 0 || len(task.Stimulus.Resources) != 0 ||
		len(task.Stimulus.Environment) != 0 {
		return nil, fmt.Errorf("%s cannot consume CSV, inputs, requirements, context, files, workdir or instruction-file semantics; retain waza run", profile)
	}
	prepared, err := orchestration.NewEvalRunner(config.NewEvalConfig(spec), nil).PrepareInlineInput(task)
	if err != nil {
		return nil, fmt.Errorf("preparing selected input: %w", err)
	}
	return &admitted{request: req, spec: spec, task: task, input: prepared}, nil
}

// EngineFactory must return a non-nil concrete engine matching the admitted profile.
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
			if cfg.EngineType == "copilot-sdk" {
				return execution.NewCopilotEngineBuilder(cfg.ModelID, &execution.CopilotEngineBuilderOptions{
					OwnClient: true, DisableCommandMocks: true,
					DisableAmbientDiscovery: true,
					// Diagnostic mode propagates lifecycle failures to the
					// terminal envelope, without a second logging sink.
					DiagnosticObserver: func(execution.ExecutionDiagnostic) error { return nil },
				}).Build(), nil
			}
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
	if native, ok := e.AgentEngine.(*execution.CopilotEngine); ok {
		stop := interruptRunnerRuntime(ctx, native.ForceStopRunnerText)
		defer stop()
	}
	response, err := e.AgentEngine.Execute(ctx, request)
	if err == nil && response != nil && !response.Success {
		return response, fmt.Errorf("engine execution failed: %s", response.ErrorMsg)
	}
	return response, err
}

func interruptRunnerRuntime(ctx context.Context, interrupt func()) func() {
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(finished)
		interrupt()
	})
	return func() {
		if !stop() {
			<-finished
		}
	}
}

// Run invokes shared negotiation and exact admission before constructing a
// fresh runtime. Each explicit repeat executes again; there is no deduplication.
func (s *Service) Run(ctx context.Context, data []byte) (report Report) {
	return s.run(ctx, data, false)
}

func (s *Service) run(ctx context.Context, data []byte, local bool) (report Report) {
	report, input := admit(data)
	if input == nil {
		return report
	}
	return s.executeAdmitted(ctx, report, input, local)
}

func (s *Service) executeAdmitted(ctx context.Context, report Report, input *admitted, local bool) Report {
	if local && input.spec.Config.EngineType != "mock" {
		report.executionError("startup", fmt.Errorf("local compatibility execution supports only inline.mock-text"))
		return report
	}
	if local && len(input.spec.Metrics) != 0 {
		report.executionError("startup", fmt.Errorf("local compatibility execution cannot project uncomputed eval metrics"))
		return report
	}
	// Local orchestration limits Execute, not grading; external requests retain
	// their end-to-end deadline, including grading and lifecycle.
	if !local {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(input.request.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		report.executionError("startup", err)
		return report
	}
	engine, err := s.factory(input.spec.Config)
	if err != nil {
		report.executionError("startup", errors.Join(fmt.Errorf("constructing runtime: %w", err), ctx.Err()))
		return report
	}
	if input.spec.Config.EngineType == "copilot-sdk" {
		native, ok := engine.(*execution.CopilotEngine)
		if !ok || native == nil {
			report.executionError("startup", errors.Join(fmt.Errorf("admitting runtime: inline.copilot-text requires a non-nil concrete *execution.CopilotEngine; factory returned %T", engine), ctx.Err()))
			return report
		}
		if err := native.ValidateRunnerTextRuntime(); err != nil {
			report.executionError("startup", err)
			return report
		}
		report.Mode = "copilot"
	} else {
		mock, ok := engine.(*execution.MockEngine)
		if !ok || mock == nil {
			report.executionError("startup", errors.Join(fmt.Errorf("admitting runtime: inline.mock-text requires a non-nil concrete *execution.MockEngine; factory returned %T", engine), ctx.Err()))
			return report
		}
		report.Mode = "mock"
	}
	if err := ctx.Err(); err != nil {
		report.executionError("startup", err)
		return report
	}
	return executeWithProjection(ctx, report, input, engine, local)
}

func (r *Report) executionError(boundary string, err error) {
	r.Status = "execution_error"
	r.failure = errors.Join(r.failure, err)
	r.boundary = boundary
	diagnostic := runnercontract.Rejected(err.Error()).Diagnostics[0]
	diagnostic.Code = "execution_error"
	r.Diagnostics = append(r.Diagnostics, diagnostic)
}

// execute owns lifecycle only after Run admits the concrete mock runtime.
func execute(ctx context.Context, report Report, input *admitted, engine execution.AgentEngine) Report {
	return executeWithProjection(ctx, report, input, engine, false)
}

func executeWithProjection(ctx context.Context, report Report, input *admitted, engine execution.AgentEngine, local bool) (result Report) {
	result = report
	started := time.Now()
	ctx, usage := execution.NewUsageScope(ctx)
	observed := &onceEngine{AgentEngine: engine}
	interrupt := func() {}
	if native, ok := engine.(*execution.CopilotEngine); ok {
		interrupt = native.ForceStopRunnerText
	}
	defer interruptRunnerRuntime(ctx, interrupt)()
	defer func() {
		shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		stopShutdownInterrupt := interruptRunnerRuntime(shutdownCtx, interrupt)
		if err := engine.Shutdown(shutdownCtx); err != nil {
			result.executionError("shutdown", fmt.Errorf("runtime shutdown: %w", err))
		}
		stopShutdownInterrupt()
		if err := shutdownCtx.Err(); err != nil {
			result.executionError("shutdown", fmt.Errorf("runtime shutdown deadline: %w", err))
		}
		result.Calls = observed.calls
		if err := ctx.Err(); err != nil && !errors.Is(result.failure, err) {
			result.executionError("shutdown", err)
		}
	}()
	if err := engine.Initialize(ctx); err != nil {
		result.executionError("startup", errors.Join(fmt.Errorf("initializing runtime: %w", err), ctx.Err()))
		return result
	}
	runner := orchestration.NewEvalRunner(config.NewEvalConfig(input.spec), observed)
	run, err := runner.ExecuteInlineAttempt(ctx, input.task, input.input, input.request.Selection.Trial, input.request.Selection.Attempt)
	result.Observation = run
	switch {
	case ctx.Err() != nil:
		result.executionError("execution", errors.Join(ctx.Err(), err))
	case err != nil:
		result.executionError("execution", err)
	case run == nil:
		result.executionError("execution", fmt.Errorf("execution returned no observation"))
	case run.Status != models.StatusPassed && run.Status != models.StatusFailed:
		result.executionError("execution", errors.New("execution did not produce a graded observation: "+run.ErrorMsg))
	default:
		result.Status = "completed"
	}
	if local && run != nil {
		result.localOutcome, err = runner.ProjectInlineOutcome(input.task, run, started, usage.Snapshot())
		if err != nil {
			result.executionError("projection", err)
		}
	}
	return result
}

// RunLocal projects the same admitted execution used by RunTerminal into the
// historical result writer contract, not the terminal transport envelope.
func (s *Service) RunLocal(ctx context.Context, data []byte) (*models.EvaluationOutcome, error) {
	report := s.run(ctx, data, true)
	// Local projection retains observed execution-error artifacts. Terminal
	// callers keep typed causes; the local CLI applies the artifact's exit policy.
	if report.localOutcome != nil && report.boundary == "execution" &&
		report.Observation != nil && report.Observation.Status == models.StatusError {
		return report.localOutcome, nil
	}
	if report.Status != "completed" || report.failure != nil || report.localOutcome == nil {
		if report.failure != nil {
			return nil, fmt.Errorf("local runner execution: %w", report.failure)
		}
		return nil, fmt.Errorf("local runner rejected input: %v %v", report.Compatibility.Diagnostics, report.Diagnostics)
	}
	return report.localOutcome, nil
}
