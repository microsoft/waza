package execution

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/sync/errgroup"
)

var enableLiveCopilotTests = os.Getenv("ENABLE_COPILOT_TESTS") == "true"

func TestSandboxPermissionHandler_ApprovesOrdinaryRequests(t *testing.T) {
	result, err := sandboxPermissionHandler(allowAllTools)(
		&copilot.PermissionRequestShell{},
		copilot.PermissionInvocation{},
	)
	require.NoError(t, err)
	_, ok := result.(*rpc.PermissionDecisionApproveOnce)
	require.True(t, ok, "expected an ApproveOnce decision, got %T", result)
}

func TestSandboxPermissionHandler_RejectsBypassRequests(t *testing.T) {
	bypass := true
	requests := map[string]copilot.PermissionRequest{
		"read":  &copilot.PermissionRequestRead{RequestSandboxBypass: &bypass},
		"shell": &copilot.PermissionRequestShell{RequestSandboxBypass: &bypass},
		"url":   &copilot.PermissionRequestURL{RequestSandboxBypass: &bypass},
		"write": &copilot.PermissionRequestWrite{RequestSandboxBypass: &bypass},
	}
	for name, request := range requests {
		t.Run(name, func(t *testing.T) {
			result, err := sandboxPermissionHandler(allowAllTools)(request, copilot.PermissionInvocation{})

			require.NoError(t, err)
			_, ok := result.(*rpc.PermissionDecisionReject)
			require.True(t, ok, "expected a Reject decision, got %T", result)
		})
	}
}

func TestSandboxPermissionHandler_RejectsManagedApprovalRequests(t *testing.T) {
	requiresApproval := true
	result, err := sandboxPermissionHandler(allowAllTools)(
		&copilot.PermissionRequestShell{ManagedApprovalRequired: &requiresApproval},
		copilot.PermissionInvocation{},
	)

	require.NoError(t, err)
	_, ok := result.(*rpc.PermissionDecisionReject)
	require.True(t, ok, "expected a Reject decision, got %T", result)
}

func TestSessionSandboxConfiguration_RestrictsAccessToWorkspace(t *testing.T) {
	workspace := t.TempDir()
	skillDir := t.TempDir()
	declaredReadonly := filepath.Join(t.TempDir(), "read-only")
	declaredReadwrite := filepath.Join(t.TempDir(), "read-write")
	canonicalTempDir, err := canonicalSandboxPath(os.TempDir())
	require.NoError(t, err)

	options, permissions, err := sessionSandboxConfiguration(workspace, []string{skillDir}, models.SandboxConfig{
		Enabled:        true,
		ReadonlyPaths:  []string{declaredReadonly},
		ReadwritePaths: []string{declaredReadwrite},
	})
	require.NoError(t, err)

	require.True(t, options.SandboxConfig.Enabled)
	require.False(t, *options.SandboxConfig.AddCurrentWorkingDirectory)
	require.False(t, *options.SandboxConfig.AllowDevToolCaches)
	require.False(t, *options.SandboxConfig.GhAuth)
	require.False(t, *options.SandboxConfig.GitAuth)
	require.Equal(
		t,
		[]string{workspace, declaredReadwrite},
		options.SandboxConfig.UserPolicy.Filesystem.ReadwritePaths,
	)
	require.Equal(t, []string{canonicalTempDir}, options.SandboxConfig.UserPolicy.Filesystem.DeniedPaths)
	require.Equal(
		t,
		[]string{skillDir, declaredReadonly},
		options.SandboxConfig.UserPolicy.Filesystem.ReadonlyPaths,
	)
	require.False(t, *options.SandboxConfig.UserPolicy.Network.AllowLocalNetwork)
	require.False(t, *options.SandboxConfig.UserPolicy.Network.AllowOutbound)
	require.False(t, *options.SandboxConfig.UserPolicy.Seatbelt.KeychainAccess)
	require.Equal(t, workspace, *permissions.Paths.WorkspacePath)
	require.Equal(
		t,
		[]string{skillDir, declaredReadonly, declaredReadwrite},
		permissions.Paths.AdditionalDirectories,
	)
	require.False(t, *permissions.Paths.IncludeTempDirectory)
	require.False(t, *permissions.Paths.Unrestricted)
}

func TestResolveSandboxPaths_ExpandsHomeAndEnvironment(t *testing.T) {
	home := newSandboxTestRoot(t)
	readonlyPath := filepath.Join(home, "cert.pem")
	readwritePath := filepath.Join(home, ".cache", "uv")
	require.NoError(t, os.WriteFile(readonlyPath, []byte("certificate"), 0o644))
	require.NoError(t, os.MkdirAll(readwritePath, 0o755))
	t.Setenv("HOME", home)
	t.Setenv("WAZA_SANDBOX_READONLY", readonlyPath)

	config, err := resolveSandboxPaths(models.SandboxConfig{
		Enabled:        true,
		ReadonlyPaths:  []string{"${WAZA_SANDBOX_READONLY}"},
		ReadwritePaths: []string{"~/.cache/uv"},
	})

	require.NoError(t, err)
	require.Equal(t, []string{readonlyPath}, config.ReadonlyPaths)
	require.Equal(t, []string{readwritePath}, config.ReadwritePaths)
}

