package copilotconfig

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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
	return ConvertMCPServersWithVersion(serverConfigs, mocks, baseDir, "", warnf)
}

// ConvertMCPServersWithVersion permits finite mock responses only when the
// enclosing eval schema version explicitly supports them.
func ConvertMCPServersWithVersion(serverConfigs map[string]any, mocks []models.MCPMockConfig, baseDir, version string, warnf func(string, ...any)) map[string]copilot.MCPServerConfig {
	servers, err := convertMCPServers(serverConfigs, mocks, baseDir, version, warnf, false, nil)
	if err != nil && warnf != nil {
		warnf("Warning: MCP configuration failed: %v\n", err)
	}
	return servers
}

// ResolveMCPServersWithVersion fails execution admission rather than dropping a
// declared mock. The conversion-only adapters retain their legacy warning policy.
func ResolveMCPServersWithVersion(serverConfigs map[string]any, mocks []models.MCPMockConfig, baseDir, version string, warnf func(string, ...any)) (map[string]copilot.MCPServerConfig, error) {
	return convertMCPServers(serverConfigs, mocks, baseDir, version, warnf, true, nil)
}

// MCPMockAttempt owns persistent mock configuration and finite sequence state
// shared by every actor turn in one task attempt.
type MCPMockAttempt struct {
	dir          string
	parent, root *os.Root
	identity     os.FileInfo
	mocks        []*mcpAttemptMock
	closeErr     error
}

type mcpAttemptMock struct {
	name     string
	identity os.FileInfo
	files    map[string]os.FileInfo
}

// ResolveMCPServersForAttempt fails admission like ResolveMCPServersWithVersion.
// Mock servers may restart between turns without resetting finite sequences.
// The caller must Close the returned attempt after the final turn.
func ResolveMCPServersForAttempt(serverConfigs map[string]any, mocks []models.MCPMockConfig, baseDir, version string, warnf func(string, ...any)) (map[string]copilot.MCPServerConfig, *MCPMockAttempt, error) {
	attempt := &MCPMockAttempt{}
	servers, err := convertMCPServers(serverConfigs, mocks, baseDir, version, warnf, true, attempt.serverConfig)
	if err != nil {
		return nil, nil, errors.Join(err, attempt.Close())
	}
	if attempt.dir == "" {
		return servers, nil, nil
	}
	return servers, attempt, nil
}

// Close removes owned configuration and state through retained directory
// handles. Replaced entries are preserved. Repeated calls return the first
// cleanup result without retrying deletion.
func (a *MCPMockAttempt) Close() error {
	if a == nil {
		return nil
	}
	if a.parent == nil {
		return a.closeErr
	}
	var errs []error
	if a.root == nil {
		if a.dir != "" {
			errs = append(errs, fmt.Errorf("MCP mock attempt ownership unavailable; inspect directory created at %s", a.dir))
		}
	} else if err := a.checkIdentity(); err != nil {
		errs = append(errs, err)
	} else {
		for _, mock := range a.mocks {
			if err := a.removeMock(mock); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if a.root != nil {
		if err := a.root.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close MCP mock attempt directory %s: %w", a.dir, err))
		}
		a.root = nil
	}
	if a.dir != "" && len(errs) == 0 {
		if err := a.checkIdentity(); err != nil {
			errs = append(errs, err)
		} else if err := a.parent.Remove(filepath.Base(a.dir)); err != nil {
			errs = append(errs, fmt.Errorf("remove MCP mock attempt directory %s: %w", a.dir, err))
		}
	}
	if err := a.parent.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close MCP mock attempt parent %s: %w", filepath.Dir(a.dir), err))
	}
	a.parent = nil
	if len(errs) > 0 {
		a.closeErr = fmt.Errorf("MCP mock cleanup incomplete; configuration or state may remain in the original directory created at %s (if renamed, locate it before manual cleanup): %w", a.dir, errors.Join(errs...))
	}
	return a.closeErr
}

