package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/embedded"
)

// stubClient is a no-op CopilotClient used only to verify SharedClient
// returns the same instance and Shutdown is idempotent.
type stubClient struct{ stops int }

func (s *stubClient) CreateSession(context.Context, *copilot.SessionConfig) (CopilotSession, error) {
	return nil, nil
}
func (s *stubClient) GetAuthStatus(context.Context) (*copilot.GetAuthStatusResponse, error) {
	return nil, nil
}
func (s *stubClient) Start(context.Context) error { return nil }
func (s *stubClient) Stop() error                 { s.stops++; return nil }
func (s *stubClient) ResumeSessionWithOptions(context.Context, string, *copilot.ResumeSessionConfig) (CopilotSession, error) {
	return nil, nil
}
func (s *stubClient) DeleteSession(context.Context, string) error { return nil }
func (s *stubClient) ListModels(context.Context) ([]copilot.ModelInfo, error) {
	return nil, nil
}

func TestSharedClient_ReturnsSameInstance(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)
	t.Setenv("COPILOT_CLI_PATH", "")

	stub := &stubClient{}
	embeddedCLIPath = func() (string, error) { return "/tmp/embedded-copilot", nil }
	t.Cleanup(func() { embeddedCLIPath = embedded.Path })
	sharedConstruct = func(*copilot.ClientOptions) CopilotClient { return stub }
	t.Cleanup(func() { sharedConstruct = newCopilotClient })

	a := SharedClient(SharedClientOptions{})
	b := SharedClient(SharedClientOptions{LogLevel: "debug"}) // ignored after first call
	if a != b {
		t.Fatalf("expected SharedClient to return the same instance")
	}
	if a == nil {
		t.Fatalf("SharedClient returned nil")
	}
}

func TestShutdownSharedClient_Idempotent(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)
	t.Setenv("COPILOT_CLI_PATH", "")

	stub := &stubClient{}
	embeddedCLIPath = func() (string, error) { return "/tmp/embedded-copilot", nil }
	t.Cleanup(func() { embeddedCLIPath = embedded.Path })
	sharedConstruct = func(*copilot.ClientOptions) CopilotClient { return stub }
	t.Cleanup(func() { sharedConstruct = newCopilotClient })

	_ = SharedClient(SharedClientOptions{})
	if err := ShutdownSharedClient(context.Background()); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := ShutdownSharedClient(context.Background()); err != nil {
		t.Fatalf("second shutdown should be no-op: %v", err)
	}
	if stub.stops != 1 {
		t.Fatalf("expected client Stop to be called once, got %d", stub.stops)
	}
}

func TestShutdownSharedClient_NoClientNeverConstructed(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)

	if err := ShutdownSharedClient(context.Background()); err != nil {
		t.Fatalf("expected no error when client never built, got %v", err)
	}
}

func TestShutdownSharedClient_BeforeConstructDoesNotPoisonShutdown(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)
	t.Setenv("COPILOT_CLI_PATH", "")

	stub := &stubClient{}
	embeddedCLIPath = func() (string, error) { return "/tmp/embedded-copilot", nil }
	t.Cleanup(func() { embeddedCLIPath = embedded.Path })
	sharedConstruct = func(*copilot.ClientOptions) CopilotClient { return stub }
	t.Cleanup(func() { sharedConstruct = newCopilotClient })

	if err := ShutdownSharedClient(context.Background()); err != nil {
		t.Fatalf("shutdown before construction: %v", err)
	}
	_ = SharedClient(SharedClientOptions{})
	if err := ShutdownSharedClient(context.Background()); err != nil {
		t.Fatalf("shutdown after construction: %v", err)
	}
	if stub.stops != 1 {
		t.Fatalf("expected client Stop to be called once, got %d", stub.stops)
	}
}

