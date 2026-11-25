package judgeevidence

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func observedInput() CaptureInput {
	return CaptureInput{
		Origin: models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"},
		Task:   "Create a report", Output: "", OutputPresent: true, ExecutionMode: "mock",
		Events: []copilot.SessionEvent{
			{Data: &copilot.UserMessageData{Content: "Create a report"}},
			{Data: &copilot.AssistantMessageData{Content: "Report completed"}},
		},
	}
}

func TestFixtureAccountingHonorsSerializedProjectionBudget(t *testing.T) {
	// The 16,384-node cap is unchanged; fields omitted by SDK JSON tags do not consume it.
	tests := []struct {
		count        int
		wantCaptured bool
	}{
		{16333, true},
		{16334, true},
		{16343, true},
		{16344, false},
	}
	for _, test := range tests {
		count := test.count
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			input := observedInput()
			input.Selection.Files = []string{"report.txt"}
			input.Files = &models.JudgeFileCapture{
				Files:     []models.JudgeEvidenceFile{{Path: "report.txt", SHA256: fmt.Sprintf("%x", sha256.Sum256(nil))}},
				Redaction: models.EvidenceRedaction{Policy: "default"},
			}
			input.Events = append(input.Events,
				copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{
					ToolCallID: "call", ToolName: "fixture", Arguments: map[string]any{"items": make([]string, count)},
				}},
				copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{
					ToolCallID: "call", Success: true, Result: &copilot.ToolExecutionCompleteResult{Content: ""},
				}},
			)
			bundle := Capture(input)
			err := Verify(bundle, input.Origin)
			if test.wantCaptured {
				require.NoError(t, err)
				require.NotNil(t, bundle.Files.Items)
				require.Len(t, *bundle.Files.Items, 1)
			} else {
				require.Error(t, err)
				require.Equal(t, "unavailable", bundle.Files.Availability)
			}
		})
	}
}

func TestProjectionBudgetMatchesJSONOmissionSemantics(t *testing.T) {
	type fields struct {
		EmptyOmitted string `json:"emptyOmitted,omitempty"`
		ZeroOmitted  int    `json:"zeroOmitted,omitzero"`
		Ignored      string `json:"-"`
		Content      string `json:"content"`
	}

	budget := projectionBudget{bytes: 64, nodes: 8}
	require.NoError(t, budget.inspect(reflect.ValueOf(fields{}), 0))
	require.Equal(t, 6, budget.nodes, "empty omitempty, zero omitzero, and json- fields do not consume nodes")
	require.Equal(t, 64, budget.bytes)
	encoded, err := json.Marshal(fields{})
	require.NoError(t, err)
	require.JSONEq(t, `{"content":""}`, string(encoded))

	budget = projectionBudget{bytes: 64, nodes: 8}
	require.NoError(t, budget.inspect(reflect.ValueOf(fields{EmptyOmitted: "x", ZeroOmitted: 1}), 0))
	require.Equal(t, 4, budget.nodes, "non-empty fields still consume projection nodes")
	require.Equal(t, 31, budget.bytes)
	encoded, err = json.Marshal(fields{EmptyOmitted: "x", ZeroOmitted: 1})
	require.NoError(t, err)
	require.JSONEq(t, `{"emptyOmitted":"x","zeroOmitted":1,"content":""}`, string(encoded))
}

func TestFixtureLiteralSelectionUnionIsBoundedDetachedAndNotSerialized(t *testing.T) {
	first, second := []string{"PROJ-1"}, []string{"fixture@example.invalid"}
	configs := []models.GraderConfig{{Parameters: models.PromptGraderParameters{
		EvidenceContext: &models.PromptEvidenceContext{NonSecretLiterals: &first},
	}}}
	task := &models.TestCase{Validators: []models.ValidatorInline{{Parameters: models.PromptGraderParameters{
		EvidenceContext: &models.PromptEvidenceContext{NonSecretLiterals: &second},
	}}}}
	selection, err := Selection(configs, task)
	require.NoError(t, err)
	require.Equal(t, []string{"PROJ-1", "fixture@example.invalid"}, *selection.NonSecretLiterals)
	first[0], second[0] = "MUTATED-1", "MUTATED-2"
	input := observedInput()
	input.Selection = selection
	bundle := Capture(input)
	require.NoError(t, Verify(bundle, input.Origin))
	require.Equal(t, &models.JudgeFixtureLiterals{DeclaredCount: 2}, bundle.FixtureLiterals)
	raw, err := json.Marshal(bundle)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PROJ-1")
	require.NotContains(t, string(raw), "fixture@example.invalid")
	require.NotContains(t, string(raw), "non_secret_literals")
	var restored models.JudgeEvidence
	require.NoError(t, json.Unmarshal(raw, &restored))
	delivery, err := Delivery(&restored, input.Origin, selection)
	require.NoError(t, err)
	require.Nil(t, delivery.Selection.NonSecretLiterals)
	require.Equal(t, bundle.FixtureLiterals, delivery.FixtureLiterals)
	for i := range 64 {
		first = append(first, fmt.Sprintf("FIRST-%d", i))
	}
	require.Error(t, ValidateSelection(configs, task))
	first = first[:64]
	require.Error(t, ValidateSelection(configs, task), "individually valid lists must not exceed the union bound")
}

