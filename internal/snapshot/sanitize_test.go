package snapshot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type invalidPayload struct{}

func (invalidPayload) MarshalJSON() ([]byte, error) {
	return nil, errors.New("sensitive@example.com")
}

func TestCaptureRedactsTypedPayloadsWithoutMutation(t *testing.T) {
	type receipt struct {
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
		Count   int64             `json:"count"`
	}
	original := &receipt{
		Message: "sensitive@example.com",
		Fields:  map[string]string{"password": "short-credential", "note": "private@example.com"},
		Count:   9007199254740993,
	}
	in := CaptureInput{
		Task: &models.TestCase{TestID: "task"},
		Request: &execution.ExecutionRequest{
			Context: map[string]any{"receipt": original},
		},
		Run: &models.RunResult{
			ToolEvents: []models.ToolEvent{{ToolName: "read", Args: original, Result: []receipt{*original}}},
			Validations: map[string]models.GraderResults{
				"check": {Name: "check", Feedback: original.Message, Details: map[string]any{"receipt": original}},
			},
		},
	}
	snap, err := Capture(in)
	require.NoError(t, err)
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NotContains(t, string(data), "sensitive@example.com")
	require.NotContains(t, string(data), "private@example.com")
	require.NotContains(t, string(data), "short-credential")
	require.Contains(t, string(data), "9007199254740993")
	require.Equal(t, "sensitive@example.com", original.Message)
	require.Equal(t, "short-credential", original.Fields["password"])
	require.Equal(t, original.Message, in.Run.Validations["check"].Feedback)
	contextReceipt, ok := snap.Prompt.Context["receipt"].(map[string]any)
	require.True(t, ok)
	contextReceipt["message"] = "changed"
	require.Equal(t, "sensitive@example.com", original.Message)
	require.Positive(t, snap.Redaction.RedactionCount)
}

func TestCaptureRejectsUnsafePayloads(t *testing.T) {
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	for name, payload := range map[string]any{
		"opaque bytes":  []byte("short-credential"),
		"nested bytes":  map[string]any{"receipt": [][]byte{[]byte("short-credential")}},
		"unsafe keys":   map[string]string{"sensitive@example.com": "value"},
		"marshal error": invalidPayload{},
		"unsupported":   func() {},
		"cycle":         cyclic,
	} {
		t.Run(name, func(t *testing.T) {
			for _, surface := range []string{"context", "arguments", "result", "details"} {
				t.Run(surface, func(t *testing.T) {
					in := CaptureInput{
						Task:    &models.TestCase{TestID: "task"},
						Request: &execution.ExecutionRequest{},
						Run:     &models.RunResult{},
					}
					switch surface {
					case "context":
						in.Request.Context = map[string]any{"value": payload}
					case "arguments":
						in.Run.ToolEvents = []models.ToolEvent{{Args: payload}}
					case "result":
						in.Run.ToolEvents = []models.ToolEvent{{Result: payload}}
					case "details":
						in.Run.Validations = map[string]models.GraderResults{"check": {Details: map[string]any{"value": payload}}}
					}
					snap, err := Capture(in)
					require.Error(t, err)
					require.Nil(t, snap)
					require.NotContains(t, err.Error(), "sensitive@example.com")
					require.NotContains(t, err.Error(), "short-credential")
				})
			}
		})
	}
}

func TestCaptureRawJSONAndCustomRules(t *testing.T) {
	in := CaptureInput{
		Task: &models.TestCase{TestID: "task"},
		Run: &models.RunResult{
			ToolEvents: []models.ToolEvent{{Args: json.RawMessage(`{"note":"sensitive@example.com","count":9007199254740993}`)}},
		},
		Policy: &Policy{Rules: []RedactionRule{{Name: "private", Pattern: `sensitive@example\.com`}}},
	}
	snap, err := Capture(in)
	require.NoError(t, err)
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NotContains(t, string(data), "sensitive@example.com")
	require.Contains(t, string(data), "9007199254740993")
	in.Policy.Rules[0].Pattern = "["
	snap, err = Capture(in)
	require.ErrorContains(t, err, "invalid redaction policy")
	require.Nil(t, snap)
}

func TestCaptureRejectsUnsafeIdentities(t *testing.T) {
	for _, surface := range []string{"task", "eval", "engine", "model", "tool", "call", "grader", "identifier", "grader type", "instruction", "environment", "rule"} {
		t.Run(surface, func(t *testing.T) {
			in := CaptureInput{Task: &models.TestCase{TestID: "task"}, Run: &models.RunResult{}}
			const unsafe = "sensitive@example.com"
			switch surface {
			case "task":
				in.Task.TestID = unsafe
			case "eval":
				in.EvalID = unsafe
			case "engine":
				in.Engine.Type = unsafe
			case "model":
				in.Engine.ModelID = unsafe
			case "tool":
				in.Run.ToolEvents = []models.ToolEvent{{ToolName: unsafe}}
			case "call":
				in.Run.ToolEvents = []models.ToolEvent{{ToolCallID: unsafe}}
			case "grader":
				in.Run.Validations = map[string]models.GraderResults{unsafe: {}}
			case "identifier":
				in.Run.Validations = map[string]models.GraderResults{"check": {Name: unsafe}}
			case "grader type":
				in.Run.Validations = map[string]models.GraderResults{"check": {Type: unsafe}}
			case "instruction":
				in.Request = &execution.ExecutionRequest{Instructions: []execution.InstructionFile{{Path: unsafe}}}
			case "environment":
				in.EnvAllowList = []string{unsafe}
			case "rule":
				in.Policy = DefaultPolicy()
				in.Policy.Rules[0].Name = unsafe
			}

			snap, err := Capture(in)
			require.ErrorContains(t, err, "requires redaction")
			require.Nil(t, snap)
			require.NotContains(t, err.Error(), unsafe)
		})
	}
}

func TestCaptureConcurrentPolicyAccounting(t *testing.T) {
	policy := DefaultPolicy()
	in := CaptureInput{
		Task:   &models.TestCase{TestID: "task"},
		Run:    &models.RunResult{FinalOutput: "sensitive@example.com"},
		Policy: policy,
	}
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			snap, err := Capture(in)
			if err != nil {
				t.Error(err)
				return
			}
			if snap.Redaction.RedactionCount != 1 || strings.Join(snap.Redaction.AppliedRules, ",") != "email" {
				t.Errorf("unexpected per-capture summary: %+v", snap.Redaction)
			}
		})
	}
	workers.Wait()
	require.Zero(t, policy.MatchCount())
	require.Empty(t, policy.MatchedRules())
}

func TestCaptureRejectsUnsafeFixturePath(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "sensitive@example.com"), []byte("fixture"), 0o600))
	snap, err := Capture(CaptureInput{
		Task: &models.TestCase{TestID: "task"}, Run: &models.RunResult{}, FixturesRoot: root,
	})
	require.ErrorContains(t, err, "fixture path requires redaction")
	require.NotContains(t, err.Error(), "sensitive@example.com")
	require.Nil(t, snap)
}

func TestCaptureSanitizesFixtureDiagnostics(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sensitive@example.com")
	require.NoError(t, os.WriteFile(file, []byte("not a directory"), 0o600))
	snap, err := Capture(CaptureInput{
		Task: &models.TestCase{TestID: "task"}, Run: &models.RunResult{}, FixturesRoot: file,
	})
	require.ErrorContains(t, err, "hash fixtures")
	require.NotContains(t, err.Error(), "sensitive@example.com")
	require.Nil(t, snap)
}
