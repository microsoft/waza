package assurance

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func qualificationReplayTestChild(t *testing.T, journal *qualificationLocalJournal) (*os.Root, qualificationReservationKey) {
	t.Helper()
	key, err := journal.key(journal.manifest)
	require.NoError(t, err)
	name, err := qualificationReservationName(key)
	require.NoError(t, err)
	child, err := journal.config.Root.OpenRoot(name)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, child.Close()) })
	return child, key
}

func qualificationReplayTestFiles(t *testing.T, child *os.Root) map[string]string {
	t.Helper()
	directory, err := child.Open(".")
	require.NoError(t, err)
	names, err := directory.Readdirnames(-1)
	require.NoError(t, err)
	require.NoError(t, directory.Close())
	result := map[string]string{}
	for _, name := range names {
		bytes, err := child.ReadFile(name)
		require.NoError(t, err)
		result[name] = string(bytes)
	}
	return result
}

func TestQualificationReplayClaimedManifestFence(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	wire, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	wire.InvocationID = strings.Replace(wire.InvocationID, "é", "è", 1)
	otherDoc, err := qualificationSeal(wire)
	require.NoError(t, err)
	require.Len(t, otherDoc.bytes(), len(manifest.document.bytes()), "same-length replacement defeats size-only identity")
	other, err := qualificationParseManifest(otherDoc.bytes(), manifest.context)
	require.NoError(t, err, "B is independently source-admitted, not malformed metadata")
	for _, n := range []int{0, 1, 4} {
		t.Run(fmt.Sprintf("resealed-history-%d", n), func(t *testing.T) {
			journal, _, _ := qualificationLocalTestJournal(t, manifest)
			_, err := journal.Claim(t.Context(), manifest)
			require.NoError(t, err)
			aEvents, _ := qualificationTestCompleteHistory(t, manifest, true)
			if n > 0 {
				qualificationTestMaterializeHistory(t, journal, aEvents[:n], nil)
			}
			child, key := qualificationReplayTestChild(t, journal)
			bKey, err := journal.key(other)
			require.NoError(t, err)
			require.Equal(t, key, bKey, "reservation excludes invocation")
			bEvents, _ := qualificationTestCompleteHistory(t, other, true)
			ack, records := qualificationReplayTestRecords(t, other, bEvents[:n])
			require.NoError(t, child.WriteFile("manifest.json", other.document.bytes(), 0o600))
			require.NoError(t, child.WriteFile(qualificationSequenceName("ack", 0), ack.bytes(), 0o600))
			for _, record := range records {
				row, err := qualificationDecode[qualificationRecordWire](record.document.bytes(), qualificationDocumentLimit)
				require.NoError(t, err)
				eventDoc, err := qualificationSeal(row.Event)
				require.NoError(t, err)
				ackDoc, err := qualificationSeal(row.Acknowledgment)
				require.NoError(t, err)
				require.NoError(t, child.WriteFile(qualificationSequenceName("event", row.Event.Sequence), eventDoc.bytes(), 0o600))
				require.NoError(t, child.WriteFile(qualificationSequenceName("ack", row.Event.Sequence), ackDoc.bytes(), 0o600))
			}
			before := qualificationReplayTestFiles(t, child)
			fault := &qualificationFaultIO{qualificationJournalIO: journal.config.IO}
			io := &qualificationReplayReadIO{qualificationJournalIO: fault}
			journal.config.IO = io
			var attempted qualificationEvent
			var terminal qualificationTerminal
			if n == 4 {
				terminal = qualificationTestOperationalTerminal(t, other, 0)
				attempted = qualificationTestTerminalEvent(t, other, 5, bEvents[3].document.sha256(), 0, terminal)
				_, err = journal.CommitTerminal(t.Context(), terminal, attempted)
			} else {
				attempted = bEvents[n]
				_, err = journal.Append(t.Context(), attempted)
			}
			require.ErrorContains(t, err, "persisted manifest differs")
			require.NotNil(t, journal.poison)
			require.Empty(t, fault.calls, "ownership rejection precedes every write/sync")
			require.Equal(t, other.document, journal.Evidence().manifest.document, "fresh disk custody retained with error")
			require.Contains(t, journal.Evidence().pending, attempted.document)
			if n == 4 {
				require.Contains(t, journal.Evidence().pending, terminal.document)
			}
			require.Contains(t, io.reads, "manifest.json")
			require.Contains(t, io.reads, "reservation.json")
			require.Contains(t, io.reads, qualificationSequenceName("ack", 0))
			for i := 1; i <= n; i++ {
				require.Contains(t, io.reads, qualificationSequenceName("event", uint64(i)))
				require.Contains(t, io.reads, qualificationSequenceName("ack", uint64(i)))
			}
			_, err = journal.Append(t.Context(), attempted)
			require.Error(t, err)
			require.Empty(t, fault.calls)
			historical, err := journal.Read(t.Context(), key, manifest.context)
			require.NoError(t, err, "read-only B recovery has no A-handle ownership assertion")
			require.Equal(t, other.document, historical.manifest.document)
			require.Len(t, historical.records, n)
			require.Equal(t, before, qualificationReplayTestFiles(t, child), "rejection and recovery perform no repair")
		})
	}
}

