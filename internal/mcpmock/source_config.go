package mcpmock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
)

// SourceResponse owns an unnormalized response fragment, including output
// presence, duplicate fields, and number lexemes used by finite validation.
type SourceResponse struct {
	Format models.FaultSourceFormat
	Data   []byte
}

// SourceConfig pairs native configuration with its selected source fragments.
// It owns its data independently of input maps, slices, and fixture files.
// Finite sources are validated here, not registered for native execution.
type SourceConfig struct {
	Config    *Config
	Responses map[string][]SourceResponse
}

// FromEvalConfigWithSources captures each external JSON file once, preserving
// native bundle recognition, lexical traversal, and whole-tool overrides.
// Inline source fragments are authoritative and must match the modeled response
// count; the modeled tool supplies metadata. All matcher resources are offline.
func FromEvalConfigWithSources(mock models.MCPMockConfig, baseDir string, inline map[string][]SourceResponse, enclosingVersion string) (*SourceConfig, error) {
	name := strings.TrimSpace(mock.Name)
	if name == "" {
		return nil, fmt.Errorf("mcp_mocks entry missing name")
	}
	resolved := &SourceConfig{
		Config:    &Config{Name: name, Tools: make(map[string]Tool)},
		Responses: make(map[string][]SourceResponse),
	}
	if mock.Fixtures != "" {
		dir := mock.Fixtures
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(baseDir, dir)
		}
		if err := loadSourceFixtureDir(resolved, dir); err != nil {
			return nil, fmt.Errorf("mcp mock %q fixtures: %w", name, err)
		}
	}
	for _, toolName := range slices.Sorted(maps.Keys(mock.Tools)) {
		tool := mock.Tools[toolName]
		sources, present := inline[toolName]
		if !present || len(sources) != len(tool.Responses) {
			return nil, fmt.Errorf("mcp mock %q tool %q inline source responses must be supplied and match modeled response count %d", name, toolName, len(tool.Responses))
		}
		// Decode metadata into owned JSON values without normalizing raw responses.
		data, err := json.Marshal(Tool{Description: tool.Description, InputSchema: tool.InputSchema})
		if err != nil {
			return nil, fmt.Errorf("mcp mock %q tool %q metadata: %w", name, toolName, err)
		}
		var owned Tool
		if err := json.Unmarshal(data, &owned); err != nil {
			return nil, fmt.Errorf("mcp mock %q tool %q metadata: %w", name, toolName, err)
		}
		resolved.Config.Tools[toolName] = owned
		resolved.Responses[toolName] = cloneSourceResponses(sources)
	}
	if len(resolved.Config.Tools) == 0 {
		return nil, fmt.Errorf("mcp mock %q must define at least one tool via tools or fixtures", name)
	}
	toolNames := slices.Sorted(maps.Keys(resolved.Config.Tools))
	for _, toolName := range toolNames {
		sources := resolved.Responses[toolName]
		if len(sources) == 0 {
			return nil, fmt.Errorf("mcp mock %q tool %q must define at least one response", name, toolName)
		}
		for i, source := range sources {
			if err := models.ValidateMCPFaultResponse(source.Data, source.Format, enclosingVersion); err != nil {
				return nil, fmt.Errorf("mcp mock %q tool %q response %d: %w", name, toolName, i, err)
			}
		}
	}
	// Only selected sources reach conversion and canonical matcher compilation.
	for _, toolName := range toolNames {
		tool := resolved.Config.Tools[toolName]
		tool.Responses = make([]Response, len(resolved.Responses[toolName]))
		for i, source := range resolved.Responses[toolName] {
			fields, _, err := models.DecodeFaultSource(source.Data, source.Format)
			if err != nil {
				return nil, fmt.Errorf("mcp mock %q tool %q response %d: %w", name, toolName, i, err)
			}
			data, err := json.Marshal(fields)
			if err != nil {
				return nil, fmt.Errorf("mcp mock %q tool %q response %d: %w", name, toolName, i, err)
			}
			if err := json.Unmarshal(data, &tool.Responses[i]); err != nil {
				return nil, fmt.Errorf("mcp mock %q tool %q response %d: %w", name, toolName, i, err)
			}
			if err := validateResponse(tool.Responses[i], schemaloader.Offline{}); err != nil {
				return nil, fmt.Errorf("mcp mock %q tool %q response %d: %w", name, toolName, i, err)
			}
		}
		resolved.Config.Tools[toolName] = tool
	}
	return resolved, nil
}

type sourceFixtureTool struct {
	Responses []json.RawMessage `json:"responses"`
}

func loadSourceFixtureDir(resolved *SourceConfig, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", dir)
	}
	return filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") {
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
		// Capture fragments first, but use exactly the native typed recognition:
		// a malformed/empty bundle can still be a valid basename-named tool.
		var rawBundle struct {
			Tools map[string]sourceFixtureTool `json:"tools"`
		}
		rawErr := json.Unmarshal(data, &rawBundle)
		var bundle struct {
			Tools map[string]Tool `json:"tools"`
		}
		if err := json.Unmarshal(data, &bundle); err == nil && len(bundle.Tools) > 0 {
			if rawErr != nil {
				return fmt.Errorf("%s: %w", path, rawErr)
			}
			for name, tool := range bundle.Tools {
				resolved.Config.Tools[name] = tool
				resolved.Responses[name] = fixtureSourceResponses(rawBundle.Tools[name].Responses)
			}
			return nil
		}
		var rawTool sourceFixtureTool
		rawErr = json.Unmarshal(data, &rawTool)
		var tool Tool
		if err := json.Unmarshal(data, &tool); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if rawErr != nil {
			return fmt.Errorf("%s: %w", path, rawErr)
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		resolved.Config.Tools[name] = tool
		resolved.Responses[name] = fixtureSourceResponses(rawTool.Responses)
		return nil
	})
}

func fixtureSourceResponses(raw []json.RawMessage) []SourceResponse {
	sources := make([]SourceResponse, len(raw))
	for i, data := range raw {
		sources[i] = SourceResponse{Format: models.FaultSourceJSON, Data: bytes.Clone(data)}
	}
	return sources
}

func cloneSourceResponses(sources []SourceResponse) []SourceResponse {
	owned := make([]SourceResponse, len(sources))
	for i, source := range sources {
		owned[i] = SourceResponse{Format: source.Format, Data: bytes.Clone(source.Data)}
	}
	return owned
}
