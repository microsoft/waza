package usagetelemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCollector struct {
	server *httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	status []int
}

func newFakeCollector(t *testing.T, status ...int) *fakeCollector {
	t.Helper()
	fc := &fakeCollector{status: status}
	fc.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fc.mu.Lock()
		fc.bodies = append(fc.bodies, body)
		code := http.StatusAccepted
		if len(fc.status) > 0 {
			code, fc.status = fc.status[0], fc.status[1:]
		}
		fc.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(fc.server.Close)
	return fc
}

func (fc *fakeCollector) events(t *testing.T) []map[string]any {
	t.Helper()
	fc.mu.Lock()
	defer fc.mu.Unlock()
	out := make([]map[string]any, 0, len(fc.bodies))
	for _, body := range fc.bodies {
		var event map[string]any
		require.NoError(t, json.Unmarshal(body, &event))
		out = append(out, event)
	}
	return out
}

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// activate points the client at collector (or "" for the shipped inert build)
// with default-on collection so policy denials can be exercised.
func activate(t *testing.T, collector *fakeCollector, ciOn bool) {
	t.Helper()
	target, client := "", (*http.Client)(nil)
	if collector != nil {
		target, client = collector.server.URL, collector.server.Client()
	}
	t.Cleanup(OverrideActivationForTesting(target, client, true, ciOn))
}

func settingsPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), ".waza", "telemetry.json")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestShippedPolicy(t *testing.T) {
	assert.Equal(t, ApprovedEndpoint, shippedEndpoint)
	assert.NotNil(t, approvedEndpoint(shippedEndpoint), "the shipped endpoint must be a credential-free https URL")
	assert.True(t, shippedDefaultOn, "collection is on by default for developers")
	assert.False(t, shippedCIOn, "CI is never collected")
	assert.False(t, shippedDevOn, "development builds are never collected")
}

func TestLocalBuildDefaultsAreNotReleases(t *testing.T) {
	for file, pattern := range map[string]string{
		"../../Makefile": `(?m)^VERSION\?=(\S+)$`,
		"../../build.sh": `(?m)^VERSION="\$\{VERSION:-([^}]+)\}"$`,
	} {
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		m := regexp.MustCompile(pattern).FindSubmatch(data)
		require.NotNil(t, m, "%s must declare a default VERSION", file)
		assert.False(t, releaseBuild(string(m[1])), "%s default %q would collect telemetry", file, m[1])
	}
}

func TestOnlyReleaseBuildsCollect(t *testing.T) {
	restore := OverrideActivationForTesting(newFakeCollector(t).server.URL, nil, true, false)
	t.Cleanup(restore)
	devBuildCollectionOn = false
	for version, enabled := range map[string]bool{
		"0.42.0": true, "1.2.3": true, "dev": false, "": false, "v0.42.0": false,
		"0.41.0-449-g5ff2e64": false, "0.42.0+meta": false, "garbage": false,
	} {
		p := Resolve(Inputs{Getenv: env(nil), SettingsPath: settingsPath(t), WorkDir: t.TempDir(), Version: version})
		assert.Equal(t, enabled, p.Enabled, "version %q: %v", version, p.Reasons)
		if !enabled {
			assert.Equal(t, []string{ReasonDevBuild}, p.Reasons, version)
			assert.False(t, p.Denied, version)
		}
	}
}

func TestInertBuildSendsNothing(t *testing.T) {
	activate(t, nil, false)
	path := settingsPath(t)
	p := Resolve(Inputs{Getenv: env(nil), SettingsPath: path, WorkDir: t.TempDir()})
	assert.False(t, p.Enabled)
	assert.Equal(t, []string{ReasonNoEndpoint}, p.Reasons)

	Start(p, "1.2.3")
	RecordCommand("run", CommandSuccess, time.Second)
	RecordExecution(SurfaceBenchmark, ModeMock, ExecutionPassed, 3, time.Second)
	RecordFeature(FeatureLLMJudge)
	assert.False(t, Active())
	Shutdown(ShutdownTimeout)
	assert.NoFileExists(t, path)
}

