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
	// fields.
	for k, v := range call.Arguments.Extra {
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		if _, present := out[k]; present {
			continue
		}
		out[k] = v
	}
	return out, nil
}

func toolCallsForGrading(session *models.SessionDigest, toolEvents []models.ToolEvent) []models.ToolCall {
	if len(toolEvents) == 0 {
		if session == nil {
			return nil
		}
		return session.ToolCalls
	}

	fallbacksByID := make(map[string]models.ToolCall)
	fallbacksByName := make(map[string][]models.ToolCall)
	if session != nil {
		for _, call := range session.ToolCalls {
			if call.ID != "" {
				fallbacksByID[call.ID] = call
			}
			fallbacksByName[call.Name] = append(fallbacksByName[call.Name], call)
		}
	}

	calls := make([]models.ToolCall, 0, len(toolEvents))
	nameOffsets := make(map[string]int)
	for _, event := range toolEvents {
		if event.ToolName == "" {
			continue
		}

		call := models.ToolCall{
			ID:      event.ToolCallID,
			Name:    event.ToolName,
			Success: event.Success,
		}
		if event.ToolCallID != "" {
			if fallback, ok := fallbacksByID[event.ToolCallID]; ok {
				call.Result = fallback.Result
				call.Arguments = fallback.Arguments
			}
		} else if byName := fallbacksByName[event.ToolName]; len(byName) > nameOffsets[event.ToolName] {
			fallback := byName[nameOffsets[event.ToolName]]
			nameOffsets[event.ToolName]++
			call.Result = fallback.Result
			call.Arguments = fallback.Arguments
		}

		if event.Args != nil {
			call.Arguments = toolCallArgsFromEvent(event.Args)
		}
		calls = append(calls, call)
	}
	if len(calls) == 0 && session != nil {
		return session.ToolCalls
	}
	return calls
}

func toolCallArgsFromEvent(args any) models.ToolCallArgs {
	raw, ok := normalizeToolEventArgs(args)
	if !ok {
		return models.ToolCallArgs{}
	}

	out := models.ToolCallArgs{}
	if s, ok := raw["path"].(string); ok {
		out.Path = s
	}
	if s, ok := raw["file_text"].(string); ok {
		out.FileText = s
	}
	if s, ok := raw["command"].(string); ok {
		out.Command = s
	}
	if s, ok := raw["description"].(string); ok {
		out.Description = s
	}
	if s, ok := raw["skill"].(string); ok {
		out.Skill = s
	}

	out.Extra = raw
	return out
}

func normalizeToolEventArgs(args any) (map[string]any, bool) {
	data, err := json.Marshal(args)
	if err != nil {
		return nil, false
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}
	return raw, true
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
