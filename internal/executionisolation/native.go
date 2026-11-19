package executionisolation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/microsoft/waza/internal/models"
)

// NativeSandboxProviderName names the Copilot-native sandbox provider (#544).
const NativeSandboxProviderName = "copilot-native-sandbox"

// NativeSandboxRunner runs probe scripts through Copilot's sandboxed shell
// tool, the same tool that model commands use, without a model turn.
type NativeSandboxRunner interface {
	// HostSupport reports whether this host can run the sandboxed shell.
	HostSupport(ctx context.Context) (supported bool, reason string, err error)
	// RunShell runs a POSIX shell script in a fresh session whose native
	// sandbox applies policy to workspace, and returns its captured output.
	RunShell(ctx context.Context, workspace string, policy models.SandboxConfig, script string) (string, error)
}

// NativeSandboxProvider proves the boundaries that Copilot's native sandbox
// enforces for the selected evals' sandbox policies. Every claim is probed by
// a differential: the Waza process performs the same write or connection
// successfully, then the sandboxed shell attempts it and must be denied.
//
// It claims only boundaries the native sandbox itself enforces. Environment
// withholding comes from Waza's allowlisted client environment and the shell
// starts child processes by design, so neither is claimed; remote tools are a
// separate trust boundary.
type NativeSandboxProvider struct {
	// Policies are the enabled sandbox policies of the selected evals.
	Policies []models.SandboxConfig
	// Unsandboxed names selected evals without an enabled sandbox.
	Unsandboxed []string
	Runner      NativeSandboxRunner
	// WorkspaceRoot holds probe workspaces. It defaults to the user-cache
	// root that sandboxed task workspaces use.
	WorkspaceRoot string
	// Listen opens the probe listener on a non-loopback address; tests may
	// replace it.
	Listen func() (net.Listener, error)
}

func (NativeSandboxProvider) Name() string { return NativeSandboxProviderName }

func (p NativeSandboxProvider) Discover(ctx context.Context) Discovery {
	switch {
	case len(p.Policies) == 0:
		return Discovery{Status: ProviderAbsent, Reason: "no selected eval enables config.sandbox"}
	case len(p.Unsandboxed) > 0:
		names := slices.Clone(p.Unsandboxed)
		slices.Sort(names)
		return Discovery{Status: ProviderUnavailable, Reason: "native sandbox boundaries must hold for every selected eval, but these do not enable config.sandbox: " + strings.Join(names, ", ")}
	case p.Runner == nil:
		return Discovery{Status: ProviderAbsent, Reason: "no Copilot sandbox runner is configured"}
	case runtime.GOOS == "windows":
		return Discovery{Status: ProviderUnavailable, Reason: "native conformance probes support the macOS and Linux sandbox shells only"}
	}
	supported, reason, err := p.Runner.HostSupport(ctx)
	if err != nil {
		return Discovery{Status: ProviderUnavailable, Reason: "checking Copilot sandbox host support failed: " + err.Error()}
	}
	if !supported {
		if reason == "" {
			reason = "Copilot reports that this host cannot run the sandboxed shell"
		}
		return Discovery{Status: ProviderUnavailable, Reason: reason}
	}
	var claims []Claim
	if !slices.ContainsFunc(p.Policies, writesOutsideWorkspace) {
		claims = append(claims, Claim{Filesystem, p.probeFilesystem})
	}
	if !slices.ContainsFunc(p.Policies, allowsNetwork) {
		claims = append(claims, Claim{Network, p.probeNetwork})
	}
	return Discovery{Status: ProviderAvailable, EnforcedBy: "Copilot CLI native sandbox", Claims: claims}
}

// writesOutsideWorkspace reports whether a policy deliberately grants write
// access outside the task workspace, which the filesystem boundary forbids.
func writesOutsideWorkspace(policy models.SandboxConfig) bool {
	return len(policy.ReadwritePaths) > 0 || policy.AllowDevToolCaches
}

// allowsNetwork reports whether a policy deliberately permits non-loopback
// connections, which the network boundary forbids.
func allowsNetwork(policy models.SandboxConfig) bool {
	return policy.AllowOutboundNetwork || policy.AllowLocalNetwork
}

func (p NativeSandboxProvider) probeFilesystem(ctx context.Context) ProbeResult {
	return p.probeEveryPolicy(ctx, p.probeFilesystemPolicy)
}

