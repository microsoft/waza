package graders

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
)

// resolveToolCallArgs returns the map of arguments to use for a tool call
// when evaluating argument matchers. It prefers the canonical
// ToolEvent.Args payload (looked up by ToolCallID) which is preserved
// verbatim through the results.json round-trip (see #474), and falls
// back to normalizeToolCallArgs on the ToolCall itself when no matching
// event is available or the event's Args is not a JSON object.
//
// events may be nil (e.g. tests or engines that do not surface tool
// events); in that case the fallback path is always used.
func resolveToolCallArgs(call models.ToolCall, events []models.ToolEvent) (map[string]any, error) {
	if len(events) > 0 && call.ID != "" {
		for i := range events {
			if events[i].ToolCallID != call.ID {
				continue
			}
			if m, ok := toolEventArgsMap(events[i].Args); ok {
				return m, nil
			}
			break
		}
	}
	return normalizeToolCallArgs(call)
}

// toolEventArgsMap coerces a ToolEvent.Args value into a map[string]any
// when possible. Args is stored as an untyped any so it may hold any
// JSON-compatible value; only object-shaped payloads carry addressable
// argument keys for matcher evaluation. Returns (nil, false) for nil,
// scalars, and arrays so the caller can fall back to the legacy path.
func toolEventArgsMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case map[string]any:
		return t, true
	}
	return nil, false
}

// normalizeToolCallArgs returns the tool call's arguments as a generic
// map[string]any suitable for argument matchers. Known fields recognized by
// ToolCallArgs (path, file_text, command, description, skill) are merged with
// any engine-specific extras captured under ToolCallArgs.Extra (populated by
// mapstructure's ",remain" support), so MCP tools and other arbitrary
// argument keys (e.g. `query`, `limit`) are visible to matchers. Empty-valued
// known fields are omitted to avoid spurious matches on the zero value;
// keys in Extra are passed through as-is.
//
// Note: ToolCallArgs.Extra has json:"-" so it does NOT survive a
// results.json round-trip. For offline grading, resolveToolCallArgs
// prefers ToolEvent.Args (which is JSON-persisted verbatim) and only
// falls back to this function when no matching event is available.
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
