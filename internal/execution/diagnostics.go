package execution

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/microsoft/waza/internal/models"
)

type DiagnosticStage string
type DiagnosticCode string

const (
	StageInitialize         DiagnosticStage = "initialize"
	StageExecute            DiagnosticStage = "execute"
	StageDisconnect         DiagnosticStage = "disconnect"
	StageEphemeralDelete    DiagnosticStage = "ephemeral_delete"
	StageUsageMetrics       DiagnosticStage = "usage_metrics"
	StageShutdownUsage      DiagnosticStage = "shutdown_usage"
	StageShutdownDelete     DiagnosticStage = "shutdown_delete"
	StageClientStop         DiagnosticStage = "client_stop"
	StageCommandMockCleanup DiagnosticStage = "commandmock_cleanup"
	StageGitCleanup         DiagnosticStage = "git_cleanup"
	StageWorkspaceCleanup   DiagnosticStage = "workspace_cleanup"
)

const (
	CodeProvider                  DiagnosticCode = "provider_failed"
	CodeStart                     DiagnosticCode = "start_failed"
	CodeAuth                      DiagnosticCode = "auth_failed"
	CodeCreate                    DiagnosticCode = "create_failed"
	CodeResume                    DiagnosticCode = "resume_failed"
	CodeSend                      DiagnosticCode = "send_failed"
	CodeExecute                   DiagnosticCode = "execute_failed"
	CodeCleanup                   DiagnosticCode = "cleanup_failed"
	CodeExpectedSkillCancellation DiagnosticCode = "expected_skill_cancellation"
	CodeUsageMissingOrPartial     DiagnosticCode = "usage_missing_or_partial"
	CodeModelAttributionMissing   DiagnosticCode = "model_attribution_missing"
	CodeShutdownRPC               DiagnosticCode = "shutdown_rpc_failed"
	CodeHistory                   DiagnosticCode = "history_failed"
	CodeFallbackDisconnect        DiagnosticCode = "fallback_disconnect_failed"
	CodeObserverFailed            DiagnosticCode = "observer_failed"
	CodeClosed                    DiagnosticCode = "engine_closed"
	CodeSessionBusy               DiagnosticCode = "session_busy"
)

// ExecutionDiagnostic contains only allowlisted classifications. SessionID is
// available for internal correlation but never serialized.
type ExecutionDiagnostic struct {
	Stage     DiagnosticStage `json:"stage"`
	Code      DiagnosticCode  `json:"code"`
	SessionID string          `json:"-"`
}

// DiagnosticError deliberately does not unwrap provider or observer causes.
type DiagnosticError struct{ Diagnostic ExecutionDiagnostic }

func (e *DiagnosticError) Error() string {
	stage, code := e.Diagnostic.Stage, e.Diagnostic.Code
	switch stage {
	case StageInitialize, StageExecute, StageDisconnect, StageEphemeralDelete, StageUsageMetrics,
		StageShutdownUsage, StageShutdownDelete, StageClientStop, StageCommandMockCleanup, StageGitCleanup, StageWorkspaceCleanup:
	default:
		stage = StageExecute
	}
	switch code {
	case CodeProvider, CodeStart, CodeAuth, CodeCreate, CodeResume, CodeSend, CodeExecute, CodeCleanup,
		CodeExpectedSkillCancellation, CodeUsageMissingOrPartial, CodeModelAttributionMissing,
		CodeShutdownRPC, CodeHistory, CodeFallbackDisconnect, CodeObserverFailed, CodeClosed, CodeSessionBusy:
	default:
		code = CodeExecute
	}
	return "copilot diagnostic: " + string(stage) + "/" + string(code)
}

type diagnosticState struct {
	mu         sync.Mutex
	observerMu sync.Mutex
	observer   func(ExecutionDiagnostic) error
	active     sync.WaitGroup
	closed     bool
	busy       map[string]bool
	pending    map[string]bool
	initOnce   sync.Once
	initErr    error
}

// Each lifecycle invocation owns its aggregate, including pre-session failures
// with no ID. SessionID is correlation data, never the diagnostic ownership key.
type diagnosticOperation struct {
	state *diagnosticState
	mu    sync.Mutex
	errs  []error
}

type diagnosticOperationKey struct{}

