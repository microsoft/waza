package executionisolation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
)

// NativeSandboxProviderName names the Copilot-native sandbox slot (#544).
const NativeSandboxProviderName = "copilot-native-sandbox"

// HostProviderName names the operator-declared host/runtime provider.
const HostProviderName = "host-declaration"

// NativeSandboxProvider represents the Copilot-native sandbox tracked in
// #544. That integration is not part of this build, so the provider reports
// itself absent and claims nothing. When it lands, it supplies claims with
// probes that run inside the native sandbox; this contract does not duplicate
// its enforcement.
type NativeSandboxProvider struct{}

func (NativeSandboxProvider) Name() string { return NativeSandboxProviderName }

func (NativeSandboxProvider) Discover(context.Context) Discovery {
	return Discovery{Status: ProviderAbsent, Reason: "Copilot-native sandbox integration (#544) is not present in this build; it claims no boundary"}
}

// Host performs the probe operations against the environment that encloses
// the Waza process. Each field defaults to the real operating-system call.
type Host struct {
	Workspace  string
	Environ    func() []string
	CreateTemp func(dir, pattern string) (*os.File, error)
	Dial       func(ctx context.Context, network, address string) (net.Conn, error)
	Spawn      func(ctx context.Context, name string, args ...string) error
	// ExternalInterfaces lists the up, non-loopback network interfaces that
	// carry an address; it defaults to the operating system's interfaces.
	ExternalInterfaces func() ([]string, error)
	// TempRoot is the shared temporary area that holds task workspaces and
	// stays writable by design; it defaults to os.TempDir().
	TempRoot string
	Timeout  time.Duration
}

// SubprocessProbeEnv makes a Waza executable exit immediately so the
// subprocess probe can spawn a known-harmless child.
const SubprocessProbeEnv = "WAZA_ISOLATION_SUBPROCESS_PROBE"

// ExitIfSubprocessProbe exits successfully when the process was spawned by
// the subprocess probe. Call it first in main and TestMain.
func ExitIfSubprocessProbe() {
	if os.Getenv(SubprocessProbeEnv) == "1" {
		os.Exit(0)
	}
}

func (h Host) withDefaults() Host {
	if h.Environ == nil {
		h.Environ = os.Environ
	}
	if h.CreateTemp == nil {
		h.CreateTemp = os.CreateTemp
	}
	if h.Dial == nil {
		h.Dial = (&net.Dialer{}).DialContext
	}
	if h.Spawn == nil {
		h.Spawn = spawn
	}
	if h.ExternalInterfaces == nil {
		h.ExternalInterfaces = externalInterfaces
	}
	if h.TempRoot == "" {
		h.TempRoot = os.TempDir()
	}
	if h.Timeout <= 0 {
		h.Timeout = 3 * time.Second
	}
	return h
}

func spawn(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), SubprocessProbeEnv+"=1")
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Wait() //nolint:errcheck // the child only needs to have started
	return nil
}

// HostProvider turns an operator host declaration into probed claims. A nil
// declaration means no outer host boundary is declared.
type HostProvider struct {
	Declaration *HostDeclaration
	Host        Host
}

func (HostProvider) Name() string { return HostProviderName }

func (p HostProvider) Discover(context.Context) Discovery {
	if p.Declaration == nil {
		return Discovery{Status: ProviderAbsent, Reason: "no host isolation declaration was supplied"}
	}
	host := p.Host.withDefaults()
	if host.Workspace == "" {
		return Discovery{Status: ProviderUnavailable, Reason: "no execution workspace was supplied for probing"}
	}
	declared := p.Declaration.Dimensions
	var claims []Claim
	if declared.Filesystem != nil {
		paths := declared.Filesystem.OutsidePaths
		claims = append(claims, Claim{Filesystem, func(context.Context) ProbeResult { return host.probeFilesystem(paths) }})
	}
	if declared.Environment != nil {
		names := declared.Environment.SecretNames
		claims = append(claims, Claim{Environment, func(context.Context) ProbeResult { return host.probeEnvironment(names) }})
	}
	if declared.Network != nil {
		targets := declared.Network.Targets
		claims = append(claims, Claim{Network, func(ctx context.Context) ProbeResult { return host.probeEndpoints(ctx, targets) }})
	}
	if declared.Subprocess != nil {
		claims = append(claims, Claim{Subprocess, host.probeSubprocess})
	}
	if declared.RemoteTools != nil {
		targets := declared.RemoteTools.Targets
		claims = append(claims, Claim{RemoteTools, func(ctx context.Context) ProbeResult { return host.probeEndpoints(ctx, targets) }})
	}
	return Discovery{Status: ProviderAvailable, EnforcedBy: p.Declaration.EnforcedBy, Claims: claims}
}

func denied(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EROFS)
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// probeFilesystem attempts to create a file outside the workspace and the
// shared temporary area that holds task workspaces. The workspace itself must
// stay writable. A successful write proves a violation; denials are only
// inconclusive, because a directory that refuses new files can still hold
// existing writable files. Declared paths add to the defaults (the home directory, the
// current directory and the workspace parent); they never replace them.
func (h Host) probeFilesystem(declared []string) ProbeResult {
	workspace, err := filepath.Abs(h.Workspace)
	if err != nil {
		return ProbeResult{Inconclusive, "workspace path could not be resolved"}
	}
	tempRoot, err := filepath.Abs(h.TempRoot)
	if err != nil {
		return ProbeResult{Inconclusive, "temporary directory could not be resolved"}
	}
	inside, err := h.CreateTemp(workspace, ".waza-isolation-probe-*")
	if err != nil {
		return ProbeResult{Inconclusive, "workspace is not writable, so a blocked write outside it proves nothing"}
	}
	removeProbe(inside)
	paths := []string{filepath.Dir(workspace)}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, home)
	}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, cwd)
	}
	paths = append(paths, declared...)
	var blocked []string
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil || within(workspace, abs) || within(tempRoot, abs) || slices.Contains(blocked, abs) {
			continue
		}
		file, err := h.CreateTemp(abs, ".waza-isolation-probe-*")
		switch {
		case err == nil:
			removeProbe(file)
			return ProbeResult{Violated, "created a file outside the workspace in " + abs}
		case denied(err):
			blocked = append(blocked, abs)
		case errors.Is(err, fs.ErrNotExist):
			continue
		default:
			return ProbeResult{Inconclusive, fmt.Sprintf("write outside the workspace in %s failed for a reason other than denial", abs)}
		}
	}
	if len(blocked) == 0 {
		return ProbeResult{Inconclusive, "no existing path outside the workspace could be probed"}
	}
	return ProbeResult{Inconclusive, "creating files outside the workspace was denied in " + strings.Join(blocked, ", ") + ", but existing writable files there could still be modified, so no write boundary is proved"}
}

