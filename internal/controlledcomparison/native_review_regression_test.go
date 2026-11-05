package controlledcomparison_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/controlledcomparison"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func nativeHTTPCollection(t *testing.T, directory string) webapi.ReleaseCollection {
	t.Helper()
	mux := http.NewServeMux()
	webapi.RegisterReleaseRoutes(mux, filepath.Dir(directory))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/release-collections", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	var items []webapi.ReleaseCollection
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &items))
	require.Len(t, items, 1)
	return items[0]
}

func TestNativeCollectionPublicPayloads(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	for _, name := range []string{"carriage-return", "large-output", "large-request", "stream-overflow"} {
		t.Run(name, func(t *testing.T) {
			source := nativeFixture(t, "fixed-model")
			design := nativeDesign()
			output := "hello\r=1+1"
			prompt := "say hello"
			if name == "large-output" {
				output = "hello" + strings.Repeat("x", 400<<10)
			}
			if name == "large-request" {
				prompt = "say hello\n" + strings.Repeat("xxxxxxxx\n", (1<<20)/8)
				task := "id: task-0\nname: native\ngolden: true\ninputs:\n  prompt_file: prompt.txt\n"
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(source.EvalPath), "task.yaml"), []byte(task), 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(source.EvalPath), "prompt.txt"), []byte(prompt), 0o600))
			}
			if name == "stream-overflow" {
				output = "hello" + strings.Repeat("x", 768<<10)
				raw, err := os.ReadFile(source.EvalPath)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(source.EvalPath, bytes.Replace(raw, []byte("trials_per_task: 1"), []byte("trials_per_task: 8"), 1), 0o600))
				design.Clusters[0].Tasks[0].TrialOrdinals = []int{1, 2, 3, 4, 5, 6, 7, 8}
			}
			policy, err := controlledcomparison.PlanNative(t.Context(), source, source, design, nativeRequirements(), nil)
			require.NoError(t, err)
			policy = fixedNativePolicy(t, policy)
			directory := filepath.Join(t.TempDir(), "native")
			calls := 0
			err = controlledcomparison.CollectNativeWithOptions(t.Context(), policy, source, source, directory,
				controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls, func(_ int, client *nativeTestClient) {
					client.session.output = output
					client.session.expectedPrompt = prompt
				})})
			if name == "stream-overflow" {
				require.ErrorContains(t, err, "stream exceeds")
				require.Less(t, calls, 16, "capacity failure must prevent all subsequent runtime starts")
				for _, file := range []string{"native-task-events.ndjson", "native-task-rows.ndjson"} {
					info, err := os.Stat(filepath.Join(directory, "native", file))
					require.NoError(t, err)
					require.LessOrEqual(t, info.Size(), int64(nativetask.CollectionStreamBytes))
				}
				decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
				require.ErrorContains(t, err, "incomplete prefix")
				require.False(t, decision.Accepted)
				require.NotEqual(t, "invalid", decision.Compatibility.State)
				require.Len(t, decision.Native.Attempts, calls)
				last := decision.Native.Attempts[len(decision.Native.Attempts)-1]
				require.Equal(t, "incomplete", last.Status)
				require.Equal(t, "unavailable", last.OutputAvailability)
				require.Nil(t, decision.Usage[last.Key.Arm][0].Value, "the unretained final session usage must remain unknown")
				stop, err := os.ReadFile(filepath.Join(directory, "native-stop.json"))
				require.NoError(t, err)
				require.Contains(t, string(stop), "stream exceeds")
				item := nativeHTTPCollection(t, directory)
				require.False(t, item.Decision.Accepted)
				require.Len(t, item.Decision.Native.Attempts, calls)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 2, calls)
			decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
			require.NoError(t, err)
			require.False(t, decision.Accepted)
			require.Equal(t, "observed", decision.Operations.State)
			require.Equal(t, "inconclusive", decision.Statistics.State)
			item := nativeHTTPCollection(t, directory)
			require.Empty(t, item.Error)
			require.Len(t, item.Decision.Native.Attempts, 2)
			for _, attempt := range item.Decision.Native.Attempts {
				require.Equal(t, output, *attempt.Output)
				require.Equal(t, "passed", attempt.Status)
			}
			if name == "carriage-return" {
				assertNativeAPI(t, filepath.Dir(directory), "carriage-return")
			}
			if name == "large-output" {
				rows, err := os.ReadFile(filepath.Join(directory, "native", "native-task-rows.ndjson"))
				require.NoError(t, err)
				require.Greater(t, len(rows), 1<<20, "exercise the actual previously rejected public stream size")
				_, err = nativetask.InspectPrefix(t.Context(), nil, rows, nil)
				require.ErrorContains(t, err, "1 MiB", "private prefix stream cap remains unchanged")
			}
		})
	}
}

