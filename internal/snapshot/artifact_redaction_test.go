package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestArtifactChangesDoNotSharePolicyCounters(t *testing.T) {
	policy := DefaultPolicy()
	var workers sync.WaitGroup
	for i := range 16 {
		workers.Go(func() {
			in := artifactCaptureInput()
			in.Policy = policy
			want := "prompt"
			if i%2 == 0 {
				in.Request.Message = "person@example.com"
			} else {
				in.Run.FinalOutput = "person@example.com"
				want = "result"
			}
			snap, err := Capture(in)
			if err != nil {
				t.Error(err)
				return
			}
			if snap.Redaction.RedactionCount != 1 {
				t.Errorf("unexpected capture count: %d", snap.Redaction.RedactionCount)
			}
			for _, artifact := range snap.Evidence.Artifacts {
				if artifact.Redacted != (artifact.ID == want) {
					t.Errorf("%s: unexpected change for %s", want, artifact.ID)
				}
			}
		})
	}
	workers.Wait()
	require.Zero(t, policy.MatchCount())
	require.Empty(t, policy.MatchedRules())
}

func TestTypedChangeTrackingRejectsChangedRequiredShape(t *testing.T) {
	type requiredShape struct {
		Password int `json:"password"`
	}
	policy := DefaultPolicy()
	value, changed, err := redactTypedWithChanges(requiredShape{Password: 42}, policy)
	require.ErrorContains(t, err, "redacted payload no longer matches its required shape")
	require.Equal(t, requiredShape{}, value)
	require.False(t, changed)
	require.Equal(t, 1, policy.MatchCount())
}

func artifactCaptureInput() CaptureInput {
	validation := models.GraderResults{Name: "check", Feedback: "plain", Details: map[string]any{"value": "plain"}}
	return CaptureInput{
		EvalID: "eval", Task: &models.TestCase{TestID: "task"},
		Request: &execution.ExecutionRequest{Message: "plain", Context: map[string]any{"value": "plain"}},
		Run: &models.RunResult{
			RunNumber: 1, Attempts: 1, FinalOutput: "plain",
			ToolEvents:         []models.ToolEvent{{ToolName: "tool", Args: map[string]any{"value": "plain"}, Result: "plain"}},
			CommandInvocations: []models.CommandInvocation{{Command: "command", Args: []string{"plain"}}},
			Validations:        map[string]models.GraderResults{"check": validation},
			Checkpoints:        []models.CheckpointOutcome{{AfterTurn: 1, Validations: map[string]models.GraderResults{"check": validation}}},
		},
	}
}