func (a *MCPMockAttempt) checkIdentity() error {
	current, err := os.Lstat(a.dir)
	if err != nil || !current.IsDir() || !os.SameFile(current, a.identity) {
		return fmt.Errorf("MCP mock attempt directory identity changed at %s: %w", a.dir, errors.Join(err, errors.New("refusing cleanup of replacement")))
	}
	current, err = a.parent.Lstat(filepath.Base(a.dir))
	if err != nil || !current.IsDir() || !os.SameFile(current, a.identity) {
		return fmt.Errorf("MCP mock attempt directory replaced under retained parent at %s: %w", a.dir, errors.Join(err, errors.New("refusing cleanup of replacement")))
	}
	return nil
}

func (a *MCPMockAttempt) removeMock(mock *mcpAttemptMock) error {
	mock.files["."] = mock.identity
	return a.removeEntry(a.root, mock.name, ".", mock)
}

func (a *MCPMockAttempt) removeEntry(parent *os.Root, name, relative string, mock *mcpAttemptMock) (resultErr error) {
	path := filepath.Join(a.dir, mock.name, relative)
	identity := mock.files[relative]
	current, err := parent.Lstat(name)
	if err != nil || !os.SameFile(current, identity) {
		return fmt.Errorf("MCP mock entry identity changed at %s: %w", path, errors.Join(err, errors.New("refusing cleanup of replacement")))
	}
	if !identity.IsDir() {
		if err := parent.Remove(name); err != nil {
			return fmt.Errorf("remove MCP mock file %s: %w", path, err)
		}
		return nil
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return fmt.Errorf("open MCP mock cleanup directory %s: %w", path, err)
	}
	rootClosed := false
	closeRoot := func() error {
		if rootClosed {
			return nil
		}
		rootClosed = true
		return root.Close()
	}
	defer func() {
		if err := closeRoot(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close MCP mock cleanup directory %s: %w", path, err))
		}
	}()
	current, err = root.Stat(".")
	if err != nil || !os.SameFile(current, identity) {
		return fmt.Errorf("MCP mock entry identity changed at %s: %w", path, errors.Join(err, errors.New("refusing cleanup of replacement")))
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return fmt.Errorf("read MCP mock cleanup directory %s: %w", path, err)
	}
	for _, entry := range entries {
		child := filepath.Join(relative, entry.Name())
		if _, owned := mock.files[child]; owned {
			resultErr = errors.Join(resultErr, a.removeEntry(root, entry.Name(), child, mock))
			continue
		}
		// Subprocesses create empty reservation markers only in the known
		// sequence directories. Never recursively remove unrecognized entries.
		info, err := root.Lstat(entry.Name())
		if err == nil && filepath.Dir(relative) == "state" &&
			info.Mode().IsRegular() && info.Size() == 0 &&
			len(entry.Name()) == len("step-00000000") && strings.HasPrefix(entry.Name(), "step-") &&
			strings.Trim(entry.Name()[len("step-"):], "0123456789") == "" {
			if err := root.Remove(entry.Name()); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove MCP mock sequence marker %s: %w", filepath.Join(path, entry.Name()), err))
			}
		} else {
			resultErr = errors.Join(resultErr, fmt.Errorf("unrecognized MCP mock cleanup entry %s; retained for inspection: %w", filepath.Join(path, entry.Name()), errors.Join(err, errors.New("refusing cleanup of unowned entry"))))
		}
	}
	if resultErr == nil {
		if err := closeRoot(); err != nil {
			return fmt.Errorf("close MCP mock cleanup directory %s: %w", path, err)
		}
		current, err = parent.Lstat(name)
		if err != nil || !os.SameFile(current, identity) {
			return fmt.Errorf("MCP mock entry identity changed during cleanup at %s: %w", path, errors.Join(err, errors.New("refusing cleanup of replacement")))
		}
		if err := parent.Remove(name); err != nil {
			return fmt.Errorf("remove MCP mock directory %s: %w", path, err)
		}
	}
	return resultErr
}