func TestNativeCollectionDiscoveryOrder(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	source := nativeFixture(t, "fixed-model")
	base := filepath.Dir(source.EvalPath)
	require.NoError(t, os.WriteFile(filepath.Join(base, "task.yaml"), []byte("id: z-task\nname: first\ninputs:\n  prompt: say hello\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "next.yaml"), []byte("id: a-task\nname: second\ninputs:\n  prompt: say hello\n"), 0o600))
	raw, err := os.ReadFile(source.EvalPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(source.EvalPath, bytes.Replace(raw, []byte("[task.yaml]"), []byte("[task.yaml, next.yaml]"), 1), 0o600))
	design := nativeDesign()
	design.Clusters[0].Tasks = []releasepolicy.PlannedTask{{ID: "z-task", Weight: "0.5", TrialOrdinals: []int{1}}, {ID: "a-task", Weight: "0.5", TrialOrdinals: []int{1}}}
	policy, err := controlledcomparison.PlanNative(t.Context(), source, source, design, nativeRequirements(), nil)
	require.NoError(t, err)
	_, core, profile, err := controlledcomparison.DecodeNativePolicy(policy)
	require.NoError(t, err)
	require.Equal(t, "z-task", core.Arms[releasepolicy.Baseline].Plan.Tasks[0].ID)
	require.Equal(t, "a-task", profile.Arms[releasepolicy.Baseline].Requests[0].TaskID)
	directory := filepath.Join(t.TempDir(), "native")
	calls := 0
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, source, source, directory,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))
	require.Equal(t, 4, calls)
	decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
	require.NoError(t, err)
	require.Len(t, decision.Native.Attempts, 4)
	item := nativeHTTPCollection(t, directory)
	require.Empty(t, item.Error)
	require.Len(t, item.Decision.Native.Attempts, 4)
	require.NoError(t, os.WriteFile(filepath.Join(base, "next.yaml"), []byte("id: a-task\nname: changed\ninputs:\n  prompt: drift\n"), 0o600))
	err = controlledcomparison.CollectNativeWithOptions(t.Context(), policy, source, source, filepath.Join(t.TempDir(), "drift"),
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)})
	require.Error(t, err)
	require.Equal(t, 4, calls, "drift rejects before another runtime starts")
}

type replaceAfterInitialPolicyReadContext struct {
	context.Context
	errCalls int
	replaced bool
	replace  func()
}

func (c *replaceAfterInitialPolicyReadContext) Err() error {
	c.errCalls++
	if c.errCalls == 4 {
		c.replace()
		c.replaced = true
	}
	return c.Context.Err()
}

func copyCollectionArtifacts(t testing.TB, source, destination string) {
	t.Helper()
	require.NoError(t, filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	}))
}

