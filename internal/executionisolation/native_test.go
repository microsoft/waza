package executionisolation

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// fakeNativeRunner stands in for Copilot's sandboxed shell.
type fakeNativeRunner struct {
	supported  bool
	reason     string
	supportErr error
	run        func(t *testing.T, workspace, script string) (string, error)
	t          *testing.T
	policies   []models.SandboxConfig
}

func (r *fakeNativeRunner) HostSupport(context.Context) (bool, string, error) {
	return r.supported, r.reason, r.supportErr
}

func (r *fakeNativeRunner) RunShell(_ context.Context, workspace string, policy models.SandboxConfig, script string) (string, error) {
	r.policies = append(r.policies, policy)
	return r.run(r.t, workspace, script)
}

// unsandboxedShell runs the probe with no sandbox at all, so every attempted
// violation succeeds.
func unsandboxedShell(t *testing.T, workspace, script string) (string, error) {
	t.Helper()
	cmd := exec.Command("/bin/bash", "-c", script)
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// denyingShell reports what a working sandbox reports: the workspace write
// succeeds and every other attempt fails, without touching the host.
func denyingShell(_ *testing.T, _ string, script string) (string, error) {
	var lines []string
	for _, match := range regexp.MustCompile(`echo 'WAZA ([a-z]+(?: \d+)?) fail'`).FindAllStringSubmatch(script, -1) {
		if match[1] == "inside" {
			lines = append(lines, "WAZA inside ok")
			continue
		}
		lines = append(lines, "WAZA "+match[1]+" fail")
	}
	return strings.Join(append(lines, "WAZA end"), "\n") + "\n", nil
}

func requirePOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native probes run the macOS and Linux sandbox shells")
	}
}

func loopbackListen() (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }

func nativeProvider(t *testing.T, runner *fakeNativeRunner, policies ...models.SandboxConfig) NativeSandboxProvider {
	runner.t = t
	return NativeSandboxProvider{Policies: policies, Runner: runner, WorkspaceRoot: t.TempDir(), Listen: loopbackListen}
}

func claimFor(t *testing.T, discovery Discovery, dimension Dimension) Claim {
	t.Helper()
	for _, claim := range discovery.Claims {
		if claim.Dimension == dimension {
			return claim
		}
	}
	t.Fatalf("no %s claim in %+v", dimension, discovery)
	return Claim{}
}

func claimedDimensions(discovery Discovery) []Dimension {
	var dimensions []Dimension
	for _, claim := range discovery.Claims {
		dimensions = append(dimensions, claim.Dimension)
	}
	return dimensions
}