func TestNoEndpointReadsNoFiles(t *testing.T) {
	activate(t, nil, false)
	dir := t.TempDir()
	// Unreadable inputs would deny if read; with no endpoint they are not read.
	settings := filepath.Join(dir, "telemetry.json")
	writeFile(t, settings, "{not json")
	writeFile(t, filepath.Join(dir, ".waza.yaml"), "telemetry: [bad")
	p := Resolve(Inputs{Getenv: env(nil), SettingsPath: settings, WorkDir: dir})
	assert.Equal(t, []string{ReasonNoEndpoint}, p.Reasons)
	assert.False(t, p.Denied)
}

func TestPolicyResolution(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		settings string
		project  string
		enabled  bool
		denied   bool
		ci       bool
	}{
		{name: "default on with endpoint", enabled: true},
		{name: "flag", args: []string{"run", "--no-telemetry"}, denied: true},
		{name: "flag explicit true", args: []string{"--no-telemetry=true"}, denied: true},
		{name: "flag false", args: []string{"--no-telemetry=false"}, enabled: true},
		{name: "flag invalid value fails closed", args: []string{"--no-telemetry=maybe"}, denied: true},
		{name: "flag after terminator denies", args: []string{"run", "--", "--no-telemetry"}, denied: true},
		{name: "flag after terminator consumed as value denies", args: []string{"run", "x.yaml", "--output", "--", "--no-telemetry"}, denied: true},
		{name: "env 1", env: map[string]string{OptOutEnv: "1"}, denied: true},
		{name: "env true", env: map[string]string{OptOutEnv: "TRUE"}, denied: true},
		{name: "env 0", env: map[string]string{OptOutEnv: "0"}, enabled: true},
		{name: "do not track", env: map[string]string{"DO_NOT_TRACK": "1"}, denied: true},
		{name: "azd denial", env: map[string]string{"AZURE_DEV_COLLECT_TELEMETRY": "no"}, denied: true},
		{name: "user disabled", settings: `{"collection":"disabled"}`, denied: true},
		{name: "user enabled loses to env", settings: `{"collection":"enabled"}`, env: map[string]string{OptOutEnv: "1"}, denied: true},
		{name: "user enabled loses to project", settings: `{"collection":"enabled"}`, project: "telemetry:\n  disabled: true\n", denied: true},
		{name: "malformed settings", settings: `{"collection":`, denied: true},
		{name: "unknown settings field", settings: `{"collection":"enabled","extra":1}`, denied: true},
		{name: "bad collection value", settings: `{"collection":"sometimes"}`, denied: true},
		{name: "null settings", settings: `null`, denied: true},
		{name: "null collection", settings: `{"collection":null}`, denied: true},
		{name: "duplicate collection keys", settings: `{"collection":"disabled","collection":"enabled"}`, denied: true},
		{name: "non-string collection", settings: `{"collection":true}`, denied: true},
		{name: "settings trailing data", settings: `{"collection":"enabled"} {}`, denied: true},
		{name: "disabled with id is invalid", settings: `{"collection":"disabled","installationId":"5f1b3a0e-8f5b-4c39-9a51-2f1f2b9d1c3e"}`, denied: true},
		{name: "project disabled", project: "telemetry:\n  disabled: true\n", denied: true},
		{name: "project explicit false", project: "telemetry:\n  disabled: false\n", enabled: true},
		{name: "project unknown key", project: "telemetry:\n  enabled: true\n", denied: true},
		{name: "project wrong type", project: "telemetry:\n  disabled: \"yes\"\n", denied: true},
		{name: "project malformed yaml", project: "telemetry: [", denied: true},
		{name: "project null section", project: "telemetry: null\n", denied: true},
		{name: "project bare section", project: "telemetry:\n", denied: true},
		{name: "ci not approved", env: map[string]string{"GITHUB_ACTIONS": "true"}, ci: true, denied: true},
		{name: "ci false is not ci", env: map[string]string{"CI": "false"}, enabled: true},
	}
	activate(t, newFakeCollector(t), false)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "home", "telemetry.json")
			if tt.settings != "" {
				writeFile(t, path, tt.settings)
			}
			if tt.project != "" {
				writeFile(t, filepath.Join(dir, ".waza.yaml"), tt.project)
			}
			p := Resolve(Inputs{Args: tt.args, Getenv: env(tt.env), SettingsPath: path, WorkDir: dir})
			assert.Equal(t, tt.enabled, p.Enabled, "reasons: %v", p.Reasons)
			assert.Equal(t, tt.denied, p.Denied, "reasons: %v", p.Reasons)
			assert.Equal(t, tt.ci, p.CI)
		})
	}
}