func TestResolveSandboxPaths_RejectsRelativeAndUnsetEnvironmentPaths(t *testing.T) {
	t.Setenv("WAZA_EMPTY_SANDBOX_PATH", "")
	for _, path := range []string{"relative/path", "${WAZA_UNSET_SANDBOX_PATH}", "${WAZA_UNSET_SANDBOX_PATH}/etc", "${WAZA_EMPTY_SANDBOX_PATH}/etc"} {
		t.Run(path, func(t *testing.T) {
			_, err := resolveSandboxPaths(models.SandboxConfig{
				Enabled:       true,
				ReadonlyPaths: []string{path},
			})
			require.ErrorContains(t, err, "sandbox path")
		})
	}
}

func TestResolveSandboxPaths_CanonicalizesSymlinks(t *testing.T) {
	root := newSandboxTestRoot(t)
	realDir := filepath.Join(root, "real")
	linkDir := filepath.Join(root, "link")
	require.NoError(t, os.Mkdir(realDir, 0o755))
	require.NoError(t, os.Symlink(realDir, linkDir))

	config, err := resolveSandboxPaths(models.SandboxConfig{Enabled: true, ReadonlyPaths: []string{linkDir}})

	require.NoError(t, err)
	require.Equal(t, []string{realDir}, config.ReadonlyPaths)
}

func TestResolveSandboxPaths_RejectsMissingAndTemporaryPaths(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		_, err := resolveSandboxPaths(models.SandboxConfig{Enabled: true, ReadonlyPaths: []string{path}})
		require.ErrorContains(t, err, "sandbox path")
	}
}

func TestValidateSandboxPathPolicy_RejectsReadWriteOverlap(t *testing.T) {
	root := newSandboxTestRoot(t)
	readonlyDir := filepath.Join(root, "read-only")
	nestedReadwriteDir := filepath.Join(readonlyDir, "read-write")
	require.NoError(t, os.MkdirAll(nestedReadwriteDir, 0o755))

	err := validateSandboxPathPolicy("", nil, models.SandboxConfig{
		Enabled:        true,
		ReadonlyPaths:  []string{readonlyDir},
		ReadwritePaths: []string{nestedReadwriteDir},
	})
	require.ErrorContains(t, err, "overlaps read-only path")

	err = validateSandboxPathPolicy("", []string{readonlyDir}, models.SandboxConfig{
		Enabled:        true,
		ReadwritePaths: []string{readonlyDir},
	})
	require.ErrorContains(t, err, "declared skill")
}

func TestPathsOverlap_UsesFilesystemIdentity(t *testing.T) {
	root := newSandboxTestRoot(t)
	first := filepath.Join(root, "first")
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.WriteFile(first, []byte("same file"), 0o644))
	if err := os.Link(first, alias); err != nil {
		t.Skipf("hard links are unavailable: %v", err)
	}

	require.True(t, pathsOverlap(first, alias))
}

func TestPathsOverlap_IsCaseInsensitiveWhenFilesystemIs(t *testing.T) {
	root := newSandboxTestRoot(t)
	mixedCase := filepath.Join(root, "MixedCase")
	require.NoError(t, os.Mkdir(mixedCase, 0o755))
	lowerCase := filepath.Join(root, "mixedcase")
	if _, err := os.Stat(lowerCase); err != nil {
		t.Skip("filesystem is case-sensitive")
	}

	require.True(t, pathsOverlap(mixedCase, lowerCase))
}

func TestValidateSandboxPathPolicy_RejectsTemporaryAndReadonlyWorkspaces(t *testing.T) {
	temporaryWorkspace, err := canonicalSandboxPath(t.TempDir())
	require.NoError(t, err)
	err = validateSandboxPathPolicy(temporaryWorkspace, nil, models.SandboxConfig{Enabled: true})
	require.ErrorContains(t, err, "overlaps denied temporary directory")

	workspace := newSandboxTestRoot(t)
	err = validateSandboxPathPolicy(workspace, nil, models.SandboxConfig{
		Enabled:       true,
		ReadonlyPaths: []string{workspace},
	})
	require.ErrorContains(t, err, "overlaps read-only path")
}

func newSandboxTestRoot(t *testing.T) string {
	t.Helper()
	cacheDir, err := os.UserCacheDir()
	require.NoError(t, err)
	root, err := os.MkdirTemp(cacheDir, "waza-sandbox-test-*")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	return root
}

func TestSessionSandboxConfiguration_AppliesExplicitCapabilityOptIns(t *testing.T) {
	options, _, err := sessionSandboxConfiguration(t.TempDir(), nil, models.SandboxConfig{
		Enabled:              true,
		AllowDevToolCaches:   true,
		AllowOutboundNetwork: true,
		AllowLocalNetwork:    true,
		GitAuth:              true,
		GHAuth:               true,
	})
	require.NoError(t, err)

	sandbox := options.SandboxConfig
	require.True(t, *sandbox.AllowDevToolCaches)
	require.True(t, *sandbox.UserPolicy.Network.AllowOutbound)
	require.True(t, *sandbox.UserPolicy.Network.AllowLocalNetwork)
	require.True(t, *sandbox.GitAuth)
	require.True(t, *sandbox.GhAuth)
}

func TestSessionSandboxConfiguration_DisabledLeavesCopilotPolicyUnchanged(t *testing.T) {
	options, permissions, err := sessionSandboxConfiguration(t.TempDir(), []string{t.TempDir()}, models.SandboxConfig{})

	require.NoError(t, err)
	require.Nil(t, options)
	require.Nil(t, permissions)
}

