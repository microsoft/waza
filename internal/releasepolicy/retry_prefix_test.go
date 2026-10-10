package releasepolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func retryTestEvents(t *testing.T, p *Policy, category string) []Event {
	t.Helper()
	events := []Event{}
	add := func(e Event) {
		e.Sequence, e.CollectionID, e.PolicyDigest = len(events)+1, "actual-test-collection", p.Digest
		events = append(events, e)
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		add(Event{Type: "begin_arm", Arm: arm, EvalID: string(arm) + "-eval"})
	}
	for ci, c := range p.Design.Clusters {
		add(Event{Type: "cluster_start", ClusterID: c.ID})
		for _, arm := range p.Design.Allocation.Assignments[ci].Order {
			for _, task := range c.Tasks {
				for _, trial := range task.TrialOrdinals {
					attempts := []AttemptSummary{}
					maximum := 1
					if category == "failed" {
						maximum = settingsFor(p, arm, task.ID).MaxAttempts
					}
					for n := 1; n <= maximum; n++ {
						key := AttemptKey{SampleKey: SampleKey{ClusterID: c.ID, TaskID: task.ID, Trial: trial},
							Arm: arm, EvalID: string(arm) + "-eval", Attempt: n}
						observation, err := testCollector(p).Attempt(t.Context(), key)
						if err != nil {
							t.Fatal(err)
						}
						a := observation.Summary
						switch category {
						case "failed":
							a.Status, a.Checks[0].Passed, a.Checks[0].Score = "failed", false, "0.000"
						case "unknown", "operational":
							a.Category, a.Status, a.Checks = category, "incomplete", nil
							a.Runtime.Availability, a.Runtime.Reason = "unavailable", "Not observed"
							a.Runtime.EngineImplementation, a.Runtime.ModelVersion = "", ""
							a.References = nil
						}
						add(Event{Type: "attempt_start", Key: &key})
						add(Event{Type: "attempt_terminal", Attempt: &a})
						attempts = append(attempts, a)
					}
					trial := summarizeTrial(attempts[0].Key.SampleKey, attempts)
					add(Event{Type: "trial_terminal", Arm: arm, Trial: &trial})
				}
			}
		}
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		add(Event{Type: "end_arm", Arm: arm, Usage: unknownTestUsage()})
	}
	add(Event{Type: "collection_end"})
	return events
}

