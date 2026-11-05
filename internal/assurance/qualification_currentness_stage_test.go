package assurance

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qualificationCountingReader struct {
	qualificationCurrentSourceReader
	calls int
}

func (reader *qualificationCountingReader) Read(ctx context.Context, request qualificationDocument) (qualificationDocument, error) {
	reader.calls++
	return reader.qualificationCurrentSourceReader.Read(ctx, request)
}

type qualificationCountingAttestor struct {
	qualificationCurrentAttestor
	calls int
}

func (attestor *qualificationCountingAttestor) Check(ctx context.Context, request qualificationDocument) (qualificationDocument, error) {
	attestor.calls++
	return attestor.qualificationCurrentAttestor.Check(ctx, request)
}

type qualificationCountingChallenge struct {
	calls int
}

func (source *qualificationCountingChallenge) Next(context.Context) ([32]byte, error) {
	source.calls++
	return [32]byte{1}, nil
}

func TestQualificationFreshnessRejectsDurableStageBeforeAnyCallback(t *testing.T) {
	for _, test := range []struct {
		name            string
		stage           string
		ordinal         *uint64
		removeAdmission bool
	}{
		{"claim only", "before_job", new(uint64(0)), true},
		{"early cleanup", "after_cleanup", nil, false},
		{"early decision", "before_decision", nil, false},
		{"wrong next ordinal", "before_job", new(uint64(1)), false},
		{"missing next ordinal", "before_job", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, journal, _, _, _, verifier := qualificationFreshnessFixture(t)
			if test.removeAdmission {
				key, err := journal.key(session.manifest)
				require.NoError(t, err)
				name, err := qualificationReservationName(key)
				require.NoError(t, err)
				child, err := journal.config.Root.OpenRoot(name)
				require.NoError(t, err)
				require.NoError(t, child.Remove("event-00001.json"))
				require.NoError(t, child.Remove("ack-00001.json"))
				require.NoError(t, child.Close())
			}
			reader := &qualificationCountingReader{qualificationCurrentSourceReader: session.config.SourceReader}
			attestor := &qualificationCountingAttestor{qualificationCurrentAttestor: session.config.Attestor}
			challenge := &qualificationCountingChallenge{}
			session.config.SourceReader, session.config.Attestor, session.challenges = reader, attestor, challenge
			fault := &qualificationFaultIO{qualificationJournalIO: journal.config.IO}
			journal.config.IO = fault
			lease, err := session.Check(t.Context(), test.stage, test.ordinal)
			require.Error(t, err)
			require.Empty(t, lease.request.bytes())
			require.Zero(t, challenge.calls)
			require.Zero(t, reader.calls)
			require.Zero(t, attestor.calls)
			require.Zero(t, verifier.calls)
			require.Empty(t, session.checked, "no admission key consumed")
			require.Empty(t, session.seen, "no challenge consumed")
			require.Empty(t, fault.calls, "no durable writes or syncs")
			require.NotNil(t, journal.poison)
			require.NotNil(t, session.stopped)
		})
	}
}

type qualificationBlockingReader struct {
	qualificationCurrentSourceReader
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (reader *qualificationBlockingReader) Read(ctx context.Context, request qualificationDocument) (qualificationDocument, error) {
	reader.once.Do(func() {
		close(reader.entered)
		select {
		case <-reader.release:
		case <-ctx.Done():
		}
	})
	return reader.qualificationCurrentSourceReader.Read(ctx, request)
}

func TestQualificationFreshnessReservesStageAgainstConcurrentAppend(t *testing.T) {
	session, journal, _, _, _, _ := qualificationFreshnessFixture(t)
	reader := &qualificationBlockingReader{qualificationCurrentSourceReader: session.config.SourceReader,
		entered: make(chan struct{}), release: make(chan struct{})}
	session.config.SourceReader = reader
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	results := make(chan error, 1)
	go func() { _, err := session.Check(ctx, "before_job", new(uint64(0))); results <- err }()
	select {
	case <-reader.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	head, err := qualificationDecode[qualificationPrefixWire](journal.Evidence().document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	event := qualificationTestEvent(t, session.manifest, head.LastSequence+1, head.LastEventSHA256, "job_admission", new(uint64(0)))
	_, err = journal.Append(ctx, event)
	require.ErrorContains(t, err, "owns mutation boundary")
	_, err = journal.Claim(ctx, session.manifest)
	require.ErrorContains(t, err, "owns mutation boundary")
	close(reader.release)
	require.NoError(t, <-results)
	require.Nil(t, journal.poison)
	require.Len(t, journal.Evidence().records, 2, "only correctly reserved currentness appended")
}

func TestQualificationFreshnessRechecksDurableHeadAfterCallbacks(t *testing.T) {
	session, journal, _, _, attestor, _ := qualificationFreshnessFixture(t)
	key, err := journal.key(session.manifest)
	require.NoError(t, err)
	name, err := qualificationReservationName(key)
	require.NoError(t, err)
	child, err := journal.config.Root.OpenRoot(name)
	require.NoError(t, err)
	defer func() { require.NoError(t, child.Close()) }()
	attestor.mutate = func(*qualificationCurrentnessAcknowledgment) {
		require.NoError(t, child.Remove("event-00001.json"))
		require.NoError(t, child.Remove("ack-00001.json"))
	}
	_, err = session.Check(t.Context(), "before_job", new(uint64(0)))
	require.ErrorContains(t, err, "head changed")
	require.NotNil(t, journal.poison)
	_, err = child.Stat("event-00002.json")
	require.ErrorIs(t, err, os.ErrNotExist, "no event published after physical head alteration")
}