func TestDefaultOffRequiresExplicitEnable(t *testing.T) {
	collector := newFakeCollector(t)
	t.Cleanup(OverrideActivationForTesting(collector.server.URL, collector.server.Client(), false, false))
	path := settingsPath(t)
	p := Resolve(Inputs{Getenv: env(nil), SettingsPath: path, WorkDir: t.TempDir()})
	assert.Equal(t, []string{ReasonNotEnabled}, p.Reasons)
	require.NoError(t, Enable(path))
	p = Resolve(Inputs{Getenv: env(nil), SettingsPath: path, WorkDir: t.TempDir()})
	assert.True(t, p.Enabled)
	s, err := LoadSettings(path)
	require.NoError(t, err)
	assert.Empty(t, s.InstallationID, "enable must not create an installation ID")
}

func TestUnapprovedEndpointsStayInert(t *testing.T) {
	for _, raw := range []string{"http://collector.example", "https://user:pw@collector.example", "not a url", "https://"} {
		assert.Nil(t, approvedEndpoint(raw), raw)
	}
}

func TestPropagateDenialAndNesting(t *testing.T) {
	t.Setenv(OptOutEnv, "")
	t.Setenv(NestedEnv, "")
	Propagate(Policy{Denied: true})
	assert.Equal(t, "1", os.Getenv(OptOutEnv))
	assert.Empty(t, os.Getenv(NestedEnv))

	t.Setenv(OptOutEnv, "")
	Propagate(Policy{Enabled: true})
	assert.Equal(t, "1", os.Getenv(NestedEnv))
	assert.Empty(t, os.Getenv(OptOutEnv))
}

func startClient(t *testing.T, getenv map[string]string, path string) Policy {
	t.Helper()
	p := Resolve(Inputs{Getenv: env(getenv), SettingsPath: path, WorkDir: t.TempDir()})
	require.True(t, p.Enabled, "reasons: %v", p.Reasons)
	Start(p, "v1.2.3-beta.1")
	t.Cleanup(func() { Shutdown(0) })
	return p
}

func TestDeliversContractEventsWithStableInstallationID(t *testing.T) {
	collector := newFakeCollector(t)
	activate(t, collector, false)
	path := settingsPath(t)
	startClient(t, nil, path)

	RecordCommand("run", CommandSuccess, 1500*time.Millisecond)
	RecordExecution(SurfaceBenchmark, ModeMock, ExecutionFailed, 7, 2*time.Minute)
	RecordFeature(FeatureLLMJudge)
	RecordFeature(FeatureLLMJudge)
	Shutdown(5 * time.Second)

	events := collector.events(t)
	require.Len(t, events, 3, "duplicate features are recorded once per process")
	s, err := LoadSettings(path)
	require.NoError(t, err)
	require.NotEmpty(t, s.InstallationID)
	for _, event := range events {
		assert.Equal(t, s.InstallationID, event["installation_id"])
		assert.Equal(t, "1.2.3", event["waza_version"])
		assert.Equal(t, false, event["ci"])
	}
	assert.Equal(t, "run", events[0]["command"])
	assert.Equal(t, "1s_10s", events[0]["duration_bucket"])
	assert.Equal(t, "6_20", events[1]["task_count_bucket"])
	assert.Equal(t, "llm_judge", events[2]["feature"])
}

func TestCIEventsCarryNoInstallationID(t *testing.T) {
	collector := newFakeCollector(t)
	activate(t, collector, true)
	path := settingsPath(t)
	startClient(t, map[string]string{"GITHUB_ACTIONS": "true"}, path)
	RecordCommand("run", CommandSuccess, time.Second)
	Shutdown(5 * time.Second)

	events := collector.events(t)
	require.Len(t, events, 1)
	assert.Equal(t, true, events[0]["ci"])
	assert.NotContains(t, events[0], "installation_id")
	assert.NoFileExists(t, path, "CI must not create an installation identifier")
}

func TestRetryReusesEventID(t *testing.T) {
	collector := newFakeCollector(t, http.StatusServiceUnavailable, http.StatusAccepted)
	activate(t, collector, false)
	startClient(t, nil, settingsPath(t))
	RecordFeature(FeatureDashboard)
	Shutdown(5 * time.Second)

	events := collector.events(t)
	require.Len(t, events, 2)
	assert.Equal(t, events[0]["event_id"], events[1]["event_id"])
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	collector := newFakeCollector(t, http.StatusBadRequest)
	activate(t, collector, false)
	startClient(t, nil, settingsPath(t))
	RecordFeature(FeatureDashboard)
	Shutdown(5 * time.Second)
	assert.Len(t, collector.events(t), 1)
}

