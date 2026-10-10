package controlledprojection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

// This is an independent JSON-v1 oracle, not evidence.JSONDigest or project.
// Preserve number tokens and require one complete JSON value.
func oracleJSON(t *testing.T, value any) models.EvidenceDigest {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	require.NoError(t, decoder.Decode(&normalized))
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
	raw, err = json.Marshal(normalized)
	require.NoError(t, err)
	hash := sha256.Sum256(raw)
	return models.EvidenceDigest{SHA256: hex.EncodeToString(hash[:]), Encoding: "json-v1"}
}

func oracleBytes(raw []byte) models.EvidenceDigest {
	hash := sha256.Sum256(raw)
	return models.EvidenceDigest{SHA256: hex.EncodeToString(hash[:]), Encoding: "source-bytes"}
}

func object(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result map[string]any
	require.NoError(t, decoder.Decode(&result))
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
	return result
}

type sourceTimeFiles struct {
	bytes    map[string][]byte
	info     map[string]os.FileInfo
	resolved map[string]string
}

func (f sourceTimeFiles) ReadFile(path string) ([]byte, error) {
	raw, ok := f.bytes[path]
	if !ok {
		return nil, fmt.Errorf("uncaptured source-time read %q", path)
	}
	return bytes.Clone(raw), nil
}
func (f sourceTimeFiles) Stat(path string) (os.FileInfo, error) {
	info, ok := f.info[path]
	if !ok {
		return nil, fmt.Errorf("uncaptured actual source-time stat %q", path)
	}
	return info, nil
}
func (f sourceTimeFiles) EvalSymlinks(path string) (string, error) {
	resolved, ok := f.resolved[path]
	if !ok {
		return "", fmt.Errorf("uncaptured actual source-time symlink resolution %q", path)
	}
	return resolved, nil
}
func (f sourceTimeFiles) WalkDir(string, fs.WalkDirFunc) error {
	return fmt.Errorf("no directory traversal in this source-time fixture")
}

type unsupportedJSON struct{}

func (unsupportedJSON) MarshalJSON() ([]byte, error) {
	panic("unsupported custom JSON method must not be invoked by native projection")
}

const numericContext = `    fixture: fixture.txt
    large: 9007199254740993
    radix: 0xFFFFFFFFFFFFFFFF
    negative: -1
    zero: 0
    nested: {values: [9007199254740993, -1, 0], absent: null}
    date: 2026-10-10
    quoted: '9007199254740993'
`

