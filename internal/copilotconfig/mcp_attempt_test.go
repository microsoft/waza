package copilotconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func setTempRoot(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, dir)
	}
}

func sequenceMock(name string) models.MCPMockConfig {
	return models.MCPMockConfig{Name: name, Tools: map[string]models.MCPMockTool{"read": {Responses: []models.MCPMockResponse{{
		Match:    map[string]any{"id": "item"},
		Sequence: []models.MCPMockStep{{Return: map[string]any{"ordinal": 1}}, {Return: map[string]any{"ordinal": 2}}},
	}}}}}
}

func attemptConfig(t *testing.T, server copilot.MCPServerConfig) (mcpmock.Config, string) {
	t.Helper()
	stdio, ok := server.(copilot.MCPStdioServerConfig)
	require.True(t, ok)
	require.Len(t, stdio.Args, 3)
	require.Equal(t, []string{"__mcp-mock", "--attempt-config-file"}, stdio.Args[:2])
	data, err := os.ReadFile(stdio.Args[2])
	require.NoError(t, err)
	var cfg mcpmock.Config
	require.NoError(t, json.Unmarshal(data, &cfg))
	return cfg, stdio.Args[2]
}

func TestResolveMCPServersForAttemptOwnsRetainedState(t *testing.T) {
	tmp := t.TempDir()
	setTempRoot(t, tmp)
	servers, attempt, err := ResolveMCPServersForAttempt(map[string]any{
		"live": map[string]any{"type": "stdio", "command": "server"},
	}, []models.MCPMockConfig{sequenceMock("counter")}, tmp, models.ScenarioSchemaVersion, nil)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.IsType(t, copilot.MCPStdioServerConfig{}, servers["live"])
	cfg, configFile := attemptConfig(t, servers["counter"])
	require.True(t, filepath.IsAbs(cfg.StateDir))
	require.DirExists(t, cfg.StateDir)
	require.FileExists(t, configFile, "restarted servers need the retained configuration")
	canonical, err := filepath.EvalSymlinks(tmp)
	require.NoError(t, err)
	rel, err := filepath.Rel(canonical, configFile)
	require.NoError(t, err)
	require.True(t, filepath.IsLocal(rel))

	_, other, err := ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{sequenceMock("counter")}, tmp, models.ScenarioSchemaVersion, nil)
	require.NoError(t, err)
	otherServers, otherAttempt, err := ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{sequenceMock("counter")}, tmp, models.ScenarioSchemaVersion, nil)
	require.NoError(t, err)
	otherCfg, _ := attemptConfig(t, otherServers["counter"])
	require.NotEqual(t, cfg.StateDir, otherCfg.StateDir, "attempts must not share sequence state")

	require.NoError(t, attempt.Close())
	require.NoError(t, attempt.Close())
	require.NoFileExists(t, configFile)
	require.NoDirExists(t, cfg.StateDir)
	require.DirExists(t, otherCfg.StateDir, "closing one attempt must not remove another")
	require.NoError(t, other.Close())
	require.NoError(t, otherAttempt.Close())
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestResolveMCPServersForAttemptOwnsAliasedState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating the parent alias requires Windows symlink privileges")
	}
	dir := t.TempDir()
	parent := filepath.Join(dir, "parent")
	require.NoError(t, os.Mkdir(parent, 0700))
	alias := filepath.Join(dir, "alias")
	require.NoError(t, os.Symlink(dir, alias))
	aliasedParent := filepath.Join(alias, "parent")
	setTempRoot(t, aliasedParent)
	servers, attempt, err := ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{sequenceMock("counter")}, dir, models.ScenarioSchemaVersion, nil)
	require.NoError(t, err)
	cfg, configFile := attemptConfig(t, servers["counter"])
	canonical, err := filepath.EvalSymlinks(aliasedParent)
	require.NoError(t, err)
	require.NotEqual(t, canonical, aliasedParent, "fixture must exercise distinct spellings of the same parent")
	for _, path := range []string{configFile, cfg.StateDir} {
		relative, err := filepath.Rel(canonical, path)
		require.NoError(t, err)
		require.True(t, filepath.IsLocal(relative))
	}
	canonicalInfo, err := os.Stat(canonical)
	require.NoError(t, err)
	aliasInfo, err := os.Stat(aliasedParent)
	require.NoError(t, err)
	require.True(t, os.SameFile(canonicalInfo, aliasInfo))
	require.NoError(t, attempt.Close())
	require.NoError(t, attempt.Close())
	entries, err := os.ReadDir(aliasedParent)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestResolveMCPServersForAttemptWithoutMocksOwnsNothing(t *testing.T) {
	tmp := t.TempDir()
	setTempRoot(t, tmp)
	servers, attempt, err := ResolveMCPServersForAttempt(map[string]any{
		"live": map[string]any{"type": "stdio", "command": "server"},
	}, nil, tmp, models.ScenarioSchemaVersion, nil)
	require.NoError(t, err)
	require.Nil(t, attempt)
	require.NoError(t, attempt.Close())
	require.Contains(t, servers, "live")
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestResolveMCPServersForAttemptRemovesPartialSetup(t *testing.T) {
	tmp := t.TempDir()
	setTempRoot(t, tmp)
	servers, attempt, err := ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{
		sequenceMock("first"),
		{Name: "second", Tools: map[string]models.MCPMockTool{"request": {Responses: []models.MCPMockResponse{{Return: make(chan int)}}}}},
	}, tmp, models.ScenarioSchemaVersion, nil)
	require.ErrorContains(t, err, `configure MCP mock "second"`)
	require.Nil(t, servers)
	require.Nil(t, attempt)
	entries, readErr := os.ReadDir(tmp)
	require.NoError(t, readErr)
	require.Empty(t, entries, "a failed attempt setup must remove only its own state")
}