func TestFixtureLiteralsDoNotExemptBindingOrInvalidDeclarations(t *testing.T) {
	input := observedInput()
	values := []string{"fixture@example.invalid"}
	input.Selection.NonSecretLiterals = &values
	input.Origin.EvalID = values[0]
	bundle := Capture(input)
	require.Error(t, Verify(bundle, input.Origin))
	require.Empty(t, bundle.Origin.EvalID, "declarations are not identity provenance")
	input = observedInput()
	values = []string{"INVALID DECLARATION CANARY"}
	input.Selection.NonSecretLiterals = &values
	bundle = Capture(input)
	require.Error(t, Verify(bundle, input.Origin))
	encoded, err := json.Marshal(bundle)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), values[0])
	require.Contains(t, string(encoded), "invalid_fixture_literals")
}

func TestFixtureLiteralsApplyToSelectedFileContentNotPaths(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("secure selected-file capture requires Linux/macOS")
	}
	root := t.TempDir()
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	values := []string{sha, "PROJ-1", "fixture@example.invalid"}
	selection := models.PromptEvidenceContext{Files: []string{"report.json"}, NonSecretLiterals: &values}
	for _, test := range []struct {
		body string
		pass bool
	}{
		{`{"projectKey":"PROJ-1","sha":"` + sha + `","contact":"fixture@example.invalid"}`, true},
		{`{"projectKey":"PRIVATE_CREDENTIAL_CANARY","sha":"` + sha + `"}`, false},
		{`{"sha":"` + sha + `aa"}`, false},
		{`{"contact":"private@example.invalid"}`, false},
	} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "report.json"), []byte(test.body), 0600))
		input := observedInput()
		input.Selection, input.Files = selection, CaptureFiles(root, selection)
		bundle := Capture(input)
		if test.pass {
			require.NoError(t, Verify(bundle, input.Origin))
			require.Positive(t, bundle.FixtureLiterals.PreservedMatches)
			require.Contains(t, (*bundle.Files.Items)[0].Content, sha)
			require.Contains(t, (*bundle.Files.Items)[0].Content, `"projectKey":"PROJ-1"`)
		} else {
			require.Error(t, Verify(bundle, input.Origin))
			require.NotEmpty(t, bundle.Diagnostics)
			raw, err := json.Marshal(bundle)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "PRIVATE_CREDENTIAL_CANARY")
		}
	}
	selection.Files = []string{sha + ".txt"}
	require.NoError(t, os.WriteFile(filepath.Join(root, selection.Files[0]), []byte("fixture"), 0600))
	require.NotEmpty(t, CaptureFiles(root, selection).Diagnostics, "fixture declarations must not weaken path admission")
}

func TestCaptureKnownEmptyAndUnobservedAreDistinct(t *testing.T) {
	input := observedInput()
	bundle := Capture(input)
	require.NoError(t, Verify(bundle, input.Origin))
	require.Equal(t, "", *bundle.Output.Items)
	require.Equal(t, "captured", bundle.ToolEvents.Availability)
	require.Equal(t, "complete", bundle.ToolEvents.Completeness)
	require.NotNil(t, bundle.ToolEvents.Items)
	require.NotNil(t, *bundle.ToolEvents.Items)
	require.Empty(t, *bundle.ToolEvents.Items)
	raw, err := json.Marshal(bundle)
	require.NoError(t, err)
	var decoded models.JudgeEvidence
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.NoError(t, Verify(&decoded, input.Origin))
	input.Events = nil
	missing := Capture(input)
	require.Equal(t, "unavailable", missing.ToolEvents.Availability)
	require.Nil(t, missing.ToolEvents.Items)
	require.Error(t, Verify(missing, input.Origin))
	input = observedInput()
	input.OutputPresent = false
	require.Error(t, Verify(Capture(input), input.Origin))
}

