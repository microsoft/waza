package execution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
)

type diagnosticRPCRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		SessionID      string   `json:"sessionId"`
		Prompt         string   `json:"prompt"`
		AvailableTools []string `json:"availableTools"`
	} `json:"params"`
}

type diagnosticRPCServer struct {
	t        *testing.T
	conn     net.Conn
	ready    chan struct{}
	done     chan struct{}
	writeMu  sync.Mutex
	mu       sync.Mutex
	requests []diagnosticRPCRequest
	handle   func(*diagnosticRPCServer, diagnosticRPCRequest)
}

func newDiagnosticRPCServer(t *testing.T, handle func(*diagnosticRPCServer, diagnosticRPCRequest)) (*diagnosticRPCServer, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &diagnosticRPCServer{t: t, ready: make(chan struct{}), done: make(chan struct{}), handle: handle}
	go func() {
		defer close(s.done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		s.conn = conn
		close(s.ready)
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("closing offline RPC: %v", err)
			}
		}()
		reader := bufio.NewReader(conn)
		for {
			body, err := readTestRPCFrame(reader)
			if err != nil {
				return
			}
			var req diagnosticRPCRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Error(err)
				return
			}
			if len(req.ID) == 0 {
				continue
			}
			s.mu.Lock()
			s.requests = append(s.requests, req)
			s.mu.Unlock()
			go s.handle(s, req)
		}
	}()
	t.Cleanup(func() {
		require.NoError(t, listener.Close())
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Error("offline SDK connection did not stop")
		}
	})
	return s, listener.Addr().String()
}

func (s *diagnosticRPCServer) write(payload any) {
	<-s.ready
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := writeTestRPCFrame(s.conn, payload); err != nil {
		select {
		case <-s.done:
		default:
			s.t.Errorf("writing offline RPC: %v", err)
		}
	}
}

func (s *diagnosticRPCServer) respond(req diagnosticRPCRequest, result any, fail bool) {
	payload := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if fail {
		payload["error"] = map[string]any{"code": -32000, "message": privateDiagnosticSentinel}
	} else {
		payload["result"] = result
	}
	s.write(payload)
}

func (s *diagnosticRPCServer) emit(id string, event copilot.SessionEvent) {
	s.write(map[string]any{"jsonrpc": "2.0", "method": "session.event", "params": map[string]any{"sessionId": id, "event": event}})
}

func (s *diagnosticRPCServer) common(req diagnosticRPCRequest) {
	var result any
	switch req.Method {
	case "connect":
		result = map[string]any{"ok": true, "protocolVersion": 3, "version": "offline-test"}
	case "auth.getStatus":
		result = map[string]any{"isAuthenticated": true}
	case "session.create", "session.resume":
		result = map[string]any{"sessionId": req.Params.SessionID}
	case "session.usage.getMetrics":
		result = completeDiagnosticMetrics()
	case "session.detach", "session.delete":
		result = map[string]any{"success": true}
	default:
		s.t.Errorf("unexpected offline SDK method %s", req.Method)
		s.respond(req, nil, true)
		return
	}
	s.respond(req, result, false)
}

func (s *diagnosticRPCServer) count(method, id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, req := range s.requests {
		if req.Method == method && (id == "" || req.Params.SessionID == id) {
			count++
		}
	}
	return count
}

func offlineDiagnosticEngine(t *testing.T, address string, observer func(ExecutionDiagnostic) error) *CopilotEngine {
	t.Helper()
	e := NewCopilotEngineBuilder("requested-model", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(options *copilot.ClientOptions) CopilotClient {
			options.Connection = copilot.URIConnection{URL: address}
			return NewOwnedCopilotClient(options)
		},
		DiagnosticObserver: observer,
	}).Build()
	e.provider = customProviderConfig{}
	t.Cleanup(func() { require.NoError(t, e.Shutdown(context.Background())) })
	require.NoError(t, e.Initialize(t.Context()))
	return e
}