func TestResolveMCPServersForAttemptPreservesAdmission(t *testing.T) {
	tmp := t.TempDir()
	setTempRoot(t, tmp)
	_, attempt, err := ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{sequenceMock("counter")}, tmp, "1.4", nil)
	require.ErrorContains(t, err, "requires explicit enclosing scenario schemaVersion")
	require.Nil(t, attempt)
	entries, readErr := os.ReadDir(tmp)
	require.NoError(t, readErr)
	require.Empty(t, entries)
}

func TestMCPMockAttemptClosePreservesReplacementDirectories(t *testing.T) {
	for _, replacement := range []string{"attempt", "parent", "parent-alias", "mock", "state", "sequence", "attempt-symlink", "mock-symlink", "state-symlink", "sequence-symlink", "config"} {
		t.Run(replacement, func(t *testing.T) {
			if runtime.GOOS == "windows" && (strings.HasSuffix(replacement, "-symlink") || replacement == "parent-alias") {
				t.Skip("creating symlinks requires Windows privileges")
			}
			tmp := t.TempDir()
			parent := filepath.Join(tmp, "parent")
			require.NoError(t, os.Mkdir(parent, 0700))
			if replacement == "parent-alias" {
				alias := filepath.Join(tmp, "alias")
				require.NoError(t, os.Symlink(tmp, alias))
				parent = filepath.Join(alias, "parent")
			}
			setTempRoot(t, parent)
			servers, attempt, err := ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{sequenceMock("counter")}, tmp, models.ScenarioSchemaVersion, nil)
			require.NoError(t, err)
			t.Cleanup(func() {
				if attempt.parent != nil {
					require.NoError(t, attempt.Close())
				}
			})
			cfg, configFile := attemptConfig(t, servers["counter"])
			configBody, err := os.ReadFile(configFile)
			require.NoError(t, err)
			// Windows defers os.Stat file-ID lookup until SameFile, after this
			// path has been renamed. Stat through the file handle instead, then
			// close it before the rename so it cannot pin the original file.
			configHandle, err := os.Open(configFile)
			require.NoError(t, err)
			configIdentity, statErr := configHandle.Stat()
			configCloseErr := configHandle.Close()
			require.NoError(t, statErr)
			require.NoError(t, configCloseErr)
			canonicalParent, err := filepath.EvalSymlinks(parent)
			require.NoError(t, err)
			relativeConfig, err := filepath.Rel(canonicalParent, configFile)
			require.NoError(t, err)
			require.True(t, filepath.IsLocal(relativeConfig), "configuration must belong to the intended parent, including through aliases")
			path := attempt.dir
			kind := strings.TrimSuffix(strings.TrimSuffix(replacement, "-symlink"), "-alias")
			switch kind {
			case "parent":
				path = canonicalParent
			case "mock":
				path = filepath.Dir(configFile)
			case "state":
				path = cfg.StateDir
			case "sequence":
				path = filepath.Join(cfg.StateDir, "tool-0000-response-0000")
			case "config":
				path = configFile
			}
			moved := path + "-moved"
			renameErr := os.Rename(path, moved)
			if runtime.GOOS == "windows" && renameErr != nil {
				require.ErrorIs(t, renameErr, syscall.Errno(32), "only a Windows sharing violation is an expected retained-root pin")
				_, rootErr := attempt.root.Stat(".")
				require.NoError(t, rootErr, "the retained attempt root must remain usable while pinned")
				require.NoError(t, attempt.root.Close())
				attempt.root = nil
				if kind == "parent" {
					_, parentErr := attempt.parent.Stat(".")
					require.NoError(t, parentErr, "the retained parent must remain usable while pinned")
				}
				require.NoError(t, attempt.parent.Close())
				attempt.parent = nil
				require.NoError(t, os.Rename(path, moved), "closing the owning roots must release the Windows pin")
				relativeConfig, err = filepath.Rel(path, configFile)
				require.NoError(t, err)
				switch kind {
				case "attempt", "parent", "mock", "config":
					movedConfig := filepath.Join(moved, relativeConfig)
					retained, err := os.ReadFile(movedConfig)
					require.NoError(t, err)
					require.Equal(t, configBody, retained)
					info, err := os.Stat(movedConfig)
					require.NoError(t, err)
					require.True(t, os.SameFile(configIdentity, info))
				case "state", "sequence":
					require.DirExists(t, moved, "replaced sequence state must be retained")
				}
				return
			}
			require.NoError(t, renameErr)
			relativeConfig, err = filepath.Rel(path, configFile)
			require.NoError(t, err)
			if kind == "attempt" || kind == "parent" || kind == "mock" || kind == "config" {
				require.True(t, relativeConfig == "." || filepath.IsLocal(relativeConfig))
				movedConfig := filepath.Join(moved, relativeConfig)
				info, err := os.Stat(movedConfig)
				require.NoError(t, err, "setup must move the original configuration before cleanup")
				require.True(t, os.SameFile(configIdentity, info))
			}
			sentinel := path
			if replacement == "config" {
				require.NoError(t, os.WriteFile(path, []byte("unrelated"), 0600))
			} else if strings.HasSuffix(replacement, "-symlink") {
				target := filepath.Join(tmp, "unrelated")
				require.NoError(t, os.Mkdir(target, 0700))
				sentinel = filepath.Join(target, "sentinel")
				require.NoError(t, os.WriteFile(sentinel, []byte("unrelated"), 0600))
				require.NoError(t, os.Symlink(target, path))
			} else {
				require.NoError(t, os.Mkdir(path, 0700))
				sentinel = filepath.Join(path, "sentinel")
				require.NoError(t, os.WriteFile(sentinel, []byte("unrelated"), 0600))
			}
			closeErr := attempt.Close()
			require.ErrorContains(t, closeErr, "identity changed")
			require.ErrorContains(t, closeErr, attempt.dir)
			require.ErrorContains(t, closeErr, "if renamed, locate it before manual cleanup")
			require.Equal(t, closeErr, attempt.Close(), "repeated Close must preserve the failure without deleting replacements")
			body, err := os.ReadFile(sentinel)
			require.NoError(t, err, "cleanup must preserve the replacement")
			require.Equal(t, "unrelated", string(body))
			switch kind {
			case "attempt", "parent", "mock", "config":
				retainedConfig := filepath.Join(moved, relativeConfig)
				retained, err := os.ReadFile(retainedConfig)
				require.NoError(t, err)
				require.Equal(t, configBody, retained, "changed directories must retain the original configuration")
			case "state", "sequence":
				require.DirExists(t, moved, "replaced sequence state must be retained")
			}
		})
	}
}