func retryTestStream(t *testing.T, events []Event) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(data)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func retryTestJournal(t *testing.T, p *Policy, events []Event, stream []byte) []byte {
	t.Helper()
	data, err := SealJSON(Journal{Kind: JournalKind, Version: Version, CollectionID: "actual-test-collection",
		Events: events, StreamDigest: SourceDigest(stream)})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func retryMustView(t *testing.T, r *RetryPrefix, err error) RetryPrefixView {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.View(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRetryPrefixEveryAcceptedBoundary(t *testing.T) {
	for _, category := range []string{"passed", "failed", "unknown", "operational"} {
		t.Run(category, func(t *testing.T) {
			p := testPolicy(t, 2)
			raw := testBytes(t, p)
			events := retryTestEvents(t, p, category)
			for n := 0; n <= len(events); n++ {
				t.Run(strconv.Itoa(n), func(t *testing.T) {
					r, err := InspectRetryPrefix(t.Context(), raw, retryTestStream(t, events[:n]))
					v := retryMustView(t, r, err)
					if len(v.Events) != n || !reflect.DeepEqual(v.Events, events[:n]) {
						t.Fatalf("accepted witness differs at boundary %d", n)
					}
					if v.CollectionEndValidated != (n == len(events)) {
						t.Fatal("end qualification mismatch")
					}
					if n == 0 && (v.CollectionID != "" || len(v.EvalIDs) != 0) {
						t.Fatal("invented identity")
					}
					if n == 1 && len(v.EvalIDs) != 1 {
						t.Fatal("first begin was not accepted")
					}
					if n >= 2 && len(v.EvalIDs) != 2 {
						t.Fatal("both begin identities missing")
					}
					// Trace records accepted starts and terminals even where the
					// original receipt's arm-local accumulator is not yet committed.
					if n >= 4 && n <= 5 {
						j := &Journal{Kind: JournalKind, Version: Version, CollectionID: events[0].CollectionID,
							Digest: p.Digest, Events: events[:n]}
						receipts, err := replay(p, j, true)
						if err != nil {
							t.Fatal(err)
						}
						arm := events[3].Key.Arm
						if len(receipts[arm].Attempts) != 0 || v.Events[3].Type != "attempt_start" {
							t.Fatal("receipt lag/start visibility")
						}
						if n == 5 && v.Events[4].Type != "attempt_terminal" {
							t.Fatal("accepted terminal missing before trial")
						}
					}
				})
			}
		})
	}
}

func TestRetryPrefixMissingTornInvalidAndSealed(t *testing.T) {
	p := testPolicy(t, 1)
	raw := testBytes(t, p)
	events := retryTestEvents(t, p, "passed")
	full := retryTestStream(t, events)
	journal := retryTestJournal(t, p, events, full)
	r, err := InspectRetryPrefix(t.Context(), raw, nil)
	missing := retryMustView(t, r, err)
	if missing.State != "missing" || missing.Stream != nil || missing.Journal != nil ||
		missing.Events == nil || missing.EvalIDs == nil {
		t.Fatal("missing inventory semantics")
	}
	r, err = InspectRetryPrefix(t.Context(), raw, []byte{})
	empty := retryMustView(t, r, err)
	if empty.State != "partial" || empty.Stream == nil || *empty.Stream != SourceDigest([]byte{}) {
		t.Fatal("empty not missing")
	}
	prefix := retryTestStream(t, events[:5])
	torn := append(bytes.Clone(prefix), []byte(`{"malformed unparsed tail`)...)
	r, err = InspectRetryPrefix(t.Context(), raw, torn)
	v := retryMustView(t, r, err)
	if v.TrailingFragmentBytes != len(torn)-len(prefix) || len(v.Events) != 5 ||
		*v.Stream != SourceDigest(torn) || v.CollectionEndValidated {
		t.Fatal("torn ownership/acceptance")
	}
	r, err = InspectRetryJournal(t.Context(), raw, journal, full)
	v = retryMustView(t, r, err)
	if v.Mode != "sealed_journal" || v.State != "structurally_complete" ||
		*v.Journal != SourceDigest(journal) || !reflect.DeepEqual(v.Events, events) {
		t.Fatal("sealed witness binding")
	}
	for name, stream := range map[string][]byte{
		"blank": []byte("\n"), "bad complete": append(bytes.Clone(prefix), []byte("{\n")...),
		"extra value": []byte("{}{}\n"), "end torn": append(bytes.Clone(full), 'x'),
		"end whitespace": append(bytes.Clone(full), ' '), "end newline": append(bytes.Clone(full), '\n'),
		"end complete": append(bytes.Clone(full), full[:bytes.IndexByte(full, '\n')+1]...),
	} {
		t.Run(name, func(t *testing.T) {
			if result, err := InspectRetryPrefix(t.Context(), raw, stream); err == nil || result != nil {
				t.Fatal("invalid prefix yielded partial success")
			}
		})
	}
	for name, stream := range map[string][]byte{
		"missing": nil, "empty": {}, "torn": full[:len(full)-1], "different bytes": append([]byte(" "), full...),
		"count": retryTestStream(t, events[:len(events)-1]),
		"order": retryTestStream(t, append(append([]Event{}, events[1], events[0]), events[2:]...)),
	} {
		t.Run("sealed "+name, func(t *testing.T) {
			if r, err := InspectRetryJournal(t.Context(), raw, journal, stream); err == nil || r != nil {
				t.Fatal("sealed source mismatch accepted")
			}
			if len(stream) > 0 && stream[len(stream)-1] == '\n' {
				// Independently bind these supplied bytes, retaining the original
				// complete journal events. Count/order still must match.
				boundBytes := retryTestJournal(t, p, events, stream)
				if _, err := DecodeJournal(boundBytes, p); err != nil {
					t.Fatal(err)
				}
				_, err := InspectRetryJournal(t.Context(), raw, boundBytes, stream)
				if name == "different bytes" {
					if err != nil {
						t.Fatalf("exactly rebound whitespace source should be accepted: %v", err)
					}
				} else if err == nil {
					t.Fatal("event count/order mismatch hidden by valid source digest")
				}
			}
		})
	}
	reformattedScore := bytes.Replace(full, []byte(`"score":1`), []byte(`"score":1.0`), 1)
	boundBytes := retryTestJournal(t, p, events, reformattedScore)
	if _, err := InspectRetryJournal(t.Context(), raw, boundBytes, reformattedScore); err == nil {
		t.Fatal("ordered event correspondence normalized score tokens")
	}
	badJournal := bytes.Replace(journal, []byte(events[0].CollectionID), []byte("different-collection"), 1)
	if _, err := InspectRetryJournal(t.Context(), raw, badJournal, full); err == nil {
		t.Fatal("invalid sealed identity")
	}
	if _, err := InspectRetryJournal(t.Context(), raw, nil, full); err == nil {
		t.Fatal("missing journal")
	}
}

func TestRetryRawConditionalStrictnessAndNestedNull(t *testing.T) {
	p := testPolicy(t, 1)
	raw := testBytes(t, p)
	for _, category := range []string{"passed", "unknown"} {
		events := retryTestEvents(t, p, category)
		for index, e := range events {
			for _, extra := range []string{`"runtime":null`, `"runtime":[]`, `"cluster_id":""`} {
				if e.Type == "cluster_start" && strings.HasPrefix(extra, `"cluster_id"`) {
					continue
				}
				encoded, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				bad := append(bytes.Clone(encoded[:len(encoded)-1]), []byte(","+extra+"}")...)
				object, err := objectJSON(bad)
				if err != nil {
					t.Fatal(err)
				}
				var legacy Event
				if err := json.Unmarshal(bad, &legacy); err != nil {
					t.Fatal(err)
				}
				// Existing decoder's runtime:[] is nonnil and replay rejects it;
				// null/zero extras otherwise demonstrate published acceptance.
				legacyEvents := append([]Event{}, events...)
				legacyEvents[index] = legacy
				j := &Journal{Kind: JournalKind, Version: Version, CollectionID: e.CollectionID, Digest: p.Digest, Events: legacyEvents}
				legacyErr := requireFields(object, reflect.TypeFor[Event](), "")
				_, replayErr := Replay(p, j)
				if extra != `"runtime":[]` && (extra != `"runtime":null` || legacyErr == nil) {
					if legacyErr != nil || replayErr != nil {
						t.Fatalf("legacy zero/null acceptance changed: %s %s: %v %v", e.Type, extra, legacyErr, replayErr)
					}
					rawJournal, err := objectJSON(retryTestJournal(t, p, events, retryTestStream(t, events)))
					if err != nil {
						t.Fatal(err)
					}
					rawEvents, ok := rawJournal["events"].([]any)
					if !ok {
						t.Fatal("fixture journal events must be an array")
					}
					rawEvents[index] = object
					sealed, err := SealJSON(rawJournal)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := DecodeJournal(sealed, p); err != nil {
						t.Fatalf("legacy decoder raw zero/null positive: %s %s: %v", e.Type, extra, err)
					}
					if _, err := InspectRetryJournal(t.Context(), raw, sealed, retryTestStream(t, events)); err == nil {
						t.Fatal("selected sealed raw extra accepted")
					}
				}
				stream := retryTestStream(t, events[:index])
				stream = append(stream, append(bad, '\n')...)
				if r, err := InspectRetryPrefix(t.Context(), raw, stream); err == nil || r != nil {
					t.Fatalf("raw conditional extra accepted: %s %s", e.Type, extra)
				}
			}
			encoded, _ := json.Marshal(e)
			obj, err := objectJSON(encoded)
			if err != nil {
				t.Fatal(err)
			}
			for name := range obj {
				for _, remove := range []bool{false, true} {
					badObject, err := objectJSON(encoded)
					if err != nil {
						t.Fatal(err)
					}
					if remove {
						delete(badObject, name)
					} else {
						badObject[name] = nil
					}
					bad, err := json.Marshal(badObject)
					if err != nil {
						t.Fatal(err)
					}
					stream := append(retryTestStream(t, events[:index]), append(bad, '\n')...)
					if _, err := InspectRetryPrefix(t.Context(), raw, stream); err == nil {
						t.Fatalf("selected raw required member accepted: %s %s removed=%v", e.Type, name, remove)
					}
				}
			}
			// Every transition's duplicate/unknown/null discriminator rejects.
			for _, suffix := range []string{`,"type":"` + e.Type + `"}`, `,"unknown":0}`} {
				stream := retryTestStream(t, events[:index])
				stream = append(stream, encoded[:len(encoded)-1]...)
				stream = append(stream, []byte(suffix+"\n")...)
				if _, err := InspectRetryPrefix(t.Context(), raw, stream); err == nil {
					t.Fatal("duplicate/unknown accepted")
				}
			}
			null := bytes.Replace(encoded, []byte(`"type":"`+e.Type+`"`), []byte(`"type":null`), 1)
			if _, err := InspectRetryPrefix(t.Context(), raw, append(retryTestStream(t, events[:index]), append(null, '\n')...)); err == nil {
				t.Fatal("null discriminator accepted")
			}
		}
		if category == "unknown" {
			r, err := InspectRetryPrefix(t.Context(), raw, retryTestStream(t, events))
			v := retryMustView(t, r, err)
			for _, e := range v.Events {
				if e.Trial != nil && (e.Trial.FirstPass != nil || e.Trial.RetryPass != nil) {
					t.Fatal("nested nullable trial endpoints strengthened")
				}
			}
		}
	}
}

func retryInvalidCases(t *testing.T, p *Policy, good []Event) map[string][]Event {
	t.Helper()
	cases := map[string][]Event{}
	trialIndex := 0
	for i, e := range good {
		if e.Trial != nil {
			trialIndex = i
			break
		}
	}
	edit := func(name string, mutate func([]Event)) {
		data, err := json.Marshal(good)
		if err != nil {
			t.Fatal(err)
		}
		var events []Event
		if err := json.Unmarshal(data, &events); err != nil {
			t.Fatal(err)
		}
		mutate(events)
		cases[name] = events
	}
	edit("begin arm", func(e []Event) { e[0].Arm = Candidate })
	edit("same eval", func(e []Event) { e[1].EvalID = e[0].EvalID })
	edit("cluster", func(e []Event) { e[2].ClusterID = "wrong" })
	edit("start arm", func(e []Event) { e[3].Key.Arm = Arm("wrong") })
	edit("start eval", func(e []Event) { e[3].Key.EvalID = "wrong" })
	edit("start task", func(e []Event) { e[3].Key.TaskID = "wrong" })
	edit("start trial", func(e []Event) { e[3].Key.Trial++ })
	edit("start retry", func(e []Event) { e[3].Key.Attempt++ })
	edit("terminal key", func(e []Event) { e[4].Attempt.Key.Attempt++ })
	edit("origin", func(e []Event) { e[4].Attempt.Origin.RunNumber++ })
	edit("runtime", func(e []Event) { e[4].Attempt.Runtime.RequestedModel = "wrong" })
	edit("runtime unavailable", func(e []Event) {
		e[4].Attempt.Runtime.Availability = "unavailable"
		e[4].Attempt.Runtime.EngineImplementation = "invented"
	})
	if len(good[4].Attempt.Checks) > 0 {
		edit("score", func(e []Event) { e[4].Attempt.Checks[0].Score = "1.001" })
		edit("grader", func(e []Event) { e[4].Attempt.Checks[0].Grader = "" })
	}
	edit("operational", func(e []Event) { e[4].Attempt.Category = "invalid" })
	edit("trial", func(e []Event) { e[trialIndex].Trial.Terminal++ })
	edit("trial contradiction", func(e []Event) { e[trialIndex].Trial.FirstPass = new(true); e[trialIndex].Trial.State = "invalid" })
	edit("usage", func(e []Event) { e[len(e)-3].Usage[0].Availability = "wrong" })
	edit("usage duplicate", func(e []Event) { e[len(e)-3].Usage[1] = e[len(e)-3].Usage[0] })
	edit("usage absent", func(e []Event) { e[len(e)-3].Usage = e[len(e)-3].Usage[:1] })
	edit("last envelope", func(e []Event) { e[2].ClusterID = "wrong"; e[len(e)-1].Sequence++ })
	edit("policy envelope", func(e []Event) { e[len(e)-1].PolicyDigest = models.EvidenceDigest{} })
	edit("collection envelope", func(e []Event) { e[len(e)-1].CollectionID = "wrong" })
	edit("extra conditional", func(e []Event) { e[2].EvalID = "wrong" })
	extra := append([]Event{}, good[:5]...)
	extra = append(extra, good[3])
	extra = append(extra, good[5:]...)
	for i := range extra {
		extra[i].Sequence = i + 1
	}
	cases["extra retry after stop"] = extra
	extra = append(append([]Event{}, good...), good[len(good)-1])
	extra[len(extra)-1].Sequence++
	cases["extra after end"] = extra
	return cases
}

func TestRetryInvalidTransitionsAndEnvelopePrecedence(t *testing.T) {
	p := testPolicy(t, 1)
	for _, category := range []string{"passed", "failed", "unknown", "operational"} {
		events := retryTestEvents(t, p, category)
		for name, invalid := range retryInvalidCases(t, p, events) {
			// Unknown/operational attempts have no checks to mutate; those
			// cases are constructed below from a behavioral tape instead.
			t.Run(category+"/"+name, func(t *testing.T) {
				r, err := InspectRetryPrefix(t.Context(), testBytes(t, p), retryTestStream(t, invalid))
				if err == nil || r != nil {
					t.Fatal("semantic error returned partial witness")
				}
				if name == "last envelope" && !strings.Contains(err.Error(), "sequence/collection/policy mismatch") {
					t.Fatal("later envelope did not win")
				}
			})
		}
	}
}

func TestRetryOwnedSourcesAndIndependentSeal(t *testing.T) {
	p := testPolicy(t, 1)
	raw := testBytes(t, p)
	events := retryTestEvents(t, p, "passed")
	events[4].Attempt.References = []models.EvidenceReference{{Origin: events[4].Attempt.Origin, ArtifactID: "actual-artifact", Pointer: "/exact"}}
	events[len(events)-3].Usage[0] = UsageAxis{Axis: "input_tokens", Availability: "available",
		Value: new(json.Number("12")), Observation: "final_complete_attributable"}
	full := retryTestStream(t, events)
	journal := retryTestJournal(t, p, events, full)
	r, err := InspectRetryJournal(t.Context(), raw, journal, full)
	v := retryMustView(t, r, err)
	raw[0], full[0], journal[0] = 'x', 'x', 'x'
	v.Stream.SHA256, v.Journal.SHA256 = "mutated", "mutated"
	v.EvalIDs[Baseline] = "mutated"
	v.Events[3].Key.TaskID = "mutated"
	v.Events[4].Attempt.Checks[0].Score = "0"
	v.Events[4].Attempt.References[0].Origin.TaskID = "mutated"
	*v.Events[5].Trial.FirstPass = false
	*v.Events[len(events)-3].Usage[0].Value = "0"
	again, err := r.View(t.Context())
	if err != nil || !reflect.DeepEqual(again.Events, events) {
		t.Fatalf("nested ownership failure: %v", err)
	}
	for _, name := range []string{"view", "resealed view", "policy", "stream", "journal", "mode", "seal"} {
		t.Run(name, func(t *testing.T) {
			r, err := InspectRetryJournal(t.Context(), testBytes(t, p), retryTestJournal(t, p, events, retryTestStream(t, events)), retryTestStream(t, events))
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "view", "resealed view":
				r.view.EvalIDs[Baseline] = "forged"
				if name == "resealed view" {
					r.seal = testDigest(t, retryPrefixSeal{Domain: retryPrefixDomain, RawPolicy: SourceDigest(r.rawPolicy), View: r.view})
				}
			case "policy":
				r.rawPolicy = append([]byte(" "), r.rawPolicy...)
			case "stream":
				r.rawStream = append([]byte(" "), r.rawStream...)
			case "journal":
				r.rawJournal = append([]byte(" "), r.rawJournal...)
			case "mode":
				r.mode = "stream_prefix"
			case "seal":
				r.seal.SHA256 = strings.Repeat("0", 64)
			}
			if _, err := r.View(t.Context()); err == nil {
				t.Fatal("tampered retained evidence accepted")
			}
		})
	}
}

func TestRetryOriginalNumericSpelling(t *testing.T) {
	p := testPolicy(t, 1)
	p.Design.Alpha = "0.0500"
	p = sealTestPolicy(t, p)
	raw := testBytes(t, p)
	decoded, err := DecodePolicy(raw)
	if err != nil || decoded.Design.Alpha != "0.0500" || decoded.Design.Clusters[0].Weight != "1" {
		t.Fatalf("original policy numeric tokens changed: %v", err)
	}
	events := retryTestEvents(t, p, "failed")
	stream := retryTestStream(t, events)
	r, err := InspectRetryPrefix(t.Context(), raw, stream)
	v := retryMustView(t, r, err)
	if v.Policy != p.Digest || v.Events[4].Attempt.Checks[0].Score != "0.000" {
		t.Fatal("inspection normalized retained numeric spelling")
	}
	modified := bytes.Replace(raw, []byte(`"alpha":0.0500`), []byte(`"alpha":0.05`), 1)
	if bytes.Equal(modified, raw) || SourceDigest(modified) == SourceDigest(raw) {
		t.Fatal("test must change actual source bytes")
	}
	if _, err := InspectRetryPrefix(t.Context(), modified, stream); err == nil {
		t.Fatal("original JSON-v1 numeric identity was not enforced")
	}
}

func TestRetryActualInputBoundaries(t *testing.T) {
	ctx := t.Context()
	policy := []byte("{}")
	for _, n := range []int{retryRawLimit, retryRawLimit + 1} {
		raw := append([]byte("{}"), bytes.Repeat([]byte(" "), n-2)...)
		err := preflightRetryInputs(ctx, raw, nil, nil, "stream_prefix")
		if (err == nil) != (n == retryRawLimit) {
			t.Fatalf("raw boundary %d: %v", n, err)
		}
		journal := append([]byte(`{"events":[]}`), bytes.Repeat([]byte(" "), n-len(`{"events":[]}`))...)
		err = preflightRetryInputs(ctx, policy, journal, []byte{}, "sealed_journal")
		if (err == nil) != (n == retryRawLimit) {
			t.Fatalf("independent journal raw boundary %d: %v", n, err)
		}
		line := append([]byte("{}"), bytes.Repeat([]byte(" "), retryLineLimit-3)...)
		line = append(line, '\n')
		stream := bytes.Repeat(line, 16)
		if n > retryRawLimit {
			stream = append(stream, 'x')
		}
		err = preflightRetryInputs(ctx, policy, nil, stream, "stream_prefix")
		if (err == nil) != (n == retryRawLimit) {
			t.Fatalf("independent stream raw boundary %d: %v", n, err)
		}
	}
	for _, cr := range []bool{false, true} {
		for _, n := range []int{retryLineLimit, retryLineLimit + 1} {
			suffix := "\n"
			if cr {
				suffix = "\r\n"
			}
			line := append([]byte("{}"), bytes.Repeat([]byte(" "), n-2-len(suffix))...)
			line = append(line, suffix...)
			err := preflightRetryInputs(ctx, policy, nil, line, "stream_prefix")
			if (err == nil) != (n == retryLineLimit) {
				t.Fatalf("LF/CR boundary %d %v: %v", n, cr, err)
			}
		}
	}
	for _, n := range []int{retryLineLimit, retryLineLimit + 1} {
		err := preflightRetryInputs(ctx, policy, nil, bytes.Repeat([]byte("x"), n), "stream_prefix")
		if (err == nil) != (n == retryLineLimit) {
			t.Fatalf("torn boundary %d: %v", n, err)
		}
	}
	for _, n := range []int{retryEventLimit, retryEventLimit + 1} {
		stream := bytes.Repeat([]byte("{}\n"), n)
		err := preflightRetryInputs(ctx, policy, nil, stream, "stream_prefix")
		if (err == nil) != (n == retryEventLimit) {
			t.Fatalf("stream event boundary %d: %v", n, err)
		}
		journal := []byte(`{"events":[` + strings.TrimSuffix(strings.Repeat("{},", n), ",") + `]}`)
		scan := newRetryScan(ctx)
		err = scan.document(journal, true)
		if (err == nil) != (n == retryEventLimit) || (err == nil && scan.events != n) {
			t.Fatalf("independent journal event boundary %d: %v", n, err)
		}
	}
	for _, n := range []int{retryTokenLimit, retryTokenLimit + 1} {
		// Open + n-2 scalars + close are exactly n actual tokens.
		raw := []byte("[" + strings.TrimSuffix(strings.Repeat("0,", n-2), ",") + "]")
		scan := newRetryScan(ctx)
		err := scan.document(raw, false)
		if (err == nil) != (n == retryTokenLimit) || (err == nil && scan.tokens != n) {
			t.Fatalf("actual token boundary %d: %v", n, err)
		}
		// Aggregate stream tokens are independent of the line/event limits.
		line := "[" + strings.TrimSuffix(strings.Repeat("0,", 16), ",") + "]\n"
		stream := strings.Repeat(line, n/18)
		remainder := n % 18
		if remainder > 0 {
			stream += "[" + strings.TrimSuffix(strings.Repeat("0,", remainder-2), ",") + "]\n"
		}
		err = preflightRetryInputs(ctx, policy, nil, []byte(stream), "stream_prefix")
		if (err == nil) != (n == retryTokenLimit) {
			t.Fatalf("aggregate stream token boundary %d: %v", n, err)
		}
	}
	// Object keys, nested opens/closes and scalars all count; this is not
	// inferred from byte length or scalar-only token totals.
	var object bytes.Buffer
	object.WriteByte('{')
	for i := 0; i < (retryTokenLimit-2)/2; i++ {
		if i != 0 {
			object.WriteByte(',')
		}
		object.WriteString(`"k` + strconv.Itoa(i) + `":0`)
	}
	object.WriteByte('}')
	scan := newRetryScan(ctx)
	if err := scan.document(object.Bytes(), false); err != nil || scan.tokens != retryTokenLimit {
		t.Fatalf("actual key/scalar token boundary: %d %v", scan.tokens, err)
	}
	more := bytes.Replace(object.Bytes(), []byte(`"k0":0`), []byte(`"k0":[]`), 1)
	if err := newRetryScan(ctx).document(more, false); err == nil {
		t.Fatal("actual nested container token +1 accepted")
	}
	for _, middle := range []string{"", "0"} {
		raw := []byte(strings.Repeat("[", 65) + middle + strings.Repeat("]", 65))
		err := newRetryScan(ctx).document(raw, false)
		if (err == nil) != (middle == "") {
			t.Fatalf("root0/depth64 boundary %q: %v", middle, err)
		}
	}
}

func TestRetryActualExpandedRepresentationBoundaries(t *testing.T) {
	ctx := t.Context()
	// Tune actual typed objects, not a raw-size or multiplication proxy. A
	// retained score token supplies the 0..5 remainder of the string charge.
	makeValue := func(kind string, text string, score json.Number) any {
		a := AttemptSummary{Checks: []CheckSummary{{Grader: text, Score: score}}, References: []models.EvidenceReference{}}
		binding := ResultBinding{Attempts: []AttemptSummary{a}}
		switch kind {
		case "attempt":
			return a
		case "binding":
			return binding
		case "receipt":
			return Receipt{Binding: &binding}
		case "trace":
			return []Event{{Type: "attempt_terminal", Attempt: &a}}
		case "view":
			return RetryPrefixView{Events: []Event{{Type: "attempt_terminal", Attempt: &a}}}
		case "seal":
			return retryPrefixSeal{View: RetryPrefixView{Events: []Event{{Type: "attempt_terminal", Attempt: &a}}}}
		default:
			t.Fatal(kind)
			return nil
		}
	}
	for _, kind := range []string{"attempt", "binding", "receipt", "trace", "view", "seal"} {
		t.Run(kind, func(t *testing.T) {
			base := newRetryBudget(ctx)
			if err := base.value(makeValue(kind, "", "1")); err != nil {
				t.Fatal(err)
			}
			delta := retryRepresentationLimit - base.used
			n, remainder := delta/6, delta%6
			scoreToken := func(n int) json.Number {
				if n == 1 {
					return "0"
				}
				if n == 2 {
					return "-0"
				}
				return json.Number("0." + strings.Repeat("0", n-2))
			}
			score := scoreToken(remainder + 1)
			value := makeValue(kind, strings.Repeat("<", n), score)
			b := newRetryBudget(ctx)
			if err := b.value(value); err != nil || b.used != retryRepresentationLimit {
				t.Fatalf("actual inclusive typed charge: %d %v", b.used, err)
			}
			score = scoreToken(remainder + 2)
			if err := newRetryBudget(ctx).value(makeValue(kind, strings.Repeat("<", n), score)); err == nil {
				t.Fatal("actual typed charge +1 accepted")
			}
		})
	}
	// Generic decoded-string charges independently hit the same exact boundary.
	base := newRetryScan(ctx)
	if err := base.document([]byte(`{"s":"","n":11111111111111111111}`), false); err != nil {
		t.Fatal(err)
	}
	delta := retryRepresentationLimit - base.generic.used
	text := strings.Repeat("<", delta/6)
	number := strings.Repeat("1", delta%6+20)
	raw := []byte(`{"s":"` + text + `","n":` + number + `}`)
	scan := newRetryScan(ctx)
	if err := scan.document(raw, false); err != nil || scan.generic.used != retryRepresentationLimit {
		t.Fatalf("generic exact cap: %d %v", scan.generic.used, err)
	}
	raw = []byte(`{"s":"` + text + `","n":` + number + `1}`)
	if err := newRetryScan(ctx).document(raw, false); err == nil {
		t.Fatal("generic charge +1 accepted")
	}
}

func TestRetryExactChargeTable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		value  any
		charge int
	}{
		{"escaped UTF8 bytes", "<>\n\u2028", 38},
		{"number spelling", json.Number("0.000"), 5},
		{"integer", 1, 20},
		{"true", true, 5},
		{"false", false, 5},
		{"null", nil, 4},
		{"null bool", (*bool)(nil), 4},
		{"null number", (*json.Number)(nil), 4},
		{"null digest", (*models.EvidenceDigest)(nil), 4},
		{"empty array", []SampleKey{}, 2},
		{"nil array", []SampleKey(nil), 4},
		{"named digest keys", models.EvidenceDigest{}, 98},
		{"sample fields", SampleKey{ClusterID: "a", TaskID: "b", Trial: 1}, 230},
		{"array punctuation", []SampleKey{{ClusterID: "a", TaskID: "b", Trial: 1}}, 233},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newRetryBudget(t.Context())
			if err := b.value(tc.value); err != nil || b.used != tc.charge {
				t.Fatalf("exact table: got %d want %d: %v", b.used, tc.charge, err)
			}
		})
	}
	if err := newRetryBudget(t.Context()).value(struct{}{}); err == nil {
		t.Fatal("open-ended representation walker")
	}
	original := Event{Runtime: []RuntimeObservation{{Reason: "owned"}}}
	cloned, err := cloneRetryEvent(t.Context(), original)
	if err != nil {
		t.Fatal(err)
	}
	cloned.Runtime[0].Reason = "mutated"
	if original.Runtime[0].Reason != "owned" {
		t.Fatal("runtime slice was not detached")
	}
}

