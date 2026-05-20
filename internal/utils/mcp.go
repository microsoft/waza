package utils

import (
	"encoding/json"

	copilot "github.com/github/copilot-sdk/go"
)

func ConvertMCPServerConfig(cfgMap map[string]any) (copilot.MCPServerConfig, bool) {
	if len(cfgMap) == 0 {
		return nil, false
	}

	cfgType := strVal(cfgMap, "type")
	url := strVal(cfgMap, "url")
	if cfgType == "http" || (cfgType == "" && url != "") {
		if url == "" {
			return nil, false
		}
		return copilot.MCPHTTPServerConfig{
			URL:     url,
			Headers: strMapVal(cfgMap, "headers"),
			Tools:   strSliceVal(cfgMap, "tools"),
			Timeout: intVal(cfgMap, "timeout"),
		}, true
	}

	command := strVal(cfgMap, "command")
	if command == "" {
		return nil, false
	}

	return copilot.MCPStdioServerConfig{
		Command: command,
		Args:    strSliceVal(cfgMap, "args"),
		Cwd:     strVal(cfgMap, "cwd"),
		Env:     strMapVal(cfgMap, "env"),
		Tools:   strSliceVal(cfgMap, "tools"),
		Timeout: intVal(cfgMap, "timeout"),
	}, true
}

func strVal(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func strSliceVal(m map[string]any, k string) []string {
	raw, _ := m[k].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func strMapVal(m map[string]any, k string) map[string]string {
	raw, _ := m[k].(map[string]any)
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]string, len(raw))
	for key, v := range raw {
		out[key], _ = v.(string)
	}
	return out
}

func intVal(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}
