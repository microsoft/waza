package nativetask

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func digest(t *testing.T, value any) models.EvidenceDigest {
	t.Helper()
	d, err := evidence.JSONDigest(value)
	require.NoError(t, err)
	return *d
}

func fixture(t *testing.T) (*Prepared, *Profile, *Admitted, Record) {
	t.Helper()
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(directory+"/eval.yaml", []byte("synthetic offline source\n"), 0o600))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	tools := []string{}
	req := &execution.ExecutionRequest{ModelID: "observed-test-model", Message: "synthetic prompt", NoSkills: true,
		SkipWorkspaceCapture: true, ToolPolicy: execution.NewToolPolicy(&tools), Resources: []execution.ResourceFile{},
		Instructions: []execution.InstructionFile{}, Context: map[string]any{}}
	prepared, err := Prepare(context.Background(), releasepolicy.Baseline, "task", req, []Source{{root, "eval.yaml"}})
	require.NoError(t, err)
	profile := &Profile{Kind: profileKind, Version: version, Nonce: strings.Repeat("a", 64),
		Selection: Selection{"native_text_task", version}, PolicyDigest: digest(t, "synthetic policy"),
		PermissionMode: "deny_all", Arms: map[releasepolicy.Arm]ArmPlan{}}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		profile.Arms[arm] = ArmPlan{digest(t, string(arm)), []RequestPlan{{"task", prepared.RequestDigest()}}}
	}
	sealed, err := releasepolicy.SealJSON(profile)
	require.NoError(t, err)
	profile, err = DecodeProfile(sealed)
	require.NoError(t, err)
	key := releasepolicy.AttemptKey{SampleKey: releasepolicy.SampleKey{ClusterID: "cluster", TaskID: "task", Trial: 1},
		Arm: releasepolicy.Baseline, EvalID: "eval", Attempt: 1}
	admitted, err := Admit(context.Background(), prepared, profile, key)
	require.NoError(t, err)
	binding, err := admitted.Binding()
	require.NoError(t, err)
	origin := models.EvidenceOrigin{EvalID: "eval", TaskID: "task", RunNumber: 1, AttemptCount: 1}
	value := "actual nonempty typed output"
	session := digest(t, "synthetic session, not provider evidence")
	event, err := json.Marshal(map[string]any{"type": "assistant.message", "data": map[string]any{"content": value}})
	require.NoError(t, err)
	result := models.GraderResults{Name: "check", Type: models.GraderKindText, Score: 1, Passed: true, Weight: 1}
	row := releasepolicy.ActualRunRow{Origin: origin, Run: models.RunResult{RunNumber: 1, Attempts: 1, Prompt: req.Message,
		Status: models.StatusPassed, FinalOutput: value, Validations: map[string]models.GraderResults{"check": result},
		SessionDigest: models.SessionDigest{ToolsUsed: []string{}, Errors: []string{}}}}
	summary := releasepolicy.AttemptSummary{Key: key, Origin: origin, Status: "passed", Category: "behavioral",
		Checks:     []releasepolicy.CheckSummary{{Scope: "eval", Grader: "check", Passed: true, Score: "1", OperationalState: "observed"}},
		Runtime:    releasepolicy.RuntimeObservation{TaskID: "task", Availability: "unavailable", Reason: "Synthetic fixture is not runtime evidence."},
		References: []models.EvidenceReference{}}
	completed := Phase{State: "completed"}
	record := Record{Kind: recordKind, Version: version, CollectionID: binding.CollectionID, PolicyDigest: binding.PolicyDigest,
		ProfileDigest: binding.ProfileDigest, PlanDigest: binding.PlanDigest, RequestDigest: binding.RequestDigest, Key: key, Origin: origin,
		Lifecycle: Lifecycle{completed, completed, completed, completed, completed},
		Response: &Response{SessionKey: &session, Success: true, FinalOutput: value, CanonicalEvents: []json.RawMessage{event},
			ToolPolicyMode: "deny_all", ToolPolicyDenials: []models.ToolPolicyDenial{}},
		Output:      Output{Availability: "available", Value: &value, Messages: []Message{{1, value, "typed_nonempty"}}},
		Diagnostics: []Diagnostic{}, EventModels: EventModels{Models: []string{}},
		Accounting: Accounting{Source: "none", Models: []string{}, ProviderCurrencyState: "unavailable", Reason: "No provider accounting in synthetic fixture."},
		ActualRow:  row, Summary: summary}
	return prepared, profile, admitted, record
}

func encoded(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func objectValue(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok)
	return object
}

func arrayValue(t *testing.T, value any) []any {
	t.Helper()
	array, ok := value.([]any)
	require.True(t, ok)
	return array
}

func operational(r Record, reason string) Record {
	r.Lifecycle.Grade = Phase{"not_attempted", reason}
	r.Summary.Category, r.Summary.Status = "operational", "incomplete"
	r.Summary.Checks = []releasepolicy.CheckSummary{}
	r.ActualRow.Run.Status, r.ActualRow.Run.ErrorMsg = models.StatusError, reason
	r.ActualRow.Run.Validations = map[string]models.GraderResults{}
	return r
}

func TestPrepareAndAdmitAreEngineFreeAndDetached(t *testing.T) {
	prepared, profile, admitted, _ := fixture(t)
	original, err := prepared.view()
	require.NoError(t, err)
	original.Sources[0].Bytes[0] = 'X'
	original.Request.Context["mutation"] = true
	fresh, err := prepared.view()
	require.NoError(t, err)
	require.Equal(t, "synthetic offline source\n", string(fresh.Sources[0].Bytes))
	require.Empty(t, fresh.Request.Context)
	profile.Selection.Operation = "changed"
	_, err = admitted.Binding()
	require.NoError(t, err)
	prepared.data[0] = 'x'
	_, err = admitted.Binding()
	require.NoError(t, err, "admission owns detached sealed preparation")
	_, err = prepared.view()
	require.Error(t, err)
}