func TestSharedClient_UsesEmbeddedCLIPath(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)
	t.Setenv("COPILOT_CLI_PATH", "")

	embeddedCLIPath = func() (string, error) { return "/cache/copilot-sdk/copilot_1.0.49", nil }
	t.Cleanup(func() { embeddedCLIPath = embedded.Path })

	stub := &stubClient{}
	var gotOptions *copilot.ClientOptions
	sharedConstruct = func(opts *copilot.ClientOptions) CopilotClient {
		gotOptions = opts
		return stub
	}
	t.Cleanup(func() { sharedConstruct = newCopilotClient })

	_ = SharedClient(SharedClientOptions{})
	if gotOptions == nil {
		t.Fatalf("expected shared client to be constructed")
	}
	if gotOptions.CLIPath != "/cache/copilot-sdk/copilot_1.0.49" {
		t.Fatalf("expected embedded CLI path, got %q", gotOptions.CLIPath)
	}
}

func TestSharedClient_UsesCOPILOTCLIPathOverride(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)

	cliPath := filepath.Join(t.TempDir(), "copilot")
	if err := os.WriteFile(cliPath, []byte("fake"), 0755); err != nil {
		t.Fatalf("write fake cli: %v", err)
	}
	t.Setenv("COPILOT_CLI_PATH", cliPath)

	embeddedCLIPath = func() (string, error) {
		t.Fatalf("embedded CLI should not be installed when COPILOT_CLI_PATH is set")
		return "", nil
	}
	t.Cleanup(func() { embeddedCLIPath = embedded.Path })

	stub := &stubClient{}
	var gotOptions *copilot.ClientOptions
	sharedConstruct = func(opts *copilot.ClientOptions) CopilotClient {
		gotOptions = opts
		return stub
	}
	t.Cleanup(func() { sharedConstruct = newCopilotClient })

	_ = SharedClient(SharedClientOptions{})
	if gotOptions == nil {
		t.Fatalf("expected shared client to be constructed")
	}
	if gotOptions.CLIPath != cliPath {
		t.Fatalf("expected COPILOT_CLI_PATH override, got %q", gotOptions.CLIPath)
	}
}

func TestSharedClient_InvalidCOPILOTCLIPathReturnsStartupError(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)

	cliPath := filepath.Join(t.TempDir(), "missing-copilot")
	t.Setenv("COPILOT_CLI_PATH", cliPath)

	embeddedCLIPath = func() (string, error) {
		t.Fatalf("embedded CLI should not be installed when COPILOT_CLI_PATH is invalid")
		return "", nil
	}
	t.Cleanup(func() { embeddedCLIPath = embedded.Path })
	sharedConstruct = func(*copilot.ClientOptions) CopilotClient {
		t.Fatalf("SDK client should not be constructed with an invalid COPILOT_CLI_PATH")
		return nil
	}
	t.Cleanup(func() { sharedConstruct = newCopilotClient })

	client := SharedClient(SharedClientOptions{})
	err := client.Start(context.Background())
	if err == nil {
		t.Fatalf("expected startup error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing file error, got %v", err)
	}
}

func TestSharedClient_EmbeddedInstallFailureReturnsStartupError(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)
	t.Setenv("COPILOT_CLI_PATH", "")

	embeddedCLIPath = func() (string, error) { return "", errors.New("install failed") }
	t.Cleanup(func() { embeddedCLIPath = embedded.Path })
	sharedConstruct = func(*copilot.ClientOptions) CopilotClient {
		t.Fatalf("SDK client should not be constructed when embedded install fails")
		return nil
	}
	t.Cleanup(func() { sharedConstruct = newCopilotClient })

	client := SharedClient(SharedClientOptions{})
	err := client.Start(context.Background())
	if err == nil {
		t.Fatalf("expected startup error")
	}
	if got := err.Error(); !strings.Contains(got, "refusing to fall back to PATH") || !strings.Contains(got, "install failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := ShutdownSharedClient(context.Background()); err != nil {
		t.Fatalf("startup error client shutdown should be a no-op, got %v", err)
	}
}