func TestCopilotNoSessionID(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	const expectedModel = "this-model-wins"

	unregisterCount := 0
	unregister := func() { unregisterCount++ }

	sourceDir := t.TempDir()
	skillDir := newSandboxTestRoot(t)
	sandbox := models.SandboxConfig{Enabled: true}

	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.SessionConfig{
			OnPermissionRequest: allowAllTools,
			Model:               expectedModel,
			SkillDirectories:    []string{skillDir},
		},
	}

	clientMock.EXPECT().CreateSession(gomock.Any(), expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().ConfigureSandbox(gomock.Any(), gomock.Any(), []string{skillDir}, sandbox).Return(nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(unregister)
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient:    func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
		SanitizeEnvironment: true,
	}).Build()

	defer func() {
		err := engine.Shutdown(context.Background())
		require.NoError(t, err)
	}()

	err := engine.Initialize(ctx)
	require.NoError(t, err)

	resp, err := engine.Execute(ctx, &ExecutionRequest{
		Message:    "hello?",
		ModelID:    "this-model-wins",
		SessionID:  "", // ie, create a new session each time
		SourceDir:  sourceDir,
		SkillPaths: []string{skillDir},
		Sandbox:    &sandbox,
	})
	require.NoError(t, err)
	require.Equal(t, "session-1", resp.SessionID)
	require.Empty(t, resp.ErrorMsg)
	require.True(t, resp.Success)
	require.Equal(t, "this-model-wins", resp.ModelID)
	require.Equal(t, 1, unregisterCount) // only slog handler is unsubscribed; events collector stays alive for shutdown
}

func TestCopilotResumeSessionID(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	sourceDir, err := os.Getwd()
	require.NoError(t, err)
	skillDir := newSandboxTestRoot(t)
	sandbox := models.SandboxConfig{Enabled: true}

	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.ResumeSessionConfig{
			Model:               "gpt-4o-mini",
			SkillDirectories:    []string{skillDir},
			OnPermissionRequest: allowAllTools,
		},
	}

	clientMock.EXPECT().ResumeSessionWithOptions(gomock.Any(), "session-1", expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().ConfigureSandbox(gomock.Any(), gomock.Any(), []string{skillDir}, sandbox).Return(nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient:    func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
		SanitizeEnvironment: true,
	}).Build()

	defer func() {
		err := engine.Shutdown(context.Background())
		require.NoError(t, err)
	}()

	err = engine.Initialize(ctx)
	require.NoError(t, err)

	resp, err := engine.Execute(ctx, &ExecutionRequest{
		Message:    "hello?",
		SessionID:  "session-1",
		SkillPaths: []string{skillDir},
		Sandbox:    &sandbox,
	})
	require.NoError(t, err)
	require.Equal(t, "session-1", resp.SessionID)
	require.Empty(t, resp.ErrorMsg)
	require.True(t, resp.Success)
}

func TestCopilotInitialize_CustomProviderSkipsAuth(t *testing.T) {
	t.Setenv("COPILOT_BASE_URL", "https://waza-test-resource.openai.azure.com/openai/v1")
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "")

	ctrl := gomock.NewController(t)
	clientMock := NewMockCopilotClient(ctrl)

	clientMock.EXPECT().Start(gomock.Any()).Times(1)
	clientMock.EXPECT().Stop().Times(1)

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	require.NoError(t, engine.Initialize(context.Background()))
}

func TestCopilotCreateSession_PassesCustomProvider(t *testing.T) {
	t.Setenv("COPILOT_BASE_URL", "https://waza-test-resource.openai.azure.com/openai/v1")
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "")
	t.Setenv("COPILOT_PROVIDER", "openai")
	t.Setenv("COPILOT_PROVIDER_TYPE", "")
	t.Setenv("COPILOT_WIRE_API", "chat_completions")
	t.Setenv("COPILOT_PROVIDER_WIRE_API", "")
	t.Setenv("COPILOT_API_KEY", "test-key")
	t.Setenv("COPILOT_PROVIDER_API_KEY", "")
	t.Setenv("COPILOT_BEARER_TOKEN", "token")
	t.Setenv("COPILOT_PROVIDER_BEARER_TOKEN", "")

	ctrl := gomock.NewController(t)
	clientMock := NewMockCopilotClient(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	clientMock.EXPECT().Start(gomock.Any()).Times(1)
	clientMock.EXPECT().Stop().Times(1)

	sourceDir := t.TempDir()
	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.SessionConfig{
			Model:               "gpt-4o-mini",
			SkillDirectories:    []string{sourceDir},
			OnPermissionRequest: allowAllTools,
			Provider: &copilot.ProviderConfig{
				Type:        "openai",
				BaseURL:     "https://waza-test-resource.openai.azure.com/openai/v1",
				WireAPI:     "chat_completions",
				APIKey:      "test-key",
				BearerToken: "token",
			},
		},
	}

	clientMock.EXPECT().CreateSession(gomock.Any(), expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")

	var eventHandlers []func(copilot.SessionEvent)
	sessionMock.EXPECT().On(gomock.Any()).Times(3).DoAndReturn(func(handler func(copilot.SessionEvent)) func() {
		eventHandlers = append(eventHandlers, handler)
		return func() {}
	})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ copilot.MessageOptions) (*copilot.SessionEvent, error) {
			in, out := int64(10), int64(2)
			cost := float64(1)
			for _, handler := range eventHandlers {
				handler(copilot.SessionEvent{
					Data: &copilot.AssistantUsageData{
						InputTokens:  &in,
						OutputTokens: &out,
						Cost:         &cost,
						Model:        "gpt-4o-mini",
					},
				})
			}
			return &copilot.SessionEvent{}, nil
		},
	)
	sessionMock.EXPECT().SessionID().Return("session-1")

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	t.Setenv("COPILOT_BASE_URL", "http://changed.invalid/v1")
	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	require.NoError(t, engine.Initialize(context.Background()))

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:   "hello?",
		SourceDir: sourceDir,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Usage)
	require.Equal(t, models.UsageProviderCustom, resp.Usage.Provider)
	require.Equal(t, "waza-test-resource.openai.azure.com", resp.Usage.ProviderHost)
}

