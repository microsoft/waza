package assurance

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qualificationClockBarrierIO struct {
	qualificationJournalIO
	clock *qualificationTestClock
	calls int
}

func (io *qualificationClockBarrierIO) SyncDirectory(ctx context.Context, root *os.Root) error {
	err := io.qualificationJournalIO.SyncDirectory(ctx, root)
	io.calls++
	if io.calls == 2 {
		io.clock.value = io.clock.value.Add(2 * time.Minute)
	}
	return err
}

type qualificationFailChallenge struct{}

func (qualificationFailChallenge) Next(context.Context) ([32]byte, error) {
	return [32]byte{}, errors.New("injected challenge failure")
}

func TestQualificationFreshnessAckBarrierExpiryAndCallbackRollback(t *testing.T) {
	t.Run("receipt expires during durable acknowledgment", func(t *testing.T) {
		session, journal, _, clock, _, _ := qualificationFreshnessFixture(t)
		journal.config.IO = &qualificationClockBarrierIO{qualificationJournalIO: journal.config.IO, clock: clock}
		lease, err := session.Check(t.Context(), "before_job", new(uint64(0)))
		require.Error(t, err)
		require.NotEmpty(t, lease.ack.bytes())
		require.Len(t, journal.Evidence().records, 2, "completed receipt barrier remains historical evidence")
		require.NotEmpty(t, journal.Evidence().pending)
		require.NotNil(t, journal.poison)
		_, err = session.Check(t.Context(), "before_job", new(uint64(1)))
		require.Error(t, err)
	})
	t.Run("attestor callback rolls clock backward", func(t *testing.T) {
		session, journal, _, clock, attestor, verifier := qualificationFreshnessFixture(t)
		attestor.mutate = func(*qualificationCurrentnessAcknowledgment) {
			clock.value = clock.value.Add(-time.Second)
		}
		lease, err := session.Check(t.Context(), "before_job", new(uint64(0)))
		require.ErrorContains(t, err, "clock")
		require.NotEmpty(t, lease.ack.bytes())
		require.Zero(t, verifier.calls, "rollback stops before authority callback")
		require.NotNil(t, journal.poison)
	})
	t.Run("nonce source failure consumes session", func(t *testing.T) {
		session, journal, _, _, _, _ := qualificationFreshnessFixture(t)
		session.challenges = qualificationFailChallenge{}
		_, err := session.Check(t.Context(), "before_job", new(uint64(0)))
		require.Error(t, err)
		require.NotNil(t, journal.poison)
		_, err = session.Check(t.Context(), "before_job", new(uint64(0)))
		require.Error(t, err)
	})
	t.Run("canceled callback cannot admit receipt", func(t *testing.T) {
		session, journal, _, _, attestor, verifier := qualificationFreshnessFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		attestor.mutate = func(*qualificationCurrentnessAcknowledgment) { cancel() }
		_, err := session.Check(ctx, "before_job", new(uint64(0)))
		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, verifier.calls)
		require.NotNil(t, journal.poison)
	})
}