func (p NativeSandboxProvider) probeNetwork(ctx context.Context) ProbeResult {
	return p.probeEveryPolicy(ctx, p.probeNetworkPolicy)
}

// probeEveryPolicy requires the boundary to hold under every selected policy.
func (p NativeSandboxProvider) probeEveryPolicy(ctx context.Context, probe func(context.Context, models.SandboxConfig) ProbeResult) ProbeResult {
	var result ProbeResult
	for i, policy := range p.Policies {
		result = probe(ctx, policy)
		if result.Outcome != Enforced {
			if len(p.Policies) > 1 {
				result.Detail = fmt.Sprintf("sandbox policy %d of %d: %s", i+1, len(p.Policies), result.Detail)
			}
			return result
		}
	}
	if len(p.Policies) > 1 {
		result.Detail = fmt.Sprintf("all %d selected sandbox policies: %s", len(p.Policies), result.Detail)
	}
	return result
}

func (p NativeSandboxProvider) probeFilesystemPolicy(ctx context.Context, policy models.SandboxConfig) ProbeResult {
	workspace, cleanup, err := p.newWorkspace()
	if err != nil {
		return ProbeResult{Inconclusive, "probe workspace could not be created: " + err.Error()}
	}
	defer cleanup()
	token, err := probeToken()
	if err != nil {
		return ProbeResult{Inconclusive, "probe name could not be generated"}
	}

	candidates := []string{filepath.Dir(workspace)}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, home)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	tempRoot := canonical(os.TempDir())
	var targets []string
	for _, candidate := range candidates {
		dir := canonical(candidate)
		if within(workspace, dir) || within(tempRoot, dir) || slices.Contains(targets, dir) {
			continue
		}
		// Positive control: a denial only means something where the Waza
		// process itself can write.
		file, err := os.CreateTemp(dir, ".waza-isolation-control-*")
		if err != nil {
			continue
		}
		removeProbe(file)
		targets = append(targets, dir)
	}
	if len(targets) == 0 {
		return ProbeResult{Inconclusive, "the Waza process could not write to any location outside the workspace, so a blocked sandbox write proves nothing"}
	}

	name := ".waza-isolation-probe-" + token
	var script strings.Builder
	fmt.Fprintf(&script, "if : > %s 2>/dev/null; then echo 'WAZA inside ok'; else echo 'WAZA inside fail'; fi\n", shellQuote(name))
	for i, dir := range targets {
		fmt.Fprintf(&script, "if : > %s 2>/dev/null; then echo 'WAZA write %d ok'; else echo 'WAZA write %d fail'; fi\n", shellQuote(filepath.Join(dir, name)), i, i)
	}
	script.WriteString("echo 'WAZA end'\n")
	output, runErr := p.Runner.RunShell(ctx, workspace, policy, script.String())

	// The host check is authoritative: remove anything the sandbox created.
	var created []string
	for _, dir := range targets {
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); err == nil {
			created = append(created, dir)
			_ = os.Remove(path) //nolint:errcheck // best-effort removal of a violating probe file
		}
	}
	if len(created) > 0 {
		return ProbeResult{Violated, "the sandboxed shell created a file outside the workspace in " + strings.Join(created, ", ")}
	}
	for i := range targets {
		if probeLine(output, fmt.Sprintf("write %d ok", i)) {
			return ProbeResult{Violated, "the sandboxed shell reported a write outside the workspace in " + targets[i]}
		}
	}
	if runErr != nil {
		return ProbeResult{Inconclusive, "the sandboxed probe did not run: " + runErr.Error()}
	}
	if !probeLine(output, "end") {
		return ProbeResult{Inconclusive, "the sandboxed probe output was incomplete"}
	}
	if !probeLine(output, "inside ok") {
		return ProbeResult{Inconclusive, "the workspace was not writable inside the sandbox, so a blocked write outside it proves nothing"}
	}
	for i := range targets {
		if !probeLine(output, fmt.Sprintf("write %d fail", i)) {
			return ProbeResult{Inconclusive, "the sandboxed probe did not report the write attempt in " + targets[i]}
		}
	}
	return ProbeResult{Enforced, fmt.Sprintf("the sandboxed shell could not create files in %s, which the Waza process can write, while the workspace stayed writable", strings.Join(targets, ", "))}
}

