package assurance

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

type calibrationCapture struct {
	calls           int
	identity, model string
}

func (capture *calibrationCapture) Execute(_ context.Context, request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	capture.calls++
	identity, err := calibrationRequestIdentity(request)
	if err != nil {
		return nil, err
	}
	capture.identity, capture.model = identity, request.ModelID
	return nil, errCalibrationCapture
}

func calibrationRequestIdentity(request *execution.ExecutionRequest) (string, error) {
	if request == nil || len(request.Tools) != 2 || request.PermissionHandler == nil ||
		request.ToolPolicy == nil || request.ToolPolicy.Mode != execution.ToolPolicyAllowList ||
		!request.NoSkills || !request.EphemeralSession || !request.SkipWorkspaceCapture ||
		request.SessionID != "" || request.WorkspaceDir != "" || request.WorkDir != "" ||
		request.SkillName != "" || len(request.SkillPaths) != 0 || len(request.Instructions) != 0 ||
		len(request.Resources) != 0 || len(request.GitResources) != 0 || request.SourceDir != "" ||
		request.SuppressSkillBody || request.TriggerSkillRouting || len(request.Context) != 0 ||
		request.MessageMode != execution.MessageModeEnqueue || !request.Streaming {
		return "", errors.New("assurance: native judge request violates the independent boundary")
	}
	names := []string{"set_waza_grade_pass", "set_waza_grade_fail"}
	filter := []string{"custom:set_waza_grade_pass", "custom:set_waza_grade_fail"}
	if !slices.Equal(calibrationModels(request.ToolPolicy.SessionToolFilter()), calibrationModels(filter)) {
		return "", errors.New("assurance: native judge tool policy differs from its callbacks")
	}
	type toolIdentity struct {
		Name, Description string
		Parameters        any
	}
	tools := make([]toolIdentity, 2)
	for i, tool := range request.Tools {
		if tool.Name != names[i] || tool.Handler == nil || !request.ToolPolicy.IsAllowed("custom:"+tool.Name) {
			return "", errors.New("assurance: native judge callbacks are unavailable")
		}
		decision, err := request.PermissionHandler(&copilot.PermissionRequestCustomTool{ToolName: tool.Name}, copilot.PermissionInvocation{})
		if _, ok := decision.(*rpc.PermissionDecisionApproveOnce); !ok || err != nil {
			return "", errors.New("assurance: native callback permission was not approved")
		}
		tools[i] = toolIdentity{tool.Name, tool.Description, tool.Parameters}
	}
	for _, name := range []string{"builtin:bash", "builtin:view", "builtin:edit", "mcp:set_waza_grade_pass", "builtin:web_fetch"} {
		if request.ToolPolicy.IsAllowed(name) {
			return "", errors.New("assurance: native judge policy allows unrelated capabilities")
		}
	}
	for _, permission := range []copilot.PermissionRequest{
		nil, (*copilot.PermissionRequestCustomTool)(nil),
		&copilot.PermissionRequestCustomTool{ToolName: "other"},
		&copilot.PermissionRequestShell{}, &copilot.PermissionRequestRead{}, &copilot.PermissionRequestWrite{},
		&copilot.PermissionRequestURL{}, &copilot.PermissionRequestMemory{},
		&copilot.PermissionRequestWorkflow{Name: "custom:set_waza_grade_pass"},
		&copilot.PermissionRequestHook{ToolName: "custom:set_waza_grade_pass"},
		&copilot.PermissionRequestMCP{ServerName: "custom", ToolName: "set_waza_grade_pass"},
	} {
		decision, err := request.PermissionHandler(permission, copilot.PermissionInvocation{})
		if _, ok := decision.(*rpc.PermissionDecisionReject); !ok || err != nil {
			return "", errors.New("assurance: native judge permission allows unrelated capabilities")
		}
	}
	digest, err := evidence.JSONDigest(struct {
		Message, Model, Reasoning string
		Tools                     []toolIdentity
		Policy                    []string
	}{request.Message, request.ModelID, request.ReasoningEffort, tools, filter})
	return digest.SHA256, err
}