func operationFrom(ctx context.Context) *diagnosticOperation {
	op, _ := ctx.Value(diagnosticOperationKey{}).(*diagnosticOperation)
	return op
}

func (o *diagnosticOperation) report(stage DiagnosticStage, code DiagnosticCode, id string) {
	d := ExecutionDiagnostic{Stage: stage, Code: code, SessionID: id}
	if code != CodeExpectedSkillCancellation {
		o.mu.Lock()
		o.errs = append(o.errs, &DiagnosticError{Diagnostic: d})
		o.mu.Unlock()
	}
	o.state.observerMu.Lock()
	err := o.state.observer(d)
	o.state.observerMu.Unlock()
	if err != nil {
		o.mu.Lock()
		o.errs = append(o.errs, &DiagnosticError{Diagnostic: ExecutionDiagnostic{Stage: stage, Code: CodeObserverFailed, SessionID: id}})
		o.mu.Unlock()
	}
}

func (o *diagnosticOperation) err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return errors.Join(o.errs...)
}

func (s *diagnosticState) admit(id string) (func(), DiagnosticCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, CodeClosed
	}
	if id != "" && s.busy[id] {
		return nil, CodeSessionBusy
	}
	if id != "" {
		s.busy[id] = true
	}
	s.active.Add(1)
	return func() {
		s.mu.Lock()
		delete(s.busy, id)
		s.mu.Unlock()
		s.active.Done()
	}, ""
}

// SessionUsageObservation describes presence before SDK pointer fields are
// flattened into UsageStats. Models are observed SDK keys, never requested IDs.
// Complete describes usage presence, not lifecycle success; consumers must also
// reject observations affected by execution or engine-lifecycle diagnostics.
type SessionUsageObservation struct {
	Source                   string   `json:"source"`
	Complete                 bool     `json:"complete"`
	ModelAttributionComplete bool     `json:"model_attribution_complete"`
	Models                   []string `json:"models"`
}

func (e *CopilotEngine) SessionUsageObservation(id string) SessionUsageObservation {
	e.usageCollectorsMu.RLock()
	collector := e.usageCollectors[id]
	e.usageCollectorsMu.RUnlock()
	if collector == nil {
		return SessionUsageObservation{}
	}
	return collector.Observation()
}

// SessionEventModelObservation describes received SDK model attribution,
// independently of authoritative usage snapshots. Complete requires at least
// one supported attribution event and usable IDs in every such received event.
// It does not guarantee exhaustive event delivery or establish billed usage.
type SessionEventModelObservation struct {
	Models         []string `json:"models"`
	EventsObserved uint64   `json:"events_observed"`
	Complete       bool     `json:"complete"`
}

func (e *CopilotEngine) SessionEventModelObservation(id string) SessionEventModelObservation {
	e.usageCollectorsMu.RLock()
	collector := e.usageCollectors[id]
	e.usageCollectorsMu.RUnlock()
	if collector == nil {
		return SessionEventModelObservation{}
	}
	return collector.EventModelObservation()
}

func (e *CopilotEngine) initializeDiagnostic(ctx context.Context) error {
	s := e.diagnostics
	done, code := s.admit("")
	op := &diagnosticOperation{state: s}
	if code != "" {
		op.report(StageInitialize, code, "")
		return op.err()
	}
	defer done()
	s.initOnce.Do(func() {
		switch {
		case e.provider.err != nil:
			op.report(StageInitialize, CodeProvider, "")
		default:
			if err := e.client.Start(context.Background()); err != nil {
				op.report(StageInitialize, CodeStart, "")
			} else if !e.provider.enabled() {
				auth, err := e.client.GetAuthStatus(ctx)
				if err != nil || auth == nil || !auth.IsAuthenticated {
					op.report(StageInitialize, CodeAuth, "")
					if e.ownsClient {
						if err := e.client.Stop(); err != nil {
							op.report(StageClientStop, CodeCleanup, "")
						}
					}
				}
			}
		}
		s.initErr = op.err()
	})
	return s.initErr
}