func TestQualificationReplayFreshFullHistoryAndLinearState(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	journal, _, _ := qualificationLocalTestJournal(t, manifest)
	_, err := journal.Claim(t.Context(), manifest)
	require.NoError(t, err)
	events, terminals := qualificationTestCompleteHistory(t, manifest, true)
	expected := qualificationTestMaterializeHistory(t, journal, events, terminals)
	child, key := qualificationReplayTestChild(t, journal)
	io := &qualificationReplayReadIO{qualificationJournalIO: journal.config.IO}
	journal.config.IO = io
	first, state, err := journal.readChildWithReplay(t.Context(), child, key, manifest.context)
	require.NoError(t, err)
	require.Len(t, state.records, len(events))
	require.Equal(t, uint64(len(events)), state.advances, "one actual transition attempt per fresh disk row")
	qualificationReplayAssertPrefix(t, expected, first)
	_, records := qualificationReplayTestRecords(t, manifest, events)
	old, err := qualificationOldDerivePrefix(manifest, expected.ack, records)
	require.NoError(t, err)
	qualificationReplayAssertPrefix(t, old, first)
	require.Len(t, io.reads, 3+2*len(events)+len(terminals))
	readPaths := slices.Clone(io.reads)
	io.reads = nil
	second, secondState, err := journal.readChildWithReplay(t.Context(), child, key, manifest.context)
	require.NoError(t, err)
	require.NotSame(t, state, secondState)
	require.NotSame(t, state.view, secondState.view, "views never cross operations")
	require.Equal(t, uint64(len(events)), secondState.advances, "no cross-operation accumulated counter/state")
	require.Equal(t, readPaths, io.reads, "all prior bytes acquired again, not just tail metadata")
	qualificationReplayAssertPrefix(t, first, second)
	state.seenChallenges["old-operation"] = true
	require.NotContains(t, secondState.seenChallenges, "old-operation")
	last := qualificationTestEvent(t, manifest, uint64(len(events)+1), events[len(events)-1].document.sha256(), "run_admission", nil)
	wire, err := qualificationDecode[qualificationEventWire](last.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	wire.Type, wire.Admission = "run_terminal", nil
	wire.Completion = &qualificationCompletionWire{State: "not_assessed", ReasonCode: "report_invalid", CompletedJobs: 4}
	doc, err := qualificationSeal(wire)
	require.NoError(t, err)
	_, finalRecords := qualificationReplayTestRecords(t, manifest, append(slices.Clone(events), qualificationEvent{doc}))
	candidate := secondState.fork()
	require.NoError(t, candidate.advance(finalRecords[len(finalRecords)-1]))
	require.Equal(t, secondState.advances+1, candidate.advances, "candidate adds exactly one step")
	require.Equal(t, uint64(len(events)), secondState.advances, "candidate never modifies recovered state")
	fault := &qualificationFaultIO{qualificationJournalIO: journal.config.IO}
	journal.config.IO = fault
	_, err = journal.Append(t.Context(), qualificationEvent{doc})
	require.NoError(t, err)
	require.Len(t, fault.calls, 6, "candidate persisted only once")
	require.NotNil(t, journal.Evidence().final)
	require.Equal(t, first.document, journal.Evidence().document, "final suffix does not expand core cutoff")
}

func TestQualificationReplayFreshReplacementOrphansAndTornHistory(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	for _, scenario := range []string{"event-same-length", "ack-same-length", "reservation-same-length", "resealed-stale-cas", "orphan", "torn"} {
		t.Run(scenario, func(t *testing.T) {
			journal, _, _ := qualificationLocalTestJournal(t, manifest)
			_, err := journal.Claim(t.Context(), manifest)
			require.NoError(t, err)
			event := qualificationTestEvent(t, manifest, 1, manifest.document.sha256(), "run_admission", nil)
			qualificationTestAppend(t, journal, manifest, event)
			child, key := qualificationReplayTestChild(t, journal)
			old, err := journal.Read(t.Context(), key, manifest.context)
			require.NoError(t, err)
			candidate := qualificationTestCurrentness(t, manifest, 2, event.document.sha256(), "before_job", new(uint64(0)), strings.Repeat("a", 64))
			switch scenario {
			case "event-same-length":
				bytes, err := child.ReadFile("event-00001.json")
				require.NoError(t, err)
				replacement := strings.Replace(string(bytes), "synthetic-invocation-é", "synthetic-invocation-è", 1)
				require.NotEqual(t, string(bytes), replacement)
				require.Len(t, replacement, len(bytes))
				require.NoError(t, child.WriteFile("event-00001.json", []byte(replacement), 0o600))
			case "ack-same-length":
				bytes, err := child.ReadFile("ack-00001.json")
				require.NoError(t, err)
				replacement := strings.Replace(string(bytes), event.document.sha256(), strings.Repeat("f", 64), 1)
				require.NotEqual(t, string(bytes), replacement)
				require.Len(t, replacement, len(bytes))
				require.NoError(t, child.WriteFile("ack-00001.json", []byte(replacement), 0o600))
			case "reservation-same-length":
				bytes, err := child.ReadFile("reservation.json")
				require.NoError(t, err)
				replacement := strings.Replace(string(bytes), key.ContractSHA256, strings.Repeat("f", 64), 1)
				require.NotEqual(t, string(bytes), replacement)
				require.Len(t, replacement, len(bytes))
				require.NoError(t, child.WriteFile("reservation.json", []byte(replacement), 0o600))
			case "resealed-stale-cas":
				wire, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
				require.NoError(t, err)
				wire.Admission.Allowed, wire.Admission.ReasonCode = false, "admission_rejected"
				doc, err := qualificationSeal(wire)
				require.NoError(t, err)
				ack, err := journal.acknowledgment(manifest, doc, 1, new(manifest.document.sha256()), nil)
				require.NoError(t, err)
				require.NoError(t, child.WriteFile("event-00001.json", doc.bytes(), 0o600))
				require.NoError(t, child.WriteFile("ack-00001.json", ack.bytes(), 0o600))
				fresh, err := journal.Read(t.Context(), key, manifest.context)
				require.NoError(t, err, "fully resealed negative history is freshly admitted")
				require.Equal(t, "stopped", fresh.nextStage)
				require.NotEqual(t, old.document, fresh.document)
			case "orphan":
				require.NoError(t, child.WriteFile("ack-00002.json", []byte("{}"), 0o600))
			case "torn":
				require.NoError(t, child.Remove("ack-00001.json"))
			}

			before := qualificationReplayTestFiles(t, child)
			fault := &qualificationFaultIO{qualificationJournalIO: journal.config.IO}
			io := &qualificationReplayReadIO{qualificationJournalIO: fault}
			journal.config.IO = io
			_, err = journal.Append(t.Context(), candidate)
			require.Error(t, err)
			require.Empty(t, fault.calls)
			require.NotNil(t, journal.poison)
			require.Contains(t, journal.Evidence().pending, candidate.document)
			require.Contains(t, io.reads, "reservation.json")
			if scenario != "reservation-same-length" {
				require.Contains(t, io.reads, "manifest.json")
				require.Contains(t, io.reads, "event-00001.json")
			}
			if scenario == "torn" {
				require.Contains(t, journal.Evidence().pending, event.document)
			}
			require.Equal(t, before, qualificationReplayTestFiles(t, child))
		})
	}
}

func TestQualificationReplayFreshPayloadSubstitution(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	journal, event, terminal := qualificationTestStartedJournal(t, manifest)
	_, err := journal.CommitTerminal(t.Context(), terminal, event)
	require.NoError(t, err)
	child, key := qualificationReplayTestChild(t, journal)
	prefix, err := journal.Read(t.Context(), key, manifest.context)
	require.NoError(t, err)
	require.Len(t, prefix.records, 5)
	other := qualificationTestOperationalTerminal(t, manifest, 1)
	wire, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	wire.PayloadSHA256 = new(other.document.sha256())
	substitution, err := qualificationSeal(wire)
	require.NoError(t, err)
	ack, err := journal.acknowledgment(manifest, substitution, wire.Sequence, new(wire.PreviousSHA256), wire.PayloadSHA256)
	require.NoError(t, err)
	require.NoError(t, child.WriteFile("job_terminal_payload-0000.json", other.document.bytes(), 0o600))
	require.NoError(t, child.WriteFile("event-00005.json", substitution.bytes(), 0o600))
	require.NoError(t, child.WriteFile("ack-00005.json", ack.bytes(), 0o600))
	before := qualificationReplayTestFiles(t, child)
	io := &qualificationReplayReadIO{qualificationJournalIO: journal.config.IO}
	journal.config.IO = io
	rejected, err := journal.Read(t.Context(), key, manifest.context)
	require.ErrorContains(t, err, "historical payload invalid")
	require.Len(t, rejected.records, 4, "last verified partial prefix survives substitution")
	require.Contains(t, rejected.pending, substitution)
	require.Contains(t, rejected.pending, other.document)
	require.Contains(t, io.reads, "job_terminal_payload-0000.json")
	require.Equal(t, before, qualificationReplayTestFiles(t, child), "no cached payload or repair")
}
