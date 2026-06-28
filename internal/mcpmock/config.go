package mcpmock

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Config is the fully resolved configuration for one deterministic MCP mock server.
type Config struct {
	Name  string          `json:"name"`
	Tools map[string]Tool `json:"tools"`
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
}

// FromEvalConfig resolves an eval.yaml mcp_mocks entry into a mock server config.
func FromEvalConfig(mock models.MCPMockConfig, baseDir string) (*Config, error) {
	name := strings.TrimSpace(mock.Name)
	if name == "" {
		return nil, fmt.Errorf("mcp_mocks entry missing name")
	}

	cfg := &Config{Name: name, Tools: make(map[string]Tool)}
	if mock.Fixtures != "" {
		fixtureDir := mock.Fixtures
		if !filepath.IsAbs(fixtureDir) {
			fixtureDir = filepath.Join(baseDir, fixtureDir)
		}
		if err := loadFixtureDir(cfg, fixtureDir); err != nil {
			return nil, fmt.Errorf("mcp mock %q fixtures: %w", name, err)
		}
	}

	for toolName, tool := range mock.Tools {
		cfg.Tools[toolName] = Tool{
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Responses:   convertResponses(tool.Responses),
		}
	}

	if len(cfg.Tools) == 0 {
		return nil, fmt.Errorf("mcp mock %q must define at least one tool via tools or fixtures", name)
	}
	for toolName, tool := range cfg.Tools {
		if len(tool.Responses) == 0 {
			return nil, fmt.Errorf("mcp mock %q tool %q must define at least one response", name, toolName)
		}
		for i, response := range tool.Responses {
			if err := validateResponse(response); err != nil {
				return nil, fmt.Errorf("mcp mock %q tool %q response %d: %w", name, toolName, i, err)
			}
		}
	}

	return cfg, nil
}

func loadFixtureDir(cfg *Config, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", dir)
	}

	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
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
		var bundle struct {
			Tools map[string]Tool `json:"tools"`
		}
		if err := json.Unmarshal(data, &bundle); err == nil && len(bundle.Tools) > 0 {
			for name, tool := range bundle.Tools {
				cfg.Tools[name] = tool
			}
			return nil
		}

		var tool Tool
		if err := json.Unmarshal(data, &tool); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		cfg.Tools[name] = tool
		return nil
	})
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
		})
	}
	return out
}

func validateResponse(response Response) error {
	for field, pattern := range response.MatchRegex {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("match_regex field %q has invalid regex %q: %w", field, pattern, err)
		}
	}
	if len(response.MatchSchema) > 0 {
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("memory://mcp-mock-schema.json", response.MatchSchema); err != nil {
			return fmt.Errorf("match_schema is invalid: %w", err)
		}
		if _, err := compiler.Compile("memory://mcp-mock-schema.json"); err != nil {
			return fmt.Errorf("match_schema is invalid: %w", err)
		}
	}
	return nil
}