func TestReadSelectedDecisionRejectsCollectionReplacedAfterSelection(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	source := nativeFixture(t, "fixed-model")
	policyA, err := controlledcomparison.PlanNative(t.Context(), source, source, nativeDesign(), nativeRequirements(), nil)
	require.NoError(t, err)
	policyA = fixedNativePolicy(t, policyA)
	changed := nativeDesign()
	changed.Margin = "0.5"
	policyB, err := controlledcomparison.PlanNative(t.Context(), source, source, changed, nativeRequirements(), nil)
	require.NoError(t, err)
	policyB = fixedNativePolicy(t, policyB)
	require.NotEqual(t, policyA, policyB)

	collectionA, collectionB := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	calls := 0
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policyA, source, source, collectionA,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policyB, source, source, collectionB,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))

	ctx := &replaceAfterInitialPolicyReadContext{Context: t.Context(), replace: func() {
		copyCollectionArtifacts(t, collectionB, collectionA)
	}}
	decision, err := controlledcomparison.ReadSelectedDecision(ctx, collectionA, policyA)
	require.True(t, ctx.replaced, "replacement occurs after selection and before assessment")
	require.Error(t, err)
	require.False(t, decision.Accepted)
	current, readErr := os.ReadFile(filepath.Join(collectionA, "policy.json"))
	require.NoError(t, readErr)
	require.Equal(t, policyB, current, "the regression replaces actual collection artifacts rather than mutating an in-memory value")
}

func TestReadSelectedDecisionRejectsMockCollectionReplacedAfterSelection(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("durable mock collection requires Linux or macOS")
	}
	source := nativeFixture(t, "fixed-model")
	raw, err := os.ReadFile(source.EvalPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), "executor: copilot-sdk")
	raw = bytes.Replace(raw, []byte("executor: copilot-sdk"), []byte("executor: mock"), 1)
	raw = bytes.Replace(raw, []byte("schemaVersion: '1.0'\nname: native\n"),
		[]byte("schemaVersion: '2.0'\nname: native\nscenario: controlled-offline\nversion: '1.0'\n"), 1)
	raw = append(raw, []byte("metrics:\n  - name: accuracy\n    weight: 1\n    threshold: 0.8\n")...)
	require.NoError(t, os.WriteFile(source.EvalPath, raw, 0o600))
	policyA, err := controlledcomparison.Plan(source, source, nativeDesign(), nativeRequirements(), nil)
	require.NoError(t, err)
	changed := nativeDesign()
	changed.Margin = "0.5"
	policyB, err := controlledcomparison.Plan(source, source, changed, nativeRequirements(), nil)
	require.NoError(t, err)
	require.NotEqual(t, policyA, policyB)
	require.False(t, controlledcomparison.IsNativePolicy(policyA))
	require.False(t, controlledcomparison.IsNativePolicy(policyB))

	collectionA, collectionB := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	require.NoError(t, controlledcomparison.Collect(t.Context(), policyA, source, source, collectionA))
	require.NoError(t, controlledcomparison.Collect(t.Context(), policyB, source, source, collectionB))
	for _, control := range []struct {
		directory string
		policy    []byte
	}{{collectionA, policyA}, {collectionB, policyB}} {
		decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), control.directory, control.policy)
		require.NoError(t, err)
		require.False(t, decision.Accepted, "valid mock evidence remains statistically inconclusive")
		require.Equal(t, "inconclusive", decision.Statistics.State)
		require.Equal(t, "compatible", decision.Compatibility.State)
		require.Equal(t, 1, decision.Accounting[releasepolicy.Baseline].Attempts)
	}

	ctx := &replaceAfterInitialPolicyReadContext{Context: t.Context(), replace: func() {
		copyCollectionArtifacts(t, collectionB, collectionA)
	}}
	decision, err := controlledcomparison.ReadSelectedDecision(ctx, collectionA, policyA)
	require.True(t, ctx.replaced, "replacement occurs after selection and before assessment")
	current, readErr := os.ReadFile(filepath.Join(collectionA, "policy.json"))
	require.NoError(t, readErr)
	require.Equal(t, policyB, current, "replace actual persisted artifacts with the independently valid B collection")
	require.ErrorContains(t, err, "selected policy identity differs from the precollection commitment")
	require.False(t, decision.Accepted)
	require.Equal(t, "mismatched", decision.Compatibility.State, "a binding rejection is not ordinary mock inconclusiveness")
	matching, err := controlledcomparison.ReadSelectedDecision(t.Context(), collectionA, policyB)
	require.NoError(t, err)
	require.Equal(t, "compatible", matching.Compatibility.State)
	require.Equal(t, "inconclusive", matching.Statistics.State)
}

