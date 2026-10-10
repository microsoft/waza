package assurance

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Materialize already parsed historical setup once. Tests still exercise the
// real mutator/freshness entrypoint under test; setup does not replay every
// preceding prefix after each individual append.
func qualificationTestMaterializeHistory(t *testing.T, journal *qualificationLocalJournal, events []qualificationEvent, terminals []qualificationTerminal) qualificationPrefix {
	t.Helper()
	manifest := journal.manifest
	key, err := journal.key(manifest)
	require.NoError(t, err)
	name, err := qualificationReservationName(key)
	require.NoError(t, err)
	child, err := journal.config.IO.OpenDirectDirectory(t.Context(), journal.config.Root, name)
	require.NoError(t, err)
	defer func() { require.NoError(t, child.Close()) }()
	payloads := map[string]qualificationTerminal{}
	for _, terminal := range terminals {
		payloads[terminal.document.sha256()] = terminal
	}
	records := []qualificationRecord{}
	for _, event := range events {
		parsed, err := qualificationParseEvent(event.document.bytes(), manifest)
		require.NoError(t, err)
		wire, err := qualificationDecode[qualificationEventWire](parsed.document.bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
		if wire.Type == "job_terminal" {
			terminal, ok := payloads[*wire.PayloadSHA256]
			require.True(t, ok)
			valid, err := qualificationParseTerminal(terminal.document.bytes(), manifest)
			require.NoError(t, err)
			require.NoError(t, qualificationMatchTerminalEvent(valid, wire))
			path, err := qualificationArtifactFilename("job_terminal_payload", wire.Ordinal)
			require.NoError(t, err)
			require.NoError(t, journal.durable(t.Context(), child, path, valid.document.bytes()))
		}
		ack, err := journal.acknowledgment(manifest, parsed.document, wire.Sequence, new(wire.PreviousSHA256), wire.PayloadSHA256)
		require.NoError(t, err)
		require.NoError(t, qualificationMatchAck(manifest, parsed.document, wire.Sequence, new(wire.PreviousSHA256), wire.PayloadSHA256, ack))
		require.NoError(t, journal.durable(t.Context(), child, qualificationSequenceName("event", wire.Sequence), parsed.document.bytes()))
		require.NoError(t, journal.durable(t.Context(), child, qualificationSequenceName("ack", wire.Sequence), ack.bytes()))
		ackWire, err := qualificationDecode[qualificationAcknowledgment](ack.bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
		record, err := qualificationSeal(qualificationRecordWire{wire, ackWire})
		require.NoError(t, err)
		records = append(records, qualificationRecord{record})
	}
	prefix, err := qualificationDerivePrefix(manifest, journal.Evidence().ack, records)
	require.NoError(t, err)
	journal.evidence = prefix
	return prefix
}

func qualificationTestCompleteHistory(t *testing.T, manifest qualificationManifest, includeDecision bool) ([]qualificationEvent, []qualificationTerminal) {
	t.Helper()
	sequence, previous := uint64(1), manifest.document.sha256()
	events := []qualificationEvent{}
	add := func(event qualificationEvent) {
		events = append(events, event)
		sequence++
		previous = event.document.sha256()
	}
	add(qualificationTestEvent(t, manifest, sequence, previous, "run_admission", nil))
	wire, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	terminals := []qualificationTerminal{}
	for ordinal := range wire.Jobs {
		o := uint64(ordinal)
		add(qualificationTestCurrentness(t, manifest, sequence, previous, "before_job", new(o), fmt.Sprintf("%064x", ordinal+1)))
		add(qualificationTestEvent(t, manifest, sequence, previous, "job_admission", new(o)))
		add(qualificationTestEvent(t, manifest, sequence, previous, "job_start", new(o)))
		terminal := qualificationTestObservedTerminal(t, manifest, o)
		add(qualificationTestTerminalEvent(t, manifest, sequence, previous, o, terminal))
		terminals = append(terminals, terminal)
	}
	add(qualificationTestCurrentness(t, manifest, sequence, previous, "after_cleanup", nil, fmt.Sprintf("%064x", 100)))
	if includeDecision {
		add(qualificationTestCurrentness(t, manifest, sequence, previous, "before_decision", nil, fmt.Sprintf("%064x", 101)))
	}
	return events, terminals
}