type calibrationExecution struct {
	mu             sync.Mutex
	engine         CalibrationEngine
	job            calibrationJob
	entry          JudgeExecution
	sessionID      string
	calls          int
	observerClosed bool
}

func (run *calibrationExecution) recordDiagnostic(stage execution.DiagnosticStage, code execution.DiagnosticCode) {
	run.mu.Lock()
	run.entry.Diagnostics = append(run.entry.Diagnostics, JudgeDiagnostic{Stage: string(stage), Code: string(code)})
	run.mu.Unlock()
}

func (run *calibrationExecution) diagnostic(stage execution.DiagnosticStage, code execution.DiagnosticCode) error {
	run.recordDiagnostic(stage, code)
	return &execution.DiagnosticError{Diagnostic: execution.ExecutionDiagnostic{Stage: stage, Code: code}}
}

func (run *calibrationExecution) observe(diagnostic execution.ExecutionDiagnostic) error {
	stage, code, valid := calibrationDiagnostic(diagnostic)
	run.mu.Lock()
	if run.observerClosed {
		run.mu.Unlock()
		return &execution.DiagnosticError{Diagnostic: execution.ExecutionDiagnostic{Stage: execution.StageExecute, Code: execution.CodeClosed}}
	}
	// Even expected-cancellation and cleanup diagnostics are evidence of an
	// operationally non-clean judge; a correct rejection cannot erase them.
	run.entry.Diagnostics = append(run.entry.Diagnostics, JudgeDiagnostic{Stage: string(stage), Code: string(code)})
	run.mu.Unlock()
	if !valid {
		return &execution.DiagnosticError{Diagnostic: execution.ExecutionDiagnostic{Stage: stage, Code: code}}
	}
	return nil
}

func calibrationDiagnostic(diagnostic execution.ExecutionDiagnostic) (execution.DiagnosticStage, execution.DiagnosticCode, bool) {
	switch diagnostic.Stage {
	case execution.StageInitialize, execution.StageExecute, execution.StageDisconnect, execution.StageEphemeralDelete,
		execution.StageUsageMetrics, execution.StageShutdownUsage, execution.StageShutdownDelete, execution.StageClientStop,
		execution.StageCommandMockCleanup, execution.StageGitCleanup, execution.StageWorkspaceCleanup:
	default:
		return execution.StageExecute, execution.CodeObserverFailed, false
	}
	switch diagnostic.Code {
	case execution.CodeProvider, execution.CodeStart, execution.CodeAuth, execution.CodeCreate, execution.CodeResume,
		execution.CodeSend, execution.CodeExecute, execution.CodeCleanup, execution.CodeExpectedSkillCancellation,
		execution.CodeUsageMissingOrPartial, execution.CodeModelAttributionMissing, execution.CodeShutdownRPC,
		execution.CodeHistory, execution.CodeFallbackDisconnect, execution.CodeObserverFailed, execution.CodeClosed,
		execution.CodeSessionBusy:
		return diagnostic.Stage, diagnostic.Code, true
	default:
		return execution.StageExecute, execution.CodeObserverFailed, false
	}
}

func calibrationErrorDiagnostics(err error) []execution.ExecutionDiagnostic {
	var diagnostics []execution.ExecutionDiagnostic
	var visit func(error)
	visit = func(cause error) {
		if cause == nil {
			return
		}
		if diagnostic, ok := cause.(*execution.DiagnosticError); ok {
			if diagnostic != nil {
				if stage, code, valid := calibrationDiagnostic(diagnostic.Diagnostic); valid {
					diagnostics = append(diagnostics, execution.ExecutionDiagnostic{Stage: stage, Code: code})
				}
			}
			return
		}
		switch cause := cause.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range cause.Unwrap() {
				visit(child)
			}
		case interface{ Unwrap() error }:
			visit(cause.Unwrap())
		default:
			var diagnostic *execution.DiagnosticError
			if errors.As(cause, &diagnostic) && diagnostic != nil {
				if stage, code, valid := calibrationDiagnostic(diagnostic.Diagnostic); valid {
					diagnostics = append(diagnostics, execution.ExecutionDiagnostic{Stage: stage, Code: code})
				}
			}
		}
	}
	visit(err)
	return diagnostics
}