type replaceDuringCoreAssessmentContext struct {
	context.Context
	replace, restore   func()
	replaced, restored bool
}

func (c *replaceDuringCoreAssessmentContext) Err() error {
	var pcs [64]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(1, pcs[:])])
	inCore, inNativeRead := false, false
	for {
		frame, more := frames.Next()
		inCore = inCore || strings.HasSuffix(frame.Function, "/releasepolicy.readDecisionProfile")
		inNativeRead = inNativeRead || strings.HasSuffix(frame.Function, "/controlledcomparison.nativeArtifactWithOpener")
		if !more {
			break
		}
	}
	if inCore && !c.replaced {
		c.replace()
		c.replaced = true
	}
	if !inCore && inNativeRead && c.replaced && !c.restored {
		c.restore()
		c.restored = true
	}
	return c.Context.Err()
}

func TestReadSelectedDecisionBindsEveryCoreArtifactConsumedByAssessment(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	source := nativeFixture(t, "fixed-model")
	design := nativeDesign()
	design.Margin = "1"
	design.Independence.Assessment = "justified"
	design.Independence.Justification = "Independent synthetic clusters exercise policy admission, not live quality."
	design.Clusters = nil
	tasks := []string{"task.yaml"}
	for i := range 8 {
		id := fmt.Sprintf("task-%d", i)
		design.Clusters = append(design.Clusters, releasepolicy.Cluster{ID: fmt.Sprintf("cluster-%d", i), Weight: "0.125",
			Tasks: []releasepolicy.PlannedTask{{ID: id, Weight: "1", TrialOrdinals: []int{1}}}})
		if i > 0 {
			name := id + ".yaml"
			tasks = append(tasks, name)
			require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(source.EvalPath), name),
				[]byte(fmt.Sprintf("id: %s\nname: native\ngolden: true\ninputs:\n  prompt: say hello\n", id)), 0o600))
		}
	}
	eval, err := os.ReadFile(source.EvalPath)
	require.NoError(t, err)
	require.Contains(t, string(eval), "tasks: [task.yaml]")
	require.NoError(t, os.WriteFile(source.EvalPath, bytes.Replace(eval, []byte("tasks: [task.yaml]"),
		[]byte("tasks: ["+strings.Join(tasks, ", ")+"]"), 1), 0o600))
	policy, err := controlledcomparison.PlanNative(t.Context(), source, source, design, nativeRequirements(), nil)
	require.NoError(t, err)
	policy = fixedNativePolicy(t, policy)
	_, core, _, err := controlledcomparison.DecodeNativePolicy(policy)
	require.NoError(t, err)
	require.Equal(t, "1", core.Design.Margin.String())
	collectionA, collectionB := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	calls := 0
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, source, source, collectionA,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls, func(_ int, client *nativeTestClient) {
			client.session.output = "goodbye"
		})}))
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, source, source, collectionB,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))
	for _, collection := range []struct {
		path     string
		accepted bool
	}{{collectionA, false}, {collectionB, true}} {
		decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), collection.path, policy)
		require.NoError(t, err)
		require.Equal(t, collection.accepted, decision.Accepted, "each same-policy core set is independently valid")
		require.Len(t, decision.Native.Attempts, 16)
		if !collection.accepted {
			require.NotEmpty(t, decision.Golden.Reasons, "the original core evidence includes a nonpassing golden attempt")
		}
	}
	require.Equal(t, 32, calls)

	coreNames := []string{"core-policy.json", "journal.json", "journal.ndjson"}
	for _, arm := range []string{"baseline", "candidate"} {
		for _, suffix := range []string{".begin.json", ".final.json", ".result-binding.json", ".results.json"} {
			coreNames = append(coreNames, arm+suffix)
		}
	}
	readSet := func(directory string) map[string][]byte {
		t.Helper()
		data := map[string][]byte{}
		for _, name := range coreNames {
			raw, err := os.ReadFile(filepath.Join(directory, name))
			require.NoError(t, err)
			data[name] = raw
		}
		return data
	}
	writeSet := func(data map[string][]byte) {
		t.Helper()
		for _, name := range coreNames {
			require.NoError(t, os.WriteFile(filepath.Join(collectionA, name), data[name], 0o600))
		}
		require.Equal(t, data, readSet(collectionA), "prove actual persisted core artifact bytes at the boundary")
		outer, err := os.ReadFile(filepath.Join(collectionA, "policy.json"))
		require.NoError(t, err)
		require.Equal(t, policy, outer, "the selected envelope is never replaced")
	}
	original, replacement := readSet(collectionA), readSet(collectionB)
	require.Equal(t, original["core-policy.json"], replacement["core-policy.json"], "the selected policy identity is unchanged")
	require.NotEqual(t, original["journal.ndjson"], replacement["journal.ndjson"])
	require.NotEqual(t, original["baseline.final.json"], replacement["baseline.final.json"])
	require.NotEqual(t, original["baseline.results.json"], replacement["baseline.results.json"])
	ctx := &replaceDuringCoreAssessmentContext{Context: t.Context(),
		replace: func() { writeSet(replacement) },
		restore: func() { writeSet(original) },
	}
	decision, err := controlledcomparison.ReadSelectedDecision(ctx, collectionA, policy)
	require.True(t, ctx.replaced, "replace only inside the actual core assessment read")
	require.True(t, ctx.restored, "restore before native replay and final row reconciliation")
	require.Equal(t, original, readSet(collectionA))
	t.Logf("same policy digest=%s; temporary B accepted=%t; restored A accepted=%t; replaced=%t restored=%t; returned accepted=%t error=%v",
		core.Digest.SHA256, true, false, ctx.replaced, ctx.restored, decision.Accepted, err)
	require.ErrorContains(t, err, "native start allocation was substituted")
	require.False(t, decision.Accepted, "reconciliation must reject substituted same-policy assessment bytes")
	require.Equal(t, "invalid", decision.Compatibility.State)
	clean, err := controlledcomparison.ReadSelectedDecision(t.Context(), collectionA, policy)
	require.NoError(t, err)
	require.False(t, clean.Accepted)
	require.NotEmpty(t, clean.Golden.Reasons)
	repeated, err := controlledcomparison.ReadSelectedDecision(t.Context(), collectionA, policy)
	require.NoError(t, err)
	require.False(t, repeated.Accepted)
	require.NotEmpty(t, repeated.Golden.Reasons)
}