func nativeSourceTimeInput(t *testing.T, overrides string) (SuiteInput, execution.ExecutionRequest) {
	t.Helper()
	base := t.TempDir()
	fixtures := filepath.Join(base, "fixtures")
	require.NoError(t, os.Mkdir(fixtures, 0700))
	files := sourceTimeFiles{bytes: map[string][]byte{}, info: map[string]os.FileInfo{}, resolved: map[string]string{}}
	for path, body := range map[string]string{
		filepath.Join(base, "fixture.txt"): "fixture",
		filepath.Join(fixtures, "z.md"):    "suite instruction",
		filepath.Join(fixtures, "a.md"):    "task instruction",
	} {
		require.NoError(t, os.WriteFile(path, []byte(body), 0600))
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		files.bytes[path] = raw
		info, err := os.Stat(path)
		require.NoError(t, err)
		files.info[path] = info
		resolved, err := filepath.EvalSymlinks(path)
		require.NoError(t, err)
		files.resolved[path] = resolved
	}
	info, err := os.Stat(base)
	require.NoError(t, err)
	files.info[base] = info
	resolved, err := filepath.EvalSymlinks(base)
	require.NoError(t, err)
	files.resolved[base] = resolved
	spec, err := models.ParseEvalSpecOffline([]byte(`schemaVersion: '2.0'
name: source-time
scenario: source-time
version: '1.0'
config:
  executor: copilot-sdk
  model: offline-model
  reasoning_effort: low
  disabled_skills: ['*']
  timeout_seconds: 30
  first_event_timeout_seconds: 5
  trials_per_task: 2
  max_attempts: 2
  instruction_files: [z.md, a.md, z.md]
graders:
  - name: text
    type: text
    config:
      contains: [hello]
tasks: [task.yaml]
`), filepath.Join(base, "eval.yaml"))
	require.NoError(t, err)
	task, err := models.ParseTestCaseOffline([]byte(`id: task
name: source-time task
description: offline description
golden: true
instruction_files: [a.md]
inputs:
  prompt: hello
  workdir: nested
  files:
    - path: inline.txt
      content: inline
  context:
`+numericContext+overrides), filepath.Join(base, "task.yaml"), files.ReadFile)
	require.NoError(t, err)
	// Actual parser projection before context expansion retains exact integers.
	native := object(t, task)
	stimulus, ok := native["stimulus"].(map[string]any)
	require.True(t, ok)
	context, ok := stimulus["metadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, json.Number("9007199254740993"), context["large"])
	require.Equal(t, json.Number("18446744073709551615"), context["radix"])
	// Actual source-time bytes, Stat and symlink results are frozen; the shared
	// constructor cannot substitute a second live read for any file input.
	for path := range files.bytes {
		require.NoError(t, os.Remove(path))
	}
	// All metadata is from the strict actual parser. No numeric conversion is
	// applied to the source-time typed constructor input.
	request, err := orchestration.BuildCapturedNativeRequest(spec, task, base, fixtures, files)
	require.NoError(t, err)
	expectedContext := map[string]any{
		"fixture": "fixture.txt", "large": json.Number("9007199254740993"),
		"radix": json.Number("18446744073709551615"), "negative": json.Number("-1"), "zero": json.Number("0"),
		"nested": map[string]any{"values": []any{json.Number("9007199254740993"), json.Number("-1"), json.Number("0")}, "absent": nil},
		"date":   "2026-10-10T00:00:00Z", "quoted": "9007199254740993",
	}
	first := 5 * time.Second
	if task.FirstEventTimeoutSec != nil {
		first = time.Duration(*task.FirstEventTimeoutSec) * time.Second
	}
	expected := execution.ExecutionRequest{ModelID: "offline-model", ReasoningEffort: "low", Message: "hello",
		Context: expectedContext, WorkDir: "nested", TaskName: "source-time task", TaskDescription: "offline description",
		NoSkills: true, SkipWorkspaceCapture: true, FirstEventTimeout: first,
		ToolPolicy: &execution.ToolPolicy{Mode: execution.ToolPolicyDenyAll},
		Resources: []execution.ResourceFile{{Path: "fixture.txt", Content: []byte("fixture")},
			{Path: "inline.txt", Content: []byte("inline")},
			{Path: "z.md", Content: []byte("suite instruction")}, {Path: "a.md", Content: []byte("task instruction")},
			{Path: "z.md", Content: []byte("suite instruction")}, {Path: "a.md", Content: []byte("task instruction")}},
		Instructions: []execution.InstructionFile{{Path: "z.md", Content: []byte("suite instruction")},
			{Path: "a.md", Content: []byte("task instruction")}, {Path: "z.md", Content: []byte("suite instruction")},
			{Path: "a.md", Content: []byte("task instruction")}}}
	require.Equal(t, expected, *request, "independent complete actual constructor expectation")
	frozen, err := orchestration.FreezeCapturedNativeRequest(request)
	require.NoError(t, err)
	// Independent full CLIENT expected bytes, not a second call to the freezer.
	type plain execution.ExecutionRequest
	expectedFull := struct {
		*plain
		PermissionHandler any
		Tools             any
	}{plain: (*plain)(&expected)}
	require.Equal(t, oracleJSON(t, expectedFull), oracleJSON(t, json.RawMessage(frozen)))
	expectedRaw, err := json.Marshal(expectedFull)
	require.NoError(t, err)
	require.Equal(t, expectedRaw, frozen, "complete exported request bytes with explicit callback/tool nulls")
	// This source-time oracle does not invent detached Stat/WalkDir custody
	// for Prepared or claim G2 reconstruction.
	return SuiteInput{Spec: *spec, Tasks: []TaskInput{{Definition: *task, Request: *request}},
		Executable: []byte("actual-source-time-executable-fixture"), LockPresent: true, LockBytes: []byte("raw lock\n")}, expected
}

func oracleNativeProjection(t *testing.T, input SuiteInput, expectedRequest execution.ExecutionRequest) SourceProjection {
	t.Helper()
	task := input.Tasks[0].Definition
	settings := object(t, input.Spec.Config)
	settings["engine_type"], settings["model_id"] = "", ""
	delete(settings, "reasoning_effort")
	delete(settings, "instruction_files")
	timeout, first := 30, 5
	if task.TimeoutSec != nil {
		timeout = *task.TimeoutSec
	}
	if task.FirstEventTimeoutSec != nil {
		first = *task.FirstEventTimeoutSec
	}
	settings["timeout_sec"] = timeout
	if first == 0 {
		delete(settings, "first_event_timeout_sec")
	} else {
		settings["first_event_timeout_sec"] = first
	}
	plan := releasepolicy.ResolvedPlan{Kind: releasepolicy.PlanKind, Version: releasepolicy.Version,
		Tasks: []releasepolicy.TaskPlan{{ID: "task", Settings: releasepolicy.Settings{
			Engine: "copilot-sdk", Model: "offline-model", ReasoningEffort: "low", TimeoutSeconds: timeout,
			MaxAttempts: 2, TrialsPerTask: 2, NoSkills: true, OtherSettingsDigest: oracleJSON(t, settings)},
			ExpectedRuntime: releasepolicy.ExpectedRuntime{Availability: "unavailable",
				Reason: "runtime implementation and model version not observed by offline native snapshot"}}},
		Identities: []releasepolicy.Identity{}}
	definition := object(t, task)
	definition["context_root"] = "@resolved_context"
	delete(definition, "instruction_files")
	resourceIDs := []map[string]any{}
	for _, resource := range expectedRequest.Resources {
		resourceIDs = append(resourceIDs, map[string]any{"path": filepath.ToSlash(resource.Path), "source_digest": oracleBytes(resource.Content)})
	}
	instructionIDs := []map[string]any{}
	for _, instruction := range expectedRequest.Instructions {
		instructionIDs = append(instructionIDs, map[string]any{"path": filepath.ToSlash(instruction.Path), "source_digest": oracleBytes(instruction.Content)})
	}
	for _, cell := range []struct {
		domain, taskID string
		value          any
	}{
		{"task_definition", "task", definition},
		{"resolved_prompt", "task", map[string]any{"message": "hello", "context": expectedRequest.Context, "work_dir": "nested"}},
		{"fixture_inventory", "task", resourceIDs},
		{"instruction_inventory", "task", instructionIDs},
		{"grader_configuration", "task", map[string]any{"eval": input.Spec.Graders, "task": task.Validators, "expectation": task.Expectation}},
		{"dependency_mode", "task", map[string]any{"mode": "copilot_sdk_denied_tools_no_declared_dependencies", "no_skills": true}},
		{"grader_implementation", "", map[string]any{"path": "waza_executable", "source_digest": oracleBytes(input.Executable)}},
	} {
		digest := oracleJSON(t, map[string]any{"domain": cell.domain, "value": cell.value})
		plan.Identities = append(plan.Identities, releasepolicy.Identity{Domain: cell.domain, TaskID: cell.taskID, Availability: "available", Digest: &digest})
	}
	empty := oracleJSON(t, map[string]any{"domain": "rubric_inventory", "entries": []any{}})
	plan.Identities = append(plan.Identities, releasepolicy.Identity{Domain: "rubric_inventory", Availability: "not_applicable", Digest: &empty})
	lock := oracleJSON(t, map[string]any{"domain": "lock_inventory",
		"value": []map[string]any{{"path": models.LockfileName, "source_digest": oracleBytes(input.LockBytes)}}})
	plan.Identities = append(plan.Identities, releasepolicy.Identity{Domain: "lock_inventory", Availability: "available", Digest: &lock})
	return SourceProjection{Arm: releasepolicy.PolicyArm{Plan: plan, Digest: oracleJSON(t, plan)}, GoldenIDs: []string{"task"}}
}

func TestNativeSourceTimeConstructorAndEveryProjectionOracle(t *testing.T) {
	for _, override := range []string{"", "timeout_seconds: 12\nfirst_event_timeout_seconds: 0\n", "timeout_seconds: 45\nfirst_event_timeout_seconds: 7\n"} {
		t.Run(fmt.Sprintf("overrides_%q", override), func(t *testing.T) {
			input, expectedRequest := nativeSourceTimeInput(t, override)
			expected := oracleNativeProjection(t, input, expectedRequest)
			actual, err := NativePlan(input)
			require.NoError(t, err)
			require.Equal(t, expected, actual, "all ordered task/suite domains, settings/runtime, arm digest and golden")
			require.Equal(t, expectedRequest, input.Tasks[0].Request, "pure projector must not mutate request")
			require.Equal(t, json.Number("9007199254740993"), input.Tasks[0].Definition.Stimulus.Metadata["large"])
		})
	}
}

func TestNativeStrictParserNumbers(t *testing.T) {
	for _, token := range []string{"1.0", "1e2", "18446744073709551616", "-9223372036854775809", ".nan", ".inf"} {
		t.Run(token, func(t *testing.T) {
			_, err := models.ParseTestCaseOffline([]byte("id: task\nname: task\ninputs:\n  prompt: hello\n  context:\n    value: "+token+"\n"),
				"task.yaml", func(string) ([]byte, error) { return nil, fmt.Errorf("unexpected source read") })
			require.Error(t, err)
		})
	}
}

func TestNativeRejectsUnsupportedNotRelabeled(t *testing.T) {
	changes := map[string]func(*SuiteInput){
		"engine":                   func(i *SuiteInput) { i.Spec.Config.EngineType = "mock" },
		"auto_model":               func(i *SuiteInput) { i.Spec.Config.ModelID = "auto" },
		"skills":                   func(i *SuiteInput) { i.Spec.SkillName = "skill" },
		"dependency":               func(i *SuiteInput) { i.Spec.Config.ServerConfigs = map[string]any{"external": true} },
		"generated":                func(i *SuiteInput) { i.Spec.TasksFrom = "tasks.json" },
		"invalid_reasoning":        func(i *SuiteInput) { i.Spec.Config.ReasoningEffort = "unsupported" },
		"invalid_trials":           func(i *SuiteInput) { i.Spec.Config.TrialsPerTask = 0 },
		"invalid_timeout":          func(i *SuiteInput) { i.Spec.Config.TimeoutSec = 0 },
		"invalid_first_event":      func(i *SuiteInput) { i.Spec.Config.FirstEventTimeoutSec = -1 },
		"task_zero_timeout":        func(i *SuiteInput) { i.Tasks[0].Definition.TimeoutSec = new(0) },
		"task_negative_first":      func(i *SuiteInput) { i.Tasks[0].Definition.FirstEventTimeoutSec = new(-1) },
		"encoding_spec":            func(i *SuiteInput) { i.Spec.Config.ServerConfigs = map[string]any{"bad": make(chan int)} },
		"custom_spec_value":        func(i *SuiteInput) { i.Spec.Config.ServerConfigs = map[string]any{"bad": unsupportedJSON{}} },
		"expectation":              func(i *SuiteInput) { i.Tasks[0].Definition.Expectation.MustInclude = []string{"hello"} },
		"grader":                   func(i *SuiteInput) { i.Spec.Graders[0].Kind = models.GraderKind("script") },
		"request_model":            func(i *SuiteInput) { i.Tasks[0].Request.ModelID = "different" },
		"request_reasoning":        func(i *SuiteInput) { i.Tasks[0].Request.ReasoningEffort = "high" },
		"request_first_event":      func(i *SuiteInput) { i.Tasks[0].Request.FirstEventTimeout = -1 },
		"request_skills":           func(i *SuiteInput) { i.Tasks[0].Request.NoSkills = false },
		"request_policy":           func(i *SuiteInput) { i.Tasks[0].Request.ToolPolicy = nil },
		"request_tools":            func(i *SuiteInput) { i.Tasks[0].Request.ToolPolicy = execution.NewToolPolicy(nil) },
		"request_stream":           func(i *SuiteInput) { i.Tasks[0].Request.Streaming = true },
		"request_message_mode":     func(i *SuiteInput) { i.Tasks[0].Request.MessageMode = "enqueue" },
		"request_session":          func(i *SuiteInput) { i.Tasks[0].Request.SessionID = "old" },
		"request_workspace":        func(i *SuiteInput) { i.Tasks[0].Request.WorkspaceDir = "old" },
		"request_capture":          func(i *SuiteInput) { i.Tasks[0].Request.SkipWorkspaceCapture = false },
		"request_ephemeral":        func(i *SuiteInput) { i.Tasks[0].Request.EphemeralSession = true },
		"request_cancel":           func(i *SuiteInput) { i.Tasks[0].Request.CancelOnSkillInvocation = true },
		"request_trigger":          func(i *SuiteInput) { i.Tasks[0].Request.TriggerSkillRouting = true },
		"request_suppress":         func(i *SuiteInput) { i.Tasks[0].Request.SuppressSkillBody = true },
		"request_skill_name":       func(i *SuiteInput) { i.Tasks[0].Request.SkillName = "skill" },
		"request_command_base":     func(i *SuiteInput) { i.Tasks[0].Request.CommandMocksBaseDir = "old" },
		"disabled":                 func(i *SuiteInput) { i.Tasks[0].Definition.Active = new(false) },
		"duplicate":                func(i *SuiteInput) { i.Tasks = append(i.Tasks, i.Tasks[0]) },
		"empty_id":                 func(i *SuiteInput) { i.Tasks[0].Definition.TestID = "" },
		"no_tasks":                 func(i *SuiteInput) { i.Tasks = nil },
		"empty_executable":         func(i *SuiteInput) { i.Executable = nil },
		"absent_lock_bytes":        func(i *SuiteInput) { i.LockPresent = false },
		"authored_float":           func(i *SuiteInput) { i.Tasks[0].Definition.Stimulus.Metadata["large"] = 1.0 },
		"rounded_request":          func(i *SuiteInput) { i.Tasks[0].Request.Context["large"] = float64(9007199254740993) },
		"nested_float":             func(i *SuiteInput) { i.Tasks[0].Request.Context["nested"] = []any{json.Number("0"), 1.0} },
		"number_exponent":          func(i *SuiteInput) { i.Tasks[0].Request.Context["large"] = json.Number("9e3") },
		"number_overflow":          func(i *SuiteInput) { i.Tasks[0].Request.Context["large"] = json.Number("18446744073709551616") },
		"number_negative_overflow": func(i *SuiteInput) { i.Tasks[0].Request.Context["large"] = json.Number("-9223372036854775809") },
		"number_invalid":           func(i *SuiteInput) { i.Tasks[0].Request.Context["large"] = json.Number("01") },
		"metadata_type":            func(i *SuiteInput) { i.Tasks[0].Request.Context["large"] = make(chan int) },
		"custom_metadata_value":    func(i *SuiteInput) { i.Tasks[0].Request.Context["large"] = unsupportedJSON{} },
		"metadata_cycle": func(i *SuiteInput) {
			cycle := map[string]any{}
			cycle["self"] = cycle
			i.Tasks[0].Request.Context = cycle
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			input, _ := nativeSourceTimeInput(t, "")
			change(&input)
			actual, err := NativePlan(input)
			require.Error(t, err)
			require.Equal(t, SourceProjection{}, actual, "no partial projection or relabeled success")
		})
	}
}