func (run *calibrationExecution) recordError(err error, stage execution.DiagnosticStage, code execution.DiagnosticCode) error {
	diagnostics := calibrationErrorDiagnostics(err)
	if len(diagnostics) == 0 {
		return run.diagnostic(stage, code)
	}
	for _, diagnostic := range diagnostics {
		run.recordDiagnostic(diagnostic.Stage, diagnostic.Code)
	}
	return &execution.DiagnosticError{Diagnostic: diagnostics[0]}
}

func (run *calibrationExecution) recordFailure(err error, stage execution.DiagnosticStage, code execution.DiagnosticCode) {
	diagnostics := calibrationErrorDiagnostics(err)
	if len(diagnostics) == 0 {
		run.recordDiagnostic(stage, code)
		return
	}
	for _, diagnostic := range diagnostics {
		run.recordDiagnostic(diagnostic.Stage, diagnostic.Code)
	}
}

func (run *calibrationExecution) Execute(ctx context.Context, request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	identity, err := calibrationRequestIdentity(request)
	if err != nil || identity != run.job.requestIdentity || request.ModelID != run.job.requestedModel {
		return nil, run.diagnostic(execution.StageExecute, execution.CodeExecute)
	}
	if err := ctx.Err(); err != nil {
		return nil, run.recordError(err, execution.StageExecute, execution.CodeExecute)
	}
	run.mu.Lock()
	run.calls++
	if run.calls != 1 {
		run.mu.Unlock()
		return nil, run.diagnostic(execution.StageExecute, execution.CodeExecute)
	}
	run.mu.Unlock()
	// Only this actual native generation's handlers are wrapped. Capture
	// handlers are never retained or shared with a paid execution.
	for i := range request.Tools {
		handler := request.Tools[i].Handler
		request.Tools[i].Handler = func(invocation copilot.ToolInvocation) (copilot.ToolResult, error) {
			run.mu.Lock()
			if run.observerClosed {
				run.mu.Unlock()
				return copilot.ToolResult{}, &execution.DiagnosticError{Diagnostic: execution.ExecutionDiagnostic{
					Stage: execution.StageExecute, Code: execution.CodeClosed,
				}}
			}
			run.entry.Callbacks++
			run.mu.Unlock()
			result, err := handler(invocation)
			if err != nil {
				return result, run.recordError(err, execution.StageExecute, execution.CodeExecute)
			}
			return result, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, run.recordError(err, execution.StageExecute, execution.CodeExecute)
	}
	run.mu.Lock()
	run.entry.Executed = true
	run.mu.Unlock()
	response, executeErr := run.engine.Execute(ctx, request)
	var sanitizedErr error
	if executeErr != nil {
		sanitizedErr = run.recordError(executeErr, execution.StageExecute, execution.CodeExecute)
	}
	if response != nil {
		run.sessionID = response.SessionID
		copy := *response
		if copy.ErrorMsg != "" || !copy.Success {
			diagnostic := run.diagnostic(execution.StageExecute, execution.CodeExecute)
			if copy.ErrorMsg != "" {
				copy.ErrorMsg = diagnostic.Error()
			}
			if sanitizedErr == nil {
				sanitizedErr = diagnostic
			}
		}
		response = &copy
	}
	if ctx.Err() != nil {
		sanitizedErr = run.recordError(ctx.Err(), execution.StageExecute, execution.CodeExecute)
	}
	return response, sanitizedErr
}

func executeCalibrationJob(ctx context.Context, factory func(string, func(execution.ExecutionDiagnostic) error) (CalibrationEngine, error),
	job calibrationJob, store *calibrationRubricStore, observation ChallengeObservation, requirement RequirementAssessment,
) (JudgeExecution, *models.GraderResults) {
	run := &calibrationExecution{job: job, entry: JudgeExecution{
		CaseID: observation.CaseID, TaskID: requirement.TaskID, RequirementID: requirement.RequirementID,
		Check: requirement.Check, RequestedModel: job.requestedModel,
		State: AssessmentError, Reason: "judge_operational_failure", Diagnostics: []JudgeDiagnostic{},
	}}
	if ctx.Err() != nil {
		run.recordFailure(ctx.Err(), execution.StageExecute, execution.CodeExecute)
		return run.entry, nil
	}
	engine, factoryErr := factory(job.requestedModel, run.observe)
	if engine != nil {
		value := reflect.ValueOf(engine)
		if value.Kind() == reflect.Pointer && value.IsNil() {
			engine = nil
		}
	}
	run.engine = engine
	var result *models.GraderResults
	if factoryErr != nil {
		run.recordFailure(factoryErr, execution.StageInitialize, execution.CodeProvider)
	}
	if ctx.Err() != nil {
		run.recordFailure(ctx.Err(), execution.StageInitialize, execution.CodeStart)
	}
	if engine == nil {
		run.recordDiagnostic(execution.StageInitialize, execution.CodeProvider)
	} else {
		func() {
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer cancel()
				if err := engine.Shutdown(shutdownCtx); err != nil {
					run.recordFailure(err, execution.StageClientStop, execution.CodeCleanup)
				}
				if shutdownCtx.Err() != nil {
					run.recordFailure(shutdownCtx.Err(), execution.StageClientStop, execution.CodeCleanup)
				}
				run.finalizeAccounting()
				if ctx.Err() != nil {
					run.recordFailure(ctx.Err(), execution.StageExecute, execution.CodeExecute)
				}
			}()
			if factoryErr != nil || run.hasDiagnostics() {
				return
			}
			if ctx.Err() != nil {
				run.recordFailure(ctx.Err(), execution.StageInitialize, execution.CodeStart)
				return
			}
			initializeErr := engine.Initialize(ctx)
			run.entry.Initialized = initializeErr == nil
			if initializeErr != nil {
				run.recordFailure(initializeErr, execution.StageInitialize, execution.CodeStart)
			}
			if ctx.Err() != nil {
				run.recordFailure(ctx.Err(), execution.StageInitialize, execution.CodeStart)
			}
			if initializeErr != nil || ctx.Err() != nil {
				return
			}
			if !run.hasDiagnostics() {
				results, err := runCalibrationGrade(ctx, job, store, run)
				if err != nil {
					run.recordFailure(err, execution.StageExecute, execution.CodeExecute)
				}
				if actual, ok := results[job.config.Identifier]; ok {
					// Preserve the actual native verdict/score/feedback. Prompt
					// details carry source text and private paths, not evidence.
					actual.Details = nil
					result = &actual
				}
			}
		}()
	}
	run.mu.Lock()
	run.observerClosed = true
	hasDiagnostics := len(run.entry.Diagnostics) != 0
	run.entry.Diagnostics = slices.Clone(run.entry.Diagnostics)
	run.mu.Unlock()
	switch {
	case hasDiagnostics:
		run.entry.State, run.entry.Reason = AssessmentError, "judge_operational_failure"
	case !run.entry.Executed || !run.entry.Initialized:
		run.entry.State, run.entry.Reason = AssessmentError, "judge_execution_unavailable"
	case run.entry.Callbacks == 0 || result == nil:
		run.entry.State, run.entry.Reason = AssessmentInsufficient, "grade_callback_missing"
	case !run.entry.UsageComplete:
		run.entry.State, run.entry.Reason = AssessmentInsufficient, "judge_accounting_incomplete"
	case !run.entry.EventModelAttributionComplete || !run.entry.AccountingModelAttributionComplete:
		run.entry.State, run.entry.Reason = AssessmentInsufficient, "judge_model_attribution_incomplete"
	case !slices.Equal(run.entry.EventModels, []string{job.requestedModel}) ||
		!slices.Equal(run.entry.AccountingModels, []string{job.requestedModel}):
		run.entry.State, run.entry.Reason = AssessmentInvalid, "observed_model_differs_from_plan"
	default:
		run.entry.State, run.entry.Reason = AssessmentPassed, "usable_judge_execution"
	}
	return run.entry, result
}