func TestDiagnosticNativeTransportPostCallbackErrorAndOwnedCleanup(t *testing.T) {
	var log bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	var deleteCount atomic.Int32
	server, address := newDiagnosticRPCServer(t, func(s *diagnosticRPCServer, req diagnosticRPCRequest) {
		switch req.Method {
		case "session.send":
			s.respond(req, map[string]any{"messageId": "judge-message"}, false)
			s.emit(req.Params.SessionID, copilot.SessionEvent{Data: &copilot.AssistantUsageData{Model: "event-conflict-model"}})
			s.emit(req.Params.SessionID, copilot.SessionEvent{Data: &copilot.ExternalToolRequestedData{
				RequestID: "judge-request", SessionID: req.Params.SessionID, ToolCallID: "judge-call",
				ToolName: "set_waza_grade_pass", Arguments: map[string]any{"reason": "pass before provider failure"},
			}})
		case "session.tools.handlePendingToolCall":
			s.respond(req, map[string]any{}, false)
			s.emit(req.Params.SessionID, copilot.SessionEvent{Data: &copilot.SessionErrorData{Message: privateDiagnosticSentinel}})
		case "session.delete":
			s.respond(req, map[string]any{"success": true}, deleteCount.Add(1) == 1)
		default:
			s.common(req)
		}
	})
	var seenMu sync.Mutex
	var seen []ExecutionDiagnostic
	e := offlineDiagnosticEngine(t, address, func(d ExecutionDiagnostic) error {
		seenMu.Lock()
		seen = append(seen, d)
		seenMu.Unlock()
		if d.Stage == StageEphemeralDelete {
			return errors.New(privateDiagnosticSentinel)
		}
		return nil
	})
	var callbackCount atomic.Int32
	allowed := []string{"custom:set_waza_grade_pass", "custom:set_waza_grade_fail"}
	req := &ExecutionRequest{
		Message: "judge", NoSkills: true, EphemeralSession: true, SkipWorkspaceCapture: true,
		ToolPolicy: NewToolPolicy(&allowed), Tools: []copilot.Tool{{
			Name: "set_waza_grade_pass", Handler: func(copilot.ToolInvocation) (copilot.ToolResult, error) {
				callbackCount.Add(1)
				return copilot.ToolResult{TextResultForLLM: "grade collected"}, nil
			},
		}},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, err := e.Execute(ctx, req)
	require.ErrorContains(t, err, "execute/send_failed")
	require.ErrorContains(t, err, "ephemeral_delete/cleanup_failed")
	require.ErrorContains(t, err, "ephemeral_delete/observer_failed")
	require.NotContains(t, err.Error(), privateDiagnosticSentinel)
	require.NotContains(t, resp.ErrorMsg, privateDiagnosticSentinel)
	require.EqualValues(t, 1, callbackCount.Load())
	require.NotNil(t, resp.Usage)
	require.Equal(t, 7, resp.Usage.InputTokens)
	require.Equal(t, 0.0, *resp.Usage.AICredits)
	require.True(t, e.SessionUsageObservation(resp.SessionID).Complete)
	require.Equal(t, []string{"observed-model"}, e.SessionUsageObservation(resp.SessionID).Models)
	require.Equal(t, SessionEventModelObservation{
		Models: []string{"event-conflict-model"}, EventsObserved: 1, Complete: true,
	}, e.SessionEventModelObservation(resp.SessionID))
	require.DirExists(t, resp.WorkspaceDir)
	require.NoError(t, e.Shutdown(t.Context()))
	require.Equal(t, SessionEventModelObservation{
		Models: []string{"event-conflict-model"}, EventsObserved: 1, Complete: true,
	}, e.SessionEventModelObservation(resp.SessionID))
	require.NoDirExists(t, resp.WorkspaceDir)
	require.Equal(t, 2, server.count("session.delete", resp.SessionID))
	require.Equal(t, 1, server.count("session.tools.handlePendingToolCall", resp.SessionID))
	select {
	case <-server.done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned SDK client was not stopped")
	}
	require.NotContains(t, log.String(), privateDiagnosticSentinel)
	seenMu.Lock()
	data, marshalErr := json.Marshal(seen)
	seenMu.Unlock()
	require.NoError(t, marshalErr)
	require.NotContains(t, string(data), resp.SessionID)
	require.NotContains(t, string(data), privateDiagnosticSentinel)
}

func TestDiagnosticNativeTransportConcurrentLifetimeDrain(t *testing.T) {
	var log bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	sent := make(chan diagnosticRPCRequest, 2)
	detachEntered, releaseDetach := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseDetach) }) }
	defer unblock()
	var failedID string
	var idMu sync.Mutex
	server, address := newDiagnosticRPCServer(t, func(s *diagnosticRPCServer, req diagnosticRPCRequest) {
		switch req.Method {
		case "session.send":
			if req.Params.Prompt == "A" {
				idMu.Lock()
				failedID = req.Params.SessionID
				idMu.Unlock()
			}
			s.respond(req, map[string]any{"messageId": req.Params.Prompt}, false)
			sent <- req
		case "session.detach":
			idMu.Lock()
			failed := req.Params.SessionID == failedID
			idMu.Unlock()
			if failed {
				close(detachEntered)
				<-releaseDetach
			}
			s.common(req)
		default:
			s.common(req)
		}
	})
	e := offlineDiagnosticEngine(t, address, func(ExecutionDiagnostic) error { return nil })
	type result struct {
		response *ExecutionResponse
		err      error
	}
	results := map[string]chan result{"A": make(chan result, 1), "B": make(chan result, 1)}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, prompt := range []string{"A", "B"} {
		go func() {
			resp, err := e.Execute(ctx, &ExecutionRequest{Message: prompt, NoSkills: true, EphemeralSession: true, SkipWorkspaceCapture: true})
			results[prompt] <- result{resp, err}
		}()
	}
	ids := make(map[string]string)
	for range 2 {
		select {
		case req := <-sent:
			ids[req.Params.Prompt] = req.Params.SessionID
		case <-ctx.Done():
			t.Fatal("native sends did not start")
		}
	}
	require.NotEqual(t, ids["A"], ids["B"])
	_, err := e.Execute(ctx, &ExecutionRequest{SessionID: ids["A"]})
	require.ErrorContains(t, err, "session_busy")
	shutdown := make(chan error, 1)
	go func() { shutdown <- e.Shutdown(context.Background()) }()
	for {
		e.diagnostics.mu.Lock()
		closed := e.diagnostics.closed
		e.diagnostics.mu.Unlock()
		if closed {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("shutdown did not close admission")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	_, err = e.Execute(ctx, &ExecutionRequest{NoSkills: true})
	require.ErrorContains(t, err, "engine_closed")
	server.emit(ids["A"], copilot.SessionEvent{Data: &copilot.SessionErrorData{Message: privateDiagnosticSentinel}})
	server.emit(ids["B"], copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: "B output"}})
	server.emit(ids["B"], copilot.SessionEvent{Data: &copilot.SessionIdleData{}})
	select {
	case <-detachEntered:
	case <-ctx.Done():
		t.Fatal("native deferred disconnect did not begin")
	}
	require.Zero(t, server.count("session.delete", ids["A"]))
	select {
	case <-shutdown:
		t.Fatal("shutdown completed before native deferred disconnect")
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	a, b := <-results["A"], <-results["B"]
	require.ErrorContains(t, a.err, "execute/send_failed")
	require.NoError(t, b.err)
	require.True(t, b.response.Success)
	require.Equal(t, "B output", b.response.FinalOutput)
	require.NoError(t, <-shutdown)
	for _, r := range []result{a, b} {
		require.NotNil(t, r.response.Usage)
		require.Equal(t, 7, r.response.Usage.InputTokens)
		require.Equal(t, 0.0, *r.response.Usage.AICredits)
		require.NoDirExists(t, r.response.WorkspaceDir)
		require.Equal(t, 1, server.count("session.delete", r.response.SessionID))
	}
	select {
	case <-server.done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned concurrent SDK client was not stopped")
	}
	require.NotContains(t, log.String(), privateDiagnosticSentinel)
}