func TestOfflineShutdownIsBounded(t *testing.T) {
	collector := newFakeCollector(t)
	activate(t, collector, false)
	startClient(t, nil, settingsPath(t))
	collector.server.Close()
	for range 10 {
		RecordExecution(SurfaceRunnerUnit, ModeLive, ExecutionPassed, 1, time.Second)
	}
	started := time.Now()
	Shutdown(200 * time.Millisecond)
	assert.Less(t, time.Since(started), 2*time.Second)
}

func TestSlowCollectorIsCutOffAtDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	t.Cleanup(OverrideActivationForTesting(server.URL, server.Client(), true, false))
	startClient(t, nil, settingsPath(t))
	RecordFeature(FeatureMCPServer)
	started := time.Now()
	Shutdown(100 * time.Millisecond)
	assert.Less(t, time.Since(started), time.Second)
}

func TestPeriodicDeliveryAndQueueBound(t *testing.T) {
	collector := newFakeCollector(t)
	activate(t, collector, false)
	startClient(t, nil, settingsPath(t))
	for range maxQueuedEvents {
		RecordExecution(SurfaceRunnerUnit, ModeMock, ExecutionPassed, 1, time.Second)
	}
	require.Eventually(t, func() bool { return len(collector.events(t)) == maxQueuedEvents }, 5*time.Second, 20*time.Millisecond)
	RecordExecution(SurfaceRunnerUnit, ModeMock, ExecutionPassed, 1, time.Second)
	require.Eventually(t, func() bool { return len(collector.events(t)) == maxQueuedEvents+1 }, 5*time.Second, 20*time.Millisecond)
	Shutdown(time.Second)
}

func TestBlockedPeriodicDeliveryIsBoundedAndCancelled(t *testing.T) {
	entered := make(chan struct{}, 1)
	exited := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
		close(exited)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	t.Cleanup(OverrideActivationForTesting(server.URL, server.Client(), true, false))
	startClient(t, nil, settingsPath(t))
	RecordExecution(SurfaceRunnerUnit, ModeMock, ExecutionPassed, 1, time.Second)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("periodic delivery did not start")
	}
	for range maxQueuedEvents * 2 {
		RecordExecution(SurfaceRunnerUnit, ModeMock, ExecutionPassed, 1, time.Second)
	}
	c := current()
	c.mu.Lock()
	assert.Len(t, c.queue, maxQueuedEvents)
	c.mu.Unlock()
	started := time.Now()
	Shutdown(100 * time.Millisecond)
	assert.Less(t, time.Since(started), time.Second)
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after shutdown")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("request was not canceled")
	}
}

func TestConcurrentRecordAndShutdown(t *testing.T) {
	collector := newFakeCollector(t)
	activate(t, collector, false)
	startClient(t, nil, settingsPath(t))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				RecordExecution(SurfaceRunnerUnit, ModeMock, ExecutionPassed, 1, time.Second)
			}
		})
	}
	Shutdown(100 * time.Millisecond)
	wg.Wait()
}

func TestDeniedPolicyMakesNoRequestsOrIdentifier(t *testing.T) {
	collector := newFakeCollector(t)
	activate(t, collector, false)
	for name, getenv := range map[string]map[string]string{
		"env":  {OptOutEnv: "1"},
		"host": {"DO_NOT_TRACK": "1"},
		"ci":   {"CI": "1"},
	} {
		t.Run(name, func(t *testing.T) {
			path := settingsPath(t)
			p := Resolve(Inputs{Getenv: env(getenv), SettingsPath: path, WorkDir: t.TempDir()})
			require.False(t, p.Enabled)
			Start(p, "1.0.0")
			RecordCommand("run", CommandSuccess, time.Second)
			RecordFeature(FeatureHTTPAPI)
			Shutdown(time.Second)
			assert.NoFileExists(t, path)
		})
	}
	assert.Empty(t, collector.events(t))
}

