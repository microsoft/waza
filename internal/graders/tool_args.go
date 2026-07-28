package graders

import (
	"encoding/json"
	"fmt"
	"regexp"

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
// canonical, when non-nil and shaped as a map[string]any, is the
// JSON-preserved args payload from the corresponding tool_event (schema
// v1.1+). It is used as a fallback source for arg keys that are absent
// from both the typed ToolCallArgs fields and Extra. This matters for
// offline grading (`waza grade`) because ToolCallArgs.Extra is
// json:"-" and therefore dropped across a results.json round-trip,
// while tool_events[].args preserves the full engine payload. Typed
// fields on ToolCallArgs take precedence when both are populated;
// canonical fills in only what would otherwise be missing.
func normalizeToolCallArgs(call models.ToolCall, canonical any) (map[string]any, error) {
	data, err := json.Marshal(call.Arguments)
	if err != nil {
		return nil, fmt.Errorf("marshaling tool args: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshaling tool args: %w", err)
	}
	canonicalMap, _ := canonical.(map[string]any)
	out := make(map[string]any, len(raw)+len(call.Arguments.Extra)+len(canonicalMap))
	for k, v := range raw {
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		out[k] = v
	}
	// Merge engine-specific extras next so they win over zero-valued known
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
	// Finally, fall back to the canonical tool_event args payload for any
	// keys still missing. This restores engine-specific arg keys that
	// were dropped when SessionDigest was written to disk and re-read for
	// offline grading. Typed fields and Extra take precedence when they
	// carry a value, so live-run behavior is unchanged.
	for k, v := range canonicalMap {
		if _, present := out[k]; present {
			continue
		}
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// toolEventArgsByCall builds a lookup that returns the canonical args
// payload for a given ToolCall from the run's tool_events. Correlation
// prefers ToolCallID (schema v1.1+ populates it for every event); when
// IDs are missing on either side the lookup falls back to positional
// order, which matches how buildToolEvents derives events from the SDK
// session stream (same source, same order as SessionDigest.ToolCalls).
// A nil or empty events slice yields a lookup that always returns nil,
// preserving pre-fix behavior for graders that don't receive tool
// events.
func toolEventArgsByCall(events []models.ToolEvent) func(idx int, call models.ToolCall) any {
	if len(events) == 0 {
		return func(int, models.ToolCall) any { return nil }
	}
	byID := make(map[string]any, len(events))
	for i := range events {
		if id := events[i].ToolCallID; id != "" {
			byID[id] = events[i].Args
		}
	}
	return func(idx int, call models.ToolCall) any {
		if call.ID != "" {
			if v, ok := byID[call.ID]; ok {
				return v
			}
		}
		if idx >= 0 && idx < len(events) {
			return events[idx].Args
		}
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