func TestRecordRejectsUnsupportedEvidenceAndContradictoryGrades(t *testing.T) {
	for _, name := range []string{"runtime version", "extra capture", "host lifecycle capture", "nontext grader", "zero weight", "unscoped check", "passed failed check", "private event id", "private nested id", "private hyphen id", "private punctuation id"} {
		t.Run(name, func(t *testing.T) {
			_, _, admitted, record := fixture(t)
			switch name {
			case "runtime version":
				record.Summary.Runtime.ModelVersion = "invented"
			case "extra capture":
				record.ActualRow.Run.SnapshotPath = "invented"
			case "host lifecycle capture":
				record.ActualRow.Run.HostLifecycle = &models.HostLifecycle{ModelTurns: []models.ModelTurn{}, Shells: []models.ShellLifecycle{}}
			case "nontext grader", "zero weight":
				grade := record.ActualRow.Run.Validations["check"]
				if name == "nontext grader" {
					grade.Type = models.GraderKindFile
				} else {
					grade.Weight = 0
				}

				record.ActualRow.Run.Validations["check"] = grade
			case "unscoped check":
				record.Summary.Checks[0].Scope = "invented"
			case "passed failed check":
				record.Summary.Checks[0].Passed = false
				grade := record.ActualRow.Run.Validations["check"]
				grade.Passed = false
				record.ActualRow.Run.Validations["check"] = grade
			case "private event id", "private nested id", "private hyphen id", "private punctuation id":
				event := map[string]any{"type": "assistant.message", "data": map[string]any{"content": record.Response.FinalOutput}}
				switch name {
				case "private event id":
					event["sessionId"] = "private"
				case "private nested id":
					event["extra"] = []any{map[string]any{"session_id": "private"}}
				case "private hyphen id":
					objectValue(t, event["data"])["session-id"] = "private"
				case "private punctuation id":
					objectValue(t, event["data"])["SeSsIoN.$-ID"] = "private"
				}
				record.Response.CanonicalEvents = []json.RawMessage{encoded(t, event)}
			}
			_, err := DecodeRecord(encoded(t, record), admitted)
			require.Error(t, err)
			if name == "host lifecycle capture" {
				require.ErrorContains(t, err, "cannot certify extra capture")
			}
		})
	}
}

func TestRecordAndTapePreserveDynamicNumberTokens(t *testing.T) {
	for _, reader := range []string{"record", "supplied tape"} {
		t.Run(reader, func(t *testing.T) {
			_, _, admitted, record := fixture(t)
			expected := map[string]any{
				"count":    json.Number("9007199254740993"),
				"negative": json.Number("-9007199254740993"),
				"huge":     json.Number("1e1000"),
				"nested": map[string]any{
					"values": []any{json.Number("1e-300"), json.Number("0.1234567890123456789")},
				},
			}
			check := record.ActualRow.Run.Validations["check"]
			check.Details = expected
			record.ActualRow.Run.Validations["check"] = check
			raw := encoded(t, record)
			var decoded *Record
			if reader == "record" {
				var err error
				decoded, err = DecodeRecord(raw, admitted)
				require.NoError(t, err)
			} else {
				root, err := os.OpenRoot(t.TempDir())
				require.NoError(t, err)
				defer func() { require.NoError(t, root.Close()) }()
				events, payload := fixtureTapeBytes(t, []*Admitted{admitted}, [][]byte{raw})
				require.NoError(t, root.WriteFile(eventFile, events, 0o600))
				require.NoError(t, root.WriteFile(payloadFile, payload, 0o600))
				prefix, err := ReadPrefix(t.Context(), root, []*Admitted{admitted})
				require.NoError(t, err)
				require.Len(t, prefix.Records, 1)
				decoded = &prefix.Records[0]
			}
			actual := decoded.ActualRow.Run.Validations["check"].Details
			require.Equal(t, expected, actual)
			require.Equal(t, encoded(t, expected), encoded(t, actual))
		})
	}
}

func TestTapeDigestsPreserveRawNumberTokens(t *testing.T) {
	_, _, admitted, record := fixture(t)
	original := encoded(t, record)
	modified := bytes.Replace(original, []byte(`"score":1,`), []byte(`"score":1.0,`), 1)
	require.NotEqual(t, original, modified)
	_, err := DecodeRecord(modified, admitted)
	require.NoError(t, err)
	first, rowFirst, err := recordDigests(original)
	require.NoError(t, err)
	second, rowSecond, err := recordDigests(modified)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	require.NotEqual(t, rowFirst, rowSecond)
	events, payload := fixtureTapeBytes(t, []*Admitted{admitted}, [][]byte{modified})
	prefix, err := verifyPrefix(context.Background(), events, payload, []*Admitted{admitted})
	require.NoError(t, err)
	require.Len(t, prefix.Records, 1)
	_, err = verifyPrefix(context.Background(), events, append(bytes.Clone(original), '\n'), []*Admitted{admitted})
	require.Error(t, err, "raw numeric spelling is part of the terminal binding")
}

func TestPrepareRejectsUnsupportedInputs(t *testing.T) {
	for _, name := range []string{"nil policy", "unrestricted", "callback", "custom tool", "skills", "ephemeral", "workspace capture", "workspace capture paths", "session", "traversal", "workdir alias", "workdir dot", "duplicate", "negative timeout", "missing source", "source alias", "source directory", "symlink", "cancel"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(directory+"/eval.yaml", []byte("source"), 0o600))
			root, err := os.OpenRoot(directory)
			require.NoError(t, err)
			defer func() { require.NoError(t, root.Close()) }()
			tools := []string{}
			req := &execution.ExecutionRequest{ModelID: "m", Message: "p", NoSkills: true, SkipWorkspaceCapture: true,
				ToolPolicy: execution.NewToolPolicy(&tools)}
			sources := []Source{{root, "eval.yaml"}}
			ctx := context.Background()
			switch name {
			case "nil policy":
				req.ToolPolicy = nil
			case "unrestricted":
				req.ToolPolicy = execution.NewToolPolicy(nil)
			case "callback":
				req.PermissionHandler = func(copilot.PermissionRequest, copilot.PermissionInvocation) (rpc.PermissionDecision, error) {
					t.Fatal("preparation must never invoke a caller permission callback")
					return nil, errors.New("unexpected permission callback")
				}
			case "custom tool":
				req.Tools = append(req.Tools, copilot.Tool{Name: "forbidden"})
			case "skills":
				req.NoSkills = false
			case "ephemeral":
				req.EphemeralSession = true
			case "workspace capture":
				req.SkipWorkspaceCapture = false
			case "workspace capture paths":
				req.WorkspaceCapturePaths = []string{"output.txt"}
			case "session":
				req.SessionID = "resume"
			case "traversal":
				req.Resources = []execution.ResourceFile{{Path: "a/../b"}}
			case "workdir alias":
				req.WorkDir = "dir/../other"
			case "workdir dot":
				req.WorkDir = "."
			case "duplicate":
				req.Resources = []execution.ResourceFile{{Path: "x"}, {Path: "x"}}
			case "negative timeout":
				req.FirstEventTimeout = -time.Second
			case "missing source":
				sources[0].Path = "missing.yaml"
			case "source alias":
				sources[0].Path = "a/../eval.yaml"
			case "source directory":
				require.NoError(t, root.Mkdir("directory", 0o700))
				sources[0].Path = "directory"
			case "symlink":
				require.NoError(t, os.Symlink("eval.yaml", directory+"/link.yaml"))
				sources[0].Path = "link.yaml"
			case "cancel":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			_, err = Prepare(ctx, releasepolicy.Baseline, "task", req, sources)
			require.Error(t, err)
		})
	}
}