func TestNativeActualTaskEntryAndOutputProjectionCaps(t *testing.T) {
	input, _ := nativeSourceTimeInput(t, "")
	task := input.Tasks[0]
	input.Tasks = make([]TaskInput, nativeLimits.tasks)
	for index := range input.Tasks {
		input.Tasks[index] = task
		input.Tasks[index].Definition.TestID = fmt.Sprintf("task-%04d", index)
	}
	result, err := NativePlan(input)
	require.NoError(t, err)
	require.Len(t, result.Arm.Plan.Tasks, 1024)
	input.Tasks = append(input.Tasks, task)
	result, err = NativePlan(input)
	require.ErrorContains(t, err, "task limit")
	require.Equal(t, SourceProjection{}, result)

	input.Tasks = []TaskInput{task}
	// Exact inclusive typed-entry cap: executable + lock + ordered request
	// resources + instructions. It is not a directory-trace custody assertion.
	input.Tasks[0].Request.Resources = make([]execution.ResourceFile, nativeLimits.entries-2-len(task.Request.Instructions))
	for index := range input.Tasks[0].Request.Resources {
		input.Tasks[0].Request.Resources[index].Path = "inline"
	}
	result, err = NativePlan(input)
	require.NoError(t, err)
	input.Tasks[0].Request.Resources = append(input.Tasks[0].Request.Resources, execution.ResourceFile{Path: "one-too-many"})
	result, err = NativePlan(input)
	require.ErrorContains(t, err, "entry limit")
	require.Equal(t, SourceProjection{}, result)

	input.Tasks = []TaskInput{task}
	// Repetition of a large task ID in all six identity rows must not evade
	// the final 16 MiB complete-projection cap through small input projections.
	input.Tasks[0].Definition.TestID = strings.Repeat("t", 3<<20)
	result, err = NativePlan(input)
	require.ErrorContains(t, err, "native source projection exceeds bounded limit")
	require.Equal(t, SourceProjection{}, result)
}

