package controlledcomparison_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/controlledcomparison"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/microsoft/waza/internal/utils"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func nativeFixture(t testing.TB, model string) controlledcomparison.Source {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "eval.yaml"), []byte(
		"schemaVersion: '1.0'\nname: native\nconfig:\n  executor: copilot-sdk\n  model: "+model+
			"\n  disabled_skills: ['*']\n  trials_per_task: 1\n  timeout_seconds: 30\n"+
			"tasks: [task.yaml]\ngraders:\n  - name: text\n    type: text\n    config:\n      contains: [hello]\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), []byte(
		"id: task-0\nname: native\ngolden: true\ninputs:\n  prompt: say hello\n"), 0o600))
	return controlledcomparison.Source{EvalPath: filepath.Join(dir, "eval.yaml")}
}

func nativeDesign() releasepolicy.Design {
	return releasepolicy.Design{Estimand: "fixed_suite_repeated_execution", Endpoint: "first_attempt_pass",
		Alpha: "0.05", FamilySize: 1, MaximumHalfWidth: "1", Margin: "1", Accept: "noninferiority",
		Independence: releasepolicy.Independence{Assessment: "unjustified",
			Justification: "One-cluster deterministic SDK fake validates lifecycle contracts, not quality.",
			Limitations:   []string{"No live quality evidence."}},
		Clusters: []releasepolicy.Cluster{{ID: "cluster", Weight: "1", Tasks: []releasepolicy.PlannedTask{
			{ID: "task-0", Weight: "1", TrialOrdinals: []int{1}}}}}}
}

func nativeRequirements() releasepolicy.Requirements {
	return releasepolicy.Requirements{IdentityDomains: []string{
		"task_definition", "resolved_prompt", "fixture_inventory", "instruction_inventory",
		"grader_configuration", "dependency_mode", "grader_implementation", "rubric_inventory", "lock_inventory"},
		Billing: []releasepolicy.BillingRequirement{}}
}

// Production allocates randomly. Fixtures precommit a fixed valid seed before
// any collection side effect; arm identities are never relabeled after running.
func fixedNativePolicy(t testing.TB, raw []byte) []byte {
	t.Helper()
	native, policy, profile, err := controlledcomparison.DecodeNativePolicy(raw)
	require.NoError(t, err)
	policy.Design.Allocation.Seed = strings.Repeat("ab", 32)
	for i, cluster := range policy.Design.Clusters {
		policy.Design.Allocation.Assignments[i].Order, err = releasepolicy.ArmOrder(policy.Design.Allocation.Seed, cluster.ID)
		require.NoError(t, err)
	}
	native.Policy, err = releasepolicy.SealJSON(policy)
	require.NoError(t, err)
	policy, err = releasepolicy.DecodePolicy(native.Policy)
	require.NoError(t, err)
	profile.PolicyDigest = policy.Digest
	native.Profile, err = releasepolicy.SealJSON(profile)
	require.NoError(t, err)
	_, err = nativetask.DecodeProfile(native.Profile)
	require.NoError(t, err)
	sealed, err := releasepolicy.SealJSON(native)
	require.NoError(t, err)
	return sealed
}

func nativeFactory(t testing.TB, calls *int, configure ...func(int, *nativeTestClient)) func(*copilot.ClientOptions) execution.CopilotClient {
	t.Helper()
	return func(*copilot.ClientOptions) execution.CopilotClient {
		*calls++
		id := fmt.Sprintf("private-session-%d", *calls)
		client := &nativeTestClient{t: t, session: &nativeTestSession{t: t, id: id, output: "hello"}}
		t.Cleanup(func() {
			if client.created {
				require.True(t, client.deleted, "native profile must clean up each owned remote session")
				require.True(t, client.stopped, "native profile must stop each owned SDK client")
			}
		})
		for _, setup := range configure {
			setup(*calls, client)
		}
		return client
	}
}

type nativeTestClient struct {
	t               testing.TB
	session         *nativeTestSession
	startErr        error
	stopErr         error
	onStop          func()
	created         bool
	deleted         bool
	stopped         bool
	verifyWorkspace func(*copilot.SessionConfig)
}