func TestProfileAdmissionBoundary(t *testing.T) {
	prepared, profile, admitted, _ := fixture(t)
	binding, err := admitted.Binding()
	require.NoError(t, err)
	for _, name := range []string{"arm", "task", "trial", "attempt", "eval"} {
		t.Run(name, func(t *testing.T) {
			key := binding.Key
			switch name {
			case "arm":
				key.Arm = releasepolicy.Candidate
			case "task":
				key.TaskID = "other"
			case "trial":
				key.Trial = 0
			case "attempt":
				key.Attempt = 0
			case "eval":
				key.EvalID = ""
			}
			_, err := Admit(context.Background(), prepared, profile, key)
			require.Error(t, err)
		})
	}
	for _, name := range []string{"selection", "digest encoding", "missing arm", "request inventory", "nonce"} {
		t.Run(name, func(t *testing.T) {
			var clone Profile
			require.NoError(t, json.Unmarshal(encoded(t, profile), &clone))
			switch name {
			case "selection":
				clone.Selection.Operation = ""
			case "digest encoding":
				clone.Arms[releasepolicy.Baseline] = ArmPlan{releasepolicy.SourceDigest([]byte("wrong")), clone.Arms[releasepolicy.Baseline].Requests}
			case "missing arm":
				delete(clone.Arms, releasepolicy.Candidate)
			case "request inventory":
				a := clone.Arms[releasepolicy.Candidate]
				a.Requests[0].TaskID = "other"
				clone.Arms[releasepolicy.Candidate] = a
			case "nonce":
				clone.Nonce = "00"
			}
			raw, err := releasepolicy.SealJSON(clone)
			require.NoError(t, err)
			_, err = DecodeProfile(raw)
			require.Error(t, err)
		})
	}
}

func TestRecordAllContributingMessagePresence(t *testing.T) {
	_, _, admitted, r := fixture(t)
	_, err := DecodeRecord(encoded(t, r), admitted)
	require.NoError(t, err)
	r = operational(r, "Empty typed message presence is unknown.")
	r.Response.CanonicalEvents = append(r.Response.CanonicalEvents, json.RawMessage(`{"type":"assistant.message","data":{"content":""}}`))
	r.Output.Messages = append(r.Output.Messages, Message{2, "", "unknown"})
	r.Output.Availability, r.Output.Value, r.Output.Reason = "unavailable", nil, "One contributing message has unknown presence."
	r.ActualRow.Run.FinalOutput = ""
	_, err = DecodeRecord(encoded(t, r), admitted)
	require.NoError(t, err)
	value := r.Response.FinalOutput
	r.Output.Availability, r.Output.Value, r.Output.Reason = "available", &value, ""
	r.ActualRow.Run.FinalOutput = value
	_, err = DecodeRecord(encoded(t, r), admitted)
	require.ErrorContains(t, err, "presence")
}

func TestRecordStrictRawAndCrossFieldValidation(t *testing.T) {
	for _, name := range []string{"duplicate", "unknown", "alias", "missing", "null boolean", "null array", "output null", "score string", "kind", "response identity", "session conflict", "output disagreement", "phase", "billing", "raw session"} {
		t.Run(name, func(t *testing.T) {
			_, _, admitted, r := fixture(t)
			raw := encoded(t, r)
			if name == "duplicate" {
				raw = append([]byte(`{"kind":"duplicate",`), raw[1:]...)
			} else {
				value, err := jsonutil.Parse(raw)
				require.NoError(t, err)
				object, ok := value.(map[string]any)
				require.True(t, ok)
				response, ok := object["response"].(map[string]any)
				require.True(t, ok)
				output, ok := object["output"].(map[string]any)
				require.True(t, ok)
				switch name {
				case "unknown":
					object["unknown"] = true
				case "alias":
					object["Kind"] = object["kind"]
					delete(object, "kind")
				case "missing":
					delete(response, "success")
				case "null boolean":
					response["success"] = nil
				case "null array":
					object["diagnostics"] = nil
				case "output null":
					output["value"] = nil
				case "score string":
					objectValue(t, arrayValue(t, objectValue(t, object["summary"])["checks"])[0])["score"] = "1"
				case "kind":
					object["kind"] = "waza.grader-assurance"
				case "response identity":
					response["session_key"] = nil
				case "session conflict":
					objectValue(t, object["accounting"])["session_key"] = map[string]any{"encoding": "json-v1", "sha256": strings.Repeat("b", 64)}
				case "output disagreement":
					response["final_output"] = "different"
				case "phase":
					objectValue(t, objectValue(t, object["lifecycle"])["shutdown"])["state"] = "not_attempted"
				case "billing":
					objectValue(t, object["accounting"])["complete"] = true
				case "raw session":
					objectValue(t, objectValue(t, objectValue(t, object["actual_row"])["run"])["session_digest"])["session_id"] = "private"
				}
				raw = encoded(t, object)
			}
			_, err := DecodeRecord(raw, admitted)
			require.Error(t, err)
		})
	}
}

func TestRecordOperationalResponseAndPartialAccounting(t *testing.T) {
	_, _, admitted, r := fixture(t)
	r = operational(r, "Execute returned an error with preserved response.")
	r.Response.Success = false
	r.Response.ErrorMsg = "copilot diagnostic: execute/send_failed"
	r.ActualRow.Run.ErrorMsg = r.Response.ErrorMsg
	r.Lifecycle.Execute = Phase{"failed", "send_failed"}
	r.Response.SessionKey = nil
	r.Response.SessionReason = "Response did not identify a session."
	key := digest(t, "diagnostic-only accounting")
	r.Accounting.SessionKey = &key
	r.Accounting.Source = "events"
	credits := 0.125
	r.Accounting.Usage = &models.UsageStats{InputTokens: 4, AICredits: &credits}
	r.Diagnostics = []Diagnostic{{"execute", "send_failed", &key}}
	decoded, err := DecodeRecord(encoded(t, r), admitted)
	require.NoError(t, err)
	require.False(t, decoded.Accounting.Complete)
	require.Equal(t, credits, *decoded.Accounting.Usage.AICredits)
	require.Equal(t, "available", decoded.Output.Availability, "availability is not operational success")
}