func TestNativeInclusiveLocalLimits(t *testing.T) {
	input, _ := nativeSourceTimeInput(t, "")
	bound := nativeLimits
	bound.executable = len(input.Executable)
	require.NoError(t, validateNative(input, bound))
	bound.executable--
	require.ErrorContains(t, validateNative(input, bound), "executable source limit")
	bound = nativeLimits
	bound.source = len("suite instruction")
	require.NoError(t, validateNative(input, bound))
	bound.source--
	require.ErrorContains(t, validateNative(input, bound), "source limit")
	bound = nativeLimits
	bound.total = len(input.Executable) + len(input.LockBytes)
	bound.entries = 2
	for _, resource := range input.Tasks[0].Request.Resources {
		bound.total += len(resource.Content)
		bound.entries++
	}
	for _, instruction := range input.Tasks[0].Request.Instructions {
		bound.total += len(instruction.Content)
		bound.entries++
	}
	require.NoError(t, validateNative(input, bound))
	bound.total--
	require.ErrorContains(t, validateNative(input, bound), "aggregate source byte limit")
	bound.total++
	bound.entries--
	require.ErrorContains(t, validateNative(input, bound), "entry limit")
	bound = nativeLimits
	rawSpec, err := json.Marshal(input.Spec)
	require.NoError(t, err)
	rawTask, err := json.Marshal(input.Tasks[0].Definition)
	require.NoError(t, err)
	rawRequest, err := orchestration.FreezeCapturedNativeRequest(&input.Tasks[0].Request)
	require.NoError(t, err)
	bound.projection = len(rawSpec) + len(rawTask) + len(rawRequest)
	require.NoError(t, validateNative(input, bound))
	bound.projection--
	require.ErrorContains(t, validateNative(input, bound), "projection exceeds bounded limit")
	bound.projection = len(rawSpec) - 1
	require.ErrorContains(t, validateNative(input, bound), "projection exceeds bounded limit")
	bound = nativeLimits
	bound.tasks = 1
	require.NoError(t, validateNative(input, bound))
	input.Tasks = append(input.Tasks, input.Tasks[0])
	require.ErrorContains(t, validateNative(input, bound), "task limit")
	require.Equal(t, limits{16 << 20, 128 << 20, 256 << 20, 16 << 20, 1024, 16384}, nativeLimits)
}