func TestCopilotResumeSession_PassesCustomProvider(t *testing.T) {
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "https://waza-test-resource.openai.azure.com/openai/v1")
	t.Setenv("COPILOT_BASE_URL", "")
	t.Setenv("COPILOT_PROVIDER", "")
	t.Setenv("COPILOT_PROVIDER_TYPE", "openai")
	t.Setenv("COPILOT_WIRE_API", "")
	t.Setenv("COPILOT_PROVIDER_WIRE_API", "responses")
	t.Setenv("COPILOT_API_KEY", "")
	t.Setenv("COPILOT_PROVIDER_API_KEY", "")
	t.Setenv("COPILOT_BEARER_TOKEN", "")
	t.Setenv("COPILOT_PROVIDER_BEARER_TOKEN", "")

	ctrl := gomock.NewController(t)
	clientMock := NewMockCopilotClient(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	clientMock.EXPECT().Start(gomock.Any()).Times(1)
	clientMock.EXPECT().Stop().Times(1)

	sourceDir, err := os.Getwd()
	require.NoError(t, err)

	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.ResumeSessionConfig{
			Model:               "gpt-4o-mini",
			SkillDirectories:    []string{sourceDir},
			OnPermissionRequest: allowAllTools,
			Provider: &copilot.ProviderConfig{
				Type:    "openai",
				BaseURL: "https://waza-test-resource.openai.azure.com/openai/v1",
				WireAPI: "responses",
			},
		},
	}

	clientMock.EXPECT().ResumeSessionWithOptions(gomock.Any(), "session-1", expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	require.NoError(t, engine.Initialize(context.Background()))

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:   "hello?",
		SessionID: "session-1",
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
}

func TestProviderFromEnv_SanitizesHost(t *testing.T) {
	t.Setenv("COPILOT_BASE_URL", "https://user:secret@example.test:8443/v1?token=abc")

	provider := providerFromEnv()
	require.NoError(t, provider.err)
	require.True(t, provider.enabled())
	require.Equal(t, "example.test:8443", provider.host)
}

func TestCopilotInitialize_CustomProviderRejectsInvalidBaseURL(t *testing.T) {
	t.Setenv("COPILOT_BASE_URL", "waza-test-resource.openai.azure.com/openai/v1")
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "")

	ctrl := gomock.NewController(t)
	clientMock := NewMockCopilotClient(ctrl)

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	err := engine.Initialize(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid custom Copilot provider base URL")
}

func TestCopilotResumeSessionID_Live(t *testing.T) {
	skipIfCopilotNotEnabled(t)

	engine := NewCopilotEngineBuilder("", nil).Build()

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		err := engine.Shutdown(ctx)
		require.NoError(t, err)
	})

	randIntAsStr := strconv.FormatInt(rand.Int63(), 10)

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message: fmt.Sprintf("Memorize this integer and echo it back to me: %s", randIntAsStr),
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.SessionID)
	require.Contains(t, resp.FinalOutput, randIntAsStr)

	resp, err = engine.Execute(context.Background(), &ExecutionRequest{
		SessionID: resp.SessionID,
		Message:   "What number did I ask you to memorize?",
	})
	require.NoError(t, err)
	require.Contains(t, resp.FinalOutput, randIntAsStr)
}

