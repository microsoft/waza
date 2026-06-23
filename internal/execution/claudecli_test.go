package execution

import (
	"os"
	"testing"
)

func TestNewClaudeCliEngine_BinaryNotFound(t *testing.T) {
	// Override PATH to an empty directory so claude is not found.
	// Also clear CLAUDE_CLI_PATH so the env-var override is not in play.
	t.Setenv("CLAUDE_CLI_PATH", "")
	t.Setenv("PATH", t.TempDir())

	_, err := NewClaudeCliEngine("claude-sonnet-4.6")
	if err == nil {
		t.Fatal("expected error when claude binary is not in PATH and CLAUDE_CLI_PATH is unset")
	}
}

func TestClaudeCliEngine_SetKeepWorkspace(t *testing.T) {
	// Arrange: resolve a real claude binary so NewClaudeCliEngine succeeds,
	// or skip the test when the CLI is unavailable in CI.
	claudePath, err := findClaudeBinary()
	if err != nil {
		t.Skip("claude CLI not available; skipping SetKeepWorkspace test:", err)
	}
	t.Setenv("CLAUDE_CLI_PATH", claudePath)

	engine, err := NewClaudeCliEngine("claude-sonnet-4.6")
	if err != nil {
		t.Fatalf("NewClaudeCliEngine() error: %v", err)
	}

	// Default should be false.
	engine.workspacesMu.Lock()
	before := engine.keepWorkspace
	engine.workspacesMu.Unlock()

	if before {
		t.Error("keepWorkspace should default to false")
	}

	engine.SetKeepWorkspace(true)

	engine.workspacesMu.Lock()
	after := engine.keepWorkspace
	engine.workspacesMu.Unlock()

	if !after {
		t.Error("keepWorkspace should be true after SetKeepWorkspace(true)")
	}
}

func TestClaudeCliEngine_DefaultAllowedTools(t *testing.T) {
	claudePath, err := findClaudeBinary()
	if err != nil {
		t.Skip("claude CLI not available; skipping DefaultAllowedTools test:", err)
	}
	t.Setenv("CLAUDE_CLI_PATH", claudePath)

	engine, err := NewClaudeCliEngine("claude-sonnet-4.6")
	if err != nil {
		t.Fatalf("NewClaudeCliEngine() error: %v", err)
	}

	if len(engine.allowedTools) != 1 || engine.allowedTools[0] != "all" {
		t.Errorf("allowedTools = %v, want [\"all\"]", engine.allowedTools)
	}
}

// findClaudeBinary returns a usable claude binary path for tests that need a
// real engine instance. It checks CLAUDE_CLI_PATH first, then PATH.
func findClaudeBinary() (string, error) {
	if p := os.Getenv("CLAUDE_CLI_PATH"); p != "" {
		return p, nil
	}
	// exec.LookPath would work but we cannot import os/exec here cleanly;
	// use a throwaway engine to piggyback on the existing lookup logic.
	// We temporarily point CLAUDE_CLI_PATH to nothing so that NewClaudeCliEngine
	// falls through to PATH lookup. If PATH lookup fails, we return the error.
	// (We call NewClaudeCliEngine with an empty PATH override.)
	orig := os.Getenv("CLAUDE_CLI_PATH")
	_ = os.Unsetenv("CLAUDE_CLI_PATH")
	defer func() {
		if orig != "" {
			_ = os.Setenv("CLAUDE_CLI_PATH", orig)
		}
	}()

	engine, err := NewClaudeCliEngine("probe")
	if err != nil {
		return "", err
	}
	return engine.cliBinaryPath, nil
}