func (c *nativeTestClient) Start(context.Context) error { return c.startErr }
func (c *nativeTestClient) Stop() error {
	c.stopped = true
	if c.onStop != nil {
		c.onStop()
	}
	return c.stopErr
}
func (c *nativeTestClient) GetAuthStatus(context.Context) (*copilot.GetAuthStatusResponse, error) {
	return &copilot.GetAuthStatusResponse{IsAuthenticated: true}, nil
}
func (c *nativeTestClient) CreateSession(_ context.Context, config *copilot.SessionConfig) (execution.CopilotSession, error) {
	c.created = true
	require.Equal(c.t, "fixed-model", config.Model)
	require.NotNil(c.t, config.AvailableTools)
	require.Empty(c.t, config.AvailableTools)
	require.Empty(c.t, config.SkillDirectories)
	require.Equal(c.t, utils.Ptr(false), config.EnableSkills)
	if c.verifyWorkspace != nil {
		c.verifyWorkspace(config)
	}
	return c.session, nil
}
func (c *nativeTestClient) ResumeSessionWithOptions(context.Context, string, *copilot.ResumeSessionConfig) (execution.CopilotSession, error) {
	return nil, fmt.Errorf("native collection must not resume sessions")
}
func (c *nativeTestClient) DeleteSession(_ context.Context, id string) error {
	c.deleted = true
	require.Equal(c.t, c.session.id, id)
	return nil
}
func (c *nativeTestClient) ListModels(context.Context) ([]copilot.ModelInfo, error) {
	return nil, fmt.Errorf("native collection must not discover models")
}

type nativeTestSession struct {
	t              testing.TB
	id             string
	handlers       []copilot.SessionEventHandler
	output         string
	sendErr        error
	usageErr       error
	disconnectErr  error
	afterSend      func()
	inputTokens    int
	credits        float64
	expectedPrompt string
}

func (s *nativeTestSession) SessionID() string { return s.id }
func (s *nativeTestSession) Disconnect() error { return s.disconnectErr }
func (s *nativeTestSession) On(handler copilot.SessionEventHandler) func() {
	s.handlers = append(s.handlers, handler)
	return func() {}
}
func (s *nativeTestSession) SendAndWait(_ context.Context, message copilot.MessageOptions) (*copilot.SessionEvent, error) {
	expected := s.expectedPrompt
	if expected == "" {
		expected = "say hello"
	}
	require.Equal(s.t, expected, message.Prompt)
	event := copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: s.output, Model: utils.Ptr("fixed-model")}}
	for _, handler := range s.handlers {
		handler(event)
	}
	if s.afterSend != nil {
		s.afterSend()
	}
	return &event, s.sendErr
}
func (s *nativeTestSession) UsageMetrics(context.Context) (*rpc.UsageGetMetricsResult, error) {
	if s.usageErr != nil {
		return nil, s.usageErr
	}
	input := 7
	if s.inputTokens != 0 {
		input = s.inputTokens
	}
	return &rpc.UsageGetMetricsResult{
		TotalNanoAiu: utils.Ptr(s.credits * 1e9), ModelMetrics: map[string]rpc.UsageMetricsModelMetric{
			"fixed-model": {TotalNanoAiu: utils.Ptr(s.credits * 1e9), Usage: rpc.UsageMetricsModelMetricUsage{InputTokens: int64(input)}},
		},
	}, nil
}
func (s *nativeTestSession) ShutdownUsage(context.Context) (*copilot.SessionShutdownData, error) {
	return nil, fmt.Errorf("no shutdown usage fallback")
}

