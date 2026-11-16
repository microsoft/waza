package usagetelemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/google/uuid"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	// MaxEventBytes is the contract limit for one serialized event.
	MaxEventBytes = 4096
	// ShutdownTimeout bounds delivery at process exit.
	ShutdownTimeout = time.Second

	maxQueuedEvents = 32
	requestTimeout  = 2 * time.Second
	maxAttempts     = 2
	schemaID        = "https://microsoft.github.io/waza/schemas/usage-telemetry-1.0.schema.json"
)

// Approved feature values.
const (
	FeatureLLMJudge  = "llm_judge"
	FeatureHTTPAPI   = "http_api"
	FeatureDashboard = "dashboard"
	FeatureMCPServer = "mcp_server"
)

// Approved execution values.
const (
	SurfaceBenchmark  = "benchmark"
	SurfaceRunnerUnit = "runner_unit"

	ModeLive = "live"
	ModeMock = "mock"

	ExecutionPassed    = "passed"
	ExecutionFailed    = "failed"
	ExecutionError     = "error"
	ExecutionCancelled = "canceled"
)

// Approved command outcomes.
const (
	CommandSuccess      = "success"
	CommandTestFailed   = "test_failed"
	CommandSetupError   = "setup_error"
	CommandRuntimeError = "runtime_error"
	CommandCancelled    = "canceled"
)

type client struct {
	policy     Policy
	version    string
	httpClient *http.Client

	mu        sync.Mutex
	installID string
	idReady   bool
	queue     [][]byte
	features  map[string]bool
}

var (
	activeMu sync.Mutex
	active   *client

	testHTTPClient *http.Client
)

// Start activates the process client when the policy allows collection.
// When it does not, nothing is allocated and every Record call is a no-op.
func Start(p Policy, wazaVersion string) {
	activeMu.Lock()
	defer activeMu.Unlock()
	active = nil
	if !p.Enabled || p.endpoint == nil {
		return
	}
	httpClient := testHTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	redirectless := *httpClient
	redirectless.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	active = &client{policy: p, version: normalizeVersion(wazaVersion), httpClient: &redirectless,
		features: map[string]bool{}}
}

// Active reports whether a collecting client is running in this process.
func Active() bool {
	return current() != nil
}

func current() *client {
	activeMu.Lock()
	defer activeMu.Unlock()
	return active
}

func discardPending() {
	activeMu.Lock()
	c := active
	active = nil
	activeMu.Unlock()
	if c != nil {
		c.mu.Lock()
		c.queue = nil
		c.mu.Unlock()
	}
}

// RecordCommand records one outer CLI invocation. Nested processes never
// report commands; their parent already does.
func RecordCommand(command, outcome string, elapsed time.Duration) {
	c := current()
	if c == nil || c.policy.Nested {
		return
	}
	c.enqueue(map[string]any{"event": "command_completed", "command": canonicalCommand(command),
		"outcome": outcome, "duration_bucket": DurationBucket(elapsed)})
}

// RecordExecution records one logical evaluation execution.
func RecordExecution(surface, mode, outcome string, tasks int, elapsed time.Duration) {
	c := current()
	if c == nil {
		return
	}
	c.enqueue(map[string]any{"event": "execution_completed", "surface": surface, "execution_mode": mode,
		"outcome": outcome, "task_count_bucket": CountBucket(tasks), "duration_bucket": DurationBucket(elapsed)})
}

// RecordFeature records that a feature was used, at most once per process.
func RecordFeature(feature string) {
	c := current()
	if c == nil {
		return
	}
	c.mu.Lock()
	seen := c.features[feature]
	c.features[feature] = true
	c.mu.Unlock()
	if !seen {
		c.enqueue(map[string]any{"event": "feature_used", "feature": feature})
	}
}

// ExecutionMode maps an engine type to the approved execution mode.
func ExecutionMode(engineType string) string {
	if strings.EqualFold(strings.TrimSpace(engineType), "mock") {
		return ModeMock
	}
	return ModeLive
}

// ExecutionOutcome classifies a finished execution without inspecting error text.
func ExecutionOutcome(ctx context.Context, err error, failed, errored int) string {
	switch {
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return ExecutionCancelled
	case err != nil || errored > 0:
		return ExecutionError
	case failed > 0:
		return ExecutionFailed
	}
	return ExecutionPassed
}

func (c *client) enqueue(fields map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) >= maxQueuedEvents || !c.ensureInstallID() {
		return
	}
	fields["schema_version"] = "1.0"
	fields["event_id"] = uuid.NewString()
	fields["waza_version"] = c.version
	fields["os"] = enumOr(runtime.GOOS, "other", "windows", "linux", "darwin")
	fields["arch"] = enumOr(runtime.GOARCH, "other", "amd64", "arm64")
	fields["distribution"] = c.policy.distribution
	if c.version == "dev" {
		fields["distribution"] = "dev"
	}
	fields["ci"] = c.policy.CI
	if !c.policy.CI {
		fields["installation_id"] = c.installID
	}
	body, err := json.Marshal(fields)
	if err != nil || ValidateEvent(body) != nil {
		return
	}
	c.queue = append(c.queue, body)
}