func retryPlanTrials(t *testing.T, trials int, id string) *Policy {
	t.Helper()
	p := testPolicy(t, 1)
	p.Design.Clusters[0].Tasks[0].ID = id
	p.Design.Clusters[0].Tasks[0].TrialOrdinals = make([]int, trials)
	for i := range trials {
		p.Design.Clusters[0].Tasks[0].TrialOrdinals[i] = i + 1
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		a := p.Arms[arm]
		a.Plan.Tasks = append([]TaskPlan{}, a.Plan.Tasks...)
		a.Plan.Identities = append([]Identity{}, a.Plan.Identities...)
		a.Plan.Tasks[0].ID, a.Plan.Tasks[0].Settings.TrialsPerTask = id, trials
		for i := range a.Plan.Identities {
			if a.Plan.Identities[i].TaskID != "" {
				a.Plan.Identities[i].TaskID = id
			}
		}
		p.Arms[arm] = a
	}
	return sealTestPolicy(t, p)
}

func TestRetryPlannedSamplesBeforeReplay(t *testing.T) {
	for _, n := range []int{retryEventLimit, retryEventLimit + 1} {
		p := retryPlanTrials(t, n, "task-0")
		raw := testBytes(t, p)
		if _, err := DecodePolicy(raw); err != nil {
			t.Fatal(err)
		}
		r, err := InspectRetryPrefix(t.Context(), raw, []byte{})
		if n == retryEventLimit {
			if err != nil || r == nil {
				t.Fatalf("inclusive planned admission: %v", err)
			}
		} else if err == nil || r != nil || !strings.Contains(err.Error(), "planned samples") {
			t.Fatalf("tiny stream huge plan was not bounded: %v", err)
		}
	}
	p := retryPlanTrials(t, 1000, strings.Repeat("long", 5000))
	raw := testBytes(t, p)
	if len(raw) >= retryRawLimit {
		t.Fatal("fixture must fit raw byte bound")
	}
	if _, err := DecodePolicy(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectRetryPrefix(t.Context(), raw, []byte{}); err == nil || !strings.Contains(err.Error(), "representation") {
		t.Fatalf("repeated planned ID expansion unbounded: %v", err)
	}
	// Sealed inspection must reject inventory expansion before its first replay,
	// even though this tiny structurally invalid tape would fail that replay.
	begin := retryTestEvents(t, testPolicy(t, 1), "passed")[:2]
	for i := range begin {
		begin[i].PolicyDigest = p.Digest
	}
	stream := retryTestStream(t, begin)
	journal := retryTestJournal(t, p, begin, stream)
	if _, err := InspectRetryJournal(t.Context(), raw, journal, stream); err == nil || !strings.Contains(err.Error(), "representation") {
		t.Fatalf("DecodeJournal ran before expansion admission: %v", err)
	}
	// Huge retry maxima do not allocate possible slots or multiply budgets.
	p = testPolicy(t, 1)
	for _, arm := range []Arm{Baseline, Candidate} {
		a := p.Arms[arm]
		a.Plan.Tasks = append([]TaskPlan{}, a.Plan.Tasks...)
		a.Plan.Tasks[0].Settings.MaxAttempts = int(^uint(0) >> 1)
		p.Arms[arm] = a
	}
	p = sealTestPolicy(t, p)
	events := retryTestEvents(t, p, "passed")
	r, err := InspectRetryPrefix(t.Context(), testBytes(t, p), retryTestStream(t, events))
	if v := retryMustView(t, r, err); !v.CollectionEndValidated {
		t.Fatal("static retry maximum was confused with observed allocation")
	}
}

func TestRetryInventoryExpansionBeforeFirstReplay(t *testing.T) {
	p := retryPlanTrials(t, 35, "task-0")
	events := retryTestEvents(t, p, "passed")
	for i := range events {
		if events[i].Attempt != nil {
			events[i].Attempt.Checks[0].Grader = strings.Repeat("g", 100000)
		}
	}
	raw := testBytes(t, p)
	stream := retryTestStream(t, events)
	journal := retryTestJournal(t, p, events, stream)
	if len(stream) > retryRawLimit || len(journal) > retryRawLimit {
		t.Fatal("inventory fixture must fit independent raw limits")
	}
	// Each raw/trace representation fits; the separately duplicated per-arm
	// receipt/binding inventory must nevertheless reject before either replay.
	if err := preflightRetryInputs(t.Context(), raw, journal, stream, "sealed_journal"); err != nil {
		t.Fatal(err)
	}
	if err := newRetryBudget(t.Context()).value(events); err != nil {
		t.Fatal(err)
	}
	if r, err := InspectRetryPrefix(t.Context(), raw, stream); err == nil || r != nil ||
		!strings.Contains(err.Error(), "representation") {
		t.Fatalf("per-arm observed inventory was not bounded: %v", err)
	}
	if r, err := InspectRetryJournal(t.Context(), raw, journal, stream); err == nil || r != nil ||
		!strings.Contains(err.Error(), "representation") {
		t.Fatalf("first DecodeJournal replay escaped inventory bounds: %v", err)
	}
}

func TestRetryContextBoundaries(t *testing.T) {
	p := testPolicy(t, 1)
	raw := testBytes(t, p)
	stream := retryTestStream(t, retryTestEvents(t, p, "passed"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if _, err := InspectRetryPrefix(ctx, raw, stream); err == nil {
			t.Fatal("unusable context accepted")
		}
		if _, err := copyRetryBytes(ctx, raw); err == nil {
			t.Fatal("copy ignored context")
		}
		if _, err := cloneRetryEvent(ctx, Event{}); err == nil {
			t.Fatal("clone ignored context")
		}
		if err := newRetryBudget(ctx).value(Event{}); err == nil {
			t.Fatal("budget ignored context")
		}
		if err := (&replayTrace{ctx: ctx}).accept(Event{}); err == nil {
			t.Fatal("trace ignored context")
		}
	}
	if _, err := (*RetryPrefix)(nil).View(t.Context()); err == nil {
		t.Fatal("nil prefix")
	}
	r, err := InspectRetryPrefix(t.Context(), raw, stream)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.View(ctx); err == nil {
		t.Fatal("view ignored context")
	}
	for _, remaining := range []int{1, 10, 100, 1000, 3000} {
		ctx := &retryCountingContext{Context: t.Context(), remaining: remaining}
		if result, err := InspectRetryPrefix(ctx, raw, stream); err == nil || result != nil {
			t.Fatalf("cancellation during new walks returned witness at check %d", remaining)
		}
	}
	for _, n := range []int{4, 5} {
		events := retryTestEvents(t, p, "passed")[:n]
		for _, remaining := range []int{1, 8, 20, 60} {
			ctx := &retryCountingContext{Context: t.Context(), remaining: remaining}
			trace := &replayTrace{ctx: ctx, events: []Event{}}
			if _, err := replayEvents(p, events[0].CollectionID, events, nil, true, trace); err == nil {
				t.Fatalf("prefix EOF hid context cancellation at check %d", remaining)
			}
		}
	}
}

type retryCountingContext struct {
	context.Context
	remaining int
}

func (c *retryCountingContext) Err() error {
	if c.remaining == 0 {
		return context.Canceled
	}
	c.remaining--
	return c.Context.Err()
}

type retryOracleInput struct {
	Policy              *Policy
	Journal             *Journal
	Raw                 []byte
	PresentEmptyRuntime []int
}

type retryOracleResult struct {
	Full         map[Arm]Receipt
	FullError    string
	Prefix       map[Arm]Receipt
	PrefixError  string
	Decoded      *Journal
	DecodeError  string
	Decision     Decision
	AccountError string
}

func retryOracleCurrent(input retryOracleInput) retryOracleResult {
	result := retryOracleResult{}
	errString := func(err error) string {
		if err != nil {
			return err.Error()
		}
		return ""
	}
	var err error
	result.Full, err = Replay(input.Policy, input.Journal)
	result.FullError = errString(err)
	result.Prefix, err = replay(input.Policy, input.Journal, true)
	result.PrefixError = errString(err)
	result.Decoded, err = DecodeJournal(input.Raw, input.Policy)
	result.DecodeError = errString(err)
	result.Decision.Accounting = map[Arm]Reliability{}
	err = accountPrefix(input.Policy, input.Journal, &result.Decision)
	result.AccountError = errString(err)
	return result
}

func TestRetryPinnedOriginalJournalOracle(t *testing.T) {
	p := testPolicy(t, 1)
	inputs := []retryOracleInput{}
	add := func(policy *Policy, journal *Journal) {
		raw, err := SealJSON(journal)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, retryOracleInput{Policy: policy, Journal: journal, Raw: raw})
	}
	for _, category := range []string{"passed", "failed", "unknown", "operational"} {
		good := retryTestEvents(t, p, category)
		for n := 0; n <= len(good); n++ {
			add(p, &Journal{Kind: JournalKind, Version: Version, CollectionID: good[0].CollectionID,
				Digest: p.Digest, StreamDigest: SourceDigest(retryTestStream(t, good[:n])), Events: good[:n]})
		}
		for _, events := range retryInvalidCases(t, p, good) {
			add(p, &Journal{Kind: JournalKind, Version: Version, CollectionID: good[0].CollectionID,
				Digest: p.Digest, StreamDigest: SourceDigest(retryTestStream(t, events)), Events: events})
		}
		for index, e := range good {
			for _, extra := range []string{`"runtime":null`, `"runtime":[]`, `"cluster_id":""`} {
				if e.Type == "cluster_start" && strings.HasPrefix(extra, `"cluster_id"`) {
					continue
				}
				encoded, err := json.Marshal(e)
				if err != nil {
					t.Fatal(err)
				}
				event, err := objectJSON(append(encoded[:len(encoded)-1], []byte(","+extra+"}")...))
				if err != nil {
					t.Fatal(err)
				}
				rawJournal, err := objectJSON(retryTestJournal(t, p, good, retryTestStream(t, good)))
				if err != nil {
					t.Fatal(err)
				}
				rawEvents, ok := rawJournal["events"].([]any)
				if !ok {
					t.Fatal("fixture journal events must be an array")
				}
				rawEvents[index] = event
				raw, err := SealJSON(rawJournal)
				if err != nil {
					t.Fatal(err)
				}
				var j Journal
				if err := json.Unmarshal(raw, &j); err != nil {
					t.Fatal(err)
				}
				inputs = append(inputs, retryOracleInput{Policy: p, Journal: &j, Raw: raw})
			}
		}
	}
	good := retryTestEvents(t, p, "passed")
	for _, mutate := range []func(*Journal){
		func(j *Journal) { j.Kind = "wrong" }, func(j *Journal) { j.Version = "wrong" },
		func(j *Journal) { j.CollectionID = "" }, func(j *Journal) { j.Digest = models.EvidenceDigest{} },
		func(j *Journal) { j.Events = nil },
	} {
		j := &Journal{Kind: JournalKind, Version: Version, CollectionID: good[0].CollectionID, Digest: p.Digest, Events: good}
		mutate(j)
		add(p, j)
	}
	inputs = append(inputs, retryOracleInput{Policy: nil, Journal: nil, Raw: []byte("{}")},
		retryOracleInput{Policy: p, Journal: nil, Raw: []byte("{}")})
	validRaw := retryTestJournal(t, p, good, retryTestStream(t, good))
	var validJournal Journal
	if err := json.Unmarshal(validRaw, &validJournal); err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{
		append(bytes.Clone(validRaw), []byte("{}")...),
		bytes.Replace(validRaw, []byte(`"version":"1.0"`), []byte(`"version":"1.0","version":"1.0"`), 1),
		bytes.Replace(validRaw, []byte(`"version":"1.0"`), []byte(`"version":"1.0","unknown":0`), 1),
		bytes.Replace(validRaw, []byte(`"kind":"`+JournalKind+`"`), []byte(`"kind":null`), 1),
	} {
		inputs = append(inputs, retryOracleInput{Policy: p, Journal: &validJournal, Raw: raw})
	}
	expected := make([]retryOracleResult, 0, len(inputs))
	for i, input := range inputs {
		if input.Journal != nil {
			for index, event := range input.Journal.Events {
				if event.Runtime != nil && len(event.Runtime) == 0 {
					// Transport only: Event's omitempty would otherwise lose
					// the original fixture's empty-vs-nil conditional slice.
					inputs[i].PresentEmptyRuntime = append(inputs[i].PresentEmptyRuntime, index)
				}
			}
		}
		expected = append(expected, retryOracleCurrent(input))
	}
	// Only the new dependent inspection files are removed/replaced in this
	// ORIGINAL subprocess; existing fixtures, parser, hashes and tests remain.
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	original, err := exec.CommandContext(t.Context(), "git", "show",
		"71186704448f61f950480c76226232536366287a:internal/releasepolicy/journal.go").Output()
	if err != nil {
		t.Fatal(err)
	}
	hash := exec.CommandContext(t.Context(), "git", "hash-object", "--stdin")
	hash.Stdin = bytes.NewReader(original)
	blob, err := hash.Output()
	if err != nil || strings.TrimSpace(string(blob)) != "6f98e45697bf826877bd9062bf147f7b3e7abd55" {
		t.Fatalf("original blob not pinned: %s %v", blob, err)
	}
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	in, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := write("inputs.json", in)
	outputPath := filepath.Join(dir, "results.json")
	overlay := map[string]any{"Replace": map[string]string{
		filepath.Join(root, "internal/releasepolicy/journal.go"):           write("journal.go", original),
		filepath.Join(root, "internal/releasepolicy/retry_prefix.go"):      write("empty.go", []byte("package releasepolicy\n")),
		filepath.Join(root, "internal/releasepolicy/retry_prefix_test.go"): write("oracle_test.go", []byte(retryOriginalOracleSource)),
	}}
	encoded, err := json.Marshal(overlay)
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := write("overlay.json", encoded)
	cmd := exec.CommandContext(t.Context(), "go", "test", "-overlay", overlayPath, "./internal/releasepolicy",
		"-run", "^TestRetryOriginalOracleOnly$", "-count=1")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "G3_ORACLE_INPUT="+inputPath, "G3_ORACLE_OUTPUT="+outputPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pinned original subprocess: %v\n%s", err, output)
	}
	actual, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, want) {
		write("expected.json", want)
		t.Fatalf("independent original replay/DecodeJournal/accountPrefix differs (outputs %s)", dir)
	}
	t.Logf("Pinned original parity: %d cases, Replay/replay(prefix)/DecodeJournal/accountPrefix; receipts, nil/order, binding, errors identical", len(inputs))
}

