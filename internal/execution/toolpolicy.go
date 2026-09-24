package execution

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/models"
)

// ToolPolicyMode describes the tool-capability boundary derived from an
// .agent.md `tools:` declaration. See [NewToolPolicy] for the tri-state
// mapping from the declaration to a mode.
type ToolPolicyMode string

const (
	// ToolPolicyUnrestricted means the agent did not declare a `tools:` key.
	// Tools provided by the Copilot runtime may be used without restriction
	// (the pre-existing default behavior).
	ToolPolicyUnrestricted ToolPolicyMode = "unrestricted"
	// ToolPolicyDenyAll means the agent declared `tools: []`. No
	// model-initiated tool execution is permitted.
	ToolPolicyDenyAll ToolPolicyMode = "deny_all"
	// ToolPolicyAllowList means the agent declared one or more tool names.
	// Only those tools (matched exactly, case-insensitively) are permitted;
	// every other tool is denied before it executes.
	ToolPolicyAllowList ToolPolicyMode = "allow_list"
)

// ToolPolicy is the resolved runtime tool-capability boundary for a session.
// It is derived from an .agent.md `tools:` declaration (see
// [NewToolPolicy]) and then applied to a Copilot SDK session in three ways:
//
//  1. Declaratively, via SessionConfig/ResumeSessionConfig.AvailableTools
//     (see [ToolPolicy.SessionToolFilter]), so the SDK itself never exposes
//     denied tools to the model when it supports the filter.
//  2. As defense in depth, via a fail-closed OnPermissionRequest handler (see
//     enforceToolPolicy in copilot.go) that denies any tool-execution attempt
//     the policy does not explicitly allow -- including requests the policy
//     resolver cannot recognize.
//  3. A pre-tool-use hook denies undeclared calls even when the tool does not
//     request permission. Source selection relies on the SDK filter because
//     hook and transcript tool names do not include source metadata.
//
// Tool-name matching is exact and case-insensitive (not regex based), plus a
// small fixed alias table (see [models.CanonicalToolName]), matching the semantics used by the
// tool_constraint grader's allow_only field (see
// internal/orchestration/agent_graders.go) so runtime enforcement and
// post-run grading agree on what a declared tool name means.
type ToolPolicy struct {
	Mode    ToolPolicyMode
	allowed map[string]struct{}
	// Preserve exact custom/MCP identifiers for the SDK's source-aware filter.
	declared []string
}

// NewToolPolicy converts the tri-state `.agent.md` `tools:` declaration into
// an explicit ToolPolicy:
//
//   - nil (key absent): [ToolPolicyUnrestricted].
//   - empty (`tools: []`): [ToolPolicyDenyAll].
//   - populated: [ToolPolicyAllowList], with the given names as the allowed set.
func NewToolPolicy(tools *[]string) *ToolPolicy {
	if tools == nil {
		return &ToolPolicy{Mode: ToolPolicyUnrestricted}
	}
	if len(*tools) == 0 {
		return &ToolPolicy{Mode: ToolPolicyDenyAll}
	}
	allowed := make(map[string]struct{}, len(*tools))
	declared := make([]string, 0, len(*tools))
	for _, t := range *tools {
		allowed[canonicalToolName(t)] = struct{}{}
		declared = append(declared, t)
	}
	return &ToolPolicy{Mode: ToolPolicyAllowList, allowed: allowed, declared: declared}
}

// IsAllowed reports whether the given tool name is permitted under the
// policy. A nil *ToolPolicy is treated as unrestricted (preserves prior
// behavior for callers that never opted in to policy enforcement).
func (p *ToolPolicy) IsAllowed(name string) bool {
	if p == nil || p.Mode == ToolPolicyUnrestricted {
		return true
	}
	if p.Mode == ToolPolicyDenyAll {
		return false
	}
	_, ok := p.allowed[canonicalToolName(name)]
	return ok
}