func TestDisableDiscardsPendingAndDeletesID(t *testing.T) {
	collector := newFakeCollector(t)
	activate(t, collector, false)
	path := settingsPath(t)
	startClient(t, nil, path)
	RecordFeature(FeatureHTTPAPI)
	s, err := LoadSettings(path)
	require.NoError(t, err)
	require.NotEmpty(t, s.InstallationID)

	require.NoError(t, Disable(path))
	assert.False(t, Active())
	Shutdown(time.Second)
	assert.Empty(t, collector.events(t), "disabling must not send pending events")

	s, err = LoadSettings(path)
	require.NoError(t, err)
	assert.Equal(t, Settings{Collection: CollectionDisabled}, s)
	p := Resolve(Inputs{Getenv: env(nil), SettingsPath: path, WorkDir: t.TempDir()})
	assert.True(t, p.Denied)
}

func TestOtherProcessDisableOrResetDropsQueuedEvents(t *testing.T) {
	collector := newFakeCollector(t)
	activate(t, collector, false)
	for name, change := range map[string]func(string) error{
		"disable":  func(path string) error { return SaveSettings(path, Settings{Collection: CollectionDisabled}) },
		"reset-id": ResetID,
		"corrupt":  func(path string) error { return os.WriteFile(path, []byte("{"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			path := settingsPath(t)
			startClient(t, nil, path)
			RecordFeature(FeatureHTTPAPI)
			require.NoError(t, change(path), "simulates another waza process")
			Shutdown(time.Second)
		})
	}
	assert.Empty(t, collector.events(t))
}

func TestInstallationIDNeverOverwritesConcurrentDisable(t *testing.T) {
	collector := newFakeCollector(t)
	activate(t, collector, false)
	path := settingsPath(t)
	startClient(t, nil, path)
	require.NoError(t, SaveSettings(path, Settings{Collection: CollectionDisabled}))
	RecordFeature(FeatureHTTPAPI)
	Shutdown(time.Second)

	s, err := LoadSettings(path)
	require.NoError(t, err)
	assert.Equal(t, Settings{Collection: CollectionDisabled}, s)
	assert.Empty(t, collector.events(t))
}

func TestSettingsLockIsNeverBypassed(t *testing.T) {
	path := settingsPath(t)
	writeFile(t, path+".lock", "")
	assert.ErrorIs(t, Enable(path), errSettingsLocked, "a live lock holder is never overwritten")
	assert.NoFileExists(t, path)

	old := time.Now().Add(-2 * staleLockAge)
	require.NoError(t, os.Chtimes(path+".lock", old, old))
	require.NoError(t, Disable(path), "an abandoned lock is broken, not bypassed")
	s, err := LoadSettings(path)
	require.NoError(t, err)
	assert.Equal(t, CollectionDisabled, s.Collection)
	assert.NoFileExists(t, path+".lock")
}

func TestPermissionIsRecheckedBeforeEveryRequest(t *testing.T) {
	for name, change := range map[string]func(string) error{
		"disable":  func(path string) error { return SaveSettings(path, Settings{Collection: CollectionDisabled}) },
		"reset-id": ResetID,
	} {
		t.Run(name, func(t *testing.T) {
			path := settingsPath(t)
			var mu sync.Mutex
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				requests++
				mu.Unlock()
				require.NoError(t, change(path), "another process withdraws permission mid-batch")
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			t.Cleanup(server.Close)
			t.Cleanup(OverrideActivationForTesting(server.URL, server.Client(), true, false))
			startClient(t, nil, path)
			RecordFeature(FeatureHTTPAPI)
			RecordFeature(FeatureDashboard)
			c := current()
			select {
			case <-c.done:
			case <-time.After(5 * time.Second):
				t.Fatal("periodic delivery did not stop after permission withdrawal")
			}
			Shutdown(5 * time.Second)
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, 1, requests, "no retry or later event after permission changes")
		})
	}
}

func TestResetIDAndInvalidSettings(t *testing.T) {
	path := settingsPath(t)
	writeFile(t, path, `{"collection":"enabled","installationId":"5f1b3a0e-8f5b-4c39-9a51-2f1f2b9d1c3e"}`)
	require.NoError(t, ResetID(path))
	s, err := LoadSettings(path)
	require.NoError(t, err)
	assert.Equal(t, Settings{Collection: CollectionEnabled}, s)

	writeFile(t, path, `{"installationId":"not-a-uuid"}`)
	assert.Error(t, ResetID(path))
	writeFile(t, path, strings.Repeat(" ", maxSettingsBytes+1))
	_, err = LoadSettings(path)
	assert.Error(t, err)
}

