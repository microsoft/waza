package runnercontract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"gopkg.in/yaml.v3"
)

type admitted struct {
	request *Request
	spec    *models.EvalSpec
	task    *models.TestCase
	id      *Identity
}

// Service has no ambient configuration, network discovery or mutable run state.
// Each call owns one engine/workspace and never implements automatic retries.
type Service struct {
	execute   func(context.Context, *admitted) (*models.RunResult, error)
	newEngine EngineFactory
}

type EngineFactory func(models.Config) (execution.AgentEngine, error)
type Option func(*Service)

// WithEngineFactory injects a runtime for deterministic lifecycle tests or an
// embedding host. It cannot broaden request admission or advertised capabilities.
func WithEngineFactory(factory EngineFactory) Option {
	return func(s *Service) { s.newEngine = factory }
}

func NewService(options ...Option) *Service {
	s := &Service{newEngine: defaultEngine}
	for _, option := range options {
		option(s)
	}
	s.execute = func(ctx context.Context, input *admitted) (*models.RunResult, error) {
		return executeInline(ctx, input, s.newEngine)
	}
	return s
}

func defaultEngine(cfg models.Config) (execution.AgentEngine, error) {
	switch cfg.EngineType {
	case "mock":
		return execution.NewMockEngine(cfg.ModelID), nil
	case "copilot-sdk":
		return execution.NewCopilotEngineBuilder(cfg.ModelID, &execution.CopilotEngineBuilderOptions{
			OwnClient: true, DisableCommandMocks: true,
		}).Build(), nil
	default:
		return nil, fmt.Errorf("unsupported admitted engine")
	}
}

type inlineEngine struct {
	execution.AgentEngine
	cause     error
	sessionID string
}

func (e *inlineEngine) Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	response, err := e.AgentEngine.Execute(ctx, req)
	if response != nil {
		e.sessionID = response.SessionID
	}
	if err == nil && response != nil && !response.Success {
		message := response.ErrorMsg
		if message == "" {
			message = "engine response did not report successful execution"
		}
		err = errors.New(message)
	}
	e.cause = err
	return response, err
}

type stageError struct {
	stage string
	err   error
}

func (e *stageError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *stageError) Unwrap() error { return e.err }

func negotiate(req *Request, result *Result) error {
	n := &result.Negotiation
	n.RequestedVersions = slices.Clone(req.Versions)
	n.RequiredCapabilities = slices.Clone(req.RequiredCapabilities)
	n.OptionalCapabilities = slices.Clone(req.OptionalCapabilities)
	if !slices.Contains(req.Versions, RequestVersion) {
		return fmt.Errorf("no supported runner version; offer %s", RequestVersion)
	}
	n.Version = RequestVersion
	for _, capability := range req.RequiredCapabilities {
		if !slices.Contains(n.Available, capability) {
			return fmt.Errorf("required capability %q is unavailable; available: %s", capability, strings.Join(n.Available, ", "))
		}
		n.Selected = append(n.Selected, capability)
	}
	for _, capability := range req.OptionalCapabilities {
		if slices.Contains(req.RequiredCapabilities, capability) {
			return fmt.Errorf("capability %q cannot be both required and optional", capability)
		}
		if slices.Contains(n.Available, capability) {
			n.Selected = append(n.Selected, capability)
		} else {
			n.UnavailableOptional = append(n.UnavailableOptional, capability)
		}
	}
	slices.Sort(n.Selected)
	return nil
}

