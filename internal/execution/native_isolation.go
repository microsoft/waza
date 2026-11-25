package execution

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/models"
)

// NativeSandboxProbeRunner runs isolation conformance probes through Copilot's
// sandboxed shell tool. Each call starts its own sandboxed Copilot CLI with the
// same allowlisted environment as sandboxed evaluations and sends no model
// turn, so probes consume no premium requests.
type NativeSandboxProbeRunner struct{}

func (NativeSandboxProbeRunner) start(ctx context.Context) (*copilot.Client, []string, error) {
	options, err := clientOptions("error", nil, true)
	if err != nil {
		return nil, nil, err
	}
	client := copilot.NewClient(options)
	if err := client.Start(ctx); err != nil {
		client.ForceStop()
		return nil, nil, fmt.Errorf("starting Copilot CLI: %w", err)
	}
	return client, options.Env, nil
}

func stopProbeClient(client *copilot.Client) {
	if err := client.Stop(); err != nil {
		client.ForceStop()
	}
}

// HostSupport asks the runtime whether this host can run sandboxed shell
// commands, without starting a session.
func (r NativeSandboxProbeRunner) HostSupport(ctx context.Context) (bool, string, error) {
	client, _, err := r.start(ctx)
	if err != nil {
		return false, "", err
	}
	defer stopProbeClient(client)
	support, err := client.RPC.Sandbox.GetHostSupport(ctx)
	if err != nil {
		return false, "", err
	}
	if !support.Supported {
		reason := "Copilot reports that this host cannot run its native sandbox"
		if support.Reason != nil && *support.Reason != "" {
			reason = *support.Reason
		}
		return false, reason, nil
	}
	for _, capability := range support.Capabilities {
		if capability.Name != "shell" {
			continue
		}
		if capability.Supported {
			return true, "", nil
		}
		reason := "Copilot reports that this host cannot run sandboxed shell commands"
		if capability.Reason != nil && *capability.Reason != "" {
			reason = *capability.Reason
		}
		return false, reason, nil
	}
	return false, "Copilot did not report sandboxed shell support", nil
}

// RunShell runs script as a user-requested shell command. That request goes
// through the session's shell tool and therefore its native sandbox policy,
// unlike session.shell.exec, which runs outside it.
func (r NativeSandboxProbeRunner) RunShell(ctx context.Context, workspace string, policy models.SandboxConfig, script string) (string, error) {
	if !policy.Enabled {
		return "", errors.New("sandbox policy is not enabled")
	}
	resolved, err := resolveSandboxPaths(policy)
	if err != nil {
		return "", err
	}
	if err := validateSandboxPathPolicy(workspace, nil, resolved); err != nil {
		return "", err
	}
	client, processEnv, err := r.start(ctx)
	if err != nil {
		return "", err
	}
	defer stopProbeClient(client)
	session, err := client.CreateSession(ctx, &copilot.SessionConfig{
		WorkingDirectory:    workspace,
		OnPermissionRequest: sandboxPermissionHandler(allowAllTools),
	})
	if err != nil {
		return "", fmt.Errorf("creating probe session: %w", err)
	}
	wrapped := &copilotSessionWrapper{inner: session, client: client, getStatus: client.GetStatus, processEnv: processEnv}
	defer func() {
		_ = wrapped.Disconnect() //nolint:errcheck // best-effort teardown of the probe session
		deleteCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = client.DeleteSession(deleteCtx, session.SessionID) //nolint:errcheck // best-effort teardown of the probe session
	}()
	if err := wrapped.ConfigureSandbox(ctx, workspace, nil, resolved); err != nil {
		return "", fmt.Errorf("configuring probe sandbox: %w", err)
	}
	requestID := make([]byte, 8)
	if _, err := rand.Read(requestID); err != nil {
		return "", err
	}
	result, err := session.RPC.Shell.ExecuteUserRequested(ctx, &rpc.ShellExecuteUserRequestedRequest{
		Command:   script,
		RequestID: "waza-isolation-" + hex.EncodeToString(requestID),
	})
	if err != nil {
		return "", fmt.Errorf("running probe in the sandboxed shell: %w", err)
	}
	return result.Output, nil
}
