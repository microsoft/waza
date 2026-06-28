package copilotconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-viper/mapstructure/v2"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/models"
)

// ConvertMCPServers converts eval YAML mcp_servers entries into Copilot SDK MCP
// configs. Invalid entries are skipped after emitting a warning through warnf.
func ConvertMCPServers(serverConfigs map[string]any, warnf func(string, ...any)) map[string]copilot.MCPServerConfig {
	return ConvertMCPServersWithMocks(serverConfigs, nil, "", warnf)
}

// ConvertMCPServersWithMocks converts eval YAML mcp_servers entries and
// mcp_mocks entries into Copilot SDK MCP configs. Mock servers are exposed as
// hermetic stdio MCP servers backed by this waza binary.
func ConvertMCPServersWithMocks(serverConfigs map[string]any, mocks []models.MCPMockConfig, baseDir string, warnf func(string, ...any)) map[string]copilot.MCPServerConfig {
	if warnf == nil {
		warnf = func(string, ...any) {}
	}
	if len(serverConfigs) == 0 {
		if len(mocks) == 0 {
			return nil
		}
	}

	result := make(map[string]copilot.MCPServerConfig, len(serverConfigs)+len(mocks))
	for name, cfg := range serverConfigs {
		cfgMap, ok := cfg.(map[string]any)
		if !ok {
			warnf("Warning: mcp_server %q config is not a map, skipping\n", name)
			continue
		}

		serverType, _ := cfgMap["type"].(string)
		switch strings.ToLower(serverType) {
		case "", "stdio":
			var stdio copilot.MCPStdioServerConfig
			if err := decode(cfgMap, &stdio); err != nil {
				warnf("Warning: mcp_server %q stdio config is invalid: %v, skipping\n", name, err)
				continue
			}
			result[name] = stdio
		case "http", "sse":
			var http copilot.MCPHTTPServerConfig
			if err := decode(cfgMap, &http); err != nil {
				warnf("Warning: mcp_server %q http config is invalid: %v, skipping\n", name, err)
				continue
			}
			result[name] = http
		default:
			warnf("Warning: mcp_server %q has unsupported type %q, skipping\n", name, serverType)
		}
	}

	for _, mock := range mocks {
		mockName := strings.TrimSpace(mock.Name)
		if mockName != "" {
			delete(result, mockName)
		}
		cfg, err := mcpmock.FromEvalConfig(mock, baseDir)
		if err != nil {
			warnf("Warning: mcp_mock %q config is invalid: %v, skipping\n", mock.Name, err)
			continue
		}
		stdio, err := mockServerConfig(*cfg)
		if err != nil {
			warnf("Warning: mcp_mock %q could not be configured: %v, skipping\n", cfg.Name, err)
			continue
		}
		result[cfg.Name] = stdio
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func mockServerConfig(cfg mcpmock.Config) (copilot.MCPStdioServerConfig, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return copilot.MCPStdioServerConfig{}, fmt.Errorf("marshal mock config: %w", err)
	}
	configFile, err := writeMockConfigFile(cfg.Name, data)
	if err != nil {
		return copilot.MCPStdioServerConfig{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return copilot.MCPStdioServerConfig{}, fmt.Errorf("resolve waza executable: %w", err)
	}
	return copilot.MCPStdioServerConfig{
		Command: exe,
		Args:    []string{"__mcp-mock", "--config-file", configFile},
		Env: map[string]string{
			"WAZA_NO_UPDATE_CHECK": "1",
		},
	}, nil
}

func writeMockConfigFile(name string, data []byte) (string, error) {
	safeName := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	if safeName == "" {
		safeName = "mock"
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("waza-mcp-mock-%s-*.json", safeName))
	file, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return "", fmt.Errorf("create mock config file: %w", err)
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", fmt.Errorf("secure mock config file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", fmt.Errorf("write mock config file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return "", fmt.Errorf("close mock config file: %w", err)
	}
	return file.Name(), nil
}

func decode(input map[string]any, output any) error {
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           output,
		TagName:          "json",
		WeaklyTypedInput: true,
	})
	if err != nil {
		return fmt.Errorf("create decoder: %w", err)
	}
	if err := decoder.Decode(input); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}
