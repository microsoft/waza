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
// Tool-name matching is exact and case-insensitive (not regex based), plus a
// small fixed alias table for implicit filesystem permission kinds (see
// [canonicalToolAliases]), matching the semantics used by the
// tool_constraint grader's allow_only field (see
// internal/orchestration/agent_graders.go) so runtime enforcement and
// post-run grading agree on what a declared tool name means.
type ToolPolicy struct {
	Mode    ToolPolicyMode
	allowed map[string]struct{}
	// declared preserves the original `tools:` entry strings (unmodified
	// case/source-qualifier) so the Copilot SDK's AvailableTools filter sees
	// exactly what the agent wrote, even though matching against permission
	// requests uses the lowercased/alias-resolved canonical form.
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
//   - the original declared names (verbatim case, source-qualifier intact),
//     sorted, for [ToolPolicyAllowList]. Original strings are preserved here
//     because the SDK's own tool registry may key on exact names (e.g. a
//     specific "mcp:server-tool" qualifier); only permission matching uses
//     the lowercased/alias-resolved canonical form.
//
// A nil receiver returns nil (unrestricted), matching [ToolPolicy.IsAllowed].
func (p *ToolPolicy) SessionToolFilter() []string {
	if p == nil || p.Mode == ToolPolicyUnrestricted {
		return nil
	}
	if p.Mode == ToolPolicyDenyAll {
		return []string{}
	}
	names := make([]string, len(p.declared))
	copy(names, p.declared)
	sort.Strings(names)
	return names
}

// Active reports whether the policy imposes any restriction at all, i.e.
// enforcement (the OnPermissionRequest wrapper) needs to run.
func (p *ToolPolicy) Active() bool {
	return p != nil && p.Mode != ToolPolicyUnrestricted
}

// canonicalToolAliases maps alternate spellings of a builtin filesystem
// capability, as documented for `.agent.md` `tools:` declarations (see
// site/src/content/docs/guides/custom-agents.mdx), to the single canonical
// name [canonicalPermissionToolName] reports for the corresponding implicit
// Copilot SDK permission-request kind (PermissionRequestRead /
// PermissionRequestWrite carry no tool name of their own). Without this,
// `tools: [readFile]` would pass session-filter checks but every actual read
// attempt -- normalized to "read" -- would be denied as undeclared.
//
// Only these two aliases exist because "read"/"write" are the only implicit
// (kind-only, name-less) permission-request kinds canonicalPermissionToolName
// maps to a hardcoded name that a declaration could plausibly spell
// differently. The other implicit kinds -- PermissionRequestShell ("bash"),
// PermissionRequestURL ("fetch"), PermissionRequestMemory ("memory") -- are
// not documented under any alternate spelling in custom-agents.mdx, so no
// alias is added for them; adding one without a real declared spelling to
// support would be speculative. Requests that carry their own name (custom
// tool, MCP, hook, factory/subagent) never need an alias: they're matched on
// the name the SDK/tool itself reports.
var canonicalToolAliases = map[string]string{
	"readfile":  "read",
	"writefile": "write",
}

// canonicalToolName normalizes a declared or requested tool name for
// matching: trims whitespace, lowercases, strips a recognized
// source-qualifier prefix ("builtin:", "mcp:", "custom:") if present, and
// resolves known aliases (see [canonicalToolAliases]) to their canonical
// form. This keeps `tools: [bash]` matching a request for "builtin:bash",
// and `tools: [readFile]` matching an actual read attempt, without treating
// source-qualification or alias spelling as significant -- matching is still
// an exact comparison on the resolved name (never a regex).
func canonicalToolName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if idx := strings.Index(name, ":"); idx > 0 {
		switch name[:idx] {
		case "builtin", "mcp", "custom":
			name = name[idx+1:]
		}
	}
	if alias, ok := canonicalToolAliases[name]; ok {
		name = alias
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
