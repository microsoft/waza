package executionisolation

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	ExitIfSubprocessProbe()
	os.Exit(m.Run())
}

var checkedAt = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func restrict(dimensions ...Dimension) Request {
	request := Request{Kind: RequestKind, SchemaVersion: Version, Dimensions: map[Dimension]Posture{}}
	for _, dimension := range dimensions {
		request.Dimensions[dimension] = Restricted
	}
	return request
}

func capability(t *testing.T, report *Report, dimension Dimension) Capability {
	t.Helper()
	for _, entry := range report.Dimensions {
		if entry.Dimension == dimension {
			return entry
		}
	}
	t.Fatalf("dimension %s missing", dimension)
	return Capability{}
}

// encodeRoundTrip proves every report the preflight emits is schema-valid.
func encodeRoundTrip(t *testing.T, report *Report) {
	t.Helper()
	data, err := report.Encode()
	require.NoError(t, err)
	parsed, err := ParseReport(data)
	require.NoError(t, err)
	require.Equal(t, report.Decision, parsed.Decision)
}

type fakeProvider struct {
	name      string
	discovery Discovery
}

func (p fakeProvider) Name() string                       { return p.name }
func (p fakeProvider) Discover(context.Context) Discovery { return p.discovery }

func fixed(outcome Outcome) func(context.Context) ProbeResult {
	return func(context.Context) ProbeResult { return ProbeResult{outcome, "fixture " + string(outcome)} }
}

func TestDefaultProvidersClaimNothingWithoutHostDeclaration(t *testing.T) {
	report := Preflight(context.Background(), restrict(Dimensions...), DefaultProviders(nil, t.TempDir()), checkedAt)
	require.Equal(t, Rejected, report.Decision)
	require.Equal(t, []ProviderReport{
		{Name: NativeSandboxProviderName, Status: ProviderAbsent, Reason: "Copilot-native sandbox integration (#544) is not present in this build; it claims no boundary"},
		{Name: HostProviderName, Status: ProviderAbsent, Reason: "no host isolation declaration was supplied"},
	}, report.Providers)
	for _, dimension := range Dimensions {
		entry := capability(t, report, dimension)
		require.False(t, entry.Available)
		require.Equal(t, EffectiveUnavailable, entry.Effective)
		require.Equal(t, CodeNoProvider, entry.Diagnostic.Code)
	}
	err := report.Admit()
	require.True(t, IsRejected(err))
	require.ErrorContains(t, err, "filesystem (no_provider)")
	encodeRoundTrip(t, report)
}

func TestUnrestrictedRequestPreservesExistingBehavior(t *testing.T) {
	report := Preflight(context.Background(), restrict(), DefaultProviders(nil, t.TempDir()), checkedAt)
	require.Equal(t, Admitted, report.Decision)
	require.NoError(t, report.Admit())
	for _, entry := range report.Dimensions {
		require.Equal(t, EffectiveUnrestricted, entry.Effective)
	}
	encodeRoundTrip(t, report)
}

func TestMatchingRequestedAvailableAndEffective(t *testing.T) {
	providers := []Provider{
		fakeProvider{"runtime-a", Discovery{Status: ProviderAvailable, EnforcedBy: "fixture runtime", Claims: []Claim{
			{Network, fixed(Enforced)}, {Filesystem, fixed(Enforced)}, {Subprocess, fixed(Violated)}, {RemoteTools, nil},
		}}},
		fakeProvider{"runtime-b", Discovery{Status: ProviderAvailable, EnforcedBy: "second runtime", Claims: []Claim{
			{Filesystem, fixed(Violated)}, {Environment, fixed(Inconclusive)},
		}}},
		fakeProvider{"runtime-c", Discovery{Status: ProviderUnavailable, Reason: "fixture unavailable", Claims: []Claim{{Environment, fixed(Enforced)}}}},
	}
	report := Preflight(context.Background(), restrict(Network), providers, checkedAt)
	require.Equal(t, Admitted, report.Decision)

	network := capability(t, report, Network)
	require.True(t, network.Available)
	require.Equal(t, EffectiveRestricted, network.Effective)
	require.Equal(t, "runtime-a", network.EnforcementSource)

	// Any violating probe outweighs another provider's enforced probe.
	filesystem := capability(t, report, Filesystem)
	require.False(t, filesystem.Available)
	require.Equal(t, EffectiveUnrestricted, filesystem.Effective)
	require.Equal(t, CodeProbeViolated, filesystem.Diagnostic.Code)
	require.Len(t, filesystem.Probes, 2)

	// A claim without a probe is never advertised; an unavailable provider's claims are not probed.
	require.Equal(t, CodeProbeInconclusive, capability(t, report, RemoteTools).Diagnostic.Code)
	environment := capability(t, report, Environment)
	require.Equal(t, CodeProbeInconclusive, environment.Diagnostic.Code)
	require.Len(t, environment.Probes, 1)
	encodeRoundTrip(t, report)

	rejected := Preflight(context.Background(), restrict(Network, Subprocess, Environment, RemoteTools), providers, checkedAt)
	require.Equal(t, Rejected, rejected.Decision)
	require.Equal(t, EffectiveUnavailable, capability(t, rejected, Subprocess).Effective)
	require.Equal(t, CodeProbeViolated, capability(t, rejected, Subprocess).Diagnostic.Code)
	require.Equal(t, EffectiveRestricted, capability(t, rejected, Network).Effective)
	encodeRoundTrip(t, rejected)
}