func TestCapturedArtifactLocatorsMatchSerializedSnapshot(t *testing.T) {
	collections := []string{"tool-events", "command-invocations", "validations", "checkpoints"}
	for _, tt := range []struct {
		name        string
		change      func(*CaptureInput)
		unavailable []string
		redacted    []string
		matches     int
	}{
		{name: "populated"},
		{name: "nil-collections", unavailable: collections, change: func(in *CaptureInput) {
			in.Run.ToolEvents = nil
			in.Run.CommandInvocations = nil
			in.Run.Validations = nil
			in.Run.Checkpoints = nil
		}},
		{name: "empty-collections", unavailable: collections, change: func(in *CaptureInput) {
			in.Run.ToolEvents = []models.ToolEvent{}
			in.Run.CommandInvocations = []models.CommandInvocation{}
			in.Run.Validations = map[string]models.GraderResults{}
			in.Run.Checkpoints = []models.CheckpointOutcome{}
		}},
		{name: "skip-graders-empty-validations", unavailable: []string{"validations"}, change: func(in *CaptureInput) {
			in.Run.Validations = map[string]models.GraderResults{}
		}},
		{name: "empty-command-invocations", unavailable: []string{"command-invocations"}, change: func(in *CaptureInput) {
			in.Run.CommandInvocations = []models.CommandInvocation{}
		}},
		{name: "empty-checkpoints", unavailable: []string{"checkpoints"}, change: func(in *CaptureInput) {
			in.Run.Checkpoints = []models.CheckpointOutcome{}
		}},
		{name: "empty-tool-events", unavailable: []string{"tool-events"}, change: func(in *CaptureInput) {
			in.Run.ToolEvents = []models.ToolEvent{}
		}},
		{name: "exact-numeric-tokens", change: func(in *CaptureInput) {
			numbers := json.RawMessage(`{"large":9007199254740993,"decimal":1.00,"exponent":1e+03}`)
			in.Request.Context = map[string]any{"numbers": numbers}
			in.Run.ToolEvents[0].Args = numbers
		}},
		{name: "no-op-redaction", matches: 1, change: func(in *CaptureInput) {
			in.Request.Message = RedactionPlaceholder
			in.Policy = &Policy{Rules: []RedactionRule{{Name: "placeholder", Pattern: `\[REDACTED\]`}}}
		}},
		{name: "redacted-validation", redacted: []string{"validations", "result"}, matches: 1, change: func(in *CaptureInput) {
			in.Policy = DefaultPolicy()
			result := in.Run.Validations["check"]
			result.Feedback = "person@example.com"
			in.Run.Validations["check"] = result
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := artifactCaptureInput()
			if tt.change != nil {
				tt.change(&in)
			}
			snap, err := Capture(in)
			require.NoError(t, err)
			if tt.name == "skip-graders-empty-validations" {
				require.NotNil(t, snap.Result.Validations)
				require.Empty(t, snap.Result.Validations)
			}
			require.Equal(t, tt.matches, snap.Redaction.RedactionCount)
			require.Equal(t, tt.matches, snap.Evidence.Redaction.MatchCount)
			require.Equal(t, snap.Redaction.AppliedRules, snap.Evidence.Redaction.AppliedRules)
			if tt.name == "no-op-redaction" {
				require.Equal(t, []string{"placeholder"}, snap.Redaction.AppliedRules)
			}

			data, err := json.Marshal(snap)
			require.NoError(t, err)
			loaded, err := ParseSnapshot(data, "memory")
			require.NoError(t, err)
			require.Equal(t, CurrentSchemaVersion, loaded.SchemaVersion)
			reencoded, err := json.Marshal(loaded)
			require.NoError(t, err)
			require.Equal(t, data, reencoded)
			if tt.name == "exact-numeric-tokens" {
				for _, token := range []string{"9007199254740993", "1.00", "1e+03"} {
					require.Contains(t, string(reencoded), token)
				}
			}
			for i, encoded := range [][]byte{data, reencoded} {
				current := []*Snapshot{snap, loaded}[i]
				requireCapturedArtifactWireContent(t, current, encoded)
				captured := 0
				for _, artifact := range current.Evidence.Artifacts {
					if slices.Contains(tt.unavailable, artifact.ID) {
						require.Equal(t, "unavailable", artifact.Availability, artifact.ID)
						require.Empty(t, artifact.Document, artifact.ID)
						require.Empty(t, artifact.Pointer, artifact.ID)
						require.Nil(t, artifact.ContentDigest, artifact.ID)
						require.Nil(t, artifact.SourceDigest, artifact.ID)
						require.Equal(t, "unknown", artifact.Completeness, artifact.ID)
						require.NotEmpty(t, artifact.Reason, artifact.ID)
					}
					require.Equal(t, slices.Contains(tt.redacted, artifact.ID), artifact.Redacted, artifact.ID)
					if artifact.Availability == "captured" {
						captured++
					}
				}
				require.Equal(t, 8-len(tt.unavailable), captured)
			}
			if slices.Contains(tt.unavailable, "validations") {
				require.Nil(t, loaded.Result.Validations)
			}
			if slices.Contains(tt.unavailable, "tool-events") {
				require.Nil(t, loaded.ToolEvents)
			}
			if slices.Contains(tt.unavailable, "command-invocations") {
				require.Nil(t, loaded.CommandInvocations)
			}
			if slices.Contains(tt.unavailable, "checkpoints") {
				require.Nil(t, loaded.Checkpoints)
			}
		})
	}
}