func TestNativeSandboxDiscovery(t *testing.T) {
	requirePOSIX(t)
	enabled := models.SandboxConfig{Enabled: true}
	for name, tc := range map[string]struct {
		provider NativeSandboxProvider
		status   ProviderStatus
		reason   string
		claims   []Dimension
	}{
		"no sandboxed eval": {
			provider: NativeSandboxProvider{Runner: &fakeNativeRunner{supported: true}},
			status:   ProviderAbsent, reason: "no selected eval enables config.sandbox",
		},
		"an unsandboxed eval in the run": {
			provider: NativeSandboxProvider{Policies: []models.SandboxConfig{enabled}, Unsandboxed: []string{"z.yaml", "a.yaml"}, Runner: &fakeNativeRunner{supported: true}},
			status:   ProviderUnavailable, reason: "these do not enable config.sandbox: a.yaml, z.yaml",
		},
		"host cannot sandbox": {
			provider: NativeSandboxProvider{Policies: []models.SandboxConfig{enabled}, Runner: &fakeNativeRunner{reason: "install bubblewrap"}},
			status:   ProviderUnavailable, reason: "install bubblewrap",
		},
		"host support check fails": {
			provider: NativeSandboxProvider{Policies: []models.SandboxConfig{enabled}, Runner: &fakeNativeRunner{supportErr: errors.New("cli missing")}},
			status:   ProviderUnavailable, reason: "cli missing",
		},
		"default policy": {
			provider: NativeSandboxProvider{Policies: []models.SandboxConfig{enabled}, Runner: &fakeNativeRunner{supported: true}},
			status:   ProviderAvailable, claims: []Dimension{Filesystem, Network},
		},
		"writable host path withdraws filesystem": {
			provider: NativeSandboxProvider{Policies: []models.SandboxConfig{enabled, {Enabled: true, ReadwritePaths: []string{"/opt/out"}}}, Runner: &fakeNativeRunner{supported: true}},
			status:   ProviderAvailable, claims: []Dimension{Network},
		},
		"dev tool caches withdraw filesystem": {
			provider: NativeSandboxProvider{Policies: []models.SandboxConfig{{Enabled: true, AllowDevToolCaches: true}}, Runner: &fakeNativeRunner{supported: true}},
			status:   ProviderAvailable, claims: []Dimension{Network},
		},
		"local network withdraws network": {
			provider: NativeSandboxProvider{Policies: []models.SandboxConfig{{Enabled: true, AllowLocalNetwork: true}}, Runner: &fakeNativeRunner{supported: true}},
			status:   ProviderAvailable, claims: []Dimension{Filesystem},
		},
		"outbound network withdraws network": {
			provider: NativeSandboxProvider{Policies: []models.SandboxConfig{enabled, {Enabled: true, AllowOutboundNetwork: true}}, Runner: &fakeNativeRunner{supported: true}},
			status:   ProviderAvailable, claims: []Dimension{Filesystem},
		},
	} {
		t.Run(name, func(t *testing.T) {
			discovery := tc.provider.Discover(context.Background())
			require.Equal(t, tc.status, discovery.Status)
			require.Contains(t, discovery.Reason, tc.reason)
			require.Equal(t, tc.claims, claimedDimensions(discovery))
			if tc.status == ProviderAvailable {
				require.Equal(t, "Copilot CLI native sandbox", discovery.EnforcedBy)
			}
		})
	}
}

func TestNativeSandboxFilesystemProbe(t *testing.T) {
	requirePOSIX(t)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	leftovers := func(t *testing.T) []string {
		t.Helper()
		matches, err := regexpGlob(home, `^\.waza-isolation-probe-`)
		require.NoError(t, err)
		return matches
	}

	t.Run("an unsandboxed shell violates and its files are removed", func(t *testing.T) {
		provider := nativeProvider(t, &fakeNativeRunner{supported: true, run: unsandboxedShell}, models.SandboxConfig{Enabled: true})
		result := claimFor(t, provider.Discover(context.Background()), Filesystem).Probe(context.Background())
		require.Equal(t, Violated, result.Outcome, result.Detail)
		require.Empty(t, leftovers(t))
	})
	t.Run("a denying sandbox is enforced", func(t *testing.T) {
		runner := &fakeNativeRunner{supported: true, run: denyingShell}
		policy := models.SandboxConfig{Enabled: true, ReadonlyPaths: []string{"/opt/in"}}
		provider := nativeProvider(t, runner, policy)
		result := claimFor(t, provider.Discover(context.Background()), Filesystem).Probe(context.Background())
		require.Equal(t, Enforced, result.Outcome, result.Detail)
		require.Contains(t, result.Detail, home)
		require.Equal(t, []models.SandboxConfig{policy}, runner.policies, "the probe must apply the eval's own policy")
	})
	t.Run("the host check overrides a shell that reports denial", func(t *testing.T) {
		lying := func(t *testing.T, workspace, script string) (string, error) {
			_, _ = unsandboxedShell(t, workspace, script)
			return denyingShell(t, workspace, script)
		}
		provider := nativeProvider(t, &fakeNativeRunner{supported: true, run: lying}, models.SandboxConfig{Enabled: true})
		result := claimFor(t, provider.Discover(context.Background()), Filesystem).Probe(context.Background())
		require.Equal(t, Violated, result.Outcome, result.Detail)
		require.Empty(t, leftovers(t))
	})
	for name, run := range map[string]func(*testing.T, string, string) (string, error){
		"no output":    func(*testing.T, string, string) (string, error) { return "", nil },
		"runner error": func(*testing.T, string, string) (string, error) { return "", errors.New("session failed") },
		"workspace read-only": func(t *testing.T, w, s string) (string, error) {
			out, err := denyingShell(t, w, s)
			return strings.Replace(out, "inside ok", "inside fail", 1), err
		},
	} {
		t.Run(name+" is inconclusive", func(t *testing.T) {
			provider := nativeProvider(t, &fakeNativeRunner{supported: true, run: run}, models.SandboxConfig{Enabled: true})
			result := claimFor(t, provider.Discover(context.Background()), Filesystem).Probe(context.Background())
			require.Equal(t, Inconclusive, result.Outcome, result.Detail)
		})
	}
	t.Run("every policy must hold", func(t *testing.T) {
		calls := 0
		run := func(t *testing.T, w, s string) (string, error) {
			calls++
			if calls == 2 {
				return unsandboxedShell(t, w, s)
			}
			return denyingShell(t, w, s)
		}
		provider := nativeProvider(t, &fakeNativeRunner{supported: true, run: run},
			models.SandboxConfig{Enabled: true}, models.SandboxConfig{Enabled: true, GitAuth: true})
		result := claimFor(t, provider.Discover(context.Background()), Filesystem).Probe(context.Background())
		require.Equal(t, Violated, result.Outcome, result.Detail)
		require.Contains(t, result.Detail, "sandbox policy 2 of 2")
	})
}

