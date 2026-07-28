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

func normalizeToolEventArgs(event models.ToolEvent) (map[string]any, error) {
	if event.Args == nil {
		return map[string]any{}, nil
	}
	data, err := json.Marshal(event.Args)
	if err != nil {
		return nil, fmt.Errorf("marshaling tool event args: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshaling tool event args: %w", err)
	}
	return raw, nil
}

type toolCallRecord struct {
	id      string
	name    string
	args    map[string]any
	argsErr error
}

func toolCallRecordsForMatching(gCtx *Context) []toolCallRecord {
	if gCtx == nil {
		return nil
	}
	var records []toolCallRecord
	seenIDs := make(map[string]struct{}, len(gCtx.ToolEvents))
	if len(gCtx.ToolEvents) > 0 {
		records = make([]toolCallRecord, 0, len(gCtx.ToolEvents))
		for _, event := range gCtx.ToolEvents {
			if event.ToolName == "" {
				continue
			}
			if event.ToolCallID != "" {
				seenIDs[event.ToolCallID] = struct{}{}
			}
			args, err := normalizeToolEventArgs(event)
			records = append(records, toolCallRecord{
				id:      event.ToolCallID,
				name:    event.ToolName,
				args:    args,
				argsErr: err,
			})
		}
	}
	if gCtx.Session == nil {
		return records
	}
	if records == nil {
		records = make([]toolCallRecord, 0, len(gCtx.Session.ToolCalls))
	}
	for _, call := range gCtx.Session.ToolCalls {
		if call.ID != "" {
			if _, ok := seenIDs[call.ID]; ok {
				continue
			}
		}
		args, err := normalizeToolCallArgs(call)
		records = append(records, toolCallRecord{
			id:      call.ID,
			name:    call.Name,
			args:    args,
			argsErr: err,
		})
	}
	return records
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