func (a *MCPMockAttempt) initialize() error {
	// These platforms retain pathnames, not directory identities.
	if runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		return errors.New("MCP mock attempts require handle-rooted filesystem ownership on this platform")
	}
	path, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return fmt.Errorf("resolve MCP mock attempt parent: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve absolute MCP mock attempt parent: %w", err)
	}
	a.parent, err = os.OpenRoot(path)
	if err != nil {
		return fmt.Errorf("open MCP mock attempt parent: %w", err)
	}
	name := "waza-mcp-attempt-" + rand.Text()
	if err := a.parent.Mkdir(name, 0700); err != nil {
		return fmt.Errorf("create MCP mock attempt directory: %w", err)
	}
	a.dir = filepath.Join(path, name)
	a.root, err = a.parent.OpenRoot(name)
	if err != nil {
		return fmt.Errorf("retain MCP mock attempt directory %s: %w", a.dir, err)
	}
	a.identity, err = a.root.Stat(".")
	if err != nil {
		return fmt.Errorf("identify MCP mock attempt directory %s: %w", a.dir, err)
	}
	return a.checkIdentity()
}

func (a *MCPMockAttempt) serverConfig(cfg mcpmock.Config) (_ copilot.MCPStdioServerConfig, resultErr error) {
	if a.root == nil {
		if err := a.initialize(); err != nil {
			return copilot.MCPStdioServerConfig{}, err
		}
	}
	name := "mock-" + rand.Text()
	if err := a.root.Mkdir(name, 0700); err != nil {
		return copilot.MCPStdioServerConfig{}, fmt.Errorf("create MCP mock %q attempt directory: %w", cfg.Name, err)
	}
	identity, err := a.root.Lstat(name)
	if err != nil {
		return copilot.MCPStdioServerConfig{}, fmt.Errorf("identify MCP mock %q directory: %w", cfg.Name, err)
	}
	mock := &mcpAttemptMock{name: name, identity: identity, files: make(map[string]os.FileInfo)}
	a.mocks = append(a.mocks, mock)
	root, err := a.root.OpenRoot(name)
	if err != nil {
		return copilot.MCPStdioServerConfig{}, fmt.Errorf("retain MCP mock %q directory: %w", cfg.Name, err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close MCP mock %q directory: %w", cfg.Name, err))
		}
	}()
	stateErr := mcpmock.PrepareSequenceState(&cfg, root, "state")
	if cfg.StateDir != "" {
		err := fs.WalkDir(root.FS(), "state", func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			identity, err := entry.Info()
			if err == nil {
				mock.files[filepath.FromSlash(path)] = identity
			}
			return err
		})
		if err != nil {
			return copilot.MCPStdioServerConfig{}, errors.Join(stateErr, fmt.Errorf("identify MCP mock sequence state: %w", err))
		}
	}
	if stateErr != nil {
		return copilot.MCPStdioServerConfig{}, stateErr
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return copilot.MCPStdioServerConfig{}, fmt.Errorf("marshal mock config: %w", err)
	}
	file, err := root.OpenFile("config.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return copilot.MCPStdioServerConfig{}, fmt.Errorf("create mock config file: %w", err)
	}
	mock.files["config.json"], err = file.Stat()
	if err != nil {
		return copilot.MCPStdioServerConfig{}, errors.Join(fmt.Errorf("identify mock config file: %w", err), file.Close())
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return copilot.MCPStdioServerConfig{}, fmt.Errorf("write mock config file: %w", err)
	}
	if err := a.checkIdentity(); err != nil {
		return copilot.MCPStdioServerConfig{}, err
	}
	return mockStdioConfig("--attempt-config-file", filepath.Join(a.dir, name, "config.json"))
}