func (run *calibrationExecution) hasDiagnostics() bool {
	run.mu.Lock()
	defer run.mu.Unlock()
	return len(run.entry.Diagnostics) != 0
}

func (run *calibrationExecution) finalizeAccounting() {
	if run.sessionID == "" {
		return
	}
	usage := run.engine.SessionUsage(run.sessionID)
	accounting := run.engine.SessionUsageObservation(run.sessionID)
	events := run.engine.SessionEventModelObservation(run.sessionID)
	run.entry.EventModels = calibrationModels(events.Models)
	run.entry.AccountingModels = calibrationModels(accounting.Models)
	if !calibrationModelIDsValid(run.entry.EventModels) {
		run.entry.EventModels = nil
	}
	if !calibrationModelIDsValid(run.entry.AccountingModels) {
		run.entry.AccountingModels = nil
	}
	run.entry.EventModelAttributionComplete = events.Complete && events.EventsObserved > 0 && calibrationModelIDsValid(run.entry.EventModels)
	run.entry.AccountingModelAttributionComplete = accounting.ModelAttributionComplete && calibrationModelIDsValid(run.entry.AccountingModels)
	switch accounting.Source {
	case "rpc", "shutdown":
		run.entry.UsageSource = accounting.Source
	}
	// Only complete observed snapshots are representable as accounting in the
	// approved 1.1 ledger. Absence/partial snapshots are not fabricated zeros.
	if !accounting.Complete || !run.entry.AccountingModelAttributionComplete ||
		!calibrationAccountingMatches(usage, run.entry.AccountingModels) || !calibrationUsageValid(usage) {
		return
	}
	if run.entry.UsageSource == "" {
		return
	}
	copy, err := calibrationJSONCopy(usage)
	if err != nil {
		run.recordDiagnostic(execution.StageUsageMetrics, execution.CodeUsageMissingOrPartial)
		return
	}
	run.entry.Usage, run.entry.UsageComplete = copy, true
	run.entry.Credits = new(*copy.AICredits)
}

