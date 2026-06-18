// Package responder implements an LLM-backed surrogate user that answers an
// interactive skill's follow-up questions during a multi-turn evaluation run.
package responder

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-viper/mapstructure/v2"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

// DecisionKind enumerates the responder's possible classifications of an agent
// message.
type DecisionKind int

const (
	// DecisionReply means the responder answered the agent's question.
	DecisionReply DecisionKind = iota
	// DecisionStop means the agent is done and no further input is needed.
	DecisionStop
	// DecisionAbstain means the responder could not answer from its brief.
	DecisionAbstain
)

// Decision is the outcome of classifying a single agent message.
type Decision struct {
	Kind   DecisionKind
	Answer string // set when Kind == DecisionReply
	Reason string // set when Kind == DecisionAbstain
}

const (
	toolRespond = "responder_reply"
	toolStop    = "responder_stop"
	toolAbstain = "responder_abstain"
)

// Executor is the narrow execution surface the responder needs. The concrete
// AgentEngine satisfies it, and tests supply a fake.
type Executor interface {
	Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error)
}

// sessionDeleter is an optional capability for explicitly tearing down a
// persistent session. *execution.CopilotEngine implements it; engines that do
// not (e.g. the mock) leave Close as a no-op.
type sessionDeleter interface {
	DeleteSession(ctx context.Context, sessionID string) error
}

// decisionRecorder captures the single decision tool the responder LLM calls.
// err is set if a handler-level failure (malformed arguments or a duplicate
// decision call) must be surfaced rather than silently swallowed. mu guards all
// fields because the Copilot SDK dispatches each tool call on its own goroutine,
// so parallel tool calls in one turn would otherwise race.
type decisionRecorder struct {
	mu       sync.Mutex
	decision Decision
	set      bool
	err      error
}

func (d *decisionRecorder) tools() []copilot.Tool {
	return []copilot.Tool{
		{
			Name:        toolRespond,
			Description: "Answer the agent's question as the user. Call this exactly once with your answer.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"answer": map[string]any{
						"type":        "string",
						"description": "Your reply to the agent's question, consistent with your configuration.",
					},
				},
				"required": []string{"answer"},
			},
			Handler: func(inv copilot.ToolInvocation) (copilot.ToolResult, error) {
				var args struct {
					Answer string `mapstructure:"answer"`
				}
				if err := mapstructure.Decode(inv.Arguments, &args); err != nil {
					return copilot.ToolResult{}, d.fail(fmt.Errorf("decode %s arguments: %w", toolRespond, err))
				}
				if strings.TrimSpace(args.Answer) == "" {
					return copilot.ToolResult{}, d.fail(fmt.Errorf("%s called with empty answer", toolRespond))
				}
				return copilot.ToolResult{}, d.record(toolRespond, Decision{Kind: DecisionReply, Answer: args.Answer})
			},
		},
		{
			Name:        toolStop,
			Description: "Signal that the agent has finished and needs no further input. Call this when there is no question to answer.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Handler: func(copilot.ToolInvocation) (copilot.ToolResult, error) {
				return copilot.ToolResult{}, d.record(toolStop, Decision{Kind: DecisionStop})
			},
		},
		{
			Name:        toolAbstain,
			Description: "Signal that you cannot answer the agent's question from your configuration. Call this only when the information is genuinely missing.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{
						"type":        "string",
						"description": "Why you cannot answer.",
					},
				},
				"required": []string{"reason"},
			},
			Handler: func(inv copilot.ToolInvocation) (copilot.ToolResult, error) {
				var args struct {
					Reason string `mapstructure:"reason"`
				}
				if err := mapstructure.Decode(inv.Arguments, &args); err != nil {
					return copilot.ToolResult{}, d.fail(fmt.Errorf("decode %s arguments: %w", toolAbstain, err))
				}
				if strings.TrimSpace(args.Reason) == "" {
					return copilot.ToolResult{}, d.fail(fmt.Errorf("%s called with empty reason", toolAbstain))
				}
				return copilot.ToolResult{}, d.record(toolAbstain, Decision{Kind: DecisionAbstain, Reason: args.Reason})
			},
		},
	}
}

