package nativesnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

type retryJoinFixture struct {
	slotFixtureResult
	events     []releasepolicy.Event
	admissions []*nativetask.Admitted
	records    []nativetask.Record
	profile    *nativetask.Profile
}

func retryJoinJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return raw
}

func retryJoinStream(t *testing.T, events []releasepolicy.Event) []byte {
	t.Helper()
	raw := []byte{}
	for _, event := range events {
		raw = append(raw, retryJoinJSON(t, event)...)
		raw = append(raw, '\n')
	}
	return raw
}

func retryNativeRecord(t *testing.T, admitted *nativetask.Admitted, prompt string, settings releasepolicy.Settings, passed bool) nativetask.Record {
	t.Helper()
	binding, err := admitted.Binding()
	require.NoError(t, err)
	key := binding.Key
	origin := models.EvidenceOrigin{EvalID: key.EvalID, TaskID: key.TaskID, RunNumber: key.Trial, AttemptCount: key.Attempt}
	status, score, number, output := models.StatusPassed, 1.0, json.Number("1"), "hello"
	if !passed {
		status, score, number, output = models.StatusFailed, 0, "0", "incorrect"
	}
	grade := models.GraderResults{Name: "text", Type: models.GraderKindText, Passed: passed, Score: score, Weight: 1,
		Details: map[string]any{"exact_integer": json.Number("9007199254740993")}}
	summary := releasepolicy.AttemptSummary{Key: key, Origin: origin, Status: string(status), Category: "behavioral",
		Checks: []releasepolicy.CheckSummary{{Scope: "eval", Grader: "text", Passed: passed, Score: number, OperationalState: "observed"}},
		Runtime: releasepolicy.RuntimeObservation{TaskID: key.TaskID, RequestedEngine: settings.Engine,
			RequestedModel: settings.Model, RequestedReasoning: settings.ReasoningEffort, Availability: "unavailable",
			Reason: "Synthetic retained bytes do not observe runtime versions."}, References: []models.EvidenceReference{}}
	session := slotTestDigest(t, key)
	completed := nativetask.Phase{State: "completed"}
	record := nativetask.Record{Kind: "waza.release-native-task-attempt", Version: "1.0",
		CollectionID: binding.CollectionID, PolicyDigest: binding.PolicyDigest, ProfileDigest: binding.ProfileDigest,
		PlanDigest: binding.PlanDigest, RequestDigest: binding.RequestDigest, Key: key, Origin: origin,
		Lifecycle: nativetask.Lifecycle{Construction: completed, Initialize: completed, Execute: completed, Grade: completed, Shutdown: completed},
		Response: &nativetask.Response{SessionKey: &session, Success: true, FinalOutput: output, ToolPolicyMode: "deny_all",
			CanonicalEvents:   []json.RawMessage{retryJoinJSON(t, map[string]any{"type": "assistant.message", "data": map[string]any{"content": output}})},
			ToolPolicyDenials: []models.ToolPolicyDenial{}},
		Output: nativetask.Output{Availability: "available", Value: &output, Messages: []nativetask.Message{
			{EventOrdinal: 1, Content: output, ContentPresence: "typed_nonempty"}}},
		Diagnostics: []nativetask.Diagnostic{}, EventModels: nativetask.EventModels{Models: []string{}},
		Accounting: nativetask.Accounting{Source: "none", ProviderCurrencyState: "unavailable", Models: []string{},
			Reason: "Synthetic fixture has no provider billing."},
		ActualRow: releasepolicy.ActualRunRow{Origin: origin, Run: models.RunResult{
			RunNumber: key.Trial, Attempts: key.Attempt, Prompt: prompt, Status: status, FinalOutput: output,
			Validations:   map[string]models.GraderResults{"text": grade},
			SessionDigest: models.SessionDigest{ToolsUsed: []string{}, Errors: []string{}}}},
		Summary: summary}
	_, err = nativetask.DecodeRecord(retryJoinJSON(t, record), admitted)
	require.NoError(t, err, "each fixture row must pass the real native record validator")
	return record
}

func newRetryJoinFixture(t *testing.T) retryJoinFixture {
	t.Helper()
	return retryJoinWithSlots(t, slotFixture(t, "multi"))
}