func requireCapturedArtifactWireContent(t *testing.T, snap *Snapshot, data []byte) {
	t.Helper()
	require.NoError(t, evidence.Validate(snap.Evidence))
	var document map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &document))
	var result map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(document["result"], &result))
	if len(snap.ToolEvents) == 0 {
		require.NotContains(t, document, "toolEvents")
	}
	if len(snap.CommandInvocations) == 0 {
		require.NotContains(t, document, "commandInvocations")
	}
	if len(snap.Checkpoints) == 0 {
		require.NotContains(t, document, "checkpoints")
	}
	if len(snap.Result.Validations) == 0 {
		require.NotContains(t, result, "validations")
	}
	contents := map[string]json.RawMessage{
		"/prompt": document["prompt"], "/toolEvents": document["toolEvents"],
		"/commandInvocations": document["commandInvocations"], "/checkpoints": document["checkpoints"],
		"/result/validations": result["validations"], "/result": document["result"],
		"/env": document["env"], "/engine": document["engine"],
	}
	for _, artifact := range snap.Evidence.Artifacts {
		if artifact.Availability != "captured" {
			continue
		}
		require.Equal(t, "snapshot", artifact.Document, artifact.ID)
		content, ok := contents[artifact.Pointer]
		require.True(t, ok, artifact.Pointer)
		require.NotEmpty(t, content, artifact.Pointer)
		require.NotEqual(t, "null", string(content), artifact.Pointer)
		digest, err := evidence.JSONDigest(content)
		require.NoError(t, err)
		require.Equal(t, artifact.ContentDigest, digest, artifact.ID)
		require.NoError(t, evidence.VerifyContent(snap.Evidence,
			models.EvidenceReference{Origin: snap.Evidence.Origin, ArtifactID: artifact.ID},
			content, false, false), artifact.ID)
	}
}

func TestArtifactRedactionIsScopedToOwnMaterializedContent(t *testing.T) {
	for _, tt := range []struct {
		name, artifact string
		change         func(*CaptureInput)
	}{
		{"prompt-message", "prompt", func(in *CaptureInput) { in.Request.Message = "person@example.com" }},
		{"prompt-context", "prompt", func(in *CaptureInput) { in.Request.Context["password"] = "value" }},
		{"prompt-followup", "prompt", func(in *CaptureInput) { in.Task.Stimulus.FollowUps = []string{"person@example.com"} }},
		{"tool-args", "tool-events", func(in *CaptureInput) { in.Run.ToolEvents[0].Args = map[string]any{"password": "value"} }},
		{"tool-result", "tool-events", func(in *CaptureInput) { in.Run.ToolEvents[0].Result = "person@example.com" }},
		{"tool-error", "tool-events", func(in *CaptureInput) { in.Run.ToolEvents[0].Error = "person@example.com" }},
		{"command", "command-invocations", func(in *CaptureInput) { in.Run.CommandInvocations[0].Args[0] = "person@example.com" }},
		{"validation-feedback", "validations", func(in *CaptureInput) {
			result := in.Run.Validations["check"]
			result.Feedback = "person@example.com"
			in.Run.Validations["check"] = result
		}},
		{"validation-details", "validations", func(in *CaptureInput) {
			result := in.Run.Validations["check"]
			result.Details = map[string]any{"password": "value"}
			in.Run.Validations["check"] = result
		}},
		{"checkpoint", "checkpoints", func(in *CaptureInput) {
			result := in.Run.Checkpoints[0].Validations["check"]
			result.Feedback = "person@example.com"
			in.Run.Checkpoints[0].Validations["check"] = result
		}},
		{"result-output", "result", func(in *CaptureInput) { in.Run.FinalOutput = "person@example.com" }},
		{"result-error", "result", func(in *CaptureInput) { in.Run.ErrorMsg = "person@example.com" }},
		{"environment-regex", "environment", func(in *CaptureInput) { in.EnvAllowList = []string{"WAZA_ARTIFACT_VALUE"} }},
		{"environment-key", "environment", func(in *CaptureInput) { in.EnvAllowList = []string{"WAZA_ARTIFACT_TOKEN"} }},
		{"engine", "engine-config", func(in *CaptureInput) { in.Engine.JudgeModel = "person@example.com" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WAZA_ARTIFACT_VALUE", "person@example.com")
			t.Setenv("WAZA_ARTIFACT_TOKEN", "unrecognized-value")
			in := artifactCaptureInput()
			tt.change(&in)
			original := artifactCaptureSourceJSON(t, in)
			policy := DefaultPolicy()
			policy.RedactString("existing@example.com")
			count := policy.MatchCount()
			in.Policy = policy
			snap, err := Capture(in)
			require.NoError(t, err)
			require.NoError(t, evidence.Validate(snap.Evidence))
			require.Positive(t, snap.Evidence.Redaction.MatchCount)
			require.Equal(t, count, policy.MatchCount())
			in.Policy = nil
			after := artifactCaptureSourceJSON(t, in)
			require.Equal(t, original, after)
			values := artifactMaterializedValues(snap)
			captured := 0
			for _, artifact := range snap.Evidence.Artifacts {
				if artifact.Availability == "captured" {
					captured++
					want := artifact.ID == tt.artifact || tt.artifact == "validations" && artifact.ID == "result"
					require.Equal(t, want, artifact.Redacted, artifact.ID)
					data, err := json.Marshal(values[artifact.ID])
					require.NoError(t, err)
					err = evidence.VerifyContent(snap.Evidence,
						models.EvidenceReference{Origin: snap.Evidence.Origin, ArtifactID: artifact.ID},
						data, false, true)
					if want {
						require.ErrorContains(t, err, "changed by redaction", artifact.ID)
					} else {
						require.NoError(t, err, artifact.ID)
					}
				}
			}
			require.Equal(t, 8, captured)
		})
	}
}