func convertMCPServers(serverConfigs map[string]any, mocks []models.MCPMockConfig, baseDir, version string, warnf func(string, ...any), strict bool, materialize func(mcpmock.Config) (copilot.MCPStdioServerConfig, error)) (map[string]copilot.MCPServerConfig, error) {
	ownedState := materialize != nil
	if materialize == nil {
		materialize = mockServerConfig
	}
	if warnf == nil {
		warnf = func(string, ...any) {}
	}
	if len(serverConfigs) == 0 {
		if len(mocks) == 0 {
			return nil, nil
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
			stdio, err := DecodeMCPStdioServer(cfgMap)
			if err != nil {
				warnf("Warning: mcp_server %q stdio config is invalid: %v, skipping\n", name, err)
				continue
			}
			result[name] = stdio
		case "http", "sse":
			http, err := DecodeMCPHTTPServer(cfgMap)
			if err != nil {
				warnf("Warning: mcp_server %q http config is invalid: %v, skipping\n", name, err)
				continue
			}
			result[name] = http
		default:
			warnf("Warning: mcp_server %q has unsupported type %q, skipping\n", name, serverType)
		}
	}

	resolved := make([]mcpmock.Config, 0, len(mocks))
	for _, mock := range mocks {
		mockName := strings.TrimSpace(mock.Name)
		if mockName != "" {
			delete(result, mockName)
		}
		cfg, err := mcpmock.FromEvalConfigWithVersion(mock, baseDir, version)
		if err != nil {
			if strict {
				return nil, fmt.Errorf("resolve MCP mock %q: %w", mock.Name, err)
			}
			warnf("Warning: mcp_mock %q config is invalid: %v, skipping\n", mock.Name, err)
			continue
		}
		resolved = append(resolved, *cfg)
	}
	var createdPaths []string
	for _, cfg := range resolved {
		stdio, err := materialize(cfg)
		if err != nil {
			if strict && ownedState {
				return nil, fmt.Errorf("configure MCP mock %q: %w", cfg.Name, err)
			}
			if strict {
				return nil, fmt.Errorf("configure MCP mock %q (earlier temporary config files: %v): %w", cfg.Name, createdPaths, err)
			}
			warnf("Warning: mcp_mock %q could not be configured: %v, skipping\n", cfg.Name, err)
			continue
		}
		createdPaths = append(createdPaths, stdio.Args[2])
		result[cfg.Name] = stdio
	}

	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// DecodeMCPStdioServer performs the runtime's local decoding without starting a server.
func DecodeMCPStdioServer(input map[string]any) (copilot.MCPStdioServerConfig, error) {
	var server copilot.MCPStdioServerConfig
	if err := decode(input, &server); err != nil {
		return server, err
	}
	defaultMCPTools(input, &server.Tools)
	return server, nil
}

// DecodeMCPHTTPServer performs the runtime's local decoding without contacting a server.
func DecodeMCPHTTPServer(input map[string]any) (copilot.MCPHTTPServerConfig, error) {
	var server copilot.MCPHTTPServerConfig
	if err := decode(input, &server); err != nil {
		return server, err
	}
	defaultMCPTools(input, &server.Tools)
	return server, nil
}

func defaultMCPTools(cfgMap map[string]any, tools *[]string) {
	if value, ok := cfgMap["tools"]; !ok || value == nil {
		*tools = []string{"*"}
	}
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
	stdio, err := mockStdioConfig("--config-file", configFile)
	if err != nil {
		return copilot.MCPStdioServerConfig{}, errors.Join(err, os.Remove(configFile))
	}
	return stdio, nil
}

func mockStdioConfig(flag, configFile string) (copilot.MCPStdioServerConfig, error) {
	exe, err := os.Executable()
	if err != nil {
		return copilot.MCPStdioServerConfig{}, fmt.Errorf("resolve waza executable: %w", err)
	}
	return copilot.MCPStdioServerConfig{
		Command: exe,
		Args:    []string{"__mcp-mock", flag, configFile},
		// The bundled CLI rejects mock servers when the allowlist is omitted.
		Tools: []string{"*"},
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