func (e *CopilotEngine) executeDiagnostic(ctx context.Context, req *ExecutionRequest) (resp *ExecutionResponse, err error) {
	s := e.diagnostics
	id := ""
	if req != nil {
		id = req.SessionID
	}
	op := &diagnosticOperation{state: s}
	done, code := s.admit(id)
	if code != "" {
		op.report(StageExecute, code, id)
		return nil, op.err()
	}
	defer done()
	ctx = context.WithValue(ctx, diagnosticOperationKey{}, op)
	resp, err = e.execute(ctx, req)
	if err != nil && op.err() == nil {
		op.report(StageExecute, CodeExecute, id)
	}
	err = op.err()
	if resp != nil && err != nil {
		resp.Success = false
		resp.ErrorMsg = err.Error()
	}
	return resp, err
}

func reportUsageObservation(op *diagnosticOperation, stage DiagnosticStage, id string, observation SessionUsageObservation) {
	if !observation.Complete {
		op.report(stage, CodeUsageMissingOrPartial, id)
	}
	if !observation.ModelAttributionComplete {
		op.report(stage, CodeModelAttributionMissing, id)
	}
}

func (e *CopilotEngine) shutdownDiagnostic(ctx context.Context) error {
	s := e.diagnostics
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	// Admission and Add are atomic under mu, so Wait includes the entire
	// execution, including deferred disconnect/delete and observer delivery.
	s.active.Wait()
	op := &diagnosticOperation{state: s}
	ctx = context.WithValue(ctx, diagnosticOperationKey{}, op)
	e.sessionsMu.Lock()
	sessions := e.sessions
	e.sessions = nil
	e.sessionsMu.Unlock()
	for id, session := range sessions {
		e.usageCollectorsMu.RLock()
		collector := e.usageCollectors[id]
		e.usageCollectorsMu.RUnlock()
		if collector != nil && !collector.hasMetrics() {
			e.captureShutdownUsage(ctx, id, session, collector)
		}

		if err := e.client.DeleteSession(ctx, id); err != nil {
			op.report(StageShutdownDelete, CodeCleanup, id)
		}
	}
	s.mu.Lock()
	pending := s.pending
	s.pending = make(map[string]bool)
	s.mu.Unlock()
	for id := range pending {
		if err := e.client.DeleteSession(ctx, id); err != nil {
			op.report(StageShutdownDelete, CodeCleanup, id)
		}
	}
	if e.ownsClient {
		if err := e.client.Stop(); err != nil {
			op.report(StageClientStop, CodeCleanup, "")
		}
	}
	e.commandMocksMu.Lock()
	commandSessions := e.commandMockSessions
	e.commandMockSessions = nil
	e.commandMocksMu.Unlock()
	for _, session := range commandSessions {
		if _, err := session.Close(); err != nil {
			op.report(StageCommandMockCleanup, CodeCleanup, "")
		}
	}
	e.workspacesMu.Lock()
	workspaces, gitResources := e.workspaces, e.gitResources
	e.workspaces, e.gitResources = nil, nil
	e.workspacesMu.Unlock()
	for _, resource := range gitResources {
		if err := resource.Cleanup(ctx); err != nil {
			op.report(StageGitCleanup, CodeCleanup, "")
		}
	}
	for _, workspace := range workspaces {
		if workspace != "" && !e.keepWorkspace {
			if err := os.RemoveAll(workspace); err != nil {
				op.report(StageWorkspaceCleanup, CodeCleanup, "")
			}
		}
	}
	return op.err()
}

func (e *CopilotEngine) deleteSessionDiagnostic(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	op := &diagnosticOperation{state: e.diagnostics}
	done, code := e.diagnostics.admit(id)
	if code != "" {
		op.report(StageShutdownDelete, code, id)
		return op.err()
	}

	defer done()
	ctx = context.WithValue(ctx, diagnosticOperationKey{}, op)
	if err := e.deleteSession(ctx, id); err != nil {
		op.report(StageShutdownDelete, CodeCleanup, id)
	}
	return op.err()
}

func (e *CopilotEngine) finalizeCommandMocksDiagnostic(workspace string) ([]models.CommandInvocation, error) {
	op := &diagnosticOperation{state: e.diagnostics}
	done, code := e.diagnostics.admit("")
	if code != "" {
		op.report(StageCommandMockCleanup, code, "")
		return nil, op.err()
	}
	defer done()
	invocations, err := e.finalizeCommandMocks(workspace)
	if err != nil {
		op.report(StageCommandMockCleanup, CodeCleanup, "")
	}
	return invocations, op.err()
}