func (p NativeSandboxProvider) probeNetworkPolicy(ctx context.Context, policy models.SandboxConfig) ProbeResult {
	listen := p.Listen
	if listen == nil {
		listen = listenNonLoopback
	}
	listener, err := listen()
	if err != nil {
		return ProbeResult{Inconclusive, "no non-loopback address could be opened for probing: " + err.Error()}
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close() //nolint:errcheck // unusable probe listener
		return ProbeResult{Inconclusive, "the probe listener has no TCP address"}
	}
	var accepted atomic.Int64
	observed := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close() //nolint:errcheck // the connection only proves reachability
			select {
			case observed <- struct{}{}:
			default:
			}
		}
	}()
	closeListener := func() {
		_ = listener.Close() //nolint:errcheck // best-effort shutdown of the probe listener
		<-done
	}

	// Positive control: the Waza process must reach the address first.
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address.String())
	if err != nil {
		closeListener()
		return ProbeResult{Inconclusive, "the Waza process could not reach " + address.String() + ", so a blocked sandbox connection proves nothing"}
	}
	_ = conn.Close() //nolint:errcheck // the control connection only proves reachability
	select {
	case <-observed:
	case <-ctx.Done():
		closeListener()
		return ProbeResult{Inconclusive, "the probe was canceled before the control connection was observed"}
	case <-time.After(5 * time.Second):
		closeListener()
		return ProbeResult{Inconclusive, "the control connection to " + address.String() + " was not observed"}
	}
	accepted.Store(0)

	workspace, cleanup, err := p.newWorkspace()
	if err != nil {
		closeListener()
		return ProbeResult{Inconclusive, "probe workspace could not be created: " + err.Error()}
	}
	defer cleanup()
	script := fmt.Sprintf("if (exec 3<>/dev/tcp/%s/%d) 2>/dev/null; then echo 'WAZA net ok'; else echo 'WAZA net fail'; fi\necho 'WAZA end'\n", address.IP.String(), address.Port)
	output, runErr := p.Runner.RunShell(ctx, workspace, policy, script)
	closeListener()

	if accepted.Load() > 0 || probeLine(output, "net ok") {
		return ProbeResult{Violated, "the sandboxed shell connected to " + address.String()}
	}
	if runErr != nil {
		return ProbeResult{Inconclusive, "the sandboxed probe did not run: " + runErr.Error()}
	}
	if !probeLine(output, "end") || !probeLine(output, "net fail") {
		return ProbeResult{Inconclusive, "the sandboxed probe output was incomplete"}
	}
	return ProbeResult{Enforced, fmt.Sprintf("the sandboxed shell could not connect to %s, which the Waza process reached, and every selected policy denies outbound and local network access; loopback is not covered", address.String())}
}

func (p NativeSandboxProvider) newWorkspace() (string, func(), error) {
	root := p.WorkspaceRoot
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", nil, err
		}
		root = filepath.Join(cache, "waza", "workspaces")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(root, "waza-isolation-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) } //nolint:errcheck // best-effort removal of the probe workspace
	return canonical(dir), cleanup, nil
}

// listenNonLoopback listens on the first up, non-loopback IPv4 interface
// address that the Waza process can reach. Managed network extensions can
// break connections to some local addresses, so each candidate is verified
// with a connection that is accepted before the listener is returned.
func listenNonLoopback() (net.Listener, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil || ipNet.IP.IsLinkLocalUnicast() {
				continue
			}
			listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: ipNet.IP})
			if err != nil {
				continue
			}
			if reachable(listener) {
				return listener, nil
			}
			_ = listener.Close() //nolint:errcheck // unreachable candidate
		}
	}
	return nil, errors.New("no reachable, up, non-loopback IPv4 interface address")
}

// reachable connects to listener and accepts that connection, leaving no
// pending connection behind.
func reachable(listener *net.TCPListener) bool {
	deadline := time.Now().Add(2 * time.Second)
	conn, err := net.DialTimeout("tcp4", listener.Addr().String(), 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close() //nolint:errcheck // the connection only proves reachability
	if err := listener.SetDeadline(deadline); err != nil {
		return false
	}
	accepted, err := listener.Accept()
	if err != nil {
		return false
	}
	_ = accepted.Close() //nolint:errcheck // the connection only proves reachability
	return listener.SetDeadline(time.Time{}) == nil
}

func canonical(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func probeToken() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// probeLine reports whether output contains the exact probe result line.
func probeLine(output, result string) bool {
	return slices.Contains(strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n"), "WAZA "+result)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
