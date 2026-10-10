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
	for _, name := range []string{"runtime version", "extra capture", "nontext grader", "zero weight", "unscoped check", "passed failed check", "private event id", "private nested id", "private hyphen id", "private punctuation id"} {
		t.Run(name, func(t *testing.T) {
			_, _, admitted, record := fixture(t)
			switch name {
			case "runtime version":
				record.Summary.Runtime.ModelVersion = "invented"
			case "extra capture":
				record.ActualRow.Run.SnapshotPath = "invented"
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
	for _, name := range []string{"nil policy", "unrestricted", "callback", "custom tool", "skills", "ephemeral", "workspace capture", "session", "traversal", "workdir alias", "workdir dot", "duplicate", "negative timeout", "missing source", "source alias", "source directory", "symlink", "cancel"} {
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