// hostReport runs the host provider for one declaration against host hooks.
func hostReport(t *testing.T, host Host, declaration HostDimensions, request Request) *Report {
	t.Helper()
	if host.Workspace == "" {
		host.Workspace = t.TempDir()
	}
	provider := HostProvider{Declaration: &HostDeclaration{Kind: HostKind, SchemaVersion: Version, EnforcedBy: "fixture host", Dimensions: declaration}, Host: host}
	report := Preflight(context.Background(), request, []Provider{provider}, checkedAt)
	encodeRoundTrip(t, report)
	return report
}

// Boundary-violation conformance: each probe must detect a real violation on
// an unrestricted host, and report enforcement only when the attempt is denied.
func TestFilesystemBoundaryConformance(t *testing.T) {
	// A private temp root makes ordinary test directories count as outside
	// the shared temporary area that legitimately stays writable.
	tempRoot := t.TempDir()
	workspace, err := os.MkdirTemp(tempRoot, "ws-")
	require.NoError(t, err)
	outside := t.TempDir()
	declaration := HostDimensions{Filesystem: &FilesystemSpec{OutsidePaths: []string{outside}}}

	report := hostReport(t, Host{Workspace: workspace, TempRoot: tempRoot}, declaration, restrict(Filesystem))
	entry := capability(t, report, Filesystem)
	require.Equal(t, Rejected, report.Decision)
	require.Equal(t, Violated, entry.Probes[0].Outcome)
	leftovers, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, leftovers, "probe files must be removed")

	onlyOutsideWritable := func(dir, pattern string) (*os.File, error) {
		if dir == workspace || dir == outside {
			return os.CreateTemp(dir, pattern)
		}
		return nil, &fs.PathError{Op: "open", Path: dir, Err: syscall.EROFS}
	}
	report = hostReport(t, Host{Workspace: workspace, TempRoot: tempRoot, CreateTemp: onlyOutsideWritable}, declaration, restrict(Filesystem))
	entry = capability(t, report, Filesystem)
	require.Equal(t, Violated, entry.Probes[0].Outcome)
	require.Contains(t, entry.Probes[0].Detail, outside, "declared paths add to the defaults")

	var probed []string
	deny := func(dir, pattern string) (*os.File, error) {
		probed = append(probed, dir)
		if dir == workspace {
			return os.CreateTemp(dir, pattern)
		}
		return nil, &fs.PathError{Op: "open", Path: dir, Err: syscall.EROFS}
	}
	report = hostReport(t, Host{Workspace: workspace, TempRoot: tempRoot, CreateTemp: deny}, declaration, restrict(Filesystem))
	entry = capability(t, report, Filesystem)
	require.Equal(t, Admitted, report.Decision)
	require.Equal(t, EffectiveRestricted, entry.Effective)
	require.Equal(t, HostProviderName, entry.EnforcementSource)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.Contains(t, probed, cwd, "the current directory is always probed")
	require.NotContains(t, probed, tempRoot, "the shared temporary area is writable by design")

	readOnlyWorkspace := func(string, string) (*os.File, error) { return nil, fs.ErrPermission }
	report = hostReport(t, Host{Workspace: workspace, TempRoot: tempRoot, CreateTemp: readOnlyWorkspace}, declaration, restrict(Filesystem))
	require.Equal(t, CodeProbeInconclusive, capability(t, report, Filesystem).Diagnostic.Code)

	missing := HostDimensions{Filesystem: &FilesystemSpec{OutsidePaths: []string{filepath.Join(outside, "missing")}}}
	denyOrMissing := func(dir, pattern string) (*os.File, error) {
		if dir == filepath.Join(outside, "missing") {
			return nil, &fs.PathError{Op: "open", Path: dir, Err: fs.ErrNotExist}
		}
		return deny(dir, pattern)
	}
	report = hostReport(t, Host{Workspace: workspace, TempRoot: tempRoot, CreateTemp: denyOrMissing}, missing, restrict(Filesystem))
	require.Equal(t, Admitted, report.Decision, "a missing declared path is skipped")

	// The current directory of an unrestricted developer or CI host is writable.
	report = hostReport(t, Host{Workspace: workspace, TempRoot: tempRoot}, HostDimensions{Filesystem: &FilesystemSpec{}}, restrict(Filesystem))
	require.Equal(t, Rejected, report.Decision)
}
func TestEnvironmentBoundaryConformance(t *testing.T) {
	t.Setenv("WAZA_ISOLATION_CANARY_TOKEN", "canary-value-must-not-leak")
	t.Setenv("WAZA_DECLARED_CANARY", "declared-value")
	declaration := HostDimensions{Environment: &EnvironmentSpec{SecretNames: []string{"WAZA_DECLARED_CANARY"}}}

	report := hostReport(t, Host{}, declaration, restrict(Environment))
	entry := capability(t, report, Environment)
	require.Equal(t, Rejected, report.Decision)
	require.Contains(t, entry.Probes[0].Detail, "WAZA_ISOLATION_CANARY_TOKEN")
	require.Contains(t, entry.Probes[0].Detail, "WAZA_DECLARED_CANARY")
	data, err := report.Encode()
	require.NoError(t, err)
	require.NotContains(t, string(data), "canary-value-must-not-leak")
	require.NotContains(t, string(data), "declared-value")

	scrubbed := func() []string { return []string{"PATH=/usr/bin", "HOME=/home/runner", "KEYBOARD=us"} }
	for name, spec := range map[string]*EnvironmentSpec{
		"declared name that never existed": {SecretNames: []string{"WAZA_NONEXISTENT_CANARY"}},
		"no declared names":                {},
	} {
		t.Run(name, func(t *testing.T) {
			report := hostReport(t, Host{Environ: scrubbed}, HostDimensions{Environment: spec}, restrict(Environment))
			require.Equal(t, Rejected, report.Decision)
			require.Equal(t, CodeProbeInconclusive, capability(t, report, Environment).Diagnostic.Code,
				"absence cannot prove a secret was withheld rather than never supplied")
		})
	}
}

