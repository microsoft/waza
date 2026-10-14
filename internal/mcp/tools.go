package mcp

import "encoding/json"

// Tool describes an MCP tool with its input schema.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolsDef returns the list of MCP tools exposed by waza.
func ToolsDef() []Tool {
	return []Tool{
		{
			Name:        "waza_eval_list",
			Description: "List canonical eval specs and native-loadable scenario YAML in a directory",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"dir": {"type": "string", "description": "Directory to search recursively for canonical evals and native-loadable scenario .yaml/.yml files"}
				}
			}`),
		},
		{
			Name:        "waza_eval_get",
			Description: "Get details of an eval spec",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path to eval.yaml file"}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        "waza_eval_validate",
			Description: "Validate an eval spec for correctness",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {"type": "string", "description": "Path to eval.yaml file"}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        "waza_eval_run",
			Description: "Run an eval and return a run ID",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path":        {"type": "string", "description": "Path to eval.yaml file"},
					"model":       {"type": "string", "description": "Model to use for the eval"},
					"judge_model": {"type": "string", "description": "Model to use for judging"}
				},
				"required": ["path"]
			}`),
		},
		{
			Name:        "waza_task_list",
			Description: "List tasks defined in an eval spec",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"eval_path": {"type": "string", "description": "Path to eval.yaml file"}
				},
				"required": ["eval_path"]
			}`),
		},
		{
			Name:        "waza_run_status",
			Description: "Get the status of an eval run",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"run_id": {"type": "string", "description": "Run ID returned by waza_eval_run"}
				},
				"required": ["run_id"]
			}`),
		},
		{
			Name:        "waza_run_cancel",
			Description: "Cancel a running eval",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"run_id": {"type": "string", "description": "Run ID to cancel"}
				},
				"required": ["run_id"]
			}`),
		},
		{
			Name:        "waza_results_summary",
			Description: "Get aggregate KPI metrics across all eval runs",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"dir": {"type": "string", "description": "Directory containing result JSON files"}
				}
			}`),
		},
		{
			Name:        "waza_results_runs",
			Description: "List all eval runs with summary info",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"dir": {"type": "string", "description": "Directory containing result JSON files"}
				}
			}`),
		},
		{
			Name:        "waza_skill_check",
			Description: "Check if a skill is ready for submission (compliance, tokens, links, eval)",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"skill_path": {"type": "string", "description": "Path to skill directory"}
				},
				"required": ["skill_path"]
			}`),
		},
	}
}