func TestMCPMockAttemptCloseReportsUnexpectedResidue(t *testing.T) {
	for _, location := range []string{"attempt", "mock", "state", "sequence"} {
		t.Run(location, func(t *testing.T) {
			tmp := t.TempDir()
			setTempRoot(t, tmp)
			servers, attempt, err := ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{sequenceMock("counter")}, tmp, models.ScenarioSchemaVersion, nil)
			require.NoError(t, err)
			cfg, configFile := attemptConfig(t, servers["counter"])
			path := attempt.dir
			switch location {
			case "mock":
				path = filepath.Dir(configFile)
			case "state":
				path = cfg.StateDir
			case "sequence":
				path = filepath.Join(cfg.StateDir, "tool-0000-response-0000")
			}
			sentinel := filepath.Join(path, "unrelated")
			require.NoError(t, os.Mkdir(sentinel, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(sentinel, "sentinel"), []byte("preserve"), 0600))

			closeErr := attempt.Close()
			require.ErrorContains(t, closeErr, "cleanup incomplete")
			require.ErrorContains(t, closeErr, attempt.dir)
			require.Equal(t, closeErr, attempt.Close())
			body, err := os.ReadFile(filepath.Join(sentinel, "sentinel"))
			require.NoError(t, err)
			require.Equal(t, "preserve", string(body))
			require.NoFileExists(t, configFile, "owned configuration should still be removed")
		})
	}
}