// SessionToolFilter returns the value to assign to the Copilot SDK's
// SessionConfig/ResumeSessionConfig.AvailableTools field:
//
//   - nil for [ToolPolicyUnrestricted]: no filter is applied.
//   - a non-nil empty slice for [ToolPolicyDenyAll]: the SDK exposes no tools.
//   - declared names for [ToolPolicyAllowList], with built-in aliases translated
//     to native SDK tool names. MCP/custom case and source qualifiers are preserved.
//
// A nil receiver returns nil (unrestricted), matching [ToolPolicy.IsAllowed].
func (p *ToolPolicy) SessionToolFilter() []string {
	if p == nil || p.Mode == ToolPolicyUnrestricted {
		return nil
	}
	if p.Mode == ToolPolicyDenyAll {
		return []string{}
	}
	names := make([]string, 0, len(p.declared))
	for _, name := range p.declared {
		// Translate documented capability aliases to the native CLI tool.
		// Keep MCP/custom identifiers and their source qualifiers verbatim.
		switch {
		case strings.HasPrefix(strings.ToLower(name), "mcp:"), strings.HasPrefix(strings.ToLower(name), "custom:"):
			names = append(names, name)
		default:
			switch canonicalToolName(name) {
			case "read":
				names = append(names, "builtin:view")
			case "write":
				names = append(names, "builtin:edit")
			case "bash":
				names = append(names, "builtin:bash")
			case "fetch":
				names = append(names, "builtin:web_fetch")
			default:
				if strings.HasPrefix(strings.ToLower(name), "builtin:") {
					names = append(names, name)
				} else {
					names = append(names, "builtin:"+name)
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// Active reports whether the policy imposes any restriction at all, i.e.
// enforcement (the OnPermissionRequest wrapper) needs to run.
func (p *ToolPolicy) Active() bool {
	return p != nil && p.Mode != ToolPolicyUnrestricted
}

func canonicalToolName(name string) string {
	return models.CanonicalToolName(name)
}

// ToolPolicyDenial records a single tool-execution attempt that was denied by
// an active [ToolPolicy]. Captured on [ExecutionResponse] so callers can
// surface the effective policy and every denial in session logs and
// results.json.
type ToolPolicyDenial struct {
	// Tool is the canonical tool name the policy evaluated (empty when the
	// request kind/shape could not be resolved to a tool name at all).
	Tool string
	// Kind is the underlying Copilot SDK permission request kind (e.g.
	// "shell", "url", "mcp", "custom-tool", "factory").
	Kind string
	// Reason explains why the request was denied.
	Reason string
}

// toolPolicyRecorder collects denials produced by enforceToolPolicy. Safe for
// concurrent use since the SDK may invoke OnPermissionRequest from multiple
// goroutines (e.g. subagents running concurrently).
type toolPolicyRecorder struct {
	mu      sync.Mutex
	denials []ToolPolicyDenial
}

func newToolPolicyRecorder() *toolPolicyRecorder {
	return &toolPolicyRecorder{}
}

func (r *toolPolicyRecorder) record(tool, kind, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.denials = append(r.denials, ToolPolicyDenial{Tool: tool, Kind: kind, Reason: reason})
}

func (r *toolPolicyRecorder) snapshot() []ToolPolicyDenial {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.denials) == 0 {
		return nil
	}
	out := make([]ToolPolicyDenial, len(r.denials))
	copy(out, r.denials)
	return out
}

// canonicalPermissionToolName resolves a Copilot SDK PermissionRequest to the
// canonical tool name a `.agent.md` `tools:` entry would declare to permit
// it. Returns ok=false when the request kind/shape can't be resolved to a
// tool name, which enforceToolPolicy treats as fail-closed (denied) whenever
// a policy is active.
//
// The mapping from implicit (kind-only) requests to tool names is a
// documented interpretation, not a value surfaced by the SDK itself:
// PermissionRequestRead -> "read", PermissionRequestWrite -> "write",
// PermissionRequestShell -> "bash", PermissionRequestURL -> "fetch". Requests
// that carry an explicit tool name (custom tool, MCP, hook) use that name
// directly; factory/subagent requests use their declared factory Name (or
// "task" if no name is present), so an allow-list can target a specific
// subagent instead of implicitly permitting every subagent.
func canonicalPermissionToolName(request copilot.PermissionRequest) (string, bool) {
	switch req := request.(type) {
	case *copilot.PermissionRequestCustomTool:
		if strings.TrimSpace(req.ToolName) == "" {
			return "", false
		}
		return canonicalToolName("custom:" + req.ToolName), true
	case *copilot.PermissionRequestMCP:
		if strings.TrimSpace(req.ServerName) == "" || strings.TrimSpace(req.ToolName) == "" {
			return "", false
		}
		return canonicalToolName("mcp:" + req.ServerName + "-" + req.ToolName), true
	case *copilot.PermissionRequestHook:
		return canonicalToolName(req.ToolName), strings.TrimSpace(req.ToolName) != ""
	case *copilot.PermissionRequestFactory:
		// Subagent (task/factory) invocations declare their own factory
		// Name (e.g. a specific subagent), which lets an allow-list target
		// that subagent directly. Fall back to the generic "task" tool name
		// when the SDK doesn't supply one.
		if req.Name != "" {
			return canonicalToolName(req.Name), true
		}

		return "task", true
	case *copilot.PermissionRequestRead:
		return "read", true
	case *copilot.PermissionRequestWrite:
		return "write", true
	case *copilot.PermissionRequestShell:
		return "bash", true
	case *copilot.PermissionRequestURL:
		return "fetch", true
	case *copilot.PermissionRequestMemory:
		return "memory", true
	default:
		return "", false
	}
}

// enforceToolCall also covers tools that do not trigger a permission request.
func enforceToolCall(policy *ToolPolicy, recorder *toolPolicyRecorder) copilot.PreToolUseHandler {
	return func(input copilot.PreToolUseHookInput, _ copilot.HookInvocation) (*copilot.PreToolUseHookOutput, error) {
		if input.ToolName != "" {
			for _, declared := range policy.declared {
				if models.MatchesToolCallName(declared, input.ToolName) {
					// Do not pre-approve: preserve the caller's permission handler.
					return nil, nil
				}
			}
		}
		reason := fmt.Sprintf("tool %q is not declared in the agent's `tools:` allow-list", input.ToolName)
		recorder.record(canonicalToolName(input.ToolName), "tool", reason)
		return &copilot.PreToolUseHookOutput{
			PermissionDecision: "deny", PermissionDecisionReason: reason,
		}, nil
	}
}

// enforceToolPolicy wraps next (the caller-supplied or default permission
// handler) with fail-closed .agent.md tool policy enforcement:
//
//   - requests whose canonical tool name is allowed by policy are forwarded
//     to next so any existing approval behavior for permitted tools is
//     preserved;
//   - every other request -- including ones the resolver can't recognize --
//     is rejected before it executes, and recorded via recorder.
//
// This runs regardless of whether the SDK's own AvailableTools filtering
// (see [ToolPolicy.SessionToolFilter]) also applies, so enforcement holds
// even against SDK versions/tool sources that don't honor that filter.
func enforceToolPolicy(policy *ToolPolicy, recorder *toolPolicyRecorder, next copilot.PermissionHandlerFunc) copilot.PermissionHandlerFunc {
	return func(request copilot.PermissionRequest, invocation copilot.PermissionInvocation) (rpc.PermissionDecision, error) {
		name, ok := canonicalPermissionToolName(request)
		if ok && policy.IsAllowed(name) {
			if next != nil {
				return next(request, invocation)
			}
			return &rpc.PermissionDecisionApproveOnce{}, nil
		}

		reason := fmt.Sprintf("tool %q is not declared in the agent's `tools:` allow-list", name)
		if !ok {
			reason = fmt.Sprintf("unrecognized permission request kind %q; denied fail-closed", request.Kind())
		} else if policy.Mode == ToolPolicyDenyAll {
			reason = "agent declared `tools: []`; no tool execution is permitted"
		}
		if recorder != nil {
			recorder.record(name, string(request.Kind()), reason)
		}
		feedback := "denied by .agent.md tool policy: " + reason
		return &rpc.PermissionDecisionReject{Feedback: &feedback}, nil
	}
}