func TestNativeCollectionDefaultClientDoesNotAllocateCommandMockRuntime(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	require.NoError(t, commandmock.CloseRuntime())
	t.Cleanup(func() { require.NoError(t, commandmock.CloseRuntime()) })
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	t.Setenv("TMP", scratch)
	t.Setenv("TEMP", scratch)
	reject := filepath.Join(scratch, "reject-copilot")
	require.NoError(t, os.WriteFile(reject, []byte("#!/bin/sh\nexit 125\n"), 0o700))
	t.Setenv("COPILOT_CLI_PATH", reject)
	source := nativeFixture(t, "fixed-model")
	policy, err := controlledcomparison.PlanNative(t.Context(), source, source, nativeDesign(), nativeRequirements(), nil)
	require.NoError(t, err)
	directory := filepath.Join(t.TempDir(), "native")
	err = controlledcomparison.CollectNative(t.Context(), policy, source, source, directory)
	require.ErrorContains(t, err, "operationally incomplete")
	decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
	require.ErrorContains(t, err, "retained failure")
	require.Len(t, decision.Native.Attempts, 2, "both actual default builders must have been reached")
	for _, attempt := range decision.Native.Attempts {
		require.Equal(t, "failed", attempt.Lifecycle["initialize"].State)
		require.Equal(t, "not_attempted", attempt.Lifecycle["execute"].State)
		require.Equal(t, "completed", attempt.Lifecycle["shutdown"].State)
		require.Equal(t, "unavailable", attempt.OutputAvailability)
	}
	registries, err := filepath.Glob(filepath.Join(scratch, "waza-command-mocks-*"))
	require.NoError(t, err)
	require.Empty(t, registries, "default native startup failure must not leak the unused registry")
}

func TestNativeCollectionProductionLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	baseline, candidate := nativeFixture(t, "fixed-model"), nativeFixture(t, "fixed-model")
	required := nativeRequirements()
	policy, err := controlledcomparison.PlanNative(t.Context(), baseline, candidate, nativeDesign(), required, nil)
	require.NoError(t, err)
	policy = fixedNativePolicy(t, policy)
	directory := filepath.Join(t.TempDir(), "native")
	calls := 0
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, baseline, candidate, directory,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))
	require.Equal(t, 2, calls, "each actual attempt owns a fresh SDK client")
	decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
	require.NoError(t, err)
	require.False(t, decision.Accepted, "one unjustified cluster is not quality evidence")
	require.NotNil(t, decision.Native)
	require.Len(t, decision.Native.Attempts, 2)
	require.Equal(t, "passed", decision.Golden.State)
	for _, attempt := range decision.Native.Attempts {
		require.Equal(t, "passed", attempt.Status)
		require.Equal(t, "available", attempt.OutputAvailability)
		require.Equal(t, utils.Ptr("hello"), attempt.Output)
		require.True(t, attempt.UsageComplete)
		require.Equal(t, "completed", attempt.Lifecycle["shutdown"].State)
	}
	_, err = releasepolicy.ReadDecision(directory)
	require.Error(t, err, "legacy core reader must reject the separate native policy kind")
	require.Error(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, baseline, candidate, directory,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))
	require.Equal(t, 2, calls, "an existing collection cannot be adopted")
	assertNativeAPI(t, filepath.Dir(directory), "passed")
}

func assertNativeAPI(t *testing.T, directory, fixture string) {
	t.Helper()
	mux := http.NewServeMux()
	webapi.RegisterReleaseRoutes(mux, directory)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/release-collections", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response []webapi.ReleaseCollection
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Len(t, response, 1)
	require.NotNil(t, response[0].Decision.Native)
	var definition, view any
	require.NoError(t, json.Unmarshal([]byte(schemas.ReleaseArtifactsSchemaJSON), &definition))
	rawView, err := json.Marshal(response[0].Decision)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(rawView, &view))
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("release.json", definition))
	schema, err := compiler.Compile("release.json")
	require.NoError(t, err)
	require.NoError(t, schema.Validate(view), "real native API decision must satisfy the published schema")
	for i := range response[0].Decision.Native.Attempts {
		attempt := &response[0].Decision.Native.Attempts[i]
		attempt.Key.EvalID = string(attempt.Key.Arm) + "-eval"
	}
	slices.SortFunc(response[0].Decision.Native.Attempts, func(a, b releasepolicy.NativeAttemptView) int {
		if a.Key.Arm != b.Key.Arm {
			return strings.Compare(string(a.Key.Arm), string(b.Key.Arm))
		}
		return a.Key.Attempt - b.Key.Attempt
	})
	normalized, err := json.MarshalIndent(response, "", "  ")
	require.NoError(t, err)
	normalized = append(normalized, '\n')
	path := filepath.Join("testdata", "native-api-"+fixture+".json")
	if os.Getenv("UPDATE_NATIVE_API_FIXTURES") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, normalized, 0o600))
	}
	expected, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(normalized),
		"producer/API fixture changes require deliberate regeneration; only fresh eval IDs and allocation order are normalized")
}