func retryJoinWithSlots(t *testing.T, f slotFixtureResult) retryJoinFixture {
	t.Helper()
	p, err := releasepolicy.DecodePolicy(f.raw)
	require.NoError(t, err)
	profile := &nativetask.Profile{Kind: "waza.release-native-task-profile", Version: "1.0",
		Nonce: strings.Repeat("b", 64), Selection: nativetask.Selection{Operation: "native_text_task", Version: "1.0"},
		PolicyDigest: p.Digest, PermissionMode: "deny_all", Arms: map[releasepolicy.Arm]nativetask.ArmPlan{}}
	primitives := map[releasepolicy.Arm]map[string]*nativetask.Prepared{}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		primitives[arm] = map[string]*nativetask.Prepared{}
		plan := nativetask.ArmPlan{PlanDigest: p.Arms[arm].Digest, Requests: []nativetask.RequestPlan{}}
		for _, task := range p.Arms[arm].Plan.Tasks {
			request, err := f.capture.prepared.Request(arm, task.ID)
			require.NoError(t, err)
			sources := []nativetask.Source{}
			for _, source := range f.original.Sources[arm] {
				sources = append(sources, nativetask.Source{Root: f.locations[arm].Root, Path: source.Path})
			}
			primitive, err := nativetask.Prepare(t.Context(), arm, task.ID, request, sources)
			require.NoError(t, err)
			primitives[arm][task.ID] = primitive
			plan.Requests = append(plan.Requests, nativetask.RequestPlan{TaskID: task.ID, RequestDigest: primitive.RequestDigest()})
		}
		slices.SortFunc(plan.Requests, func(a, b nativetask.RequestPlan) int { return strings.Compare(a.TaskID, b.TaskID) })
		profile.Arms[arm] = plan
	}
	raw, err := releasepolicy.SealJSON(profile)
	require.NoError(t, err)
	profile, err = nativetask.DecodeProfile(raw)
	require.NoError(t, err)
	result := retryJoinFixture{slotFixtureResult: f, profile: profile}
	appendEvent := func(event releasepolicy.Event) {
		event.Sequence, event.CollectionID, event.PolicyDigest = len(result.events)+1, profile.Digest.SHA256, p.Digest
		result.events = append(result.events, event)
	}
	appendEvent(releasepolicy.Event{Type: "begin_arm", Arm: releasepolicy.Baseline, EvalID: slotTestIDs.Baseline})
	appendEvent(releasepolicy.Event{Type: "begin_arm", Arm: releasepolicy.Candidate, EvalID: slotTestIDs.Candidate})
	for ci, cluster := range p.Design.Clusters {
		appendEvent(releasepolicy.Event{Type: "cluster_start", ClusterID: cluster.ID})
		for _, arm := range p.Design.Allocation.Assignments[ci].Order {
			evalID := slotTestIDs.Baseline
			if arm == releasepolicy.Candidate {
				evalID = slotTestIDs.Candidate
			}
			for _, task := range cluster.Tasks {
				var settings releasepolicy.Settings
				for _, planned := range p.Arms[arm].Plan.Tasks {
					if planned.ID == task.ID {
						settings = planned.Settings
					}
				}
				for _, trial := range task.TrialOrdinals {
					sample := releasepolicy.SampleKey{ClusterID: cluster.ID, TaskID: task.ID, Trial: trial}
					firstPass, retryPass := len(result.records) != 0, true
					count := 1
					if !firstPass {
						count = 2
					}
					for attempt := 1; attempt <= count; attempt++ {
						key := releasepolicy.AttemptKey{SampleKey: sample, Arm: arm, EvalID: evalID, Attempt: attempt}
						admitted, err := nativetask.Admit(t.Context(), primitives[arm][task.ID], profile, key)
						require.NoError(t, err)
						request, err := f.capture.prepared.Request(arm, task.ID)
						require.NoError(t, err)
						record := retryNativeRecord(t, admitted, request.Message, settings, firstPass || attempt == 2)
						result.admissions = append(result.admissions, admitted)
						result.records = append(result.records, record)
						appendEvent(releasepolicy.Event{Type: "attempt_start", Key: &key})
						appendEvent(releasepolicy.Event{Type: "attempt_terminal", Attempt: &record.Summary})
					}
					appendEvent(releasepolicy.Event{Type: "trial_terminal", Arm: arm, Trial: &releasepolicy.TrialSummary{
						Key: sample, Terminal: count, State: "complete", FirstPass: &firstPass, RetryPass: &retryPass}})
				}
			}
		}
	}
	usage := []releasepolicy.UsageAxis{}
	for _, axis := range []string{"input_tokens", "output_tokens", "ai_credits"} {
		usage = append(usage, releasepolicy.UsageAxis{Axis: axis, Availability: "unavailable",
			Reason: "Synthetic fixture has no usage.", Observation: "unknown"})
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		appendEvent(releasepolicy.Event{Type: "end_arm", Arm: arm, Usage: usage})
	}
	appendEvent(releasepolicy.Event{Type: "collection_end"})
	_, err = releasepolicy.InspectRetryPrefix(t.Context(), f.raw, retryJoinStream(t, result.events))
	require.NoError(t, err, "fixture must pass the real paired replay independently")
	return result
}