func TestLegacySessionDefaultsDoNotCertifyAbsence(t *testing.T) {
	_, _, admitted, r := fixture(t)
	r.EventModels = EventModels{Models: []string{"observed-test-model"}, EventsObserved: 2, Complete: true}
	decoded, err := DecodeRecord(encoded(t, r), admitted)
	require.NoError(t, err)
	require.Zero(t, decoded.ActualRow.Run.SessionDigest.ToolCallCount)
	require.Empty(t, decoded.ActualRow.Run.SessionDigest.ToolsUsed)
	require.Equal(t, "available", decoded.Output.Availability)
	require.Equal(t, Capabilities{"not_assessed", "not_assessed", "not_assessed", "not_assessed", "not_assessed"},
		decoded.Capabilities(), "neither default counts nor received model completeness certify tool/session evidence")
}

func TestPublicTapeReaderBindsBehavioralObservedModelsToRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*Record)
		wantErr string
	}{
		{
			name: "behavioral null response with completed execution",
			mutate: func(r *Record) {
				r.Response = nil
				r.Output = Output{Availability: "unavailable", Messages: []Message{}, Reason: "Response is missing."}
				r.ActualRow.Run.FinalOutput = ""
			},
			wantErr: "native response/output contradicts execution/grading lifecycle",
		},
		{
			name: "behavioral null response with failed execution",
			mutate: func(r *Record) {
				r.Response = nil
				r.Output = Output{Availability: "unavailable", Messages: []Message{}, Reason: "Response is missing."}
				r.ActualRow.Run.FinalOutput = ""
				r.Lifecycle.Execute = Phase{"failed", "send_failed"}
				r.Lifecycle.Grade = Phase{"not_attempted", "Execution failed."}
			},
			wantErr: "behavioral row has unfinished or failed phase",
		},
		{
			name: "behavioral event model mismatch",
			mutate: func(r *Record) {
				r.EventModels = EventModels{Models: []string{"substituted-model"}, EventsObserved: 1, Complete: true}
			},
			wantErr: "observed model differs from fixed requested model",
		},
		{
			name: "behavioral accounting model mismatch",
			mutate: func(r *Record) {
				r.Accounting = Accounting{SessionKey: r.Response.SessionKey, Source: "rpc", Models: []string{"substituted-model"},
					ProviderCurrencyState: "unavailable", Reason: "Partial SDK accounting observation."}
			},
			wantErr: "observed model differs from fixed requested model",
		},
		{
			name: "behavioral canonical event model mismatch",
			mutate: func(r *Record) {
				r.Response.CanonicalEvents = []json.RawMessage{json.RawMessage(
					`{"type":"assistant.message","data":{"content":"actual nonempty typed output","model":"substituted-model"}}`)}
			},
			wantErr: "canonical event model differs from fixed requested model",
		},
		{
			name: "matching canonical event model and declaration",
			mutate: func(r *Record) {
				r.EventModels = EventModels{Models: []string{"observed-test-model"}, EventsObserved: 1, Complete: true}
				r.Response.CanonicalEvents = []json.RawMessage{json.RawMessage(
					`{"type":"assistant.message","data":{"content":"actual nonempty typed output","model":"observed-test-model"}}`)}
			},
		},
		{
			name: "operational mismatch retained",
			mutate: func(r *Record) {
				*r = operational(*r, "Observed SDK model differs from fixed requested model.")
				r.EventModels = EventModels{Models: []string{"substituted-model"}, EventsObserved: 1, Complete: true}
				r.Accounting = Accounting{SessionKey: r.Response.SessionKey, Source: "events", Models: []string{"substituted-model"},
					ProviderCurrencyState: "unavailable", Reason: "Partial SDK accounting observation."}
			},
		},
		{
			name: "matching observed models",
			mutate: func(r *Record) {
				zero := 0.0
				r.EventModels = EventModels{Models: []string{"observed-test-model"}, EventsObserved: 1, Complete: true}
				r.Accounting = Accounting{SessionKey: r.Response.SessionKey, Source: "rpc", Complete: true,
					ModelAttributionComplete: true, Models: []string{"observed-test-model"}, ProviderCurrencyState: "unavailable",
					Usage: &models.UsageStats{AICredits: &zero, ModelMetrics: map[string]models.ModelUsage{
						"observed-test-model": {AICredits: &zero},
					}}}
			},
		},
		{
			name:   "absent model observations",
			mutate: func(*Record) {},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prepared, admitted, _, _, record := collectionFixture(t)
			require.Equal(t, "2.0", record.Version)
			data, err := prepared.view()
			require.NoError(t, err)
			require.Equal(t, "2.0", data.CollectionVersion)
			require.Equal(t, "observed-test-model", data.Request.Model)
			tc.mutate(&record)
			raw := encoded(t, record)
			if record.Response == nil {
				value, err := jsonutil.Parse(raw)
				require.NoError(t, err)
				object := objectValue(t, value)
				require.Contains(t, object, "response")
				require.Nil(t, object["response"], "the public record must explicitly retain response:null")
				require.Equal(t, "behavioral", record.Summary.Category)
			}
			events, payload := recordTapeBytes(t, admitted, raw)
			prefix, err := InspectCollectionPrefix(t.Context(), events, payload, []*Admitted{admitted})
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Nil(t, prefix, "invalid public records must not return a partial prefix")
				t.Logf("Public prefix rejected record: %v", err)
				return
			}
			require.NoError(t, err)
			require.Len(t, prefix.Records, 1)
			require.Equal(t, "2.0", prefix.Records[0].Version)
			require.Equal(t, record.Summary.Category, prefix.Records[0].Summary.Category)
			require.Equal(t, record.EventModels, prefix.Records[0].EventModels)
			require.Equal(t, record.Accounting, prefix.Records[0].Accounting)
		})
	}
}

func recordTapeBytes(t *testing.T, admitted *Admitted, raw []byte) ([]byte, []byte) {
	t.Helper()
	binding, err := admitted.Binding()
	require.NoError(t, err)
	var record Record
	require.NoError(t, decodeExact(raw, &record))
	recordDigest, rowDigest, err := recordDigests(raw)
	require.NoError(t, err)
	events := append(encoded(t, Event{Kind: eventKind, Version: version, Sequence: 1, Type: "start", Admission: binding}), '\n')
	events = append(events, encoded(t, Event{Kind: eventKind, Version: version, Sequence: 2, Type: "terminal", Admission: binding,
		State: record.Summary.Category, RecordDigest: recordDigest, RowDigest: rowDigest})...)
	events = append(events, '\n')
	return events, append(bytes.Clone(raw), '\n')
}