func TestMCPMockAttemptCloseReportsUnavailableHandle(t *testing.T) {
	tmp := t.TempDir()
	setTempRoot(t, tmp)
	servers, attempt, err := ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{sequenceMock("counter")}, tmp, models.ScenarioSchemaVersion, nil)
	require.NoError(t, err)
	_, configFile := attemptConfig(t, servers["counter"])
	require.NoError(t, attempt.root.Close())

	closeErr := attempt.Close()
	require.ErrorContains(t, closeErr, "cleanup incomplete")
	require.ErrorContains(t, closeErr, attempt.dir)
	require.ErrorIs(t, closeErr, os.ErrClosed)
	require.Equal(t, closeErr, attempt.Close())
	require.FileExists(t, configFile, "an unreadable retained directory must not trigger pathname-based deletion")
}

func TestMCPMockAttemptClosePreservesUnrecognizedSequenceEntries(t *testing.T) {
	for _, kind := range []string{"directory", "nonempty-file", "malformed-name", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			if runtime.GOOS == "windows" && kind == "symlink" {
				t.Skip("creating symlinks requires Windows privileges")
			}
			tmp := t.TempDir()
			setTempRoot(t, tmp)
			servers, attempt, err := ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{sequenceMock("counter")}, tmp, models.ScenarioSchemaVersion, nil)
			require.NoError(t, err)
			cfg, _ := attemptConfig(t, servers["counter"])
			path := filepath.Join(cfg.StateDir, "tool-0000-response-0000", "step-00000000")
			switch kind {
			case "directory":
				require.NoError(t, os.Mkdir(path, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(path, "sentinel"), []byte("preserve"), 0600))
			case "nonempty-file":
				require.NoError(t, os.WriteFile(path, []byte("preserve"), 0600))
			case "malformed-name":
				path = filepath.Join(filepath.Dir(path), "step-0000000x")
				require.NoError(t, os.WriteFile(path, nil, 0600))
			case "symlink":
				target := filepath.Join(tmp, "sentinel")
				require.NoError(t, os.WriteFile(target, []byte("preserve"), 0600))
				require.NoError(t, os.Symlink(target, path))
			}
			closeErr := attempt.Close()
			require.ErrorContains(t, closeErr, "unrecognized MCP mock cleanup entry")
			require.ErrorContains(t, closeErr, path)
			info, err := os.Lstat(path)
			require.NoError(t, err, "unrecognized reservation entries must be preserved")
			switch kind {
			case "directory":
				require.True(t, info.IsDir())
				body, err := os.ReadFile(filepath.Join(path, "sentinel"))
				require.NoError(t, err)
				require.Equal(t, "preserve", string(body))
			case "nonempty-file", "symlink":
				body, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, "preserve", string(body))
			case "malformed-name":
				require.Zero(t, info.Size())
			}
		})
	}
}

func TestResolveMCPServersForAttemptRejectsUnavailableParent(t *testing.T) {
	tmp := t.TempDir()
	setTempRoot(t, filepath.Join(tmp, "missing"))
	servers, attempt, err := ResolveMCPServersForAttempt(nil, []models.MCPMockConfig{sequenceMock("counter")}, tmp, models.ScenarioSchemaVersion, nil)
	require.ErrorContains(t, err, "resolve MCP mock attempt parent")
	require.Nil(t, servers)
	require.Nil(t, attempt)
	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	require.Empty(t, entries)
}