func retryJoinNativeBytes(t *testing.T, admissions []*nativetask.Admitted, records []nativetask.Record, pending bool) ([]byte, []byte) {
	t.Helper()
	require.Equal(t, len(records)+boolCount(pending), len(admissions))
	events, rows := []byte{}, []byte{}
	for i, admitted := range admissions {
		binding, err := admitted.Binding()
		require.NoError(t, err)
		start := nativetask.Event{Kind: "waza.release-native-task-event", Version: "1.0",
			Sequence: 2*i + 1, Type: "start", Admission: binding}
		events = append(events, append(retryJoinJSON(t, start), '\n')...)
		if i == len(records) {
			continue
		}
		raw := retryJoinJSON(t, records[i])
		_, err = nativetask.DecodeRecord(raw, admitted)
		require.NoError(t, err)
		value, err := jsonutil.Parse(raw)
		require.NoError(t, err)
		object, ok := value.(map[string]any)
		require.True(t, ok)
		digest, err := evidence.JSONDigest(value)
		require.NoError(t, err)
		row, err := evidence.JSONDigest(object["actual_row"])
		require.NoError(t, err)
		terminal := nativetask.Event{Kind: start.Kind, Version: start.Version, Sequence: start.Sequence + 1,
			Type: "terminal", Admission: binding, State: records[i].Summary.Category, RecordDigest: digest, RowDigest: row}
		events = append(events, append(retryJoinJSON(t, terminal), '\n')...)
		rows = append(rows, append(raw, '\n')...)
	}
	return events, rows
}

func boolCount(v bool) int {
	if v {
		return 1
	}
	return 0
}

func joinFixturePrefix(t *testing.T, f retryJoinFixture, coreCount, nativeCount int, pending, sealed bool) *RetryJoined {
	t.Helper()
	stream := retryJoinStream(t, f.events[:coreCount])
	var retry *releasepolicy.RetryPrefix
	var err error
	if sealed {
		journal, sealErr := releasepolicy.SealJSON(releasepolicy.Journal{Kind: releasepolicy.JournalKind, Version: releasepolicy.Version,
			CollectionID: f.events[0].CollectionID, StreamDigest: releasepolicy.SourceDigest(stream), Events: f.events[:coreCount]})
		require.NoError(t, sealErr)
		retry, err = releasepolicy.InspectRetryJournal(t.Context(), f.raw, journal, stream)
	} else {
		retry, err = releasepolicy.InspectRetryPrefix(t.Context(), f.raw, stream)
	}
	require.NoError(t, err)
	admissions := f.admissions[:nativeCount+boolCount(pending)]
	events, rows := retryJoinNativeBytes(t, admissions, f.records[:nativeCount], pending)
	joined, err := CheckRetryJoin(t.Context(), f.set, retry, events, rows, admissions)
	require.NoError(t, err)
	return joined
}