func TestCaptureOnlyVisibleConversationAndCanonicalTools(t *testing.T) {
	input := observedInput()
	args := map[string]any{"count": json.Number("9007199254740993")}
	input.Events = append(input.Events[:1],
		copilot.SessionEvent{Data: &copilot.AssistantReasoningData{Content: "PRIVATE_REASONING_CANARY"}},
		copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{ToolCallID: "call", ToolName: "query", Arguments: args}},
		copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "call", Success: false,
			Result: &copilot.ToolExecutionCompleteResult{Content: ""}}},
		copilot.SessionEvent{Data: &copilot.AssistantMessageData{Content: "The tool failed"}},
	)
	bundle := Capture(input)
	require.NoError(t, Verify(bundle, input.Origin))
	require.Len(t, *bundle.Messages.Items, 2)
	tools := *bundle.ToolEvents.Items
	require.Len(t, tools, 1)
	require.False(t, tools[0].Success)
	require.NotNil(t, tools[0].Result, "an observed empty result is not an absent result")
	args["count"] = 0
	copied, ok := tools[0].Args.(map[string]any)
	require.True(t, ok)
	require.Equal(t, json.Number("9007199254740993"), copied["count"])
	encoded, err := json.Marshal(bundle)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PRIVATE_REASONING_CANARY")
	for _, mutate := range []func(*CaptureInput){
		func(in *CaptureInput) { in.Events = append(in.Events[:3], in.Events[4:]...) },
		func(in *CaptureInput) {
			in.Events[3] = copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "call"}}
		},
		func(in *CaptureInput) { in.Events = append(in.Events, in.Events[2]) },
	} {
		copy := input
		copy.Events = append([]copilot.SessionEvent(nil), input.Events...)
		mutate(&copy)
		failed := Capture(copy)
		require.Error(t, Verify(failed, input.Origin))
		require.Equal(t, "missing_tool_result", failed.ToolEvents.Reason)
	}
}

func TestCaptureRequiredRedactionAndEncodedLimits(t *testing.T) {
	for _, value := range []string{
		"Bearer " + strings.Repeat("s", 32),
		strings.Repeat("x", models.JudgeEvidenceMaxBytes+1),
		strings.Repeat("<", models.JudgeEvidenceMaxBytes/4),
	} {
		input := observedInput()
		input.Output = value
		bundle := Capture(input)
		require.Error(t, Verify(bundle, input.Origin))
		data, err := json.Marshal(bundle)
		require.NoError(t, err)
		require.LessOrEqual(t, len(data), models.JudgeEvidenceMaxBytes)
		require.NotContains(t, string(data), value)
		require.NotEmpty(t, bundle.Diagnostics)
	}

	input := observedInput()
	input.Events = append(input.Events,
		copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{ToolCallID: "call", ToolName: "query",
			Arguments: map[string]any{"password": "SHORT_SENSITIVE_CANARY"}}},
		copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{ToolCallID: "call",
			Result: &copilot.ToolExecutionCompleteResult{Content: "ok"}}},
	)
	bundle := Capture(input)
	require.Error(t, Verify(bundle, input.Origin))
	encoded, err := json.Marshal(bundle)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "SHORT_SENSITIVE_CANARY")
}

func TestCaptureObservedToolFailuresWithoutInventingResults(t *testing.T) {
	for _, test := range []struct {
		name    string
		success bool
		result  *copilot.ToolExecutionCompleteResult
		failure *copilot.ToolExecutionCompleteError
		valid   bool
	}{
		{"error only", false, nil, &copilot.ToolExecutionCompleteError{Message: "dependency unavailable"}, true},
		{"error and result", false, &copilot.ToolExecutionCompleteResult{Content: "failed"}, &copilot.ToolExecutionCompleteError{Message: "dependency unavailable"}, true},
		{"missing result and error", false, nil, nil, false},
		{"empty error", false, nil, &copilot.ToolExecutionCompleteError{}, false},
		{"successful error-only completion", true, nil, &copilot.ToolExecutionCompleteError{Message: "dependency unavailable"}, false},
		{"redacted error", false, nil, &copilot.ToolExecutionCompleteError{Message: "Bearer " + strings.Repeat("s", 32)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := observedInput()
			input.Events = append(input.Events,
				copilot.SessionEvent{Data: &copilot.ToolExecutionStartData{ToolCallID: "call", ToolName: "request"}},
				copilot.SessionEvent{Data: &copilot.ToolExecutionCompleteData{
					ToolCallID: "call", Success: test.success, Result: test.result, Error: test.failure,
				}},
			)
			bundle := Capture(input)
			if !test.valid {
				require.Error(t, Verify(bundle, input.Origin))
				return
			}
			require.NoError(t, Verify(bundle, input.Origin))
			require.Len(t, *bundle.ToolEvents.Items, 1)
			event := (*bundle.ToolEvents.Items)[0]
			require.False(t, event.Success)
			require.Equal(t, test.failure.Message, event.Error)
			if test.result == nil {
				require.Nil(t, event.Result)
			} else {
				require.NotNil(t, event.Result)
			}
			data, err := json.Marshal(bundle)
			require.NoError(t, err)
			var restored models.JudgeEvidence
			require.NoError(t, json.Unmarshal(data, &restored))
			require.NoError(t, Verify(&restored, input.Origin))
			delivery, err := Delivery(&restored, input.Origin, models.PromptEvidenceContext{})
			require.NoError(t, err)
			require.Equal(t, event.Error, (*delivery.ToolEvents.Items)[0].Error)
			require.Equal(t, event.Result, (*delivery.ToolEvents.Items)[0].Result)
		})
	}
}