func TestNativeCollectionAcceptedAndFrozenReader(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	baseline, candidate := nativeFixture(t, "fixed-model"), nativeFixture(t, "fixed-model")
	d := nativeDesign()
	d.Clusters = nil
	d.Independence.Assessment = "justified"
	d.Independence.Justification = "Independent deterministic fake clients exercise the statistical admission contract only."
	for _, source := range []controlledcomparison.Source{baseline, candidate} {
		task, err := os.ReadFile(filepath.Join(filepath.Dir(source.EvalPath), "task.yaml"))
		require.NoError(t, err)
		for i := 0; i < 8; i++ {
			require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(source.EvalPath), fmt.Sprintf("task-%d.yaml", i)),
				[]byte(strings.Replace(string(task), "id: task-0", fmt.Sprintf("id: task-%d", i), 1)), 0o600))
		}
		require.NoError(t, os.Remove(filepath.Join(filepath.Dir(source.EvalPath), "task.yaml")))
		raw, err := os.ReadFile(source.EvalPath)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(source.EvalPath, []byte(strings.Replace(string(raw), "[task.yaml]", "['task-*.yaml']", 1)), 0o600))
	}
	for i := 0; i < 8; i++ {
		d.Clusters = append(d.Clusters, releasepolicy.Cluster{ID: fmt.Sprintf("cluster-%d", i), Weight: "0.125",
			Tasks: []releasepolicy.PlannedTask{{ID: fmt.Sprintf("task-%d", i), Weight: "1", TrialOrdinals: []int{1}}}})
	}
	policy, err := controlledcomparison.PlanNative(t.Context(), baseline, candidate, d, nativeRequirements(), nil)
	require.NoError(t, err)
	policy = fixedNativePolicy(t, policy)
	directory := filepath.Join(t.TempDir(), "native")
	calls := 0
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, baseline, candidate, directory,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))
	require.Equal(t, 16, calls)
	decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
	require.NoError(t, err)
	require.True(t, decision.Accepted, "%+v", decision)
	assertNativeAPI(t, filepath.Dir(directory), "accepted")
	binary := os.Getenv("WAZA_FROZEN_RELEASE_READER")
	checkFrozen := func(label string, selected []byte) {
		t.Helper()
		if binary == "" {
			return
		}
		data, err := os.ReadFile(binary)
		require.NoError(t, err)
		require.Equal(t, "9fc882fa3fe1a030f483db8a80e8d129b89f072201a3c47adcbab64933d54570", fmt.Sprintf("%x", sha256.Sum256(data)))
		selectedPath := filepath.Join(t.TempDir(), "selected.json")
		require.NoError(t, os.WriteFile(selectedPath, selected, 0o600))
		command := exec.CommandContext(t.Context(), binary, "compare", "--release-policy", selectedPath,
			"--collection-dir", directory, "--format", "json")
		output, err := command.CombinedOutput()
		require.Error(t, err, "frozen reader must reject %s", label)
		require.Contains(t, string(output), `"accepted": false`)
		t.Logf("frozen b64c6bb10 %s: %s", label, output)
	}
	checkFrozen("intact-native", policy)
	native, _, _, err := controlledcomparison.DecodeNativePolicy(policy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "policy.json"), native.Policy, 0o600))
	require.NoError(t, os.Remove(filepath.Join(directory, "native-capture.json")))
	for _, name := range []string{"native-task-events.ndjson", "native-task-rows.ndjson"} {
		require.NoError(t, os.Remove(filepath.Join(directory, "native", name)))
	}
	require.NoError(t, os.Remove(filepath.Join(directory, "native")))
	current, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, native.Policy)
	require.Error(t, err)
	require.False(t, current.Accepted)
	checkFrozen("stripped-core-only", native.Policy)
}