// ensureInstallID loads or lazily creates the installation ID. CI clients
// never use one. Without a persisted ID nothing is sent, so failures cannot
// inflate active-installation counts.
func (c *client) ensureInstallID() bool {
	if c.policy.CI {
		return true
	}
	if c.idReady {
		return true
	}
	path := c.policy.settingsPath
	var id string
	err := withSettingsLock(path, func() error {
		s, err := LoadSettings(path)
		if err != nil {
			return err
		}
		if s.Collection == CollectionDisabled {
			return errors.New("telemetry disabled")
		}
		if s.InstallationID == "" {
			s.InstallationID = uuid.NewString()
			if err := SaveSettings(path, s); err != nil {
				return err
			}
		}
		id = s.InstallationID
		return nil
	})
	if err != nil {
		return false
	}
	c.installID, c.idReady = id, true
	return true
}

// stillPermitted re-reads persistent settings before delivery, so a disable or
// reset-id from another process drops events queued by this one.
func (c *client) stillPermitted() bool {
	s, err := LoadSettings(c.policy.settingsPath)
	if err != nil || s.Collection == CollectionDisabled {
		return false
	}
	return c.policy.CI || s.InstallationID == c.installID
}

// Shutdown delivers queued events best-effort within timeout, then drops the
// rest. It never writes output and never returns an error.
func Shutdown(timeout time.Duration) {
	activeMu.Lock()
	c := active
	active = nil
	activeMu.Unlock()
	if c == nil {
		return
	}
	c.mu.Lock()
	queue := c.queue
	c.queue = nil
	c.mu.Unlock()
	if len(queue) == 0 || !c.stillPermitted() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for _, body := range queue {
		if ctx.Err() != nil {
			return
		}
		c.deliver(ctx, body)
	}
}

// deliver sends one event, retrying once with the identical body so the
// event ID is stable across retries.
func (c *client) deliver(ctx context.Context, body []byte) {
	for attempt := 0; attempt < maxAttempts && ctx.Err() == nil; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.policy.endpoint.String(), bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
				return
			}
		}
	}
}

var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemas.UsageTelemetrySchemaJSON))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	if err := compiler.AddResource(schemaID, doc); err != nil {
		return nil, err
	}
	return compiler.Compile(schemaID)
})

// ValidateEvent checks a serialized event against the closed v1 contract.
func ValidateEvent(body []byte) error {
	if len(body) > MaxEventBytes {
		return fmt.Errorf("usage event exceeds %d bytes", MaxEventBytes)
	}
	schema, err := compiledSchema()
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return err
	}
	return schema.Validate(doc)
}

var commandEnum = sync.OnceValue(func() map[string]bool {
	var doc struct {
		Defs struct {
			Command struct {
				Enum []string `json:"enum"`
			} `json:"command"`
		} `json:"$defs"`
	}
	allowed := map[string]bool{}
	if json.Unmarshal([]byte(schemas.UsageTelemetrySchemaJSON), &doc) == nil {
		for _, name := range doc.Defs.Command.Enum {
			allowed[name] = true
		}
	}
	return allowed
})

// CommandNames returns the approved command values from the contract.
func CommandNames() map[string]bool {
	out := map[string]bool{}
	for name := range commandEnum() {
		out[name] = true
	}
	return out
}

func canonicalCommand(command string) string {
	if command == "" {
		return "root"
	}
	if commandEnum()[command] {
		return command
	}
	return "unknown"
}

// DurationBucket maps a duration to an approved bucket.
func DurationBucket(d time.Duration) string {
	switch {
	case d < time.Second:
		return "lt_1s"
	case d < 10*time.Second:
		return "1s_10s"
	case d < time.Minute:
		return "10s_1m"
	case d < 10*time.Minute:
		return "1m_10m"
	case d < time.Hour:
		return "10m_1h"
	}
	return "ge_1h"
}

// CountBucket maps a count to an approved bucket.
func CountBucket(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n == 1:
		return "1"
	case n <= 5:
		return "2_5"
	case n <= 20:
		return "6_20"
	case n <= 100:
		return "21_100"
	}
	return "gt_100"
}

func normalizeVersion(raw string) string {
	if raw == "" || raw == "dev" {
		return "dev"
	}
	v, err := semver.NewVersion(raw)
	if err != nil || v.Major() > 9999 || v.Minor() > 9999 || v.Patch() > 9999 {
		return "other"
	}
	return fmt.Sprintf("%d.%d.%d", v.Major(), v.Minor(), v.Patch())
}

func enumOr(value, fallback string, allowed ...string) string {
	for _, a := range allowed {
		if value == a {
			return value
		}
	}
	return fallback
}