func TestNativeTerminalPolicyAndLateStop(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native durable creation requires Linux or macOS")
	}
	source := nativeFixture(t, "fixed-model")
	policy, err := controlledcomparison.PlanNative(t.Context(), source, source, nativeDesign(), nativeRequirements(), nil)
	require.NoError(t, err)
	directory := filepath.Join(t.TempDir(), "native")
	calls := 0
	require.NoError(t, controlledcomparison.CollectNativeWithOptions(t.Context(), policy, source, source, directory,
		controlledcomparison.NativeCollectorOptions{NewCopilotClient: nativeFactory(t, &calls)}))
	clean, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
	require.NoError(t, err)
	require.Equal(t, "observed", clean.Operations.State)
	t.Run("late-stop", func(t *testing.T) {
		require.NoError(t, os.WriteFile(filepath.Join(directory, "native-stop.json"), []byte(`{"kind":"waza.release-native-stop","version":"1.0","reason":"retained late close failure"}`), 0o600))
		stopped, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
		require.Error(t, err)
		require.Equal(t, "failed", stopped.Operations.State)
		require.Equal(t, "complete", stopped.Completeness.State)
		require.False(t, stopped.Accepted)
		require.Len(t, stopped.Native.Attempts, 2)
		item := nativeHTTPCollection(t, directory)
		require.NotEmpty(t, item.Error)
		require.Equal(t, "failed", item.Decision.Operations.State)
		require.Len(t, item.Decision.Native.Attempts, 2)
	})
	require.NoError(t, os.Remove(filepath.Join(directory, "native-stop.json")))

	journalPath := filepath.Join(directory, "journal.ndjson")
	journal, err := os.ReadFile(journalPath)
	require.NoError(t, err)
	lines := bytes.SplitAfter(journal, []byte{'\n'})
	prefix := []byte{}
	for _, line := range lines {
		prefix = append(prefix, line...)
		if bytes.Contains(line, []byte(`"type":"attempt_start"`)) {
			break
		}
	}
	require.Contains(t, string(prefix), `"type":"attempt_start"`)
	require.NotContains(t, string(prefix), `"type":"attempt_terminal"`)
	require.NoError(t, os.WriteFile(journalPath, prefix, 0o600))
	require.NoError(t, os.Remove(filepath.Join(directory, "journal.json")))
	rowPath := filepath.Join(directory, "native", "native-task-rows.ndjson")
	rows, err := os.ReadFile(rowPath)
	require.NoError(t, err)
	first := bytes.Split(rows, []byte{'\n'})[0]
	eventPath := filepath.Join(directory, "native", "native-task-events.ndjson")
	allEvents, err := os.ReadFile(eventPath)
	require.NoError(t, err)
	events := bytes.Split(allEvents, []byte{'\n'})
	for _, field := range []string{"valid", "model", "engine", "reasoning"} {
		t.Run(field, func(t *testing.T) {
			var record nativetask.Record
			require.NoError(t, json.Unmarshal(first, &record))
			switch field {
			case "model":
				record.Summary.Runtime.RequestedModel = "substituted-model"
			case "engine":
				record.Summary.Runtime.RequestedEngine = "substituted-engine"
			case "reasoning":
				record.Summary.Runtime.RequestedReasoning = "substituted-reasoning"
			}
			raw, err := json.Marshal(record)
			require.NoError(t, err)
			var terminal nativetask.Event
			require.NoError(t, json.Unmarshal(events[1], &terminal))
			terminal.RecordDigest, err = evidence.JSONDigest(json.RawMessage(raw))
			require.NoError(t, err)
			end, err := json.Marshal(terminal)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(rowPath, append(raw, '\n'), 0o600))
			stream := append(bytes.Clone(events[0]), '\n')
			stream = append(stream, end...)
			require.NoError(t, os.WriteFile(eventPath, append(stream, '\n'), 0o600))
			decision, err := controlledcomparison.ReadSelectedDecision(t.Context(), directory, policy)
			require.Error(t, err)
			require.False(t, decision.Accepted)
			if field == "valid" {
				require.Contains(t, err.Error(), "incomplete prefix")
				require.NotEqual(t, "invalid", decision.Compatibility.State)
				require.NotEqual(t, "mismatched", decision.Compatibility.State)
				require.Len(t, decision.Native.Attempts, 1)
				require.Equal(t, "hello", *decision.Native.Attempts[0].Output)
			} else {
				require.Contains(t, err.Error(), "effective settings")
				require.Equal(t, "invalid", decision.Compatibility.State)
				for _, attempt := range decision.Native.Attempts {
					require.Nil(t, attempt.Output, "invalid terminal must not be exposed as a verified observation")
				}
			}
		})
	}
}
