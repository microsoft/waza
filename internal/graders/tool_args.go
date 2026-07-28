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

// toolEventArgsByCallID indexes ToolEvent.Args by ToolCallID for O(1) lookup
// from a graders.Context. Only entries whose Args deserialise to a JSON
// object are included; scalar or nil args are skipped because argument
// matchers key on named fields. Duplicate IDs are resolved by keeping the
// first entry (start events precede complete events; both carry the same
// arg payload after buildToolEvents normalisation).
func toolEventArgsByCallID(events []models.ToolEvent) map[string]map[string]any {
	if len(events) == 0 {
		return nil
	}
	out := make(map[string]map[string]any, len(events))
	for _, ev := range events {
		if ev.ToolCallID == "" {
			continue
		}
		if _, exists := out[ev.ToolCallID]; exists {
			continue
		}
		m, ok := coerceArgsToMap(ev.Args)
		if !ok {
			continue
		}
		out[ev.ToolCallID] = m
	}
	return out
}

// coerceArgsToMap best-effort converts a ToolEvent.Args value (which is
// `any` and may arrive as map[string]any from a JSON round-trip, or as a
// typed struct from a live in-memory build) into a map[string]any. Returns
// false when the payload isn't object-shaped.
func coerceArgsToMap(args any) (map[string]any, bool) {
	if args == nil {
		return nil, false
	}
	if m, ok := args.(map[string]any); ok {
		return m, true
	}
	// Fall back to a JSON round-trip so typed structs (or map[string]string
	// etc.) also normalise to a plain map. This mirrors how buildToolEvents
	// canonicalises args, but is defensive for future callers.
	data, err := json.Marshal(args)
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, false
	}
	return out, true
}

// normalizeToolCallArgsWithEvents returns the tool call's arguments as a
// map[string]any, preferring keys from a matching ToolEvent (looked up by
// call.ID in eventArgs) over the typed ToolCallArgs. This is the offline-
// safe path: ToolCallArgs.Extra is `json:"-"` and is dropped when a
// results.json is written and reloaded for `waza grade`, whereas
// ToolEvent.Args is JSON-preserving. When no matching event exists, this
// falls back to normalizeToolCallArgs (the live-run behavior).
//
// Behavior on collision: the typed ToolCallArgs values win over
// ToolEvent.Args, matching normalizeToolCallArgs's original semantics
// (typed known fields > Extra). ToolEvent keys only fill entries missing
// from the typed base — restoring MCP args like `query` that would
// otherwise be absent after a round-trip.
func normalizeToolCallArgsWithEvents(call models.ToolCall, eventArgs map[string]map[string]any) (map[string]any, error) {
	base, err := normalizeToolCallArgs(call)
	if err != nil {
		return nil, err
	}
	if len(eventArgs) == 0 || call.ID == "" {
		return base, nil
	}
	evArgs, ok := eventArgs[call.ID]
	if !ok || len(evArgs) == 0 {
		return base, nil
	}
	for k, v := range evArgs {
		if _, present := base[k]; present {
			continue
		}
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		base[k] = v
	}
	return base, nil
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