func TestCopilotNativeSandbox_Live(t *testing.T) {
	skipIfCopilotNotEnabled(t)
	t.Setenv("WAZA_VISIBLE_CANARY", "must-not-reach-copilot")

	cwd, err := os.Getwd()
	require.NoError(t, err)
	root, err := os.MkdirTemp(filepath.Dir(cwd), ".waza-sandbox-canary-*")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	skillDir := filepath.Join(root, "sandbox-canary")
	require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: sandbox-canary
description: Runs the bundled sandbox canary helper.
---

When requested, run the bundled scripts/helper.sh relative to this SKILL.md file.
`), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(skillDir, "scripts", "helper.sh"),
		[]byte("#!/bin/sh\nprintf '%s\\n' 'skill-script-ran'\n"),
		0o755,
	))

	outsideRead := filepath.Join(root, "outside-read.txt")
	outsideWrite := filepath.Join(root, "outside-write.txt")
	skillWrite := filepath.Join(skillDir, "forbidden-write.txt")
	require.NoError(t, os.WriteFile(outsideRead, []byte("outside-must-not-be-readable\n"), 0o644))
	declaredReadonlyDir := filepath.Join(root, "declared-read-only")
	declaredReadwriteDir := filepath.Join(root, "declared-read-write")
	require.NoError(t, os.MkdirAll(declaredReadonlyDir, 0o755))
	require.NoError(t, os.MkdirAll(declaredReadwriteDir, 0o755))
	declaredReadonlyFile := filepath.Join(declaredReadonlyDir, "input.txt")
	declaredReadonlyWrite := filepath.Join(declaredReadonlyDir, "forbidden-write.txt")
	declaredReadwriteFile := filepath.Join(declaredReadwriteDir, "input.txt")
	declaredReadwriteWrite := filepath.Join(declaredReadwriteDir, "allowed-write.txt")
	require.NoError(t, os.WriteFile(declaredReadonlyFile, []byte("declared-read-only-readable\n"), 0o644))
	require.NoError(t, os.WriteFile(declaredReadwriteFile, []byte("declared-read-write-readable\n"), 0o644))
	tempOutsideDir := t.TempDir()
	tempOutsideRead := filepath.Join(tempOutsideDir, "outside-read.txt")
	tempOutsideWrite := filepath.Join(tempOutsideDir, "outside-write.txt")
	require.NoError(t, os.WriteFile(tempOutsideRead, []byte("temp-must-not-be-readable\n"), 0o644))
	canaryScript := fmt.Sprintf(`#!/bin/sh
cp inside.txt inside-copy.txt
%q > skill-output.txt
cat %q > declared-read-only-output.txt
touch %q
cat %q > declared-read-write-output.txt
touch %q
cat %q > outside-read-output.txt
touch %q
touch %q
cat %q > temp-read-output.txt
touch %q
printenv WAZA_VISIBLE_CANARY > inherited-environment.txt
exit 0
`, filepath.Join(skillDir, "scripts", "helper.sh"), declaredReadonlyFile, declaredReadonlyWrite, declaredReadwriteFile, declaredReadwriteWrite, outsideRead, outsideWrite, skillWrite, tempOutsideRead, tempOutsideWrite)

	engine := NewCopilotEngineBuilder("gpt-5.6-luna", &CopilotEngineBuilderOptions{SanitizeEnvironment: true}).Build()
	require.NoError(t, engine.Initialize(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		require.NoError(t, engine.Shutdown(ctx))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	resp, err := engine.Execute(ctx, &ExecutionRequest{
		Message: "Use Bash now to run exactly `sh canary.sh` without modifying it. You must execute the command rather than describe it. Finish immediately after it runs.",
		Resources: []ResourceFile{
			{Path: "inside.txt", Content: []byte("workspace-readable\n")},
			{Path: "canary.sh", Content: []byte(canaryScript)},
		},
		SkillPaths: []string{skillDir},
		Sandbox: &models.SandboxConfig{
			Enabled:        true,
			ReadonlyPaths:  []string{declaredReadonlyDir},
			ReadwritePaths: []string{declaredReadwriteDir},
		},
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.ErrorMsg)
	if string(resp.WorkspaceFiles["inside-copy.txt"]) != "workspace-readable\n" {
		t.Logf("sandbox canary final output: %s", resp.FinalOutput)
		for _, call := range resp.ToolCalls {
			t.Logf("sandbox canary tool call: name=%s command=%q", call.Name, call.Arguments.Command)
		}
	}
	require.Equal(t, "workspace-readable\n", string(resp.WorkspaceFiles["inside-copy.txt"]))
	require.Equal(t, "skill-script-ran\n", string(resp.WorkspaceFiles["skill-output.txt"]))
	require.Equal(t, "declared-read-only-readable\n", string(resp.WorkspaceFiles["declared-read-only-output.txt"]))
	require.Equal(t, "declared-read-write-readable\n", string(resp.WorkspaceFiles["declared-read-write-output.txt"]))
	require.Empty(t, resp.WorkspaceFiles["outside-read-output.txt"])
	require.Empty(t, resp.WorkspaceFiles["temp-read-output.txt"])
	require.Empty(t, resp.WorkspaceFiles["inherited-environment.txt"])
	require.NoFileExists(t, declaredReadonlyWrite)
	require.FileExists(t, declaredReadwriteWrite)
	require.NoFileExists(t, outsideWrite)
	require.NoFileExists(t, skillWrite)
	require.NoFileExists(t, tempOutsideWrite)
}

func TestCopilotNativeSandbox_ConcurrentPoliciesDoNotLeak_Live(t *testing.T) {
	skipIfCopilotNotEnabled(t)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	root, err := os.MkdirTemp(filepath.Dir(cwd), ".waza-sandbox-concurrent-*")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })

	allowedDirs := []string{filepath.Join(root, "task-a"), filepath.Join(root, "task-b")}
	for i, dir := range allowedDirs {
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "input.txt"), []byte(fmt.Sprintf("task-%d\n", i)), 0o644))
	}

	engine := NewCopilotEngineBuilder("gpt-5.6-luna", &CopilotEngineBuilderOptions{SanitizeEnvironment: true}).Build()
	require.NoError(t, engine.Initialize(context.Background()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		require.NoError(t, engine.Shutdown(ctx))
	})

	responses := make([]*ExecutionResponse, len(allowedDirs))
	g, ctx := errgroup.WithContext(context.Background())
	for i := range allowedDirs {
		i := i
		g.Go(func() error {
			other := allowedDirs[1-i]
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			resp, err := engine.Execute(ctx, &ExecutionRequest{
				Message: fmt.Sprintf(`Use a separate Bash tool call for each command and attempt every command exactly once:
1. cat %q > own-output.txt
2. touch %q
3. cat %q > cross-output.txt
4. touch %q
Finish after all four attempts.`, filepath.Join(allowedDirs[i], "input.txt"), filepath.Join(allowedDirs[i], "own-write.txt"), filepath.Join(other, "input.txt"), filepath.Join(other, fmt.Sprintf("escaped-from-%d.txt", i))),
				NoSkills: true,
				Sandbox: &models.SandboxConfig{
					Enabled:        true,
					ReadwritePaths: []string{allowedDirs[i]},
				},
			})
			responses[i] = resp
			return err
		})
	}
	require.NoError(t, g.Wait())

	for i, resp := range responses {
		require.NotNil(t, resp)
		require.True(t, resp.Success, resp.ErrorMsg)
		require.Equal(t, fmt.Sprintf("task-%d\n", i), string(resp.WorkspaceFiles["own-output.txt"]))
		require.Empty(t, resp.WorkspaceFiles["cross-output.txt"])
		require.FileExists(t, filepath.Join(allowedDirs[i], "own-write.txt"))
		require.NoFileExists(t, filepath.Join(allowedDirs[1-i], fmt.Sprintf("escaped-from-%d.txt", i)))

		blockedPaths := map[string]bool{
			filepath.Join(allowedDirs[1-i], "input.txt"):                           false,
			filepath.Join(allowedDirs[1-i], fmt.Sprintf("escaped-from-%d.txt", i)): false,
		}
		for _, call := range resp.ToolCalls {
			for path := range blockedPaths {
				if call.Name == "bash" && strings.Contains(call.Arguments.Command, path) {
					require.NotNil(t, call.Result)
					require.Contains(t, call.Result.Content, "Operation not permitted")
					blockedPaths[path] = true
				}
			}
		}
		for path, wasBlocked := range blockedPaths {
			require.Truef(t, wasBlocked, "expected task %d to be denied access to %s", i, path)
		}
	}
}

func TestCopilotSendAndWaitReturnsErrorInResult(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	sourceDir := t.TempDir()
	const sessionErrorMsg = "session error occurred"

	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.SessionConfig{
			Model:               "gpt-4o-mini",
			SkillDirectories:    []string{sourceDir},
			OnPermissionRequest: allowAllTools,
		},
	}

	clientMock.EXPECT().CreateSession(gomock.Any(), expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(nil, errors.New(sessionErrorMsg))
	sessionMock.EXPECT().SessionID().Return("session-1")

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	defer func() {
		err := engine.Shutdown(context.Background())
		require.NoError(t, err)
	}()

	err := engine.Initialize(context.Background())
	require.NoError(t, err)

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:   "message",
		SourceDir: sourceDir,
	})
	require.NoError(t, err)
	require.Equal(t, sessionErrorMsg, resp.ErrorMsg)
}

func TestCopilotInitialize_PropagatesStartError(t *testing.T) {
	// Regression test: Initialize() must propagate Start() errors so callers see
	// copilot CLI startup failures instead of hanging or proceeding silently.
	ctrl := gomock.NewController(t)
	clientMock := NewMockCopilotClient(ctrl)

	// Start returns an error, simulating a copilot CLI that fails to start.
	clientMock.EXPECT().Start(gomock.Any()).Return(errors.New("context canceled"))
	clientMock.EXPECT().Stop().AnyTimes()

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	defer func() { require.NoError(t, engine.Shutdown(context.Background())) }()

	err := engine.Initialize(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "copilot failed to start")
}

func TestCopilotExecuteParallel_Live(t *testing.T) {
	skipIfCopilotNotEnabled(t)

	for range 5 {
		engine := NewCopilotEngineBuilder("gpt-4o-mini", nil).Build()

		err := engine.Initialize(context.Background())
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		eg := errgroup.Group{}

		for range 10 {
			eg.Go(func() error {
				_, err := engine.Execute(ctx, &ExecutionRequest{
					Message: "hello!",
				})
				return err
			})
		}

		err = eg.Wait()
		require.NoError(t, err)
		require.NoError(t, engine.Shutdown(context.Background()))
	}
}

func TestCopilotNotAuthenticated(t *testing.T) {
	t.Run("not authenticated", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		clientMock := NewMockCopilotClient(ctrl)

		clientMock.EXPECT().Start(gomock.Any())
		clientMock.EXPECT().GetAuthStatus(gomock.Any()).Times(1).Return(&copilot.GetAuthStatusResponse{
			IsAuthenticated: false,
		}, nil)

		engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
			NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
		}).Build()
		defer func() {
			clientMock.EXPECT().Stop()
			require.NoError(t, engine.Shutdown(context.Background()))
		}()

		clientMock.EXPECT().Stop()
		err := engine.Initialize(context.Background())
		require.Error(t, err)
		require.ErrorContains(t, err, "not authenticated")
	})

	t.Run("error checking authentication status", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		clientMock := NewMockCopilotClient(ctrl)

		// Start returns an error, simulating a copilot CLI that fails to start.
		clientMock.EXPECT().Start(gomock.Any())
		clientMock.EXPECT().GetAuthStatus(gomock.Any()).Times(1).Return(nil, errors.New("auth status not available or something"))

		engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
			NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
		}).Build()
		defer func() {
			clientMock.EXPECT().Stop()
			require.NoError(t, engine.Shutdown(context.Background()))
		}()

		clientMock.EXPECT().Stop() // we fail in our init
		err := engine.Initialize(context.Background())
		require.Error(t, err)
		require.ErrorContains(t, err, "failed to get copilot authentication status")
	})
}

type sessionConfigMatcher struct {
	expected  any
	sourceDir string
	t         *testing.T
}

func (m sessionConfigMatcher) Matches(x any) bool {
	switch tempC := x.(type) {
	case *copilot.SessionConfig:
		c := *tempC
		expected, ok := m.expected.(copilot.SessionConfig)
		require.True(m.t, ok)

		require.NotEqual(m.t, m.sourceDir, c.WorkingDirectory)
		require.NotEmpty(m.t, c.WorkingDirectory)

		if expected.OnPermissionRequest == nil {
			require.Nil(m.t, c.OnPermissionRequest)
		} else {
			require.NotNil(m.t, c.OnPermissionRequest)
		}

		c.WorkingDirectory = ""

		// Equal can't compare function ptrs..
		expected.OnPermissionRequest = nil
		c.OnPermissionRequest = nil

		// streamingPtr always returns a non-nil *bool now; when an expected
		// fixture omits Streaming, treat actual *bool(false) as equivalent.
		if expected.Streaming == nil && c.Streaming != nil && !*c.Streaming {
			c.Streaming = nil
		}

		require.Equal(m.t, expected, c)
	case *copilot.ResumeSessionConfig:
		c := *tempC
		expected, ok := m.expected.(copilot.ResumeSessionConfig)
		require.True(m.t, ok)

		require.NotEqual(m.t, m.sourceDir, c.WorkingDirectory)
		require.NotEmpty(m.t, c.WorkingDirectory)

		if expected.OnPermissionRequest == nil {
			require.Nil(m.t, c.OnPermissionRequest)
		} else {
			require.NotNil(m.t, c.OnPermissionRequest)
		}

		c.WorkingDirectory = ""

		// Equal can't compare function ptrs..
		expected.OnPermissionRequest = nil
		c.OnPermissionRequest = nil

		if expected.Streaming == nil && c.Streaming != nil && !*c.Streaming {
			c.Streaming = nil
		}

		require.Equal(m.t, expected, c)
	default:
		require.FailNow(m.t, "Unhandled session configuration type %T", tempC)
	}

	return true
}

func (m sessionConfigMatcher) String() string {
	return ""
}

func newClientMock(ctrl *gomock.Controller) *MockCopilotClient {
	clientMock := NewMockCopilotClient(ctrl)

	// This is the basic sequence of calls that occurs anytime a copilot engine is initialized
	clientMock.EXPECT().Start(gomock.Any()).Times(1)
	clientMock.EXPECT().Stop().Times(1)
	clientMock.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{
		IsAuthenticated: true,
	}, nil).Times(1)

	return clientMock
}

func skipIfCopilotNotEnabled(t *testing.T) {
	if !enableLiveCopilotTests {
		t.Skip("ENABLE_COPILOT_TESTS must be set in order to run live copilot tests")
	}
}

func TestCopilotCreateSession_InjectsSkillSystemMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	sourceDir := t.TempDir()

	// Write a SKILL.md in the source dir
	skillContent := "---\nname: test-skill\ndescription: A test\n---\n# Rules\nAlways greet"
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte(skillContent), 0644))

	expectedSystemMsg := buildSkillSystemMessage([]string{sourceDir}, "test-skill", true)
	require.NotEmpty(t, expectedSystemMsg)

	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.SessionConfig{
			Model:               "gpt-4o-mini",
			SkillDirectories:    []string{sourceDir},
			OnPermissionRequest: allowAllTools,
			SystemMessage: &copilot.SystemMessageConfig{
				Mode:    "append",
				Content: expectedSystemMsg,
			},
		},
	}

	clientMock.EXPECT().CreateSession(gomock.Any(), expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	require.NoError(t, engine.Initialize(context.Background()))

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:   "hello",
		SkillName: "test-skill",
		SourceDir: sourceDir,
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
}

func TestCopilotCreateSession_InjectsInstructionSystemMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	sourceDir := t.TempDir()
	skillContent := "---\nname: test-skill\ndescription: A test\n---\n# Rules\nAlways greet"
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte(skillContent), 0644))

	instructions := []InstructionFile{{
		Path:    ".github/instructions/project.instructions.md",
		Content: []byte("Prefer small functions."),
	}}
	expectedSystemMsg := strings.Join([]string{
		buildSkillSystemMessage([]string{sourceDir}, "test-skill", true),
		buildInstructionSystemMessage(instructions),
	}, "\n")
	require.NotEmpty(t, expectedSystemMsg)

	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.SessionConfig{
			Model:               "gpt-4o-mini",
			SkillDirectories:    []string{sourceDir},
			OnPermissionRequest: allowAllTools,
			SystemMessage: &copilot.SystemMessageConfig{
				Mode:    "append",
				Content: expectedSystemMsg,
			},
		},
	}

	clientMock.EXPECT().CreateSession(gomock.Any(), expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	require.NoError(t, engine.Initialize(context.Background()))

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:      "hello",
		SkillName:    "test-skill",
		SourceDir:    sourceDir,
		Instructions: instructions,
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
}

func TestCopilotCreateSession_PassesMCPServers(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	sourceDir := t.TempDir()

	mcpServers := map[string]copilot.MCPServerConfig{
		"test-mcp": copilot.MCPStdioServerConfig{Command: "echo", Args: []string{"hello"}},
	}

	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.SessionConfig{
			Model:               "gpt-4o-mini",
			SkillDirectories:    []string{sourceDir},
			OnPermissionRequest: allowAllTools,
			MCPServers:          mcpServers,
		},
	}

	clientMock.EXPECT().CreateSession(gomock.Any(), expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-1")

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-1")

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	require.NoError(t, engine.Initialize(context.Background()))

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:    "hello",
		SourceDir:  sourceDir,
		MCPServers: mcpServers,
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
}

func TestCopilotResumeSession_PassesMCPServersAndSystemMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	sourceDir := t.TempDir()

	// Write a SKILL.md
	skillContent := "---\nname: resume-skill\ndescription: Resume test\n---\nResume body"
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "SKILL.md"), []byte(skillContent), 0644))

	expectedSystemMsg := buildSkillSystemMessage([]string{sourceDir}, "resume-skill", true)

	mcpServers := map[string]copilot.MCPServerConfig{
		"mcp-srv": copilot.MCPStdioServerConfig{Command: "test"},
	}

	expectedConfig := sessionConfigMatcher{
		t:         t,
		sourceDir: sourceDir,
		expected: copilot.ResumeSessionConfig{
			Model:               "gpt-4o-mini",
			SkillDirectories:    []string{sourceDir},
			OnPermissionRequest: allowAllTools,
			SystemMessage: &copilot.SystemMessageConfig{
				Mode:    "append",
				Content: expectedSystemMsg,
			},
			MCPServers: mcpServers,
		},
	}

	clientMock.EXPECT().ResumeSessionWithOptions(gomock.Any(), "session-resume", expectedConfig).Return(sessionMock, nil)
	sessionMock.EXPECT().Disconnect()
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-resume")

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)
	sessionMock.EXPECT().SessionID().Return("session-resume")

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(clientOptions *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()

	defer func() {
		require.NoError(t, engine.Shutdown(context.Background()))
	}()

	require.NoError(t, engine.Initialize(context.Background()))

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:    "hello",
		SessionID:  "session-resume",
		SkillName:  "resume-skill",
		SourceDir:  sourceDir,
		MCPServers: mcpServers,
	})
	require.NoError(t, err)
	require.True(t, resp.Success)
}

func TestCopilotExecute_CancelOnSkillInvocation(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	sourceDir := t.TempDir()

	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(sessionMock, nil)
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-skill-cancel")

	sessionMock.EXPECT().SessionID().Return("session-skill-cancel")
	sessionMock.EXPECT().Disconnect()

	// Capture the event handlers registered via session.On so we can
	// fire a SkillInvoked event from inside SendAndWait.
	var eventHandlers []func(copilot.SessionEvent)
	sessionMock.EXPECT().On(gomock.Any()).Times(3).DoAndReturn(func(handler func(copilot.SessionEvent)) func() {
		eventHandlers = append(eventHandlers, handler)
		return func() {}
	})

	skillName := "my-skill"
	skillPath := "/skills/my-skill/SKILL.md"

	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, _ copilot.MessageOptions) (*copilot.SessionEvent, error) {
			// Simulate a SkillInvoked event arriving mid-stream.
			for _, handler := range eventHandlers {
				handler(copilot.SessionEvent{
					Data: &copilot.SkillInvokedData{
						Name: skillName,
						Path: skillPath,
					},
				})
			}
			// Block until context is canceled (which our callback should do).
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(_ *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	defer func() { require.NoError(t, engine.Shutdown(context.Background())) }()

	require.NoError(t, engine.Initialize(context.Background()))

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:                 "invoke a skill please",
		SourceDir:               sourceDir,
		CancelOnSkillInvocation: true,
	})
	require.NoError(t, err)
	require.True(t, resp.Success, "cancellation from skill invocation should be treated as success")
	require.Empty(t, resp.ErrorMsg)
	require.Len(t, resp.SkillInvocations, 1)
	require.Equal(t, "my-skill", resp.SkillInvocations[0].Name)
}

func TestCopilotExecute_CancelOnSkillInvocation_NoSkillFired(t *testing.T) {
	ctrl := gomock.NewController(t)
	clientMock := newClientMock(ctrl)
	sessionMock := NewMockCopilotSession(ctrl)

	sourceDir := t.TempDir()

	clientMock.EXPECT().CreateSession(gomock.Any(), gomock.Any()).Return(sessionMock, nil)
	clientMock.EXPECT().DeleteSession(gomock.Any(), "session-no-skill")

	sessionMock.EXPECT().SessionID().Return("session-no-skill")
	sessionMock.EXPECT().Disconnect()

	sessionMock.EXPECT().On(gomock.Any()).Times(3).Return(func() {})
	sessionMock.EXPECT().SendAndWait(gomock.Any(), gomock.Any()).Return(&copilot.SessionEvent{}, nil)

	engine := NewCopilotEngineBuilder("gpt-4o-mini", &CopilotEngineBuilderOptions{
		NewCopilotClient: func(_ *copilot.ClientOptions) CopilotClient { return clientMock },
	}).Build()
	defer func() { require.NoError(t, engine.Shutdown(context.Background())) }()

	require.NoError(t, engine.Initialize(context.Background()))

	resp, err := engine.Execute(context.Background(), &ExecutionRequest{
		Message:                 "do something normal",
		SourceDir:               sourceDir,
		CancelOnSkillInvocation: true,
	})
	require.NoError(t, err)
	require.True(t, resp.Success, "flag should be safe even when no skill fires")
	require.Empty(t, resp.ErrorMsg)
}
