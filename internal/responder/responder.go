// Package responder implements an LLM-backed surrogate user that answers an
// interactive skill's follow-up questions during a multi-turn evaluation run.
package responder

import (
	"context"

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

// decisionRecorder captures the single decision tool the responder LLM calls.
type decisionRecorder struct {
	decision Decision
	set      bool
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
				_ = mapstructure.Decode(inv.Arguments, &args)
				d.decision = Decision{Kind: DecisionReply, Answer: args.Answer}
				d.set = true
				return copilot.ToolResult{}, nil
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
				d.decision = Decision{Kind: DecisionStop}
				d.set = true
				return copilot.ToolResult{}, nil
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
				_ = mapstructure.Decode(inv.Arguments, &args)
				d.decision = Decision{Kind: DecisionAbstain, Reason: args.Reason}
				d.set = true
				return copilot.ToolResult{}, nil
			},
		},
	}
}

// Classifier is added in the next task; declared here so the package compiles
// with its dependencies referenced.
var _ = models.ResponderConfig{}