func TestNativeCollectionObservedFailures(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}

	for _, name := range []string{"retry", "initialize", "execute", "shutdown", "disconnect", "empty-output", "unknown-usage", "unknown-budget", "reached-budget"} {
		t.Run(name, func(t *testing.T) {
			baseline, candidate := nativeFixture(t, "fixed-model"), nativeFixture(t, "fixed-model")
			for _, source := range []controlledcomparison.Source{baseline, candidate} {
				raw, err := os.ReadFile(source.EvalPath)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(source.EvalPath,
					[]byte(strings.Replace(string(raw), "timeout_seconds: 30", "timeout_seconds: 30\n  max_attempts: 2", 1)), 0o600))
			}
			required := nativeRequirements()
			if name == "unknown-budget" || name == "reached-budget" {
				required.Billing = []releasepolicy.BillingRequirement{{Axis: "input_tokens", Maximum: "7"}}
			}
			policy, err := controlledcomparison.PlanNative(t.Context(), baseline, candidate, nativeDesign(), required, nil)
			require.NoError(t, err)
			policy = fixedNativePolicy(t, policy)
			directory := filepath.Join(t.TempDir(), "native")
			calls := 0
			setup := func(ordinal int, client *nativeTestClient) {
				failure := errors.New("injected SDK failure")
				switch name {
				case "retry":
					if ordinal%2 == 1 {
						client.session.output = "incorrect"
					}
				case "initialize":
					client.startErr = failure
				case "execute":
					client.session.sendErr = failure
				case "shutdown":
					client.stopErr = failure
				case "disconnect":
					client.session.disconnectErr = failure
				case "empty-output":
					client.session.output = ""
				case "unknown-usage", "unknown-budget":
					client.session.usageErr = failure
				}
			}
			err = controlledcomparison.CollectNativeWithOptions(t.Context(), policy, baseline, candidate, directory,
				controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls, setup)})
			budgetStops := name == "unknown-budget" || name == "reached-budget"
			if budgetStops {
				require.Error(t, err)
				require.Equal(t, 1, calls)
			} else {
				if name == "retry" {
					require.NoError(t, err)
				} else {
					require.Error(t, err, "operational collection failures must propagate to command exit")
				}
				if name == "retry" {
					require.Equal(t, 4, calls)
				} else {
					require.Equal(t, 2, calls, "operational failures must not authorize behavioral retry")
				}
			}
			decision, readErr := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
			if name != "retry" {
				require.Error(t, readErr)
			} else {
				require.NoError(t, readErr)
			}
			require.False(t, decision.Accepted)
			require.NotNil(t, decision.Native)
			require.Len(t, decision.Native.Attempts, calls)
			switch name {
			case "retry":
				require.Equal(t, "failed", decision.Golden.State, "recovery cannot erase a first-attempt golden failure")
				for _, accounting := range decision.Accounting {
					require.Equal(t, 2, accounting.Attempts)
				}
			case "initialize", "execute", "shutdown", "disconnect", "empty-output":
				for _, attempt := range decision.Native.Attempts {
					require.Equal(t, "operational", attempt.Category)
					require.NotEmpty(t, attempt.Error)
				}
			}
			assertNativeAPI(t, filepath.Dir(directory), name)
		})
	}
}

// BenchmarkReadSelectedDecisionActualCollectionCapture exercises the complete
// production reader, including capture digest verification, with admitted test
// executable bytes rather than a synthetic payload.
func BenchmarkReadSelectedDecisionActualCollectionCapture(b *testing.B) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		b.Skip("native durable creation requires Linux or macOS")
	}
	baseline, candidate := nativeFixture(b, "fixed-model"), nativeFixture(b, "fixed-model")
	policy, err := controlledcomparison.PlanNative(b.Context(), baseline, candidate, nativeDesign(), nativeRequirements(), nil)
	require.NoError(b, err)
	policy = fixedNativePolicy(b, policy)
	directory := filepath.Join(b.TempDir(), "native")
	calls := 0
	require.NoError(b, controlledcomparison.CollectNativeWithOptions(b.Context(), policy, baseline, candidate, directory,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(b, &calls)}))
	require.Equal(b, 2, calls)
	raw, err := os.ReadFile(filepath.Join(directory, "native-capture.json"))
	require.NoError(b, err)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		decision, err := controlledcomparison.ReadSelectedDecision(b.Context(), directory, policy)
		require.NoError(b, err)
		require.NotNil(b, decision.Native)
	}
}