func TestRetryJoinActualOrderRecoveryAndOwnership(t *testing.T) {
	f := newRetryJoinFixture(t)
	for _, sealed := range []bool{false, true} {
		joined := joinFixturePrefix(t, f, len(f.events), len(f.records), false, sealed)
		view, err := joined.View(t.Context())
		require.NoError(t, err)
		require.Equal(t, "structurally_matched", view.State)
		require.Len(t, view.Attempts, 9)
		require.Len(t, f.set.slots, 16, "unused possible retry slots are not missing evidence")
		require.Equal(t, "failed", view.Attempts[0].Core.Status)
		require.Equal(t, "passed", view.Attempts[1].Core.Status)
		require.Equal(t, 2, view.Attempts[1].Slot.Key.Attempt)
		require.Greater(t, view.Attempts[3].Slot.Index, view.Attempts[2].Slot.Index+1, "actual order skips unconsumed retry slots")
		for _, attempt := range view.Attempts {
			require.Equal(t, "terminal", attempt.NativeState)
			require.NotNil(t, attempt.Native)
			require.Equal(t, attempt.Slot.Key, attempt.Admission.Key)
			require.Nil(t, attempt.Native.ActualRow.Run.Usage)
			require.Equal(t, "unavailable", attempt.Native.Accounting.ProviderCurrencyState)
			require.Equal(t, json.Number("9007199254740993"), attempt.Native.ActualRow.Run.Validations["text"].Details["exact_integer"])
		}
		view.Attempts[0].Native.ActualRow.Run.Validations["mutation"] = models.GraderResults{}
		view.Attempts[0].Core.Checks[0].Score = "0.5"
		view.NativeRows.Digest.SHA256 = "mutation"
		again, err := joined.View(t.Context())
		require.NoError(t, err)
		require.NotContains(t, again.Attempts[0].Native.ActualRow.Run.Validations, "mutation")
		require.Equal(t, json.Number("0"), again.Attempts[0].Core.Checks[0].Score)
		require.NotEqual(t, "mutation", again.NativeRows.Digest.SHA256)
	}
	joined := joinFixturePrefix(t, f, len(f.events), len(f.records), false, false)
	before, err := joined.View(t.Context())
	require.NoError(t, err)
	events, rows := retryJoinNativeBytes(t, f.admissions, f.records, false)
	stream := retryJoinStream(t, f.events)
	retry, err := releasepolicy.InspectRetryPrefix(t.Context(), f.raw, stream)
	require.NoError(t, err)
	admissions := append([]*nativetask.Admitted{}, f.admissions...)
	owned, err := CheckRetryJoin(t.Context(), f.set, retry, events, rows, admissions)
	require.NoError(t, err)
	events[0], rows[0], stream[0], admissions[0] = 'x', 'x', 'x', nil
	f.raw[0] = 'x'
	after, err := owned.View(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, after)
	// Source-root availability is unrelated to retained consistency.
	require.NoError(t, f.locations[releasepolicy.Baseline].Root.Close())
	after, err = owned.View(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestRetryJoinCleanPrefixesAndPresence(t *testing.T) {
	f := newRetryJoinFixture(t)
	for _, tc := range []struct {
		name        string
		core        int
		native      int
		pending     bool
		starts      int
		nativeState string
	}{
		{"empty", 0, 0, false, 0, ""},
		{"first begin", 1, 0, false, 0, ""},
		{"before native start", 4, 0, false, 1, "missing"},
		{"native start", 4, 0, true, 1, "pending"},
		{"native terminal before core", 4, 1, false, 1, "terminal"},
		{"core terminal", 5, 1, false, 1, "terminal"},
		{"recovered terminal before trial", 7, 2, false, 2, "terminal"},
		{"complete core missing native", len(f.events), 0, false, len(f.records), "missing"},
		{"complete core partial native", len(f.events), 1, true, len(f.records), "terminal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			joined := joinFixturePrefix(t, f, tc.core, tc.native, tc.pending, false)
			view, err := joined.View(t.Context())
			require.NoError(t, err)
			require.Equal(t, "incomplete", view.State)
			require.Len(t, view.Attempts, tc.starts)
			if tc.starts > 0 {
				require.Equal(t, tc.nativeState, view.Attempts[0].NativeState)
			}
			if tc.name == "recovered terminal before trial" {
				require.NotNil(t, view.Attempts[1].Core)
				require.Equal(t, "passed", view.Attempts[1].Core.Status)
				require.Equal(t, 2, view.Attempts[1].Core.Key.Attempt)
			}
		})
	}
	for _, events := range [][]byte{nil, {}} {
		for _, rows := range [][]byte{nil, {}} {
			retry, err := releasepolicy.InspectRetryPrefix(t.Context(), f.raw, nil)
			require.NoError(t, err)
			joined, err := CheckRetryJoin(t.Context(), f.set, retry, events, rows, nil)
			require.NoError(t, err)
			view, err := joined.View(t.Context())
			require.NoError(t, err)
			require.Equal(t, "missing", view.CoreState)
			require.Nil(t, view.CoreStream)
			require.Equal(t, events != nil, view.NativeEvents.Present)
			require.Equal(t, rows != nil, view.NativeRows.Present)
			require.Equal(t, events == nil, view.NativeEvents.Digest == nil)
			require.Equal(t, rows == nil, view.NativeRows.Digest == nil)
		}
	}
	stream := append(retryJoinStream(t, f.events[:4]), []byte(`{"torn-core-tail`)...)
	retry, err := releasepolicy.InspectRetryPrefix(t.Context(), f.raw, stream)
	require.NoError(t, err)
	joined, err := CheckRetryJoin(t.Context(), f.set, retry, nil, nil, nil)
	require.NoError(t, err)
	view, err := joined.View(t.Context())
	require.NoError(t, err)
	require.Equal(t, "partial", view.CoreState)
	require.Equal(t, len(`{"torn-core-tail`), view.TrailingFragmentBytes)
	require.Equal(t, "incomplete", view.State)
}

func TestRetryJoinRejectsContradictionsWithoutPartialResults(t *testing.T) {
	f := newRetryJoinFixture(t)
	retry, err := releasepolicy.InspectRetryPrefix(t.Context(), f.raw, retryJoinStream(t, f.events))
	require.NoError(t, err)
	for _, name := range []string{"torn event", "torn row", "orphan", "duplicate row", "extra event", "omit earlier failure",
		"reorder", "summary mismatch", "numeric token mismatch", "extra admission", "nil admission"} {
		t.Run(name, func(t *testing.T) {
			admissions := append([]*nativetask.Admitted{}, f.admissions...)
			records := append([]nativetask.Record{}, f.records...)
			events, rows := retryJoinNativeBytes(t, admissions, records, false)
			switch name {
			case "torn event":
				events = events[:len(events)-1]
			case "torn row":
				rows = rows[:len(rows)-1]
			case "orphan":
				events = bytes.SplitAfterN(events, []byte{'\n'}, 2)[0]
			case "duplicate row":
				rows = append(rows, bytes.SplitAfterN(rows, []byte{'\n'}, 2)[0]...)
			case "extra event":
				events = append(events, []byte("{}\n")...)
			case "omit earlier failure":
				admissions, records = admissions[1:], records[1:]
				events, rows = retryJoinNativeBytes(t, admissions, records, false)
			case "reorder":
				admissions[0], admissions[1] = admissions[1], admissions[0]
				records[0], records[1] = records[1], records[0]
				events, rows = retryJoinNativeBytes(t, admissions, records, false)
			case "summary mismatch":
				records[0].Summary.Runtime.Reason = "Different, independently valid unavailable reason."
				events, rows = retryJoinNativeBytes(t, admissions, records, false)
			case "numeric token mismatch":
				records[0].Summary.Checks = append([]releasepolicy.CheckSummary{}, records[0].Summary.Checks...)
				records[0].Summary.Checks[0].Score = "0.0"
				events, rows = retryJoinNativeBytes(t, admissions, records, false)
			case "extra admission":
				admissions = append(admissions, admissions[0])
			case "nil admission":
				admissions[0] = nil
			}
			joined, err := CheckRetryJoin(t.Context(), f.set, retry, events, rows, admissions)
			require.Error(t, err)
			require.Nil(t, joined, "invalid evidence must never publish a reduced valid prefix")
		})
	}
	early, err := releasepolicy.InspectRetryPrefix(t.Context(), f.raw, retryJoinStream(t, f.events[:3]))
	require.NoError(t, err)
	events, rows := retryJoinNativeBytes(t, f.admissions[:1], f.records[:1], false)
	joined, err := CheckRetryJoin(t.Context(), f.set, early, events, rows, f.admissions[:1])
	require.Error(t, err, "native start without a core start is invalid")
	require.Nil(t, joined)
	otherPolicy, err := releasepolicy.InspectRetryPrefix(t.Context(), append(bytes.Clone(f.raw), ' '), nil)
	require.NoError(t, err)
	joined, err = CheckRetryJoin(t.Context(), f.set, otherPolicy, nil, nil, nil)
	require.Error(t, err, "semantically matching policies cannot substitute original policy bytes")
	require.Nil(t, joined)
}

func TestRetryJoinCancellationAndRetainedTampering(t *testing.T) {
	f := newRetryJoinFixture(t)
	joined := joinFixturePrefix(t, f, len(f.events), len(f.records), false, false)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, input := range []context.Context{nil, ctx} {
		view, err := joined.View(input)
		require.Error(t, err)
		require.Equal(t, RetryJoinView{}, view)
		if input != nil {
			require.ErrorIs(t, err, context.Canceled)
		}
		result, err := CheckRetryJoin(input, f.set, joined.retry, joined.events, joined.rows, joined.admissions)
		require.Error(t, err)
		require.Nil(t, result)
	}
	_, err := (*RetryJoined)(nil).View(t.Context())
	require.Error(t, err)
	_, err = CheckRetryJoin(t.Context(), f.set, nil, nil, nil, nil)
	require.Error(t, err)
	joined.seal.SHA256 = strings.Repeat("0", 64)
	view, err := joined.View(t.Context())
	require.Error(t, err)
	require.Equal(t, RetryJoinView{}, view)
}

func TestRetryJoinRejectsReboundIdentitiesAndChangedPreparedSources(t *testing.T) {
	f := newRetryJoinFixture(t)
	events, rows := retryJoinNativeBytes(t, f.admissions, f.records, false)
	stream := retryJoinStream(t, f.events)
	for _, pair := range [][2]string{
		{f.events[0].CollectionID, strings.Repeat("0", 64)},
		{slotTestIDs.Baseline, "different-eval"},
	} {
		changed := bytes.ReplaceAll(stream, []byte(pair[0]), []byte(pair[1]))
		require.NotEqual(t, stream, changed)
		retry, err := releasepolicy.InspectRetryPrefix(t.Context(), f.raw, changed)
		require.NoError(t, err, "the supplied retry stream is independently valid")
		joined, err := CheckRetryJoin(t.Context(), f.set, retry, events, rows, f.admissions)
		require.Error(t, err, "valid but differently bound evidence must not join")
		require.Nil(t, joined)
	}
	first := f.records[0].Key
	request, err := f.capture.prepared.Request(first.Arm, first.TaskID)
	require.NoError(t, err)
	require.NoError(t, f.locations[first.Arm].Root.WriteFile(string(first.Arm)+"/context/a.txt", []byte("changed source"), 0600))
	sources := []nativetask.Source{}
	for _, source := range f.original.Sources[first.Arm] {
		sources = append(sources, nativetask.Source{Root: f.locations[first.Arm].Root, Path: source.Path})
	}
	prepared, err := nativetask.Prepare(t.Context(), first.Arm, first.TaskID, request, sources)
	require.NoError(t, err)
	substituted, err := nativetask.Admit(t.Context(), prepared, f.profile, first)
	require.NoError(t, err)
	want, err := f.admissions[0].Binding()
	require.NoError(t, err)
	got, err := substituted.Binding()
	require.NoError(t, err)
	require.Equal(t, want, got, "matching binding fields alone cannot prove complete captured source equality")
	retry, err := releasepolicy.InspectRetryPrefix(t.Context(), f.raw, stream)
	require.NoError(t, err)
	admissions := append([]*nativetask.Admitted{}, f.admissions...)
	admissions[0] = substituted
	joined, err := CheckRetryJoin(t.Context(), f.set, retry, events, rows, admissions)
	require.ErrorContains(t, err, "source")
	require.Nil(t, joined)
}

func TestRetryJoinRetainsOperationalCancellationWithoutRetryAuthority(t *testing.T) {
	f := newRetryJoinFixture(t)
	for _, canceled := range []bool{false, true} {
		record := f.records[0]
		record.Summary.Category, record.Summary.Status = "operational", "incomplete"
		record.Summary.Checks = []releasepolicy.CheckSummary{}
		record.ActualRow.Run.Validations = map[string]models.GraderResults{}
		record.ActualRow.Run.Status, record.ActualRow.Run.ErrorMsg = models.StatusError, "synthetic operational failure"
		record.Response = new(*record.Response)
		record.Response.Success, record.Response.ErrorMsg = false, record.ActualRow.Run.ErrorMsg
		record.Lifecycle.Grade = nativetask.Phase{State: "not_attempted", Reason: "Execution did not complete."}
		record.Lifecycle.Execute = nativetask.Phase{State: "failed", Reason: "Synthetic execution failure."}
		if canceled {
			record.Lifecycle.Execute.State = "cancelled" //nolint:misspell // Existing protocol spelling.
		}
		core := append([]releasepolicy.Event{}, f.events[:5]...)
		core[4].Attempt = &record.Summary
		trial := *f.events[7].Trial
		trial.Terminal, trial.State, trial.FirstPass, trial.RetryPass = 1, "incomplete", nil, nil
		terminal := f.events[7]
		terminal.Sequence, terminal.Trial = 6, &trial
		core = append(core, terminal)
		retry, err := releasepolicy.InspectRetryPrefix(t.Context(), f.raw, retryJoinStream(t, core))
		require.NoError(t, err)
		events, rows := retryJoinNativeBytes(t, f.admissions[:1], []nativetask.Record{record}, false)
		joined, err := CheckRetryJoin(t.Context(), f.set, retry, events, rows, f.admissions[:1])
		require.NoError(t, err)
		view, err := joined.View(t.Context())
		require.NoError(t, err)
		require.Len(t, view.Attempts, 1)
		require.Equal(t, "operational", view.Attempts[0].Native.Summary.Category)
		require.Equal(t, record.Lifecycle.Execute.State, view.Attempts[0].Native.Lifecycle.Execute.State)
		require.Nil(t, view.Attempts[0].Native.Accounting.Usage)
		// The existing replay machine, not the static second slot, determines
		// whether a retry belongs to the supplied execution history.
		illegal := f.events[5]
		illegal.Sequence = 7
		core = append(core, illegal)
		invalid, err := releasepolicy.InspectRetryPrefix(t.Context(), f.raw, retryJoinStream(t, core))
		require.Error(t, err)
		require.Nil(t, invalid)
	}
}

func TestRetryJoinActualTapeWriterThroughRootFreeReader(t *testing.T) {
	f := newRetryJoinFixture(t)
	parent, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, parent.Close()) }()
	tape, err := nativetask.CreateTape(t.Context(), parent, "native")
	require.NoError(t, err)
	for i, admitted := range f.admissions {
		require.NoError(t, tape.Start(t.Context(), admitted))
		require.NoError(t, tape.Complete(t.Context(), retryJoinJSON(t, f.records[i])))
	}
	require.NoError(t, tape.Close())
	root, err := parent.OpenRoot("native")
	require.NoError(t, err)
	events, err := root.ReadFile("native-task-events.ndjson")
	require.NoError(t, err)
	rows, err := root.ReadFile("native-task-rows.ndjson")
	require.NoError(t, err)
	expectedEvents, expectedRows := retryJoinNativeBytes(t, f.admissions, f.records, false)
	require.Equal(t, expectedEvents, events)
	require.Equal(t, expectedRows, rows)
	require.NoError(t, root.Close())
	retry, err := releasepolicy.InspectRetryPrefix(t.Context(), f.raw, retryJoinStream(t, f.events))
	require.NoError(t, err)
	joined, err := CheckRetryJoin(t.Context(), f.set, retry, events, rows, f.admissions)
	require.NoError(t, err)
	view, err := joined.View(t.Context())
	require.NoError(t, err)
	require.Equal(t, "structurally_matched", view.State)
	require.Len(t, view.Attempts, len(f.records))
}

