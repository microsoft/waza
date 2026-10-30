package mcpmock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Config is the fully resolved configuration for one deterministic MCP mock server.
type Config struct {
	Name  string          `json:"name"`
	Tools map[string]Tool `json:"tools"`
	// StateDir persists finite sequence progress across server restarts in one
	// task attempt. Empty keeps the legacy per-process state.
	StateDir string `json:"state_dir,omitempty"`
}

// Tool defines one mocked MCP tool.
type Tool struct {
	Description string     `json:"description,omitempty"`
	InputSchema any        `json:"input_schema,omitempty"`
	Responses   []Response `json:"responses,omitempty"`
}

// Response defines one fixture response and its matching rules.
type Response struct {
	Match       map[string]any    `json:"match,omitempty"`
	MatchSchema map[string]any    `json:"match_schema,omitempty"`
	MatchRegex  map[string]string `json:"match_regex,omitempty"`
	Return      any               `json:"return,omitempty"`
	Error       string            `json:"error,omitempty"`
	Sequence    []ResponseStep    `json:"sequence,omitempty"`
}

type ResponseStep struct {
	Return  any    `json:"return,omitempty"`
	Error   string `json:"error,omitempty"`
	DelayMS int64  `json:"delay_ms,omitempty"`
}

// FromEvalConfig resolves an eval.yaml mcp_mocks entry into a mock server config.
func FromEvalConfig(mock models.MCPMockConfig, baseDir string) (*Config, error) {
	return fromEvalConfig(mock, baseDir, nil, "", false)
}

// FromEvalConfigWithVersion resolves public finite sequences only when their
// enclosing eval explicitly enables scenario schema 2.0.
func FromEvalConfigWithVersion(mock models.MCPMockConfig, baseDir, version string) (*Config, error) {
	return fromEvalConfig(mock, baseDir, nil, version, true)
}

// ValidateEvalMocks checks runtime mock admission without creating subprocess
// configuration files or starting an engine. Offline callers use the offline resolver.
func ValidateEvalMocks(mocks []models.MCPMockConfig, baseDir, version string) error {
	var errs []error
	for _, mock := range mocks {
		if _, err := FromEvalConfigWithVersion(mock, baseDir, version); err != nil {
			errs = append(errs, fmt.Errorf("resolve MCP mock %q: %w", mock.Name, err))
		}
	}
	return errors.Join(errs...)
}

// FromEvalConfigOffline reads declared local fixtures while refusing external
// schema resources, including the compiler's default filesystem loader.
// Response validation errors retain the resolved config for inventory only.
func FromEvalConfigOffline(mock models.MCPMockConfig, baseDir string) (*Config, error) {
	return fromEvalConfig(mock, baseDir, schemaloader.Offline{}, "", false)
}

// FromEvalConfigOfflineWithVersion validates selected finite responses without
// allowing matcher schemas to load external resources.
func FromEvalConfigOfflineWithVersion(mock models.MCPMockConfig, baseDir, version string) (*Config, error) {
	return fromEvalConfig(mock, baseDir, schemaloader.Offline{}, version, true)
}

