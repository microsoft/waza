package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestShutdownSharedClient_StopsAllCLIArgClients(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)
	t.Setenv("COPILOT_CLI_PATH", "")

	embeddedCLIPath = func() (string, error) { return "/tmp/embedded-copilot", nil }
	t.Cleanup(func() { embeddedCLIPath = embedded.Path })

	var clients []*stubClient
	sharedConstruct = func(*copilot.ClientOptions) CopilotClient {
		stub := &stubClient{}
		clients = append(clients, stub)
		return stub
	}
	t.Cleanup(func() { sharedConstruct = newCopilotClient })

	_ = SharedClient(SharedClientOptions{CLIArgs: []string{"--model", "claude-sonnet-4.5"}})
	_ = SharedClient(SharedClientOptions{CLIArgs: []string{"--model", "gpt-5.4"}})

	if err := ShutdownSharedClient(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if len(clients) != 2 {
		t.Fatalf("expected two shared clients, got %d", len(clients))
	}
	for i, client := range clients {
		if client.stops != 1 {
			t.Fatalf("expected client %d Stop to be called once, got %d", i, client.stops)
		}
	}
}

func TestShutdownSharedClient_PreventsNewClientsAfterShutdown(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)
	t.Setenv("COPILOT_CLI_PATH", "")

	embeddedCLIPath = func() (string, error) { return "/tmp/embedded-copilot", nil }
	t.Cleanup(func() { embeddedCLIPath = embedded.Path })

	stub := &stubClient{}
	constructs := 0
	sharedConstruct = func(*copilot.ClientOptions) CopilotClient {
		constructs++
		return stub
	}
	t.Cleanup(func() { sharedConstruct = newCopilotClient })

	_ = SharedClient(SharedClientOptions{CLIArgs: []string{"--model", "claude-sonnet-4.5"}})
	if err := ShutdownSharedClient(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	client := SharedClient(SharedClientOptions{CLIArgs: []string{"--model", "gpt-5.4"}})
	if err := client.Start(context.Background()); !errors.Is(err, errSharedClientClosed) {
		t.Fatalf("expected shared client closed error, got %v", err)
	}
	if constructs != 1 {
		t.Fatalf("expected no new client construction after shutdown, got %d", constructs)
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

func TestSharedClient_PassesCLIArgs(t *testing.T) {
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

	cliArgs := []string{"--model", "claude-sonnet-4.5"}
	_ = SharedClient(SharedClientOptions{CLIArgs: cliArgs})
	if gotOptions == nil {
		t.Fatalf("expected shared client to be constructed")
	}
	if !reflect.DeepEqual(gotOptions.CLIArgs, cliArgs) {
		t.Fatalf("expected CLIArgs %v, got %v", cliArgs, gotOptions.CLIArgs)
	}
}

func TestSharedClient_SeparatesDifferentCLIArgs(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)
	t.Setenv("COPILOT_CLI_PATH", "")

	embeddedCLIPath = func() (string, error) { return "/cache/copilot-sdk/copilot_1.0.49", nil }
	t.Cleanup(func() { embeddedCLIPath = embedded.Path })

	var gotArgs [][]string
	sharedConstruct = func(opts *copilot.ClientOptions) CopilotClient {
		gotArgs = append(gotArgs, append([]string{}, opts.CLIArgs...))
		return &stubClient{}
	}
	t.Cleanup(func() { sharedConstruct = newCopilotClient })

	sonnetArgs := []string{"--model", "claude-sonnet-4.5"}
	gptArgs := []string{"--model", "gpt-5.4"}
	sonnetA := SharedClient(SharedClientOptions{CLIArgs: sonnetArgs})
	gpt := SharedClient(SharedClientOptions{CLIArgs: gptArgs})
	sonnetB := SharedClient(SharedClientOptions{CLIArgs: sonnetArgs, LogLevel: "debug"})

	if sonnetA == gpt {
		t.Fatalf("different CLIArgs must get distinct shared clients")
	}
	if sonnetA != sonnetB {
		t.Fatalf("same CLIArgs must reuse the shared client")
	}
	if len(gotArgs) != 2 {
		t.Fatalf("expected two client constructions, got %d", len(gotArgs))
	}
	if !reflect.DeepEqual(gotArgs[0], sonnetArgs) {
		t.Fatalf("expected first CLIArgs %v, got %v", sonnetArgs, gotArgs[0])
	}
	if !reflect.DeepEqual(gotArgs[1], gptArgs) {
		t.Fatalf("expected second CLIArgs %v, got %v", gptArgs, gotArgs[1])
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