func TestNativeNullEmptyOrderingGoldenAndDomainControls(t *testing.T) {
	input, _ := nativeSourceTimeInput(t, "")
	first, err := NativePlan(input)
	require.NoError(t, err)
	secondTask := input.Tasks[0]
	secondTask.Definition.TestID = "a-task"
	secondTask.Definition.Golden = false
	input.Tasks = append(input.Tasks, secondTask)
	result, err := NativePlan(input)
	require.NoError(t, err)
	require.Equal(t, []string{"task", "a-task"}, []string{result.Arm.Plan.Tasks[0].ID, result.Arm.Plan.Tasks[1].ID})
	require.Equal(t, []string{"task"}, result.GoldenIDs)
	for n, domain := range []string{"task_definition", "resolved_prompt", "fixture_inventory", "instruction_inventory", "grader_configuration", "dependency_mode"} {
		require.Equal(t, domain, result.Arm.Plan.Identities[n].Domain)
		require.Equal(t, domain, result.Arm.Plan.Identities[n+6].Domain)
		require.Equal(t, "a-task", result.Arm.Plan.Identities[n+6].TaskID)
	}
	require.Equal(t, "grader_implementation", result.Arm.Plan.Identities[12].Domain)
	input.Tasks[1].Definition.Golden = true
	bothGolden, err := NativePlan(input)
	require.NoError(t, err)
	require.Equal(t, []string{"task", "a-task"}, bothGolden.GoldenIDs, "per-arm owning order, not sorting")
	union, err := GoldenUnion(bothGolden, first)
	require.NoError(t, err)
	require.Equal(t, []string{"a-task", "task"}, union, "both-arm sorted deduplicated union")
	unionJSON, err := json.Marshal(union)
	require.NoError(t, err)
	for _, supplied := range [][]string{nil, {}, {"task"}, {"task", "a-task"}, {"a-task", "task", "extra"}} {
		suppliedJSON, err := json.Marshal(supplied)
		require.NoError(t, err)
		require.NotEqual(t, unionJSON, suppliedJSON, "omitted/added/reordered/null policy lists must not be normalized into equality")
	}
	// Ordered resource and instruction multiplicity are not globally sorted.
	input.Tasks = input.Tasks[:1]
	input.Tasks[0].Request.Resources[0], input.Tasks[0].Request.Resources[1] = input.Tasks[0].Request.Resources[1], input.Tasks[0].Request.Resources[0]
	reordered, err := NativePlan(input)
	require.NoError(t, err)
	require.NotEqual(t, first.Arm.Plan.Identities[2].Digest, reordered.Arm.Plan.Identities[2].Digest)
	input.Tasks[0].Request.Instructions[0], input.Tasks[0].Request.Instructions[1] = input.Tasks[0].Request.Instructions[1], input.Tasks[0].Request.Instructions[0]
	reordered, err = NativePlan(input)
	require.NoError(t, err)
	require.NotEqual(t, first.Arm.Plan.Identities[3].Digest, reordered.Arm.Plan.Identities[3].Digest)
	input.LockPresent, input.LockBytes = false, nil
	absent, err := NativePlan(input)
	require.NoError(t, err)
	require.Equal(t, "not_applicable", absent.Arm.Plan.Identities[8].Availability)
	require.NotEqual(t, first.Arm.Plan.Identities[8].Digest, absent.Arm.Plan.Identities[8].Digest)
	input.LockPresent = true
	emptyLock, err := NativePlan(input)
	require.NoError(t, err)
	require.Equal(t, "available", emptyLock.Arm.Plan.Identities[8].Availability)
	require.NotEqual(t, absent.Arm.Plan.Identities[8].Digest, emptyLock.Arm.Plan.Identities[8].Digest)
	input.Tasks[0].Request.Context = nil
	nilContext, err := NativePlan(input)
	require.NoError(t, err)
	input.Tasks[0].Request.Context = map[string]any{}
	emptyContext, err := NativePlan(input)
	require.NoError(t, err)
	require.NotEqual(t, nilContext.Arm.Plan.Identities[1].Digest, emptyContext.Arm.Plan.Identities[1].Digest)
	input.Tasks[0].Definition.Validators = []models.ValidatorInline{}
	emptyValidators, err := NativePlan(input)
	require.NoError(t, err)
	require.NotEqual(t, emptyContext.Arm.Plan.Identities[4].Digest, emptyValidators.Arm.Plan.Identities[4].Digest)
	input.Tasks[0].Request.Resources = nil
	input.Tasks[0].Request.Instructions = nil
	nilInventory, err := NativePlan(input)
	require.NoError(t, err)
	input.Tasks[0].Request.Resources = []execution.ResourceFile{}
	input.Tasks[0].Request.Instructions = []execution.InstructionFile{}
	emptyInventory, err := NativePlan(input)
	require.NoError(t, err)
	require.Equal(t, nilInventory.Arm.Plan.Identities[2].Digest, emptyInventory.Arm.Plan.Identities[2].Digest, "inventory always initializes []")
	require.Equal(t, nilInventory.Arm.Plan.Identities[3].Digest, emptyInventory.Arm.Plan.Identities[3].Digest, "instruction inventory always initializes []")
	input.Tasks[0].Definition.Golden = false
	none, err := NativePlan(input)
	require.NoError(t, err)
	require.NotNil(t, none.GoldenIDs)
	union, err = GoldenUnion(first, none)
	require.NoError(t, err)
	require.Equal(t, []string{"task"}, union)
	union, err = GoldenUnion(none, none)
	require.NoError(t, err)
	require.Equal(t, []string{}, union)
	stale := first
	stale.Arm.Digest.SHA256 = strings.Repeat("0", 64)
	union, err = GoldenUnion(first, stale)
	require.Error(t, err)
	require.Nil(t, union)
	for _, incomplete := range []SourceProjection{{}, {Arm: first.Arm, GoldenIDs: nil}, {GoldenIDs: []string{}}} {
		union, err := GoldenUnion(first, incomplete)
		require.Error(t, err)
		require.Nil(t, union)
	}
	identity, err := project("domain", "one", map[string]any{"number": json.Number("9007199254740993")})
	require.NoError(t, err)
	otherTask, err := project("domain", "two", map[string]any{"number": json.Number("9007199254740993")})
	require.NoError(t, err)
	require.Equal(t, identity.Digest, otherTask.Digest, "task ID remains outside domain wrapper")
	otherDomain, err := project("other", "one", map[string]any{"number": json.Number("9007199254740993")})
	require.NoError(t, err)
	require.NotEqual(t, identity.Digest, otherDomain.Digest)
	_, err = project("domain", "one", make(chan int))
	require.ErrorContains(t, err, "projecting domain/one")
}

func TestMockDoesNotAcquireNativeBoundsOrNormalization(t *testing.T) {
	input := SuiteInput{Spec: models.EvalSpec{Config: models.Config{EngineType: "mock"}},
		Tasks: []TaskInput{{Definition: models.TestCase{TestID: "legacy"},
			Request: execution.ExecutionRequest{Message: strings.Repeat("x", nativeLimits.projection+1),
				Context: map[string]any{"rounded": float64(9007199254740993)}}}}}
	result, err := MockPlan(input)
	require.NoError(t, err)
	require.Equal(t, "mock", result.Arm.Plan.Tasks[0].Settings.Engine)
	require.Equal(t, "mock_no_provider", result.Arm.Plan.Tasks[0].ExpectedRuntime.ModelVersion)
	require.Equal(t, []string{}, result.GoldenIDs)
	input.Tasks[0].Request.Context["invalid"] = make(chan int)
	result, err = MockPlan(input)
	require.Error(t, err)
	require.Equal(t, SourceProjection{}, result)
}