func listen(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return listener.Addr().String()
}

func refusedTarget(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}

func endpointConformance(t *testing.T, dimension Dimension, declare func(targets []string) HostDimensions) {
	reachable := listen(t)
	report := hostReport(t, Host{}, declare([]string{reachable}), restrict(dimension))
	entry := capability(t, report, dimension)
	require.Equal(t, Rejected, report.Decision)
	require.Equal(t, Violated, entry.Probes[0].Outcome)
	require.Contains(t, entry.Probes[0].Detail, reachable)

	refused := refusedTarget(t)
	online := func() ([]string, error) { return []string{"eth0"}, nil }
	offline := func() ([]string, error) { return nil, nil }
	report = hostReport(t, Host{ExternalInterfaces: offline}, declare([]string{refused, reachable}), restrict(dimension))
	require.Equal(t, CodeProbeViolated, capability(t, report, dimension).Diagnostic.Code, "a later reachable target must still be reported")

	failures := map[string]error{
		"refused":          &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)},
		"timeout":          context.DeadlineExceeded,
		"no resolver":      &net.DNSError{Err: "server misbehaving", Name: "tools.example.test"},
		"host unreachable": &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)},
		"no route":         &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)},
		"denied":           &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EPERM)},
	}
	for name, failure := range failures {
		t.Run(name, func(t *testing.T) {
			dial := func(context.Context, string, string) (net.Conn, error) { return nil, failure }
			report := hostReport(t, Host{Dial: dial, ExternalInterfaces: online}, declare([]string{"tools.example.test:443"}), restrict(dimension))
			require.Equal(t, CodeProbeInconclusive, capability(t, report, dimension).Diagnostic.Code,
				"a failed sampled target does not prove the policy while other interfaces are up")

			report = hostReport(t, Host{Dial: dial, ExternalInterfaces: offline}, declare([]string{"tools.example.test:443"}), restrict(dimension))
			require.Equal(t, Admitted, report.Decision)
			require.Equal(t, EffectiveRestricted, capability(t, report, dimension).Effective)
		})
	}

	undeclaredLoopback := listen(t)
	report = hostReport(t, Host{ExternalInterfaces: offline}, declare([]string{refused}), restrict(dimension))
	entry = capability(t, report, dimension)
	require.Equal(t, EffectiveRestricted, entry.Effective, "the boundary is off-host traffic; loopback stays inside it")
	require.Contains(t, entry.Probes[0].Detail, "loopback is not covered")
	conn, err := net.Dial("tcp", undeclaredLoopback)
	require.NoError(t, err, "an undeclared loopback service remains reachable under an admitted restriction")
	_ = conn.Close()

	report = hostReport(t, Host{ExternalInterfaces: online}, declare([]string{refused}), restrict(dimension))
	require.Equal(t, Rejected, report.Decision, "a refused target on a connected host cannot prove a boundary")
	broken := func() ([]string, error) { return nil, errors.New("netlink unavailable") }
	report = hostReport(t, Host{ExternalInterfaces: broken}, declare([]string{refused}), restrict(dimension))
	require.Equal(t, CodeProbeInconclusive, capability(t, report, dimension).Diagnostic.Code)
}