func removeProbe(file *os.File) {
	name := file.Name()
	_ = file.Close()    //nolint:errcheck // best-effort cleanup of an empty probe file
	_ = os.Remove(name) //nolint:errcheck // best-effort cleanup of an empty probe file
}

var secretPatterns = []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "API_KEY", "APIKEY", "ACCESS_KEY", "PRIVATE_KEY", "CONNECTION_STRING"}

// probeEnvironment inspects variable names only; values are never read into
// the report. A visible secret-named or declared variable proves a
// violation, but absence never proves enforcement: from inside the boundary
// a withheld secret cannot be told apart from one that never existed.
func (h Host) probeEnvironment(declared []string) ProbeResult {
	var exposed []string
	for _, entry := range h.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		secret := slices.Contains(declared, name) || slices.ContainsFunc(secretPatterns, func(p string) bool { return strings.Contains(upper, p) })
		if secret && !slices.Contains(exposed, name) {
			exposed = append(exposed, name)
		}
	}
	if len(exposed) > 0 {
		slices.Sort(exposed)
		return ProbeResult{Violated, "secret-named variables are exposed: " + strings.Join(exposed, ", ")}
	}
	return ProbeResult{Inconclusive, "no secret-named or declared variables are visible, but absence cannot prove they were withheld rather than never supplied"}
}

// probeEndpoints dials every declared target, which must be reachable
// without the boundary. A failed dial to a sampled target cannot prove a
// whole network policy, so enforcement additionally requires that the
// process has no network interface other than loopback. The boundary is
// off-host traffic: loopback stays reachable by definition.
func (h Host) probeEndpoints(ctx context.Context, targets []string) ProbeResult {
	if len(targets) == 0 {
		return ProbeResult{Inconclusive, "no probe targets were declared"}
	}
	for _, target := range targets {
		dialCtx, cancel := context.WithTimeout(ctx, h.Timeout)
		conn, err := h.Dial(dialCtx, "tcp", target)
		cancel()
		if err == nil {
			_ = conn.Close() //nolint:errcheck // the connection only proves reachability
			return ProbeResult{Violated, "connected to " + target}
		}
	}
	interfaces, err := h.ExternalInterfaces()
	if err != nil {
		return ProbeResult{Inconclusive, "network interfaces could not be listed: " + err.Error()}
	}
	if len(interfaces) > 0 {
		slices.Sort(interfaces)
		return ProbeResult{Inconclusive, fmt.Sprintf("connections to %s failed, but non-loopback interfaces are up (%s), so other destinations may be reachable", strings.Join(targets, ", "), strings.Join(interfaces, ", "))}
	}
	return ProbeResult{Enforced, "no non-loopback network interface is up and connections to " + strings.Join(targets, ", ") + " failed; loopback is not covered"}
}

func externalInterfaces() ([]string, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, iface := range all {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		if len(addrs) > 0 {
			names = append(names, iface.Name)
		}
	}
	return names, nil
}

// probeSubprocess attempts to start two unrelated harmless children: the
// Waza executable and the platform shell. Either starting is a violation.
// Denials are only samples: executable-specific rules can deny both while
// allowing another child, so the host provider never proves this boundary.
func (h Host) probeSubprocess(ctx context.Context) ProbeResult {
	self, err := os.Executable()
	if err != nil {
		return ProbeResult{Inconclusive, "the Waza executable could not be located"}
	}
	shell, shellArgs := "/bin/sh", []string{"-c", "exit 0"}
	if runtime.GOOS == "windows" {
		shell, shellArgs = os.Getenv("ComSpec"), []string{"/c", "exit 0"}
		if shell == "" {
			shell = `C:\Windows\System32\cmd.exe`
		}
	}
	candidates := []struct {
		name string
		args []string
	}{{self, nil}, {shell, shellArgs}}
	denials := 0
	for _, candidate := range candidates {
		err := h.Spawn(ctx, candidate.name, candidate.args...)
		switch {
		case err == nil:
			return ProbeResult{Violated, "started a child process: " + filepath.Base(candidate.name)}
		case denied(err):
			denials++
		}
	}
	if denials == len(candidates) {
		return ProbeResult{Inconclusive, "starting the Waza executable and the platform shell were both denied, but executable-specific rules could still allow other children, so no process-wide restriction is proved"}
	}
	return ProbeResult{Inconclusive, "starting a child process failed for a reason other than a process-wide denial"}
}

// DefaultProviders returns the providers this build can discover: the
// Copilot-native sandbox slot (absent) and the optional host declaration.
func DefaultProviders(declaration *HostDeclaration, workspace string) []Provider {
	return []Provider{NativeSandboxProvider{}, HostProvider{Declaration: declaration, Host: Host{Workspace: workspace}}}
}
