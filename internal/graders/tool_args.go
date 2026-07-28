package graders

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
)

// normalizedCall is a per-call view used by the tool_calls grader that
// unifies the two possible sources of arguments (canonical ToolEvents vs.
// legacy SessionDigest.ToolCalls). Args is the fully normalized argument
// bag suitable for argmatchers; ArgsErr is set only when normalization
// of the source data failed and no args map could be produced.
type normalizedCall struct {
	Name    string
	ID      string
	Args    map[string]any
	ArgsErr error
}

// buildNormalizedCalls returns the list of tool calls the grader should
// consider, preferring the canonical tool_events record when present.
//
// tool_events (schema 1.1+) is the source of truth: its Args is a raw
// JSON value that preserves MCP/custom tool arguments across
// results.json round-trips. SessionDigest.ToolCalls does not — its
// ToolCallArgs.Extra field is json:"-" and drops after unmarshal —
// so we only fall back to it when tool_events is empty (pre-1.1
// records, or engines that don't emit tool events).
func buildNormalizedCalls(gCtx *Context) []normalizedCall {
	if gCtx == nil {
		return nil
	}

	if len(gCtx.ToolEvents) > 0 {
		out := make([]normalizedCall, 0, len(gCtx.ToolEvents))
		for _, ev := range gCtx.ToolEvents {
			args, err := coerceEventArgs(ev.Args)
			out = append(out, normalizedCall{
				Name:    ev.ToolName,
				ID:      ev.ToolCallID,
				Args:    args,
				ArgsErr: err,
			})
		}
		return out
	}

	if gCtx.Session == nil {
		return nil
	}
	out := make([]normalizedCall, 0, len(gCtx.Session.ToolCalls))
	for _, call := range gCtx.Session.ToolCalls {
		args, err := normalizeToolCallArgs(call)
		out = append(out, normalizedCall{
			Name:    call.Name,
			ID:      call.ID,
			Args:    args,
			ArgsErr: err,
		})
	}
	return out
}

// coerceEventArgs converts the raw ToolEvent.Args value into a
// map[string]any suitable for argmatchers. It accepts either a
// map[string]any directly, or a JSON-marshalable value that
// unmarshals into an object; scalar / array payloads return nil map
// with no error (matchers on named keys will simply report "not
// present"). A nil input yields an empty map.
func coerceEventArgs(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	if m, ok := v.(map[string]any); ok {
		return m, nil
	}
	// Round-trip through JSON so any concrete type (struct, map with
	// non-string keys, etc.) that marshals to a JSON object is still
	// usable by matchers. Non-object payloads (arrays, scalars, null)
	// intentionally return an empty map so key-based matchers cleanly
	// report "not present" instead of erroring out.
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshaling tool_event args: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}, nil
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

// map[string]any suitable for argument matchers. Known fields recognized by
// ToolCallArgs (path, file_text, command, description, skill) are merged with
// any engine-specific extras captured under ToolCallArgs.Extra (populated by
// mapstructure's ",remain" support), so MCP tools and other arbitrary
// argument keys (e.g. `query`, `limit`) are visible to matchers. Empty-valued
// known fields are omitted to avoid spurious matches on the zero value;
// keys in Extra are passed through as-is.
func normalizeToolCallArgs(call models.ToolCall) (map[string]any, error) {
	data, err := json.Marshal(call.Arguments)
	if err != nil {
		return nil, fmt.Errorf("marshaling tool args: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshaling tool args: %w", err)
	}
	out := make(map[string]any, len(raw)+len(call.Arguments.Extra))
	for k, v := range raw {
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		out[k] = v
	}
	// Merge engine-specific extras last so they win over zero-valued known
	// fields. Known-field collisions (e.g. an MCP tool that happens to
	// declare a `path` arg with extra metadata) keep the typed value from
	// ToolCallArgs since it has already been written above and Extra by
	// construction holds only keys mapstructure could not place.
	for k, v := range call.Arguments.Extra {
		if _, present := out[k]; present {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// evaluateArgMatchers returns a slice of human-readable failures describing
// any matcher in `matchers` whose key was absent from `args` or whose value
// failed to match. An empty slice means every matcher passed.
func evaluateArgMatchers(matchers map[string]argmatcher.Matcher, args map[string]any) []string {
	if len(matchers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(matchers))
	for k := range matchers {
		keys = append(keys, k)
	}
	// deterministic order for stable feedback / replay
	sortStrings(keys)

	var failures []string
	for _, key := range keys {
		m := matchers[key]
		v, present := args[key]
		if !present {
			failures = append(failures, fmt.Sprintf("argument %q: not present on tool call", key))
			continue
		}
		ok, reason := m.Match(v)
		if !ok {
			failures = append(failures, fmt.Sprintf("argument %q: %s", key, reason))
		}
	}
	return failures
}

// compileToolRegex compiles a tool-name matcher. Empty patterns match any
// tool name. Case-insensitive by default.
func compileToolRegex(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	return regexp.Compile("(?i)" + pattern)
}

// sortStrings is a tiny inline sort to avoid pulling sort.Strings into hot
// paths from a single call site. Keeps allocations predictable.
func sortStrings(s []string) {
	// insertion sort — matcher keys are typically <5 entries.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