func TestQualificationFreshnessAcrossSuccessfulJobsUsesSameClock(t *testing.T) {
	session, journal, _, clock, _, _ := qualificationFreshnessFixture(t)
	old, err := session.Check(t.Context(), "before_job", new(uint64(0)))
	require.NoError(t, err)
	head, err := qualificationDecode[qualificationPrefixWire](journal.Evidence().document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	for _, kind := range []string{"job_admission", "job_start"} {
		event := qualificationTestEvent(t, session.manifest, head.LastSequence+1, head.LastEventSHA256, kind, new(uint64(0)))
		qualificationTestAppend(t, journal, session.manifest, event)
		head, err = qualificationDecode[qualificationPrefixWire](journal.Evidence().document.bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
	}
	terminal := qualificationTestObservedTerminal(t, session.manifest, 0)
	event := qualificationTestTerminalEvent(t, session.manifest, head.LastSequence+1, head.LastEventSHA256, 0, terminal)
	_, err = journal.CommitTerminal(t.Context(), terminal, event)
	require.NoError(t, err)
	clock.value = clock.value.Add(time.Second)
	_, err = session.Check(t.Context(), "before_job", new(uint64(1)))
	require.NoError(t, err)
	clock.value = clock.value.Add(-time.Second)
	require.Error(t, old.Revalidate(t.Context()), "older lease cannot bypass invocation-wide clock")
	require.NotNil(t, journal.poison)
}

func TestQualificationFreshnessRandomChallengeAndCanceledGeneration(t *testing.T) {
	source := qualificationRandomChallenge{}
	first, err := source.Next(t.Context())
	require.NoError(t, err)
	second, err := source.Next(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, [32]byte{}, first)
	require.NotEqual(t, first, second)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = source.Next(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestQualificationFreshnessFinalAckExpiryRetainsNonpassEvidence(t *testing.T) {
	session, journal, _, clock, _, verifier := qualificationFreshnessFixtureSetup(t, false)
	events, terminals := qualificationTestCompleteHistory(t, session.manifest, false)
	// Seed only the preceding history; the last terminal, cleanup receipt and
	// expiring final receipt still enter the real production methods.
	events = events[:len(events)-2]
	previous := session.manifest.document.sha256()
	for i, event := range events {
		wire, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
		wire.PreviousSHA256 = previous
		if wire.Type == "currentness" {
			request, err := qualificationSeal(*wire.CurrentnessRequest)
			require.NoError(t, err)
			ack, err := session.config.Attestor.Check(t.Context(), request)
			require.NoError(t, err)
			lease := &qualificationReceiptLease{owner: session, request: request, ack: ack}
			require.NoError(t, session.verify(t.Context(), lease), "each historical receipt retains actual current-source/proof verification")
			ackWire, err := qualificationDecode[qualificationCurrentnessAcknowledgment](ack.bytes(), qualificationDocumentLimit)
			require.NoError(t, err)
			wire.CurrentnessAcknowledgment = &ackWire
			session.seen[wire.CurrentnessRequest.Challenge] = true
			checkKey := wire.CurrentnessRequest.Stage
			if wire.CurrentnessRequest.JobSHA256 != nil {
				checkKey += ":" + *wire.CurrentnessRequest.JobSHA256
			}
			session.checked[checkKey] = true
		}
		doc, err := qualificationSeal(wire)
		require.NoError(t, err)
		events[i], err = qualificationParseEvent(doc.bytes(), session.manifest)
		require.NoError(t, err)
		previous = events[i].document.sha256()
	}
	require.Equal(t, 4, verifier.calls, "four independent jobs preserve source-specific proof controls")
	prefix := qualificationTestMaterializeHistory(t, journal, events, terminals[:len(terminals)-1])
	require.Equal(t, "job_terminal", prefix.nextStage)
	head, err := qualificationDecode[qualificationPrefixWire](prefix.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	terminal := terminals[len(terminals)-1]
	event := qualificationTestTerminalEvent(t, session.manifest, head.LastSequence+1, head.LastEventSHA256, 3, terminal)
	_, err = journal.CommitTerminal(t.Context(), terminal, event)
	require.NoError(t, err)
	session.challenges = &qualificationTestChallenges{value: 99}
	_, err = session.Check(t.Context(), "after_cleanup", nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, verifier.calls, 6, "cleanup still validates proof before and after its actual acknowledgment")
	journal.config.IO = &qualificationClockBarrierIO{qualificationJournalIO: journal.config.IO, clock: clock}
	lease, err := session.Check(t.Context(), "before_decision", nil)
	require.Error(t, err, "final durable acknowledgment is not an expiry exemption")
	require.NotEmpty(t, lease.ack.bytes())
	require.NotEmpty(t, journal.Evidence().pending)
	require.NotNil(t, journal.poison)
	require.Error(t, lease.Revalidate(t.Context()))
	head, err = qualificationDecode[qualificationPrefixWire](journal.Evidence().document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	event = qualificationTestEvent(t, session.manifest, head.LastSequence+1, head.LastEventSHA256, "run_admission", nil)
	_, err = journal.Append(t.Context(), event)
	require.Error(t, err, "no terminal can upgrade expired final authority evidence")
}

func TestQualificationFreshnessConstructorMaterialBudgetBeforeCopies(t *testing.T) {
	session, _, _, _, _, _ := qualificationFreshnessFixture(t)
	for _, total := range []uint64{1, qualificationTotalLimit} {
		journal, _, _ := qualificationLocalTestJournal(t, session.manifest)
		_, err := journal.Claim(t.Context(), session.manifest)
		require.NoError(t, err)
		calls := 0
		role := uint64(1)
		if total == 1 {
			role = MaxLabelBytes
		}
		_, err = qualificationOpenFreshnessSessionBounded(t.Context(), journal, session.config, &qualificationTestChallenges{},
			role, total, func() { calls++ })
		require.Error(t, err)
		require.Zero(t, calls, "role/aggregate material budgets precede defensive-copy allocation")
		require.NotNil(t, journal.poison)
	}
}

func TestQualificationFreshnessUnseededCompleteProductionSequence(t *testing.T) {
	// This fixture calls actual Claim and run-admission Append. No history is
	// seeded; synthetic terminal evidence never invokes a provider.
	session, journal, _, _, _, verifier := qualificationFreshnessFixture(t)
	manifest := session.manifest
	m, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	require.Len(t, m.Jobs, 4)
	challenges := map[string]bool{}
	check := func(stage string, ordinal *uint64) *qualificationReceiptLease {
		t.Helper()
		lease, err := session.Check(t.Context(), stage, ordinal)
		require.NoError(t, err)
		request, err := qualificationDecode[qualificationCurrentnessRequest](lease.request.bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
		require.Equal(t, stage, request.Stage)
		require.Equal(t, ordinal, request.Ordinal)
		require.False(t, challenges[request.Challenge], "each actual Check generates a fresh invocation challenge")
		challenges[request.Challenge] = true
		receipt, err := qualificationParseCurrentnessAcknowledgment(lease.ack.bytes())
		require.NoError(t, err)
		value, err := qualificationDecode[qualificationCurrentnessAcknowledgment](receipt.bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
		require.Equal(t, lease.request.sha256(), value.RequestSHA256)
		require.True(t, value.Current)
		require.True(t, value.AssociationValid)
		require.NoError(t, lease.Revalidate(t.Context()))
		return lease
	}
	head := func() qualificationPrefixWire {
		t.Helper()
		value, err := qualificationDecode[qualificationPrefixWire](journal.Evidence().document.bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
		return value
	}
	for ordinal := range m.Jobs {
		o := uint64(ordinal)
		check("before_job", new(o))
		for _, kind := range []string{"job_admission", "job_start"} {
			h := head()
			event := qualificationTestEvent(t, manifest, h.LastSequence+1, h.LastEventSHA256, kind, new(o))
			qualificationTestAppend(t, journal, manifest, event)
		}
		terminal := qualificationTestObservedTerminal(t, manifest, o)
		h := head()
		event := qualificationTestTerminalEvent(t, manifest, h.LastSequence+1, h.LastEventSHA256, o, terminal)
		ack, err := journal.CommitTerminal(t.Context(), terminal, event)
		require.NoError(t, err)
		require.NoError(t, qualificationMatchAck(manifest, event.document, h.LastSequence+1, new(h.LastEventSHA256),
			new(terminal.document.sha256()), ack))
	}
	check("after_cleanup", nil)
	finalLease := check("before_decision", nil)
	prefix := journal.Evidence()
	require.True(t, prefix.complete)
	require.Empty(t, prefix.pending)
	require.Len(t, prefix.records, 19)
	require.Len(t, challenges, 6)
	require.Len(t, session.checked, 6)
	require.GreaterOrEqual(t, verifier.calls, 18, "every receipt invokes real source-specific verification before/after ack and revalidation")
	// Nonpassing completion describes this synthetic engine-free test, not an
	// actual paid calibration claim. Publication cannot expand the core cutoff.
	h := head()
	event := qualificationTestEvent(t, manifest, h.LastSequence+1, h.LastEventSHA256, "run_admission", nil)
	wire, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	wire.Type, wire.Admission = "run_terminal", nil
	wire.Completion = &qualificationCompletionWire{State: "not_assessed", ReasonCode: "report_invalid", CompletedJobs: uint64(len(m.Jobs))}
	document, err := qualificationSeal(wire)
	require.NoError(t, err)
	final, err := qualificationParseEvent(document.bytes(), manifest)
	require.NoError(t, err)
	require.NoError(t, finalLease.Revalidate(t.Context()), "actual final receipt rechecked immediately before publication")
	ack := qualificationTestAppend(t, journal, manifest, final)
	require.NoError(t, qualificationMatchAck(manifest, final.document, h.LastSequence+1, new(h.LastEventSHA256), nil, ack))
	published := journal.Evidence()
	require.NotNil(t, published.final)
	require.Equal(t, prefix.document.bytes(), published.document.bytes())
	key, err := journal.key(manifest)
	require.NoError(t, err)
	recovered, err := journal.Read(t.Context(), key, manifest.context)
	require.NoError(t, err)
	require.True(t, recovered.complete)
	require.NotNil(t, recovered.final)
	require.Equal(t, prefix.document.bytes(), recovered.document.bytes())
	require.Equal(t, published.final.document.bytes(), recovered.final.document.bytes())
	require.Nil(t, journal.poison)
}