func fromEvalConfig(mock models.MCPMockConfig, baseDir string, loader jsonschema.URLLoader, version string, allowSequences bool) (*Config, error) {
	name := strings.TrimSpace(mock.Name)
	if name == "" {
		return nil, fmt.Errorf("mcp_mocks entry missing name")
	}

	cfg := &Config{Name: name, Tools: make(map[string]Tool)}
	var errs []error
	if mock.Fixtures != "" {
		fixtureDir := mock.Fixtures
		if !filepath.IsAbs(fixtureDir) {
			fixtureDir = filepath.Join(baseDir, fixtureDir)
		}
		if err := loadFixtureDir(cfg, fixtureDir, version, allowSequences, mock.Tools); err != nil {
			failure := fmt.Errorf("mcp mock %q fixtures: %w", name, err)
			if loader == nil {
				return nil, failure
			}
			errs = append(errs, failure)
		}
	}

	for toolName, tool := range mock.Tools {
		resolved := Tool{
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Responses:   convertResponses(tool.Responses),
		}
		if !allowSequences {
			clearSequences(&resolved)
		}
		cfg.Tools[toolName] = resolved
	}

	if len(cfg.Tools) == 0 {
		return nil, errors.Join(append(errs, fmt.Errorf("mcp mock %q must define at least one tool via tools or fixtures", name))...)
	}
	for toolName, tool := range cfg.Tools {
		if len(tool.Responses) == 0 {
			failure := fmt.Errorf("mcp mock %q tool %q must define at least one response", name, toolName)
			if loader == nil {
				return nil, failure
			}
			errs = append(errs, failure)
			continue
		}
		for i, response := range tool.Responses {
			if err := validateResponse(response, loader); err != nil {
				failure := fmt.Errorf("mcp mock %q tool %q response %d: %w", name, toolName, i, err)
				if loader == nil {
					return nil, failure
				}
				errs = append(errs, failure)
			}
			if len(response.Sequence) > 0 && version != models.ScenarioSchemaVersion {
				failure := fmt.Errorf("mcp mock %q tool %q response %d: fault sequence requires explicit enclosing scenario schemaVersion %s", name, toolName, i, models.ScenarioSchemaVersion)
				if loader == nil {
					return cfg, failure
				}
				errs = append(errs, failure)
			}
			if len(response.Sequence) > models.MaxMockFaultSequenceSteps {
				errs = append(errs, fmt.Errorf("mcp mock %q tool %q response %d: fault sequence must contain at most %d steps", name, toolName, i, models.MaxMockFaultSequenceSteps))
			}
			for stepIndex, step := range response.Sequence {
				if step.DelayMS < 0 || step.DelayMS > models.MaxMockFaultDelayMS {
					errs = append(errs, fmt.Errorf("mcp mock %q tool %q response %d sequence[%d]: delay_ms must be between 0 and %d", name, toolName, i, stepIndex, models.MaxMockFaultDelayMS))
				}
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return cfg, err
	}

	return cfg, nil
}

func loadFixtureDir(cfg *Config, dir, version string, allowSequences bool, overrides map[string]models.MCPMockTool) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", dir)
	}

	sourceByTool := make(map[string][]byte)
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		typedData := stripSourceSequences(data)
		var bundle struct {
			Tools map[string]Tool `json:"tools"`
		}
		if err := json.Unmarshal(typedData, &bundle); err == nil && len(bundle.Tools) > 0 {
			var rawBundle struct {
				Tools map[string]json.RawMessage `json:"tools"`
			}
			if err := json.Unmarshal(data, &rawBundle); err != nil {
				return err
			}
			for name, tool := range bundle.Tools {
				if !allowSequences {
					clearSequences(&tool)
				}
				cfg.Tools[name] = tool
				sourceByTool[name] = rawBundle.Tools[name]
			}
			return nil
		}

		var tool Tool
		if err := json.Unmarshal(typedData, &tool); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if !allowSequences {
			clearSequences(&tool)
		}
		cfg.Tools[name] = tool
		sourceByTool[name] = data
		return nil
	}); err != nil {
		return err
	}
	if allowSequences {
		var errs []error
		for toolName, source := range sourceByTool {
			if _, overridden := overrides[toolName]; overridden {
				continue
			}
			if err := validateFixtureTool(source, version); err != nil {
				errs = append(errs, fmt.Errorf("tool %q: %w", toolName, err))
				continue
			}
			var tool Tool
			if err := json.Unmarshal(source, &tool); err != nil {
				return fmt.Errorf("tool %q: %w", toolName, err)
			}
			cfg.Tools[toolName] = tool
		}
		return errors.Join(errs...)
	}
	return nil
}

func convertResponses(in []models.MCPMockResponse) []Response {
	if len(in) == 0 {
		return nil
	}
	out := make([]Response, 0, len(in))
	for _, r := range in {
		out = append(out, Response{
			Match:       r.Match,
			MatchSchema: r.MatchSchema,
			MatchRegex:  r.MatchRegex,
			Return:      r.Return,
			Error:       r.Error,
			Sequence:    convertSteps(r.Sequence),
		})
	}
	return out
}

func convertSteps(in []models.MCPMockStep) []ResponseStep {
	if len(in) == 0 {
		return nil
	}
	out := make([]ResponseStep, 0, len(in))
	for _, step := range in {
		out = append(out, ResponseStep{Return: step.Return, Error: step.Error, DelayMS: step.DelayMS})
	}
	return out
}

func validateFixtureTool(data []byte, version string) error {
	var tool struct {
		Responses []json.RawMessage `json:"responses"`
	}
	if err := json.Unmarshal(data, &tool); err != nil {
		return err
	}
	var errs []error
	for index, response := range tool.Responses {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(response, &fields); err != nil {
			errs = append(errs, fmt.Errorf("response %d: %w", index, err))
			continue
		}
		_, hasSequence := fields["sequence"]
		_, hasDelay := fields["delay_ms"]
		if !hasSequence && !hasDelay {
			continue
		}
		if err := models.ValidateMCPFaultResponse(response, models.FaultSourceJSON, version); err != nil {
			errs = append(errs, fmt.Errorf("response %d: %w", index, err))
		}
	}
	return errors.Join(errs...)
}

func clearSequences(tool *Tool) {
	for index := range tool.Responses {
		tool.Responses[index].Sequence = nil
	}
}

func validateResponse(response Response, loader jsonschema.URLLoader) error {
	return models.ValidateMCPMockResponseWithLoader(models.MCPMockResponse{
		MatchRegex:  response.MatchRegex,
		MatchSchema: response.MatchSchema,
	}, loader)
}