func TestHostileAndAmbientDataNeverSent(t *testing.T) {
	collector := newFakeCollector(t)
	activate(t, collector, false)
	hostile := `C:\Users\secret\prompt.txt "; DROP https://evil.example?token=abc`
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "secret.attr="+hostile)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://otel.example")
	startClient(t, map[string]string{"OTEL_SERVICE_NAME": hostile}, settingsPath(t))

	RecordCommand(hostile, CommandSuccess, time.Second)
	RecordExecution(hostile, ModeLive, ExecutionPassed, 1, time.Second)
	RecordFeature(hostile)
	Shutdown(5 * time.Second)

	events := collector.events(t)
	require.Len(t, events, 1, "only the canonicalized command is valid; unapproved values are dropped")
	assert.Equal(t, "unknown", events[0]["command"])
	collector.mu.Lock()
	defer collector.mu.Unlock()
	for _, body := range collector.bodies {
		for _, leak := range []string{"secret", "evil", "otel", "token", "prompt"} {
			assert.NotContains(t, strings.ToLower(string(body)), leak)
		}
	}
}

func TestContractRejectsUnapprovedEvents(t *testing.T) {
	valid := map[string]any{
		"schema_version": "1.0", "event_id": "5f1b3a0e-8f5b-4c39-9a51-2f1f2b9d1c3e", "event": "feature_used",
		"waza_version": "0.42.0", "os": "linux", "arch": "amd64", "distribution": "standalone", "ci": false,
		"installation_id": "0b8c8a3e-1d2f-4a6b-8c9d-0e1f2a3b4c5d", "feature": "dashboard",
	}
	encode := func(mutate func(map[string]any)) []byte {
		event := map[string]any{}
		for k, v := range valid {
			event[k] = v
		}
		mutate(event)
		body, err := json.Marshal(event)
		require.NoError(t, err)
		return body
	}
	require.NoError(t, ValidateEvent(encode(func(map[string]any) {})))

	rejects := map[string]func(map[string]any){
		"extra field":           func(e map[string]any) { e["prompt"] = "hi" },
		"field from other kind": func(e map[string]any) { e["command"] = "run" },
		"bad feature":           func(e map[string]any) { e["feature"] = "custom" },
		"bad os":                func(e map[string]any) { e["os"] = "plan9" },
		"raw version":           func(e map[string]any) { e["waza_version"] = "1.2.3-beta+sha.abc" },
		"non v4 event id":       func(e map[string]any) { e["event_id"] = "5f1b3a0e-8f5b-1c39-9a51-2f1f2b9d1c3e" },
		"missing installation":  func(e map[string]any) { delete(e, "installation_id") },
		"ci with installation":  func(e map[string]any) { e["ci"] = true },
		"missing event id":      func(e map[string]any) { delete(e, "event_id") },
		"oversized":             func(e map[string]any) { e["pad"] = strings.Repeat("x", MaxEventBytes) },
	}
	for name, mutate := range rejects {
		assert.Error(t, ValidateEvent(encode(mutate)), name)
	}
}

func TestExecutionOutcomeAndBuckets(t *testing.T) {
	ctx := t.Context()
	assert.Equal(t, ExecutionPassed, ExecutionOutcome(ctx, nil, 0, 0))
	assert.Equal(t, ExecutionFailed, ExecutionOutcome(ctx, nil, 1, 0))
	assert.Equal(t, ExecutionError, ExecutionOutcome(ctx, nil, 1, 1))
	assert.Equal(t, ExecutionError, ExecutionOutcome(ctx, io.EOF, 0, 0))
	assert.Equal(t, ExecutionCancelled, ExecutionOutcome(ctx, context.Canceled, 0, 0))
	assert.Equal(t, ModeMock, ExecutionMode(" Mock "))
	assert.Equal(t, ModeLive, ExecutionMode("copilot-sdk"))
	assert.Equal(t, "gt_100", CountBucket(101))
	assert.Equal(t, "ge_1h", DurationBucket(2*time.Hour))
	assert.Equal(t, "other", normalizeVersion("not-a-version"))
	assert.Equal(t, "dev", normalizeVersion(""))
}
