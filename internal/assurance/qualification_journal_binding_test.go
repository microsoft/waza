package assurance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualificationJournalRejectsIndependentlyValidOtherJobTerminal(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		name := "live"
		if recovery {
			name = "read-only recovery"
		}
		t.Run(name, func(t *testing.T) {
			manifest, _ := qualificationProtocolFixture(t)
			journal, event, _ := qualificationTestStartedJournal(t, manifest)
			other := qualificationTestOperationalTerminal(t, manifest, 1)
			_, err := qualificationParseTerminal(other.document.bytes(), manifest)
			require.NoError(t, err, "other ordinal independently valid against same manifest")
			wire, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
			require.NoError(t, err)
			require.Equal(t, uint64(0), *wire.Ordinal)
			wire.PayloadSHA256 = new(other.document.sha256())
			document, err := qualificationSeal(wire)
			require.NoError(t, err)
			substituted, err := qualificationParseEvent(document.bytes(), manifest)
			require.NoError(t, err, "event independently valid, digest genuinely recomputed")
			if !recovery {
				fault := &qualificationFaultIO{qualificationJournalIO: journal.config.IO}
				journal.config.IO = fault
				_, err = journal.CommitTerminal(t.Context(), other, substituted)
				require.Error(t, err)
				require.Empty(t, fault.calls, "cross-job payload rejected before any write/sync")
				require.NotNil(t, journal.poison)
				require.NotEmpty(t, journal.Evidence().pending)
				return
			}
			key, err := journal.key(manifest)
			require.NoError(t, err)
			path, err := qualificationReservationName(key)
			require.NoError(t, err)
			child, err := journal.config.Root.OpenRoot(path)
			require.NoError(t, err)
			defer func() { require.NoError(t, child.Close()) }()
			ack, err := journal.acknowledgment(manifest, substituted.document, wire.Sequence, new(wire.PreviousSHA256), wire.PayloadSHA256)
			require.NoError(t, err)
			require.NoError(t, child.WriteFile("job_terminal_payload-0000.json", other.document.bytes(), 0o600))
			require.NoError(t, child.WriteFile(qualificationSequenceName("event", wire.Sequence), substituted.document.bytes(), 0o600))
			require.NoError(t, child.WriteFile(qualificationSequenceName("ack", wire.Sequence), ack.bytes(), 0o600))
			prefix, err := journal.Read(t.Context(), key, manifest.context)
			require.Error(t, err, "even a correctly recomputed acknowledgment cannot rebind another job")
			require.False(t, prefix.complete)
			require.NotEmpty(t, prefix.pending)
			actual, err := child.ReadFile("job_terminal_payload-0000.json")
			require.NoError(t, err)
			require.Equal(t, other.document.bytes(), actual, "read-only rejection never repairs evidence")
		})
	}
}
