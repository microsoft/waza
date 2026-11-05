package releasepolicy

import (
	"bytes"
	"encoding/json"
	"testing"
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