func calibrationModelIDsValid(models []string) bool {
	if len(models) == 0 {
		return false
	}
	for _, model := range models {
		if !referenceIdentifier(model) {
			return false
		}
		switch strings.ToLower(model) {
		case "auto", "unknown":
			return false
		}
	}
	return true
}

func calibrationUsageValid(usage *models.UsageStats) bool {
	if usage == nil || usage.AICredits == nil || !calibrationFiniteNonnegative(*usage.AICredits) || len(usage.ModelMetrics) == 0 {
		return false
	}
	for _, count := range []int{usage.Turns, usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens} {
		if count < 0 || uint64(count) > 9007199254740991 {
			return false
		}
	}
	if !calibrationFiniteNonnegative(usage.PremiumRequests) {
		return false
	}
	for _, model := range usage.ModelMetrics {
		if model.AICredits == nil || !calibrationFiniteNonnegative(*model.AICredits) ||
			!calibrationFiniteNonnegative(model.RequestCost) || !calibrationFiniteNonnegative(model.RequestCount) {
			return false
		}
		for _, count := range []int{model.InputTokens, model.OutputTokens, model.CacheReadTokens, model.CacheWriteTokens} {
			if count < 0 || uint64(count) > 9007199254740991 {
				return false
			}
		}
	}
	// Reject NaN/Inf in any native metric, not only the billing projection.
	data, err := json.Marshal(usage)
	if err != nil {
		return false
	}
	var value map[string]any
	return json.Unmarshal(data, &value) == nil
}

func calibrationFiniteNonnegative(number float64) bool {
	return !math.IsNaN(number) && !math.IsInf(number, 0) && number >= 0
}