func TestNetworkBoundaryConformance(t *testing.T) {
	endpointConformance(t, Network, func(targets []string) HostDimensions {
		return HostDimensions{Network: &EndpointSpec{Targets: targets}}
	})
}

func TestRemoteToolBoundaryConformance(t *testing.T) {
	endpointConformance(t, RemoteTools, func(targets []string) HostDimensions {
		return HostDimensions{RemoteTools: &EndpointSpec{Targets: targets}}
	})
}

func TestSubprocessBoundaryConformance(t *testing.T) {
	declaration := HostDimensions{Subprocess: &struct{}{}}
	report := hostReport(t, Host{}, declaration, restrict(Subprocess))
	require.Equal(t, Rejected, report.Decision)
	require.Equal(t, Violated, capability(t, report, Subprocess).Probes[0].Outcome, "the real child process must start on an unrestricted host")

	for err, outcome := range map[error]Outcome{
		&os.PathError{Op: "fork/exec", Path: "waza", Err: syscall.EPERM}:  Inconclusive,
		&os.PathError{Op: "fork/exec", Path: "waza", Err: syscall.EACCES}: Inconclusive,
		&os.PathError{Op: "fork/exec", Path: "waza", Err: syscall.ENOENT}: Inconclusive,
		errors.New("resource temporarily unavailable"):                    Inconclusive,
	} {
		var started []string
		spawn := func(_ context.Context, name string, _ ...string) error {
			started = append(started, name)
			return err
		}
		report := hostReport(t, Host{Spawn: spawn}, declaration, restrict(Subprocess))
		require.Equal(t, outcome, capability(t, report, Subprocess).Probes[0].Outcome, err.Error())
		require.Len(t, started, 2, "both the Waza executable and the shell are attempted")
	}

	bothSampledDenied := func(_ context.Context, name string, _ ...string) error {
		return &os.PathError{Op: "fork/exec", Path: name, Err: syscall.EACCES}
	}
	report = hostReport(t, Host{Spawn: bothSampledDenied}, declaration, restrict(Subprocess))
	require.Equal(t, Rejected, report.Decision, "denying both sampled executables does not prove another child cannot start")
	require.Equal(t, CodeProbeInconclusive, capability(t, report, Subprocess).Diagnostic.Code)

	self, err := os.Executable()
	require.NoError(t, err)
	onlySelfDenied := func(_ context.Context, name string, _ ...string) error {
		if name == self {
			return &os.PathError{Op: "fork/exec", Path: name, Err: syscall.EACCES}
		}
		return nil
	}
	report = hostReport(t, Host{Spawn: onlySelfDenied}, declaration, restrict(Subprocess))
	require.Equal(t, Violated, capability(t, report, Subprocess).Probes[0].Outcome, "an executable-specific denial is not a process-wide boundary")
}

func TestHostProviderWithoutWorkspaceIsUnavailable(t *testing.T) {
	provider := HostProvider{Declaration: &HostDeclaration{EnforcedBy: "fixture", Dimensions: HostDimensions{Subprocess: &struct{}{}}}}
	report := Preflight(context.Background(), restrict(Subprocess), []Provider{provider}, checkedAt)
	require.Equal(t, ProviderUnavailable, report.Providers[0].Status)
	require.Equal(t, CodeNoProvider, capability(t, report, Subprocess).Diagnostic.Code)
	encodeRoundTrip(t, report)
}