func TestNativeSandboxNetworkProbe(t *testing.T) {
	requirePOSIX(t)
	t.Run("an unsandboxed shell connects", func(t *testing.T) {
		provider := nativeProvider(t, &fakeNativeRunner{supported: true, run: unsandboxedShell}, models.SandboxConfig{Enabled: true})
		result := claimFor(t, provider.Discover(context.Background()), Network).Probe(context.Background())
		require.Equal(t, Violated, result.Outcome, result.Detail)
	})
	t.Run("a denying sandbox is enforced", func(t *testing.T) {
		provider := nativeProvider(t, &fakeNativeRunner{supported: true, run: denyingShell}, models.SandboxConfig{Enabled: true})
		result := claimFor(t, provider.Discover(context.Background()), Network).Probe(context.Background())
		require.Equal(t, Enforced, result.Outcome, result.Detail)
		require.Contains(t, result.Detail, "loopback is not covered")
	})
	t.Run("the listener overrides a shell that reports denial", func(t *testing.T) {
		lying := func(t *testing.T, workspace, script string) (string, error) {
			_, _ = unsandboxedShell(t, workspace, script)
			return denyingShell(t, workspace, script)
		}
		provider := nativeProvider(t, &fakeNativeRunner{supported: true, run: lying}, models.SandboxConfig{Enabled: true})
		result := claimFor(t, provider.Discover(context.Background()), Network).Probe(context.Background())
		require.Equal(t, Violated, result.Outcome, result.Detail)
	})
	t.Run("no probe address is inconclusive", func(t *testing.T) {
		provider := nativeProvider(t, &fakeNativeRunner{supported: true, run: denyingShell}, models.SandboxConfig{Enabled: true})
		provider.Listen = func() (net.Listener, error) { return nil, errors.New("no interface") }
		result := claimFor(t, provider.Discover(context.Background()), Network).Probe(context.Background())
		require.Equal(t, Inconclusive, result.Outcome, result.Detail)
	})
	t.Run("missing output is inconclusive", func(t *testing.T) {
		provider := nativeProvider(t, &fakeNativeRunner{supported: true, run: func(*testing.T, string, string) (string, error) { return "WAZA end\n", nil }}, models.SandboxConfig{Enabled: true})
		result := claimFor(t, provider.Discover(context.Background()), Network).Probe(context.Background())
		require.Equal(t, Inconclusive, result.Outcome, result.Detail)
	})
}

func regexpGlob(dir, pattern string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(pattern)
	var matches []string
	for _, entry := range entries {
		if re.MatchString(entry.Name()) {
			matches = append(matches, entry.Name())
		}
	}
	return matches, nil
}