// record atomically stores the single decision, enforcing the "call exactly one
// decision tool, exactly once" contract advertised in each tool description. If
// a decision was already recorded it refuses rather than letting invocation
// order silently pick the winner. The lock makes the check-and-set safe when the
// SDK dispatches parallel tool calls on separate goroutines.
func (d *decisionRecorder) record(name string, dec Decision) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.set {
		err := fmt.Errorf("responder called %s after a decision was already recorded", name)
		if d.err == nil {
			d.err = err
		}
		return err
	}
	d.decision = dec
	d.set = true
	return nil
}

// fail captures the first handler-level failure (e.g. malformed arguments) so
// Classify can surface it instead of fabricating a blank decision.
func (d *decisionRecorder) fail(err error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err == nil {
		d.err = err
	}
	return err
}

// Classifier maintains a persistent surrogate-user session and classifies each
// agent message into a Decision.
type Classifier struct {
	exec         Executor
	model        string
	instructions string
	sessionID    string // empty until the first Classify creates the session
}

// New constructs a Classifier. defaultModel is used when cfg.Model is empty.
func New(exec Executor, cfg models.ResponderConfig, defaultModel string) *Classifier {
	model := cfg.Model
	if model == "" {
		model = defaultModel
	}
	return &Classifier{
		exec:         exec,
		model:        model,
		instructions: cfg.Instructions,
	}
}

// Classify sends the agent's latest message to the responder LLM and returns
// its decision. The first call seeds the session with the responder
// instructions; subsequent calls resume the same session.
func (c *Classifier) Classify(ctx context.Context, agentMessage string) (Decision, error) {
	rec := &decisionRecorder{}

	req := &execution.ExecutionRequest{
		ModelID:     c.model,
		Message:     c.buildMessage(agentMessage),
		Tools:       rec.tools(),
		MessageMode: execution.MessageModeEnqueue,
		Streaming:   true,
		SessionID:   c.sessionID,
		NoSkills:    true,
		// The responder session must persist across turns so it can be resumed
		// (and so its instructions need only be sent once). It is torn down
		// explicitly via Close. EphemeralSession would delete it after the
		// first turn, breaking resume.
		EphemeralSession:     false,
		SkipWorkspaceCapture: true,
	}

	resp, err := c.exec.Execute(ctx, req)
	if resp != nil && resp.SessionID != "" {
		c.sessionID = resp.SessionID
	}
	// A handler-level failure (malformed tool arguments, or the model calling
	// more than one decision tool) takes precedence: surfacing it as an error
	// is more useful than silently returning a possibly-bogus decision.
	if rec.err != nil {
		return Decision{}, fmt.Errorf("responder tool call invalid: %w", rec.err)
	}
	if err != nil {
		if rec.set {
			return rec.decision, nil
		}
		return Decision{}, fmt.Errorf("responder execution failed: %w", err)
	}
	if !rec.set {
		return Decision{}, errors.New("responder did not call a decision tool")
	}
	return rec.decision, nil
}

// Close tears down the persistent responder session if one was created. It is
// safe to call multiple times and is a no-op when the underlying executor does
// not support explicit session deletion.
func (c *Classifier) Close(ctx context.Context) error {
	if c.sessionID == "" {
		return nil
	}
	sessionID := c.sessionID
	c.sessionID = ""
	if d, ok := c.exec.(sessionDeleter); ok {
		return d.DeleteSession(ctx, sessionID)
	}
	return nil
}

func (c *Classifier) buildMessage(agentMessage string) string {
	if c.sessionID == "" {
		return fmt.Sprintf(
			"%s\n\nYou are role-playing as the user. The agent just said:\n\n%s\n\n"+
				"Respond by calling exactly one tool: %s to answer, %s if the agent is finished and needs nothing, or %s if you genuinely cannot answer from your configuration.",
			c.instructions, agentMessage, toolRespond, toolStop, toolAbstain,
		)
	}
	return fmt.Sprintf(
		"The agent just said:\n\n%s\n\nRespond by calling exactly one tool (%s, %s, or %s).",
		agentMessage, toolRespond, toolStop, toolAbstain,
	)
}