func admit(req *Request) (*admitted, error) {
	evalCanonical, err := canonicalSource(req.Input.Eval)
	if err != nil {
		return nil, fmt.Errorf("captured eval identity: %w", err)
	}
	taskCanonical, err := canonicalSource(req.Input.Task)
	if err != nil {
		return nil, fmt.Errorf("captured task identity: %w", err)
	}
	spec, err := models.ParseCapturedEvalSpecOffline([]byte(req.Input.Eval), "eval.yaml")
	if err != nil {
		return nil, fmt.Errorf("captured eval: %w", err)
	}
	task, err := models.ParseCapturedTestCaseOffline([]byte(req.Input.Task), "task.yaml", func(string) ([]byte, error) {
		return nil, fmt.Errorf("prompt_file is not supported by inline.mock-text; inline inputs.prompt or keep using waza run")
	})
	if err != nil {
		return nil, fmt.Errorf("captured task: %w", err)
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
	if spec.Config.EngineType == "copilot-sdk" &&
		(!req.AllowPaidExecution || !slices.Contains(req.RequiredCapabilities, "inline.native-text")) {
		return nil, fmt.Errorf("native execution requires allow_paid_execution: true and required capability inline.native-text; this may incur model usage")
	}
	if err := orchestration.ValidateInlineConfiguration(spec, task); err != nil {
		return nil, fmt.Errorf("inline text profile: %w; retain waza run for unsupported local semantics", err)
	}
	if spec.TasksFrom != "" || spec.Range != [2]int{} || spec.Adversarial != nil ||
		len(spec.Inputs) != 0 || len(spec.Metrics) != 0 || len(task.Requirements) != 0 ||
		len(spec.Config.InstructionFiles) != 0 || len(task.InstructionFiles) != 0 ||
		task.ContextRoot != "" || task.Stimulus.WorkDir != "" ||
		len(task.Stimulus.Metadata) != 0 {
		return nil, fmt.Errorf("inline text cannot preserve CSV, inputs, metrics, requirements, context, workdir or instruction-file semantics; retain waza run")
	}
	seen := map[string]bool{}
	for _, resource := range task.Stimulus.Resources {
		p := resource.Location
		if resource.Body == "" || p == "" || path.IsAbs(p) || path.Clean(p) != p ||
			p == "." || p == ".." || strings.HasPrefix(p, "../") ||
			strings.ContainsAny(p, "\\:\x00") || seen[strings.ToLower(p)] {
			return nil, fmt.Errorf("inline.mock-text requires unique canonical relative resources with nonempty inline content")
		}
		seen[strings.ToLower(p)] = true
	}
	for p := range seen {
		for parent := path.Dir(p); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return nil, fmt.Errorf("inline resources cannot conflict as file/directory paths")
			}
		}
	}
	// Canonicalize authored sources, not models with lossy omission/default tags.
	// Captured parsers above already rejected aliases, unknown fields and floats.
	id, err := derive(req, evalCanonical, taskCanonical)
	if err != nil {
		return nil, err
	}
	return &admitted{request: req, spec: spec, task: task, id: id}, nil
}

func canonicalSource(source string) (any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(source), &doc); err != nil {
		return nil, err
	}
	var portable func(*yaml.Node) error
	portable = func(node *yaml.Node) error {
		if node.Kind == yaml.AliasNode || node.Anchor != "" {
			return fmt.Errorf("identity source cannot contain aliases or anchors")
		}
		if node.Tag == "!!int" {
			var value int64
			if err := node.Decode(&value); err != nil || value < -2147483648 || value > 2147483647 {
				return fmt.Errorf("identity source integers must fit signed 32-bit on every supported OS")
			}
		}
		for _, child := range node.Content {
			if err := portable(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := portable(&doc); err != nil {
		return nil, err
	}
	var value any
	if err := doc.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// Run converges every recoverable termination on one validated terminal result.
// SIGKILL, host loss and a broken output channel cannot promise an envelope.
func (s *Service) Run(ctx context.Context, data []byte) (result Result) {
	result = baseResult()
	defer func() {
		if recovered := recover(); recovered != nil {
			result.fail(InfrastructureFailure, "execution", "runner_panic", fmt.Sprintf("runner panic: %v", recovered))
		}
		if err := ValidateResult(&result); err != nil {
			original := result
			result = baseResult()
			result.Requested, result.Resolved, result.SelectionValid = original.Requested, original.Resolved, original.SelectionValid
			result.Identity, result.Negotiation = original.Identity, original.Negotiation
			result.fail(InfrastructureFailure, "terminalization", "invalid_envelope", err.Error())
			if ValidateResult(&result) != nil {
				result = baseResult()
				result.fail(InfrastructureFailure, "terminalization", "invalid_envelope", err.Error())
			}
		}
	}()
	req, err := Parse(data)
	if err != nil {
		result.fail(InvalidRequest, "admission", "invalid_request", err.Error())
		return result
	}
	result.Requested = &req.Selection
	if err := negotiate(req, &result); err != nil {
		result.fail(UnsupportedCapability, "negotiation", "incompatible_runner", err.Error())
		return result
	}
	input, err := admit(req)
	if err != nil {
		result.fail(InvalidRequest, "selection", "invalid_selection", err.Error())
		return result
	}
	result.Resolved, result.SelectionValid, result.Identity = &req.Selection, true, input.id
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutMS)*time.Millisecond)
	defer cancel()
	if err := ctx.Err(); err != nil {
		result.contextFailure(err)
		return result
	}
	run, err := s.execute(ctx, input)
	result.Observation = run
	if ctx.Err() != nil {
		result.contextFailure(ctx.Err())
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		result.contextFailure(err)
	} else if err != nil {
		boundary := "execution"
		var staged *stageError
		if errors.As(err, &staged) {
			boundary = staged.stage
		}
		result.fail(InfrastructureFailure, boundary, "execution_error", err.Error())
	} else if run == nil {
		result.fail(InfrastructureFailure, "execution", "missing_observation", "execution returned no observation")
	} else {
		switch run.Status {
		case models.StatusPassed:
			if run.ErrorMsg != "" || len(run.Validations) == 0 {
				result.fail(InfrastructureFailure, "execution", "incomplete_observation", "passed observation lacks complete validation or contains an error")
				break
			}
			result.Outcome, result.ExitCode, result.Boundary = Success, Success.ExitCode(), "none"
		case models.StatusFailed:
			result.fail(EvaluationFailure, "grading", "evaluation_failed", "one or more checks failed")
		default:
			message := run.ErrorMsg
			if message == "" {
				message = "execution did not produce a graded observation"
			}
			result.fail(InfrastructureFailure, "execution", "execution_failed", message)
		}
	}
	return result
}

func (r *Result) contextFailure(err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		r.fail(Timeout, "execution", "deadline_exceeded", err.Error())
	} else {
		r.fail(Canceled, "execution", "canceled", err.Error())
	}
}