func TestPublicTapeReaderReconcilesAllCanonicalModelObservations(t *testing.T) {
	events := []struct {
		name string
		raw  string
	}{
		{"usage", `{"type":"assistant.usage","data":{"model":"MODEL"}}`},
		{"usage billing", `{"type":"assistant.usage","data":{"model":"observed-test-model","copilotUsage":{"model":"MODEL"}}}`},
		{"usage token billing", `{"type":"assistant.usage","data":{"model":"observed-test-model","copilotUsage":{"tokenDetails":[{"model":"MODEL"}]}}}`},
		{"turn start", `{"type":"assistant.turn_start","data":{"model":"MODEL"}}`},
		{"turn end", `{"type":"assistant.turn_end","data":{"model":"MODEL"}}`},
		{"turn retry", `{"type":"assistant.turn_retry","data":{"model":"MODEL"}}`},
		{"model call start", `{"type":"model.call_start","data":{"model":"MODEL"}}`},
		{"model call failure", `{"type":"model.call_failure","data":{"model":"MODEL"}}`},
		{"session start", `{"type":"session.start","data":{"selectedModel":"MODEL"}}`},
		{"session resume", `{"type":"session.resume","data":{"selectedModel":"MODEL"}}`},
		{"session change new", `{"type":"session.model_change","data":{"newModel":"MODEL"}}`},
		{"session change previous", `{"type":"session.model_change","data":{"newModel":"observed-test-model","previousModel":"MODEL"}}`},
		{"session deselected", `{"type":"session.model_deselected","data":{"previousModel":"MODEL"}}`},
		{"shutdown current", `{"type":"session.shutdown","data":{"currentModel":"MODEL"}}`},
		{"shutdown metrics", `{"type":"session.shutdown","data":{"modelMetrics":{"MODEL":{}}}}`},
		{"shutdown agent metrics", `{"type":"session.shutdown","data":{"agentMetrics":{"agent":{"modelMetrics":{"MODEL":{}}}}}}`},
		{"compaction start", `{"type":"session.compaction_start","data":{"model":"MODEL"}}`},
		{"compaction complete", `{"type":"session.compaction_complete","data":{"compactionTokensUsed":{"model":"MODEL"}}}`},
		{"compaction billing", `{"type":"session.compaction_complete","data":{"compactionTokensUsed":{"model":"observed-test-model","copilotUsage":{"model":"MODEL"}}}}`},
		{"compaction token billing", `{"type":"session.compaction_complete","data":{"compactionTokensUsed":{"model":"observed-test-model","copilotUsage":{"tokenDetails":[{"model":"MODEL"}]}}}}`},
		{"fusion started", `{"type":"assistant.fusion_phase_started","data":{"model":"MODEL"}}`},
		{"fusion completed", `{"type":"assistant.fusion_phase_completed","data":{"model":"MODEL"}}`},
		{"fusion failed", `{"type":"assistant.fusion_phase_failed","data":{"model":"MODEL"}}`},
		{"subagent configured", `{"type":"subagent.configured","data":{"model":"MODEL"}}`},
		{"subagent started", `{"type":"subagent.started","data":{"model":"MODEL"}}`},
		{"subagent completed", `{"type":"subagent.completed","data":{"model":"MODEL"}}`},
		{"subagent failed", `{"type":"subagent.failed","data":{"model":"MODEL"}}`},
		{"agent interrupted", `{"type":"agent.interrupted","data":{"model":"MODEL"}}`},
		{"tools updated", `{"type":"session.tools_updated","data":{"model":"MODEL"}}`},
		{"tool start", `{"type":"tool.execution_start","data":{"model":"MODEL"}}`},
		{"tool complete", `{"type":"tool.execution_complete","data":{"model":"MODEL"}}`},
		{"skill invoked", `{"type":"skill.invoked","data":{"model":"MODEL"}}`},
		{"exit plan", `{"type":"exit_plan_mode.requested","data":{"model":"MODEL"}}`},
	}
	for _, event := range events {
		t.Run(event.name, func(t *testing.T) {
			for _, mode := range []string{"matching", "mismatch", "undeclared", "operational", "private"} {
				t.Run(mode, func(t *testing.T) {
					_, admitted, _, _, record := collectionFixture(t)
					if mode == "private" {
						_, _, admitted, record = fixture(t)
					}
					record.EventModels = EventModels{Models: []string{"observed-test-model"}, EventsObserved: 2, Complete: true}
					model := "observed-test-model"
					if mode == "mismatch" || mode == "operational" || mode == "private" {
						model = "substituted-model"
					}
					if mode == "undeclared" {
						record.EventModels = EventModels{Models: []string{}}
					}
					if mode == "operational" {
						record = operational(record, "Observed SDK model differs from fixed requested model.")
					}
					matching := strings.ReplaceAll(event.raw, "MODEL", "observed-test-model")
					mutated := strings.ReplaceAll(event.raw, "MODEL", model)
					var sdkEvent copilot.SessionEvent
					require.NoError(t, json.Unmarshal([]byte(mutated), &sdkEvent))
					collector := execution.NewSessionUsageCollector()
					collector.On(sdkEvent)
					require.Contains(t, collector.EventModelObservation().Models, model,
						"fixture must exercise a model observation actually supported by the collector")
					if model != "observed-test-model" {
						require.NotEqual(t, matching, mutated, "change only the selected canonical model field")
					}
					record.Response.CanonicalEvents = append(record.Response.CanonicalEvents, json.RawMessage(mutated))
					raw := encoded(t, record)
					tape, rows := recordTapeBytes(t, admitted, raw)
					var prefix *Prefix
					var err error
					if mode == "private" {
						prefix, err = InspectPrefix(t.Context(), tape, rows, []*Admitted{admitted})
					} else {
						prefix, err = InspectCollectionPrefix(t.Context(), tape, rows, []*Admitted{admitted})
					}
					if mode == "mismatch" || mode == "undeclared" {
						want := "canonical event model differs from fixed requested model"
						if mode == "undeclared" {
							want = "canonical event model is absent from declared event observations"
						}
						require.ErrorContains(t, err, want)
						require.Nil(t, prefix)
						return
					}
					require.NoError(t, err)
					require.Len(t, prefix.Records, 1)
					require.Equal(t, record.Response.CanonicalEvents, prefix.Records[0].Response.CanonicalEvents)
					require.Equal(t, record.Summary.Category, prefix.Records[0].Summary.Category)
				})
			}
		})
	}
}