const retryOriginalOracleSource = `package releasepolicy
import ("encoding/json"; "os"; "testing")
type retryOracleInput struct { Policy *Policy; Journal *Journal; Raw []byte; PresentEmptyRuntime []int }
type retryOracleResult struct {
 Full map[Arm]Receipt; FullError string; Prefix map[Arm]Receipt; PrefixError string;
 Decoded *Journal; DecodeError string; Decision Decision; AccountError string
}
func TestRetryOriginalOracleOnly(t *testing.T) {
 data, err := os.ReadFile(os.Getenv("G3_ORACLE_INPUT")); if err != nil { t.Fatal(err) }
 var inputs []retryOracleInput; if err := json.Unmarshal(data, &inputs); err != nil { t.Fatal(err) }
 results := []retryOracleResult{}
 errString := func(err error) string { if err != nil { return err.Error() }; return "" }
 for _, input := range inputs {
  for _, index := range input.PresentEmptyRuntime { input.Journal.Events[index].Runtime = []RuntimeObservation{} }
  r := retryOracleResult{}
  r.Full, err = Replay(input.Policy, input.Journal); r.FullError = errString(err)
  r.Prefix, err = replay(input.Policy, input.Journal, true); r.PrefixError = errString(err)
  r.Decoded, err = DecodeJournal(input.Raw, input.Policy); r.DecodeError = errString(err)
  r.Decision.Accounting = map[Arm]Reliability{}
  err = accountPrefix(input.Policy, input.Journal, &r.Decision); r.AccountError = errString(err)
  results = append(results, r)
 }
 data, err = json.Marshal(results); if err != nil { t.Fatal(err) }
 if err := os.WriteFile(os.Getenv("G3_ORACLE_OUTPUT"), data, 0600); err != nil { t.Fatal(err) }
}
`