func artifactCaptureSourceJSON(t *testing.T, in CaptureInput) []byte {
	t.Helper()
	var request map[string]any
	if in.Request != nil {
		require.Nil(t, in.Request.PermissionHandler)
		value := reflect.ValueOf(*in.Request)
		request = make(map[string]any, value.NumField()-1)
		for i := range value.NumField() {
			name := value.Type().Field(i).Name
			if name != "PermissionHandler" {
				request[name] = value.Field(i).Interface()
			}
		}
	}
	in.Request = nil
	data, err := json.Marshal(struct {
		CaptureInput
		Request map[string]any
	}{in, request})
	require.NoError(t, err)
	return data
}

func artifactMaterializedValues(snap *Snapshot) map[string]any {
	return map[string]any{
		"prompt": snap.Prompt, "tool-events": snap.ToolEvents,
		"command-invocations": snap.CommandInvocations, "validations": snap.Result.Validations,
		"checkpoints": snap.Checkpoints, "result": snap.Result,
		"environment": snap.Env, "engine-config": snap.Engine,
	}
}

func TestUnrelatedMatchesDoNotRedactCapturedArtifacts(t *testing.T) {
	for _, field := range []string{"eval", "task", "workspace"} {
		t.Run(field, func(t *testing.T) {
			in := artifactCaptureInput()
			switch field {
			case "eval":
				in.EvalName = "person@example.com"
			case "task":
				in.Task.DisplayName = "person@example.com"
			case "workspace":
				in.Run.WorkspaceDir = t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(in.Run.WorkspaceDir, "output.txt"), []byte("person@example.com"), 0o600))
				in.WorkspacePaths = []string{"output.txt"}
				in.WorkspaceLimits = WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 2048}
			}
			snap, err := Capture(in)
			require.NoError(t, err)
			require.NoError(t, evidence.Validate(snap.Evidence))
			if field != "workspace" || runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
				require.Positive(t, snap.Evidence.Redaction.MatchCount)
			}
			for _, artifact := range snap.Evidence.Artifacts {
				if artifact.Availability == "captured" && artifact.Kind != "workspace_file" {
					require.False(t, artifact.Redacted, artifact.ID)
				}
			}
			if field == "workspace" {
				if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
					require.Len(t, snap.WorkspaceFiles, 1)
					require.True(t, snap.WorkspaceFiles[0].Redacted)
				} else {
					require.Empty(t, snap.WorkspaceFiles)
					require.Contains(t, snap.Evidence.Diagnostics[0].Message, "unsupported")
				}
			}
		})
	}
}

