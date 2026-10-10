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
	session, journal, _, clock, _, _ := qualificationFreshnessFixture(t)
	for ordinal := uint64(0); ordinal < 4; ordinal++ {
		_, err := session.Check(t.Context(), "before_job", new(ordinal))
		require.NoError(t, err)
		for _, kind := range []string{"job_admission", "job_start"} {
			head, err := qualificationDecode[qualificationPrefixWire](journal.Evidence().document.bytes(), qualificationDocumentLimit)
			require.NoError(t, err)
			event := qualificationTestEvent(t, session.manifest, head.LastSequence+1, head.LastEventSHA256, kind, new(ordinal))
			qualificationTestAppend(t, journal, session.manifest, event)
		}
		terminal := qualificationTestObservedTerminal(t, session.manifest, ordinal)
		head, err := qualificationDecode[qualificationPrefixWire](journal.Evidence().document.bytes(), qualificationDocumentLimit)
		require.NoError(t, err)
		event := qualificationTestTerminalEvent(t, session.manifest, head.LastSequence+1, head.LastEventSHA256, ordinal, terminal)
		_, err = journal.CommitTerminal(t.Context(), terminal, event)
		require.NoError(t, err)
	}
	_, err := session.Check(t.Context(), "after_cleanup", nil)
	require.NoError(t, err)
	journal.config.IO = &qualificationClockBarrierIO{qualificationJournalIO: journal.config.IO, clock: clock}
	lease, err := session.Check(t.Context(), "before_decision", nil)
	require.Error(t, err, "final durable acknowledgment is not an expiry exemption")
	require.NotEmpty(t, lease.ack.bytes())
	require.NotEmpty(t, journal.Evidence().pending)
	require.NotNil(t, journal.poison)
	require.Error(t, lease.Revalidate(t.Context()))
	head, err := qualificationDecode[qualificationPrefixWire](journal.Evidence().document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	event := qualificationTestEvent(t, session.manifest, head.LastSequence+1, head.LastEventSHA256, "run_admission", nil)
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
