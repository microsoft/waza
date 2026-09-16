package execution

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
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
// It is derived once from an .agent.md `tools:` declaration (see
// [NewToolPolicy]) and then applied to a Copilot SDK session in two ways:
//
//  1. Declaratively, via SessionConfig/ResumeSessionConfig.AvailableTools
//     (see [ToolPolicy.SessionToolFilter]), so the SDK itself never exposes
//     denied tools to the model when it supports the filter.
//  2. As defense in depth, via a fail-closed OnPermissionRequest handler (see
//     enforceToolPolicy in copilot.go) that denies any tool-execution attempt
//     the policy does not explicitly allow -- including requests the policy
//     resolver cannot recognize.
//
// Tool-name matching is exact and case-insensitive (not regex or alias
// based), matching the semantics used by the tool_constraint grader's
// allow_only field (see internal/orchestration/agent_graders.go) so runtime
// enforcement and post-run grading agree on what a declared tool name means.
type ToolPolicy struct {
	Mode    ToolPolicyMode
	allowed map[string]struct{}
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
	for _, t := range *tools {
		allowed[canonicalToolName(t)] = struct{}{}
	}
	return &ToolPolicy{Mode: ToolPolicyAllowList, allowed: allowed}
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
//   - the sorted declared names for [ToolPolicyAllowList].
//
// A nil receiver returns nil (unrestricted), matching [ToolPolicy.IsAllowed].
func (p *ToolPolicy) SessionToolFilter() []string {
	if p == nil || p.Mode == ToolPolicyUnrestricted {
		return nil
	}
	if p.Mode == ToolPolicyDenyAll {
		return []string{}
	}
	names := make([]string, 0, len(p.allowed))
	for n := range p.allowed {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Active reports whether the policy imposes any restriction at all, i.e.
// enforcement (the OnPermissionRequest wrapper) needs to run.
func (p *ToolPolicy) Active() bool {
	return p != nil && p.Mode != ToolPolicyUnrestricted
}

// canonicalToolName normalizes a declared or requested tool name for
// matching: trims whitespace, lowercases, and strips a recognized
// source-qualifier prefix ("builtin:", "mcp:", "custom:") if present. This
// keeps `tools: [bash]` matching a request for "builtin:bash" without
// treating source-qualification as significant, while still requiring an
// exact match on the remaining name (never a regex or alias).
func canonicalToolName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if idx := strings.Index(name, ":"); idx > 0 {
		switch name[:idx] {
		case "builtin", "mcp", "custom":
			name = name[idx+1:]
		}
	}
	return name
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
		return canonicalToolName(req.ToolName), true
	case *copilot.PermissionRequestMCP:
		return canonicalToolName(req.ToolName), true
	case *copilot.PermissionRequestHook:
		return canonicalToolName(req.ToolName), true
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