func TestPublicTapeReaderCanonicalModelAvailability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		wantErr string
	}{
		{"absent", `{"type":"assistant.turn_start","data":{}}`, ""},
		{"null model", `{"type":"assistant.turn_start","data":{"model":null}}`, ""},
		{"null data", `{"type":"assistant.turn_start","data":null}`, ""},
		{"unknown model", `{"type":"assistant.turn_start","data":{"model":"unknown"}}`, ""},
		{"malformed model", `{"type":"assistant.turn_start","data":{"model":42}}`, "decoding native canonical event models"},
		{"malformed nested model", `{"type":"assistant.usage","data":{"model":"observed-test-model","copilotUsage":{"tokenDetails":[{"model":42}]}}}`, "decoding native canonical event models"},
		{"type shadow", `{"type":"assistant.turn_start","Type":"session.info","data":{"model":"substituted-model"}}`, "native JSON key"},
		{"data shadow", `{"type":"assistant.turn_start","data":{"model":"substituted-model"},"Data":{"model":"observed-test-model"}}`, "native JSON key"},
		{"model shadow", `{"type":"assistant.turn_start","data":{"model":"substituted-model","Model":"observed-test-model"}}`, "native JSON key"},
		{"nested model shadow", `{"type":"assistant.usage","data":{"model":"observed-test-model","copilotUsage":{"model":"substituted-model","Model":"observed-test-model"}}}`, "native JSON key"},
		{"unrelated event", `{"type":"session.info","data":{"message":"info","model":"other-model"}}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, admitted, _, _, record := collectionFixture(t)
			record.EventModels = EventModels{Models: []string{"observed-test-model"}, EventsObserved: 1}
			record.Response.CanonicalEvents = append(record.Response.CanonicalEvents, json.RawMessage(tc.raw))
			tape, rows := recordTapeBytes(t, admitted, encoded(t, record))
			prefix, err := InspectCollectionPrefix(t.Context(), tape, rows, []*Admitted{admitted})
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Nil(t, prefix)
				return
			}
			require.NoError(t, err)
			require.Len(t, prefix.Records, 1)
			require.Equal(t, record.Response.CanonicalEvents, prefix.Records[0].Response.CanonicalEvents)
		})
	}
}

func TestFinalAccountingZeroVersusMissingAndPartial(t *testing.T) {
	_, _, admitted, r := fixture(t)
	zero := 0.0
	r.Accounting = Accounting{SessionKey: r.Response.SessionKey, Source: "rpc", Complete: true,
		ModelAttributionComplete: true, Models: []string{"observed-test-model"}, ProviderCurrencyState: "unavailable",
		Usage: &models.UsageStats{AICredits: &zero, ModelMetrics: map[string]models.ModelUsage{
			"observed-test-model": {AICredits: &zero},
		}}}
	decoded, err := DecodeRecord(encoded(t, r), admitted)
	require.NoError(t, err)
	require.NotNil(t, decoded.Accounting.Usage.AICredits)
	require.Zero(t, *decoded.Accounting.Usage.AICredits)
	require.Equal(t, "not_assessed", decoded.Capabilities().ProviderCurrency)
	r.Lifecycle.Shutdown = Phase{"failed", "client_stop"}
	r = operational(r, "Cleanup did not establish final cessation.")
	_, err = DecodeRecord(encoded(t, r), admitted)
	require.Error(t, err, "known counters are retained, not finalized while cleanup is failed")
	r.Accounting.Complete = false
	_, err = DecodeRecord(encoded(t, r), admitted)
	require.NoError(t, err)
	r.Lifecycle.Shutdown = Phase{State: "completed"}
	r.Accounting.Complete = true
	r.Accounting.Usage.AICredits = nil
	_, err = DecodeRecord(encoded(t, r), admitted)
	require.Error(t, err)
	r.Accounting.Complete = false
	decoded, err = DecodeRecord(encoded(t, r), admitted)
	require.NoError(t, err)
	require.Nil(t, decoded.Accounting.Usage.AICredits)
	require.False(t, decoded.Accounting.Complete)
}

func TestResponseRequiresActualAttemptAndUnavailableOutputCannotBeGraded(t *testing.T) {
	for _, name := range []string{"response before execute", "completed without response", "unavailable graded"} {
		t.Run(name, func(t *testing.T) {
			_, _, admitted, r := fixture(t)
			r = operational(r, "synthetic operational failure")
			switch name {
			case "response before execute":
				r.Lifecycle.Execute = Phase{"not_attempted", "not_started"}
			case "completed without response":
				r.Response = nil
				r.Output = Output{Availability: "unavailable", Messages: []Message{}, Reason: "No response."}
				r.ActualRow.Run.FinalOutput = ""
			case "unavailable graded":
				r.Output.Availability, r.Output.Value, r.Output.Reason = "unavailable", nil, "Unusable text."
				r.ActualRow.Run.FinalOutput = ""
				r.Lifecycle.Grade = Phase{State: "completed"}
			}
			_, err := DecodeRecord(encoded(t, r), admitted)
			require.Error(t, err)
		})
	}
}

func TestTapeBindsMultipleAttemptsIncludingOperationalRows(t *testing.T) {
	prepared, profile, first, r1 := fixture(t)
	key := r1.Key
	key.Attempt = 2
	second, err := Admit(context.Background(), prepared, profile, key)
	require.NoError(t, err)
	r2 := operational(r1, "Synthetic second attempt failed grading.")
	r2.Key, r2.Summary.Key = key, key
	r2.Origin.AttemptCount = 2
	r2.ActualRow.Origin, r2.Summary.Origin = r2.Origin, r2.Origin
	r2.ActualRow.Run.Attempts = 2
	r2.Lifecycle.Grade = Phase{"failed", "grade_failed"}
	events, payload := fixtureTapeBytes(t, []*Admitted{first, second}, [][]byte{encoded(t, r1), encoded(t, r2)})
	prefix, err := verifyPrefix(context.Background(), events, payload, []*Admitted{first, second})
	require.NoError(t, err)
	require.Len(t, prefix.Records, 2)
	require.Equal(t, "operational", prefix.Records[1].Summary.Category)
	_, err = verifyPrefix(context.Background(), events, payload, []*Admitted{second, first})
	require.Error(t, err)
	rows, err := lines(payload)
	require.NoError(t, err)
	reordered := append(append(append([]byte{}, rows[1]...), '\n'), rows[0]...)
	reordered = append(reordered, '\n')
	_, err = verifyPrefix(context.Background(), events, reordered, []*Admitted{first, second})
	require.Error(t, err)
	_, err = verifyPrefix(context.Background(), events, append(bytes.Clone(payload), rows[0]...), []*Admitted{first, second})
	require.Error(t, err)
	_, err = verifyPrefix(context.Background(), events, append(bytes.Clone(rows[0]), '\n'), []*Admitted{first, second})
	require.Error(t, err)
}

func TestAbsentResponseCannotBecomeEmptyText(t *testing.T) {
	_, _, admitted, r := fixture(t)
	r = operational(r, "No execution response was preserved.")
	r.Response = nil
	r.Lifecycle.Execute = Phase{"failed", "execute_failed"}
	r.Output = Output{Availability: "unavailable", Messages: []Message{}, Reason: "No actual response."}
	r.ActualRow.Run.FinalOutput = ""
	decoded, err := DecodeRecord(encoded(t, r), admitted)
	require.NoError(t, err)
	require.Nil(t, decoded.Output.Value)
	empty := ""
	r.Output = Output{Availability: "available", Value: &empty, Messages: []Message{}}
	_, err = DecodeRecord(encoded(t, r), admitted)
	require.Error(t, err)
}

func TestTapeReaderRequiresRootedRegularArtifactsAndUniqueAllocation(t *testing.T) {
	_, _, admitted, r := fixture(t)
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	events, payload := fixtureTapeBytes(t, []*Admitted{admitted}, [][]byte{encoded(t, r)})
	require.NoError(t, root.WriteFile(eventFile, events, 0o600))
	require.NoError(t, root.WriteFile(payloadFile, payload, 0o600))
	prefix, err := ReadPrefix(context.Background(), root, []*Admitted{admitted})
	require.NoError(t, err)
	require.Len(t, prefix.Records, 1)
	_, err = ReadPrefix(context.Background(), root, []*Admitted{admitted, admitted})
	require.Error(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ReadPrefix(ctx, root, []*Admitted{admitted})
	require.ErrorIs(t, err, context.Canceled)
	_, err = ReadPrefix(context.Background(), nil, []*Admitted{admitted})
	require.ErrorContains(t, err, "requires an evaluator root")

	t.Run("symlink capability or actual reader rejection", func(t *testing.T) {
		require.NoError(t, root.Rename(payloadFile, "real-rows.ndjson"))
		t.Cleanup(func() {
			require.NoError(t, root.Rename("real-rows.ndjson", payloadFile))
		})
		err := root.Symlink("real-rows.ndjson", payloadFile)
		if err != nil {
			require.Equal(t, "windows", runtime.GOOS, "symlink creation must succeed on Linux/macOS")
			var linkErr *os.LinkError
			require.ErrorAs(t, err, &linkErr)
			require.Equal(t, "symlinkat", linkErr.Op)
			// Windows ERROR_PRIVILEGE_NOT_HELD is 1314, not fs.ErrPermission.
			require.True(t, errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.Errno(1314)),
				"unexpected Windows symlink creation error: %v", err)
			_, statErr := root.Lstat(payloadFile)
			require.ErrorIs(t, statErr, fs.ErrNotExist)
			t.Log("Windows symlink creation capability unavailable; reader symlink rejection NOT certified")
			return
		}
		t.Cleanup(func() { require.NoError(t, root.Remove(payloadFile)) })
		info, err := root.Lstat(payloadFile)
		require.NoError(t, err)
		require.NotZero(t, info.Mode()&os.ModeSymlink)
		_, err = ReadPrefix(context.Background(), root, []*Admitted{admitted})
		require.ErrorContains(t, err, "contains a symlink")
		_, err = ReadPrefix(ctx, root, []*Admitted{admitted})
		require.ErrorIs(t, err, context.Canceled)
	})

	for _, artifact := range []string{eventFile, payloadFile} {
		t.Run("directory "+artifact, func(t *testing.T) {
			require.NoError(t, root.Rename(artifact, "regular-artifact"))
			require.NoError(t, root.Mkdir(artifact, 0o700))
			_, err := ReadPrefix(context.Background(), root, []*Admitted{admitted})
			require.ErrorContains(t, err, "must be a regular file")
			require.NoError(t, root.Remove(artifact))
			require.NoError(t, root.Rename("regular-artifact", artifact))
		})
		t.Run("missing "+artifact, func(t *testing.T) {
			require.NoError(t, root.Rename(artifact, "regular-artifact"))
			_, err := ReadPrefix(context.Background(), root, []*Admitted{admitted})
			require.ErrorIs(t, err, fs.ErrNotExist)
			require.NoError(t, root.Rename("regular-artifact", artifact))
		})
	}
	prefix, err = ReadPrefix(context.Background(), root, []*Admitted{admitted})
	require.NoError(t, err)
	require.Len(t, prefix.Records, 1)
}

func TestTapePayloadBeforeTerminalAndCrashPrefixes(t *testing.T) {
	for _, failure := range []string{"none", "payload sync", "terminal sync", "start only", "orphan", "null start field", "torn", "duplicate payload", "changed payload", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			_, _, admitted, r := fixture(t)
			// Synthetic byte prefixes model observations, not fsync acknowledgments.
			events, payload := fixtureTapeBytes(t, []*Admitted{admitted}, [][]byte{encoded(t, r)})
			switch failure {
			case "orphan", "payload sync":
				events = bytes.SplitAfter(events, []byte{'\n'})[0]
			case "start only", "cancel":
				events = bytes.SplitAfter(events, []byte{'\n'})[0]
				payload = nil
			case "null start field":
				events = bytes.Replace(events, []byte(`"type":"start"`), []byte(`"type":"start","row_digest":null`), 1)
			case "torn":
				events = events[:len(events)-1]
			case "duplicate payload":
				payload = append(payload, bytes.Clone(payload)...)
			case "changed payload":
				payload = bytes.Replace(payload, []byte("actual nonempty"), []byte("tampered nonempty"), 1)
			}
			prefix, err := verifyPrefix(context.Background(), events, payload, []*Admitted{admitted})
			switch failure {
			case "none", "terminal sync":
				require.NoError(t, err)
				require.Len(t, prefix.Records, 1)
				require.Nil(t, prefix.Pending)
			case "start only", "cancel":
				require.NoError(t, err)
				require.NotNil(t, prefix.Pending)
			default:
				require.Error(t, err)
			}
			if failure == "payload sync" {
				require.NotContains(t, string(events), `"type":"terminal"`)
			}
		})
	}
}

// fixtureTapeBytes supplies valid local bytes without constructing a durable writer.
func fixtureTapeBytes(t *testing.T, admissions []*Admitted, records [][]byte) ([]byte, []byte) {
	t.Helper()
	require.Len(t, records, len(admissions))
	var events, payload []byte
	for i, admitted := range admissions {
		binding, err := admitted.Binding()
		require.NoError(t, err)
		record, err := DecodeRecord(records[i], admitted)
		require.NoError(t, err)
		recordDigest, rowDigest, err := recordDigests(records[i])
		require.NoError(t, err)
		start := Event{Kind: eventKind, Version: version, Sequence: 2*i + 1, Type: "start", Admission: binding}
		terminal := Event{Kind: eventKind, Version: version, Sequence: 2*i + 2, Type: "terminal", Admission: binding,
			State: record.Summary.Category, RecordDigest: recordDigest, RowDigest: rowDigest}
		events = append(events, encoded(t, start)...)
		events = append(events, '\n')
		events = append(events, encoded(t, terminal)...)
		events = append(events, '\n')
		payload = append(payload, records[i]...)
		payload = append(payload, '\n')
	}
	return events, payload
}

// Frozen private 1.0 fixture and acceptance oracle: b64c6bb10, before public
// model binding. Both observed model inventories intentionally differ from the request.
const frozenPrivateModelMismatchRecord = `{
  "kind": "waza.release-native-task-attempt",
  "version": "1.0",
  "collection_id": "c8809a2f690e9145dc3074b48551a56338da64510d8eb468d2a731e3a6a21f92",
  "policy_digest": {
    "sha256": "785c7c9e8eb92b41a6b69771102ecc8b635532dc69a8af65a6f3e68fb29245a2",
    "encoding": "json-v1"
  },
  "profile_digest": {
    "sha256": "c8809a2f690e9145dc3074b48551a56338da64510d8eb468d2a731e3a6a21f92",
    "encoding": "json-v1"
  },
  "resolved_plan_digest": {
    "sha256": "df1912786a1008385da3992614e8d95a5a0a0e48fa3a10d933169148f9e158a3",
    "encoding": "json-v1"
  },
  "request_digest": {
    "sha256": "5ba27545763382701704a7b779d4a73695916b98c7f0ada1260a637c231cc6ef",
    "encoding": "json-v1"
  },
  "key": {
    "cluster_id": "cluster",
    "task_id": "task",
    "trial_ordinal": 1,
    "arm": "baseline",
    "eval_id": "eval",
    "attempt_ordinal": 1
  },
  "origin": {
    "eval_id": "eval",
    "task_id": "task",
    "run_number": 1,
    "attempt_count": 1,
    "prior_attempts": ""
  },
  "lifecycle": {
    "construction": {
      "state": "completed"
    },
    "initialize": {
      "state": "completed"
    },
    "execute": {
      "state": "completed"
    },
    "grade": {
      "state": "completed"
    },
    "shutdown": {
      "state": "completed"
    }
  },
  "response": {
    "session_key": {
      "sha256": "cce7ed05612e0aa42deb2737659e9b4e5231548733b2bd8c6571725f098a127e",
      "encoding": "json-v1"
    },
    "success": true,
    "error_msg": "",
    "final_output": "actual nonempty typed output",
    "duration_ms": 0,
    "canonical_events": [
      {
        "data": {
          "content": "actual nonempty typed output"
        },
        "type": "assistant.message"
      }
    ],
    "tool_policy_mode": "deny_all",
    "tool_policy_denials": [],
    "usage_is_cumulative": false,
    "usage_revision": 0,
    "usage": null
  },
  "output": {
    "availability": "available",
    "value": "actual nonempty typed output",
    "messages": [
      {
        "event_ordinal": 1,
        "content": "actual nonempty typed output",
        "content_presence": "typed_nonempty"
      }
    ]
  },
  "diagnostics": [],
  "event_models": {
    "models": [
      "substituted-model"
    ],
    "events_observed": 1,
    "complete": true
  },
  "accounting": {
    "session_key": {
      "sha256": "cce7ed05612e0aa42deb2737659e9b4e5231548733b2bd8c6571725f098a127e",
      "encoding": "json-v1"
    },
    "source": "rpc",
    "complete": false,
    "model_attribution_complete": false,
    "models": [
      "substituted-model"
    ],
    "usage": null,
    "provider_currency_state": "unavailable",
    "reason": "Partial SDK accounting observation."
  },
  "actual_row": {
    "origin": {
      "eval_id": "eval",
      "task_id": "task",
      "run_number": 1,
      "attempt_count": 1,
      "prior_attempts": ""
    },
    "run": {
      "run_number": 1,
      "attempts": 1,
      "prompt": "synthetic prompt",
      "status": "passed",
      "duration_ms": 0,
      "validations": {
        "check": {
          "identifier": "check",
          "type": "text",
          "score": 1,
          "weight": 1,
          "passed": true,
          "feedback": "",
          "duration_ms": 0
        }
      },
      "session_digest": {
        "tool_call_count": 0,
        "tools_used": [],
        "errors": []
      },
      "final_output": "actual nonempty typed output"
    }
  },
  "summary": {
    "key": {
      "cluster_id": "cluster",
      "task_id": "task",
      "trial_ordinal": 1,
      "arm": "baseline",
      "eval_id": "eval",
      "attempt_ordinal": 1
    },
    "origin": {
      "eval_id": "eval",
      "task_id": "task",
      "run_number": 1,
      "attempt_count": 1,
      "prior_attempts": ""
    },
    "status": "passed",
    "category": "behavioral",
    "checks": [
      {
        "scope": "eval",
        "grader": "check",
        "passed": true,
        "score": 1,
        "operational_state": "observed"
      }
    ],
    "runtime": {
      "task_id": "task",
      "requested_engine": "",
      "requested_model": "",
      "requested_reasoning": "",
      "availability": "unavailable",
      "engine_implementation": "",
      "model_version": "",
      "reason": "Synthetic fixture is not runtime evidence."
    },
    "evidence_references": []
  }
}`

func TestPrivateTapeReaderPreservesFrozenModelMismatch(t *testing.T) {
	prepared, profile, admitted, record := fixture(t)
	require.Equal(t, "1.0", record.Version)
	require.Equal(t, "1.0", profile.Version)
	data, err := prepared.view()
	require.NoError(t, err)
	require.Empty(t, data.CollectionVersion)
	require.Equal(t, "observed-test-model", data.Request.Model)
	var compact bytes.Buffer
	require.NoError(t, json.Compact(&compact, []byte(frozenPrivateModelMismatchRecord)))
	var frozen Record
	require.NoError(t, json.Unmarshal(compact.Bytes(), &frozen))
	require.Equal(t, "1.0", frozen.Version)
	require.Equal(t, "behavioral", frozen.Summary.Category)
	require.Equal(t, []string{"substituted-model"}, frozen.EventModels.Models)
	require.Equal(t, []string{"substituted-model"}, frozen.Accounting.Models)
	events, payload := recordTapeBytes(t, admitted, compact.Bytes())
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	require.NoError(t, root.WriteFile(eventFile, events, 0o600))
	require.NoError(t, root.WriteFile(payloadFile, payload, 0o600))
	prefix, err := ReadPrefix(t.Context(), root, []*Admitted{admitted})
	require.NoError(t, err)
	require.Len(t, prefix.Records, 1)
	require.Equal(t, frozen, prefix.Records[0])
	require.Nil(t, prefix.Pending)
}