func TestEnvironmentChangeDoesNotBlockUnchangedPrompt(t *testing.T) {
	t.Setenv("WAZA_ARTIFACT_VALUE", "person@example.com")
	in := artifactCaptureInput()
	in.EnvAllowList = []string{"WAZA_ARTIFACT_VALUE"}
	snap, err := Capture(in)
	require.NoError(t, err)
	for _, tt := range []struct {
		id    string
		value any
		want  bool
	}{{"prompt", snap.Prompt, false}, {"environment", snap.Env, true}} {
		data, err := json.Marshal(tt.value)
		require.NoError(t, err)
		err = evidence.VerifyContent(snap.Evidence,
			models.EvidenceReference{Origin: snap.Evidence.Origin, ArtifactID: tt.id}, data, false, true)
		if tt.want {
			require.ErrorContains(t, err, "changed by redaction")
		} else {
			require.NoError(t, err)
		}
	}
}

type nullArtifactPayload struct{ calls *int }

func (value nullArtifactPayload) MarshalJSON() ([]byte, error) {
	*value.calls++
	return []byte("null"), nil
}

func TestArtifactProjectionNormalizationAndNoOpChanges(t *testing.T) {
	for _, field := range []string{"typed-nil-args", "typed-nil-result", "marshal-null", "numeric-context", "no-op-string", "no-op-key", "no-op-environment", "empty-typed-fields", "unavailable"} {
		t.Run(field, func(t *testing.T) {
			in := artifactCaptureInput()
			calls := 0
			wantToolChange := false
			var empty *string
			switch field {
			case "typed-nil-args":
				in.Run.ToolEvents[0].Args = empty
				wantToolChange = true
			case "typed-nil-result":
				in.Run.ToolEvents[0].Result = empty
				wantToolChange = true
			case "marshal-null":
				in.Run.ToolEvents[0].Args = nullArtifactPayload{calls: &calls}
				wantToolChange = true
			case "numeric-context":
				in.Request.Context = map[string]any{"large": int64(9007199254740993), "raw": json.RawMessage(` { "value": 1.00 } `)}
			case "no-op-string":
				in.Request.Message = RedactionPlaceholder
				in.Policy = &Policy{Rules: []RedactionRule{{Name: "placeholder", Pattern: `\[REDACTED\]`}}}
			case "no-op-key":
				in.Request.Context = map[string]any{"password": RedactionPlaceholder}
			case "no-op-environment":
				t.Setenv("WAZA_ARTIFACT_TOKEN", RedactionPlaceholder)
				in.EnvAllowList = []string{"WAZA_ARTIFACT_TOKEN"}
			case "empty-typed-fields":
				in.Run.CommandInvocations[0].Args = []string{}
				in.Run.Checkpoints[0].Validations = map[string]models.GraderResults{}
			case "unavailable":
				in.Run.ToolEvents = nil
				in.Run.CommandInvocations = nil
				in.Run.Checkpoints = nil
				in.Run.Validations = nil
			}
			snap, err := Capture(in)
			require.NoError(t, err)
			require.NoError(t, evidence.Validate(snap.Evidence))
			for _, artifact := range snap.Evidence.Artifacts {
				if artifact.Availability == "captured" {
					require.Equal(t, artifact.ID == "tool-events" && wantToolChange, artifact.Redacted, artifact.ID)
				} else {
					require.False(t, artifact.Redacted, artifact.ID)
				}
			}

			if field == "marshal-null" {
				require.Equal(t, 1, calls)
			}
			if field == "numeric-context" {
				require.Equal(t, json.Number("9007199254740993"), snap.Prompt.Context["large"])
				raw, ok := snap.Prompt.Context["raw"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, json.Number("1.00"), raw["value"])
			}
			if field == "no-op-string" || field == "no-op-key" || field == "no-op-environment" {
				require.Positive(t, snap.Evidence.Redaction.MatchCount)
			}
			if field == "unavailable" {
				for _, id := range []string{"tool-events", "command-invocations", "checkpoints", "validations"} {
					for _, artifact := range snap.Evidence.Artifacts {
						if artifact.ID == id {
							require.Equal(t, "unavailable", artifact.Availability)
						}
					}
				}
			}
		})
	}
}