func TestRetryJoinManyDistinctStartedTasks(t *testing.T) {
	base, locations := fixture(t)
	const taskCount = 16
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		prefix := string(arm)
		write(t, base, prefix+"/context/a.txt", "identical fixture bytes")
		require.NoError(t, os.Remove(filepath.Join(base, prefix, "tasks/task.yaml")))
		eval := strings.Replace(snapshotEval, "  trials_per_task: 1", "  trials_per_task: 1\n  max_attempts: 2", 1)
		write(t, base, prefix+"/eval.yaml", eval)
		for i := range taskCount {
			id := fmt.Sprintf("task-%02d", i)
			write(t, base, prefix+"/tasks/"+id+".yaml", "id: "+id+"\ngolden: true\ninputs: {prompt: hello}\n")
		}
	}
	original := frozenOriginalCapture(t, locations)
	capture, err := prepareProjectionCapture(t.Context(), locations)
	require.NoError(t, err)
	require.Equal(t, original.Canonical, capture.prepared.canonical)
	raw := slotTestPolicy(t, original.Projection)
	set, err := PrepareSlotSet(t.Context(), raw, capture, slotTestIDs)
	require.NoError(t, err)
	f := retryJoinWithSlots(t, slotFixtureResult{base, locations, capture, original, raw, set})
	joined := joinFixturePrefix(t, f, len(f.events), len(f.records), false, false)
	allocations := testing.AllocsPerRun(1, func() {
		_, err := CheckRetryJoin(t.Context(), f.set, joined.retry, joined.events, joined.rows, joined.admissions)
		require.NoError(t, err)
	})
	// Allows over 20% headroom on the indexed production path, but rejects
	// the independently exercised former full-capture-per-task lookup.
	require.Less(t, allocations, 950_000.0, "production joins must index the retained capture once")
	view, err := joined.View(t.Context())
	require.NoError(t, err)
	require.Equal(t, "structurally_matched", view.State)
	require.Len(t, view.Attempts, 2*taskCount+1)
	started := map[releasepolicy.Arm]map[string]bool{
		releasepolicy.Baseline: {}, releasepolicy.Candidate: {},
	}
	for _, attempt := range view.Attempts {
		require.Equal(t, "terminal", attempt.NativeState)
		require.Equal(t, attempt.Slot.Key.TaskID, attempt.Native.Origin.TaskID)
		started[attempt.Slot.Key.Arm][attempt.Slot.Key.TaskID] = true
	}
	require.Len(t, started[releasepolicy.Baseline], taskCount)
	require.Len(t, started[releasepolicy.Candidate], taskCount)
}