func TestFileCaptureBoundariesAndPerJudgeDetachedSelections(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("secure selected-file capture requires Linux/macOS")
	}
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("FIRST_FILE_CANARY"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "b.txt"), []byte("SECOND_FILE_CANARY"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "empty.txt"), nil, 0600))
	selection := models.PromptEvidenceContext{Files: []string{"a.txt", "b.txt", "empty.txt"}}
	input := observedInput()
	input.Selection, input.Files = selection, CaptureFiles(root, selection)
	bundle := Capture(input)
	require.NoError(t, Verify(bundle, input.Origin))
	first, err := Delivery(bundle, input.Origin, models.PromptEvidenceContext{Files: []string{"a.txt", "empty.txt"}})
	require.NoError(t, err)
	raw, err := json.Marshal(first)
	require.NoError(t, err)
	require.Contains(t, string(raw), "FIRST_FILE_CANARY")
	require.NotContains(t, string(raw), "SECOND_FILE_CANARY")
	require.NotContains(t, string(raw), "b.txt")
	(*first.Files.Items)[0].Content = "mutated"
	(*first.Messages.Items)[0].Content = "mutated"
	require.Equal(t, "FIRST_FILE_CANARY", (*bundle.Files.Items)[0].Content)
	require.Equal(t, "Create a report", (*bundle.Messages.Items)[0].Content)
	second, err := Delivery(bundle, input.Origin, models.PromptEvidenceContext{Files: []string{"b.txt"}})
	require.NoError(t, err)
	require.NotEqual(t, first.SHA256, second.SHA256)
	_, err = Delivery(bundle, input.Origin, models.PromptEvidenceContext{Files: []string{"not-captured.txt"}})
	require.ErrorContains(t, err, "not captured")
	_, err = Delivery(bundle, input.Origin, models.PromptEvidenceContext{Files: []string{"a.txt"}, EvaluatorOnly: []string{"a.txt"}})
	require.ErrorContains(t, err, "evaluator-only")
	wrongOrigin := input.Origin
	wrongOrigin.AttemptCount++
	require.ErrorContains(t, Verify(bundle, wrongOrigin), "invalid_binding")
	(*bundle.Files.Items)[0].Content = "tampered"
	require.ErrorContains(t, Verify(bundle, input.Origin), "digest mismatch")
	for _, size := range []int{models.JudgeEvidenceMaxFileBytes, models.JudgeEvidenceMaxFileBytes + 1} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "bound.txt"), []byte(strings.Repeat("x", size)), 0600))
		capture := CaptureFiles(root, models.PromptEvidenceContext{Files: []string{"bound.txt"}})
		if size == models.JudgeEvidenceMaxFileBytes {
			require.Empty(t, capture.Diagnostics)
			require.Len(t, capture.Files, 1)
			require.Len(t, capture.Files[0].Content, size)
		} else {
			require.NotEmpty(t, capture.Diagnostics)
		}
	}
}

func TestFileCaptureFailsClosedAndJSONFormattingIsNotRedaction(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("secure selected-file capture requires Linux/macOS")
	}
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "report.json"), []byte("{\n \"ok\": true\n}\n"), 0600))
	selection := models.PromptEvidenceContext{Files: []string{"report.json"}}
	input := observedInput()
	input.Selection, input.Files = selection, CaptureFiles(root, selection)
	require.Empty(t, input.Files.Diagnostics)
	require.NoError(t, Verify(Capture(input), input.Origin))
	require.NoError(t, os.WriteFile(filepath.Join(root, "report.json"), []byte(`{"password":"SHORT_SENSITIVE_CANARY"}`), 0600))
	input.Files = CaptureFiles(root, selection)
	require.NotEmpty(t, input.Files.Diagnostics)
	bundle := Capture(input)
	require.Error(t, Verify(bundle, input.Origin))
	encoded, err := json.Marshal(bundle)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "SHORT_SENSITIVE_CANARY")
	for _, selected := range [][]string{{"missing.txt"}, {"../outside"}, {"."}, {"report.json"}} {
		selection = models.PromptEvidenceContext{Files: selected, EvaluatorOnly: []string{"report.json"}}
		require.NotEmpty(t, CaptureFiles(root, selection).Diagnostics)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("OUTSIDE_FILE_CANARY"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link.txt")))
	require.NotEmpty(t, CaptureFiles(root, models.PromptEvidenceContext{Files: []string{"link.txt"}}).Diagnostics)
}
