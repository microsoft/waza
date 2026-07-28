package graders

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
)

// normalizeToolCallArgs returns the tool call's arguments as a generic
// map[string]any suitable for argument matchers. Known fields recognized by
// ToolCallArgs (path, file_text, command, description, skill) are merged with
// any engine-specific extras captured under ToolCallArgs.Extra (populated by
// mapstructure's ",remain" support), so MCP tools and other arbitrary
// argument keys (e.g. `query`, `limit`) are visible to matchers. Empty-valued
// known fields are omitted to avoid spurious matches on the zero value;
// keys in Extra are passed through as-is.
//
// `override` is an optional canonical args map (typically sourced from the
// matching RunResult.ToolEvents[].Args record). It is layered underneath
// ToolCallArgs.Extra so that offline grading from results.json — where
// ToolCallArgs.Extra is empty because it is not JSON-persisted — still sees
// MCP-specific keys like `query`. Typed non-empty fields (path/command/etc.)
// always win to preserve the tool-specific matcher behavior.
func normalizeToolCallArgs(call models.ToolCall, override map[string]any) (map[string]any, error) {
	data, err := json.Marshal(call.Arguments)
	if err != nil {
		return nil, fmt.Errorf("marshaling tool args: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshaling tool args: %w", err)
	}
	out := make(map[string]any, len(raw)+len(call.Arguments.Extra)+len(override))
	for k, v := range raw {
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		out[k] = v
	}
	// Merge engine-specific extras and the ToolEvent override. Typed fields
	// (already written above) win; between the remaining two, we prefer the
	// live in-memory Extra bag because it matches historical behavior. The
	// override fills any keys the Extra bag lacks (which is the offline
	// results.json round-trip case, where Extra is empty).
	for k, v := range call.Arguments.Extra {
		if _, present := out[k]; present {
			continue
		}
		out[k] = v
	}
	for k, v := range override {
		if _, present := out[k]; present {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// toolEventArgsFor returns the canonical args map for a specific tool call by
// consulting the ToolEvents slice. It correlates by:
//
//  1. exact match on ToolCallID when both call.ID and event.ToolCallID are
//     non-empty; else
//  2. positional match by tool name — the n-th event whose ToolName equals
//     call.Name (case-insensitive) that has not already been consumed by an
//     earlier same-name call.
//
// Returns nil when no correlation can be established, or when the correlated
// event's Args is not a JSON object (arrays / scalars are not addressable by
// argmatcher keys).
//
// callsSoFar counts how many previous calls share the same case-insensitive
// name — pass the caller's positional index within its same-name run.
func toolEventArgsFor(call models.ToolCall, callsSoFar int, events []models.ToolEvent) map[string]any {
	if len(events) == 0 {
		return nil
	}
	if call.ID != "" {
		for _, ev := range events {
			if ev.ToolCallID == "" {
				continue
			}
			if ev.ToolCallID == call.ID {
				return coerceArgsToMap(ev.Args)
			}
		}
	}
	// Positional fallback: pick the (callsSoFar)-th event with a matching
	// name. Only consider events whose ToolCallID is empty, so we don't
	// steal args from a call that had an unrelated non-matching ID.
	target := strings.ToLower(call.Name)
	seen := 0
	for _, ev := range events {
		if !strings.EqualFold(ev.ToolName, target) {
			continue
		}
		if call.ID != "" && ev.ToolCallID != "" && ev.ToolCallID != call.ID {
			// Different explicit IDs — not the same call.
			continue
		}
		if seen == callsSoFar {
			return coerceArgsToMap(ev.Args)
		}
		seen++
	}
	return nil
}

// coerceArgsToMap normalises a ToolEvent.Args value into a keyed map suitable
// for argmatcher lookup. Object-shaped values are returned as-is; anything
// else (arrays, scalars, nil) returns nil so callers can fall back to the
// in-memory ToolCallArgs shape.
func coerceArgsToMap(v any) map[string]any {
	switch a := v.(type) {
	case map[string]any:
		return a
	case map[string]string:
		out := make(map[string]any, len(a))
		for k, s := range a {
			out[k] = s
		}
		return out
	default:
		return nil
	}
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