func TestRetryJoinRequestIndexBoundsParsingAndPreservesNumbers(t *testing.T) {
	const taskCount = 64
	tasks := make([]taskSnapshot, taskCount)
	for i := range tasks {
		id := fmt.Sprintf("task-%02d", i)
		request := execution.ExecutionRequest{Message: id, Context: map[string]any{
			"exact": json.Number("9007199254740993")}, NoSkills: true, SkipWorkspaceCapture: true,
			ToolPolicy: &execution.ToolPolicy{Mode: execution.ToolPolicyDenyAll}}
		frozen, err := orchestration.FreezeCapturedNativeRequest(&request)
		require.NoError(t, err)
		tasks[i] = taskSnapshot{ID: id, Enabled: true, Request: frozen,
			Native: retryJoinJSON(t, map[string]any{"retained": strings.Repeat("bytes", 64)})}
	}
	snapshot := armSnapshot{Tasks: append(tasks, taskSnapshot{ID: "disabled", Enabled: false})}
	raw := retryJoinJSON(t, map[releasepolicy.Arm]armSnapshot{
		releasepolicy.Baseline: snapshot, releasepolicy.Candidate: snapshot})
	// A loose linear allocation ceiling excludes reparsing both full arm
	// snapshots per distinct task; it does not depend on elapsed time.
	allocations := testing.AllocsPerRun(1, func() {
		requests, err := retryJoinRequests(t.Context(), raw)
		require.NoError(t, err)
		for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
			require.Len(t, requests[arm], taskCount)
			for _, task := range tasks {
				require.Equal(t, task.ID, requests[arm][task.ID].Message)
				require.Equal(t, json.Number("9007199254740993"), requests[arm][task.ID].Context["exact"])
			}
		}
	})
	require.Less(t, allocations, float64(taskCount*256), "full captures must not be reparsed for every task")
	requests, err := retryJoinRequests(t.Context(), raw)
	require.NoError(t, err)
	raw[0] = 'x'
	require.Equal(t, "task-00", requests[releasepolicy.Baseline]["task-00"].Message)
	requests[releasepolicy.Baseline]["task-00"].Context["exact"] = "mutation"
	require.Equal(t, json.Number("9007199254740993"), requests[releasepolicy.Candidate]["task-00"].Context["exact"])
}

func TestRetryJoinRequestIndexRejectsInvalidInputs(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte("{"),
		retryJoinJSON(t, map[releasepolicy.Arm]armSnapshot{
			releasepolicy.Baseline: {Tasks: []taskSnapshot{{ID: "invalid", Enabled: true, Request: json.RawMessage(`"not a request"`)}}},
		}),
	} {
		requests, err := retryJoinRequests(t.Context(), raw)
		require.Error(t, err)
		require.Nil(t, requests)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		requests, err := retryJoinRequests(ctx, nil)
		require.Error(t, err)
		require.Nil(t, requests)
	}
}