func TestParseRejectsInvalidDocuments(t *testing.T) {
	request, err := ParseRequest([]byte(`{"kind":"waza.execution-isolation-request","schema_version":"1.0","dimensions":{"network":"restricted"}}`))
	require.NoError(t, err)
	require.Equal(t, Restricted, request.Posture(Network))
	require.Equal(t, Unrestricted, request.Posture(Filesystem))

	host, err := ParseHostDeclaration([]byte(`{"kind":"waza.execution-isolation-host","schema_version":"1.0","enforced_by":"docker --network none","dimensions":{"network":{"targets":["example.com:443"]},"subprocess":{}}}`))
	require.NoError(t, err)
	require.Equal(t, []string{"example.com:443"}, host.Dimensions.Network.Targets)
	require.NotNil(t, host.Dimensions.Subprocess)

	for name, document := range map[string]string{
		"unknown dimension":       `{"kind":"waza.execution-isolation-request","schema_version":"1.0","dimensions":{"gpu":"restricted"}}`,
		"unknown posture":         `{"kind":"waza.execution-isolation-request","schema_version":"1.0","dimensions":{"network":"partial"}}`,
		"network without targets": `{"kind":"waza.execution-isolation-host","schema_version":"1.0","enforced_by":"x","dimensions":{"network":{}}}`,
		"empty declaration":       `{"kind":"waza.execution-isolation-host","schema_version":"1.0","enforced_by":"x","dimensions":{}}`,
		"duplicate key":           `{"kind":"waza.execution-isolation-request","kind":"waza.execution-isolation-request","schema_version":"1.0","dimensions":{}}`,
		"wrong kind":              `{"kind":"waza.execution-isolation-host","schema_version":"1.0","enforced_by":"x","dimensions":{"subprocess":{}}}`,
	} {
		_, err := ParseRequest([]byte(document))
		if name == "network without targets" || name == "empty declaration" {
			_, err = ParseHostDeclaration([]byte(document))
		}
		require.ErrorIs(t, err, ErrInvalid, name)
	}
}

// The report schema refuses claims a preflight could never emit.
func TestReportSchemaRejectsOverclaims(t *testing.T) {
	providers := []Provider{fakeProvider{"runtime", Discovery{Status: ProviderAvailable, EnforcedBy: "fixture", Claims: []Claim{{Network, fixed(Enforced)}}}}}
	base, err := Preflight(context.Background(), restrict(Filesystem), providers, checkedAt).Encode()
	require.NoError(t, err)
	for name, mutate := range map[string]func(map[string]any){
		"admitted with an unavailable boundary": func(doc map[string]any) { doc["decision"] = "admitted" },
		"restricted without availability": func(doc map[string]any) {
			dimensionAt(t, doc, 0)["effective"] = "restricted"
		},
		"available without enforcement source": func(doc map[string]any) {
			delete(dimensionAt(t, doc, 2), "enforcement_source")
		},
		"available yet unrestricted": func(doc map[string]any) {
			dimensionAt(t, doc, 2)["effective"] = "unrestricted"
		},
		"missing dimension": func(doc map[string]any) { doc["dimensions"] = dimensionList(t, doc)[1:] },
		"enforced without a passing probe": func(doc map[string]any) {
			probes, ok := dimensionAt(t, doc, 2)["probes"].([]any)
			require.True(t, ok)
			probe, ok := probes[0].(map[string]any)
			require.True(t, ok)
			probe["outcome"] = "inconclusive"
		},
		"enforced from an unknown provider": func(doc map[string]any) {
			dimensionAt(t, doc, 2)["enforcement_source"] = "forged"
		},
		"dimensions out of order": func(doc map[string]any) {
			list := dimensionList(t, doc)
			list[0], list[1] = list[1], list[0]
		},
		"diagnostic contradicts probes": func(doc map[string]any) {
			dimensionAt(t, doc, 1)["diagnostic"] = map[string]any{"code": "probe_violated", "message": "forged"}
		},
	} {
		var doc map[string]any
		require.NoError(t, json.Unmarshal(base, &doc))
		mutate(doc)
		data, err := json.Marshal(doc)
		require.NoError(t, err)
		_, err = ParseReport(data)
		require.ErrorIs(t, err, ErrInvalid, name)
	}
	require.True(t, strings.HasSuffix(string(base), "\n"))
}

func dimensionList(t *testing.T, doc map[string]any) []any {
	t.Helper()
	list, ok := doc["dimensions"].([]any)
	require.True(t, ok)
	return list
}

func dimensionAt(t *testing.T, doc map[string]any, index int) map[string]any {
	t.Helper()
	entry, ok := dimensionList(t, doc)[index].(map[string]any)
	require.True(t, ok)
	return entry
}