func executeInline(ctx context.Context, input *admitted, factory EngineFactory) (run *models.RunResult, err error) {
	timeoutSec := input.spec.Config.TimeoutSec
	if input.task.TimeoutSec != nil {
		timeoutSec = *input.task.TimeoutSec
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	if factory == nil {
		return nil, fmt.Errorf("missing runtime factory")
	}
	engine, err := factory(input.spec.Config)
	if err != nil {
		return nil, err
	}
	if engine == nil {
		return nil, fmt.Errorf("runtime factory returned no engine")
	}
	observedEngine := &inlineEngine{AgentEngine: engine}
	runner := orchestration.NewEvalRunner(config.NewEvalConfig(input.spec), observedEngine)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if shutdownErr := engine.Shutdown(shutdownCtx); shutdownErr != nil {
			err = errors.Join(err, &stageError{stage: "shutdown", err: shutdownErr})
		}
		if run != nil {
			if run.SessionDigest.SessionID == "" {
				run.SessionDigest.SessionID = observedEngine.sessionID
			}
			if run.SessionDigest.SessionID != "" {
				run.SessionDigest.Usage = engine.SessionUsage(run.SessionDigest.SessionID)
				run.Usage = run.SessionDigest.Usage
			}
		}
	}()
	if err := engine.Initialize(ctx); err != nil {
		return nil, err
	}
	observation, err := runner.ExecuteInlineAttempt(ctx, input.task, models.EvidenceOrigin{
		EvalID: input.id.Run, TaskID: input.task.TestID,
		RunNumber: input.request.Selection.Trial, AttemptCount: input.request.Selection.Attempt,
	})
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return observation, ctx.Err()
	}
	if observedEngine.cause != nil {
		return observation, observedEngine.cause
	}
	return observation, nil
}

// Write emits exactly one JSON document, only after validating the full result.
// The caller owns handling write/close errors; an unwritable sink is not success.
func Write(w io.Writer, result *Result) error {
	if err := ValidateResult(result); err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func Rejected(message string) Result {
	result := baseResult()
	result.fail(InvalidRequest, "admission", "input_error", message)
	return result
}

// Interrupted represents cancellation while waiting for a complete input, before
// a request/selection was available. It must not invent a resolved identity.
func Interrupted(err error) Result {
	result := baseResult()
	result.contextFailure(err)
	return result
}
