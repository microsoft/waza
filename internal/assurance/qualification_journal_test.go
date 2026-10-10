package assurance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qualificationFaultIO struct {
	qualificationJournalIO
	calls []string
	fail  int
}

func (fault *qualificationFaultIO) record(call string) error {
	fault.calls = append(fault.calls, call)
	if fault.fail == len(fault.calls) {
		return errors.New("injected local persistence failure")
	}
	return nil
}
func (fault *qualificationFaultIO) WriteExclusive(ctx context.Context, root *os.Root, name string, data []byte) error {
	if err := fault.record("write:" + name); err != nil {
		return err
	}
	return fault.qualificationJournalIO.WriteExclusive(ctx, root, name, data)
}
func (fault *qualificationFaultIO) SyncFile(ctx context.Context, root *os.Root, name string) error {
	if err := fault.record("file:" + name); err != nil {
		return err
	}
	return fault.qualificationJournalIO.SyncFile(ctx, root, name)
}
func (fault *qualificationFaultIO) SyncDirectory(ctx context.Context, root *os.Root) error {
	if err := fault.record("directory"); err != nil {
		return err
	}
	return fault.qualificationJournalIO.SyncDirectory(ctx, root)
}

func TestQualificationJournalClaimOneShotConcurrencyAndNoResume(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	first, root, _ := qualificationLocalTestJournal(t, manifest)
	second, err := qualificationOpenLocalJournal(t.Context(), first.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, second.Close()) }()
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for _, journal := range []*qualificationLocalJournal{first, second} {
		wg.Go(func() { _, err := journal.Claim(t.Context(), manifest); outcomes <- err })
	}
	wg.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes, "only one process-coordinated local reservation owner")
	var winner *qualificationLocalJournal
	if first.poison == nil {
		winner = first
	} else {
		winner = second
	}
	_, err = winner.Claim(t.Context(), manifest)
	require.Error(t, err, "same-instance duplicate Claim is not new ownership")
	reopened, err := qualificationOpenLocalJournal(t.Context(), first.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	key, err := first.key(manifest)
	require.NoError(t, err)
	prefix, err := reopened.Read(t.Context(), key, manifest.context)
	require.NoError(t, err)
	require.False(t, prefix.complete)
	_, err = reopened.Claim(t.Context(), manifest)
	require.Error(t, err, "persisted identical manifest does not authorize resume")
	_, err = root.Stat(".")
	require.NoError(t, err, "borrowed caller root remains open")
}

func qualificationTestAppend(t *testing.T, journal *qualificationLocalJournal, manifest qualificationManifest, event qualificationEvent) qualificationDocument {
	t.Helper()
	ack, err := journal.Append(t.Context(), event)
	require.NoError(t, err)
	wire, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	require.NoError(t, qualificationMatchAck(manifest, event.document, wire.Sequence, new(wire.PreviousSHA256), wire.PayloadSHA256, ack))
	return ack
}

func qualificationTestTerminalEvent(t *testing.T, manifest qualificationManifest, sequence uint64, previous string, ordinal uint64, terminal qualificationTerminal) qualificationEvent {
	t.Helper()
	event := qualificationTestEvent(t, manifest, sequence, previous, "job_start", new(ordinal))
	wire, err := qualificationDecode[qualificationEventWire](event.document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	wire.Type, wire.PayloadSHA256 = "job_terminal", new(terminal.document.sha256())
	doc, err := qualificationSeal(wire)
	require.NoError(t, err)
	parsed, err := qualificationParseEvent(doc.bytes(), manifest)
	require.NoError(t, err)
	return parsed
}

func qualificationTestStartedJournal(t *testing.T, manifest qualificationManifest) (*qualificationLocalJournal, qualificationEvent, qualificationTerminal) {
	t.Helper()
	journal, _, _ := qualificationLocalTestJournal(t, manifest)
	_, err := journal.Claim(t.Context(), manifest)
	require.NoError(t, err)
	previous := manifest.document.sha256()
	event := qualificationTestEvent(t, manifest, 1, previous, "run_admission", nil)
	qualificationTestAppend(t, journal, manifest, event)
	previous = event.document.sha256()
	event = qualificationTestCurrentness(t, manifest, 2, previous, "before_job", new(uint64(0)), strings.Repeat("a", 64))
	qualificationTestAppend(t, journal, manifest, event)
	previous = event.document.sha256()
	event = qualificationTestEvent(t, manifest, 3, previous, "job_admission", new(uint64(0)))
	qualificationTestAppend(t, journal, manifest, event)
	previous = event.document.sha256()
	event = qualificationTestEvent(t, manifest, 4, previous, "job_start", new(uint64(0)))
	qualificationTestAppend(t, journal, manifest, event)
	terminal := qualificationTestOperationalTerminal(t, manifest, 0)
	return journal, qualificationTestTerminalEvent(t, manifest, 5, event.document.sha256(), 0, terminal), terminal
}

func TestQualificationJournalEveryClaimWriteAndSyncFailure(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	for failure := 1; failure <= 10; failure++ {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			journal, _, _ := qualificationLocalTestJournal(t, manifest)
			fault := &qualificationFaultIO{qualificationJournalIO: qualificationOSJournalIO{}, fail: failure}
			journal.config.IO = fault
			_, err := journal.Claim(t.Context(), manifest)
			require.Error(t, err)
			require.NotNil(t, journal.poison)
			_, err = journal.Claim(t.Context(), manifest)
			require.Error(t, err)
			require.Len(t, fault.calls, failure, "poisoned duplicate Claim performs no new writes")
		})
	}
}

func TestQualificationJournalEveryTerminalBarrierFailureRetainsEvidence(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	for failure := 0; failure <= 9; failure++ {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			journal, event, terminal := qualificationTestStartedJournal(t, manifest)
			fault := &qualificationFaultIO{qualificationJournalIO: qualificationOSJournalIO{}, fail: failure}
			journal.config.IO = fault
			ack, err := journal.CommitTerminal(t.Context(), terminal, event)
			if failure == 0 {
				require.NoError(t, err)
				require.NotEmpty(t, ack.canonical)
				require.Equal(t, []string{"write:job_terminal_payload-0000.json", "file:job_terminal_payload-0000.json", "directory",
					"write:event-00005.json", "file:event-00005.json", "directory",
					"write:ack-00005.json", "file:ack-00005.json", "directory"}, fault.calls)
			} else {
				require.Error(t, err)
				require.NotNil(t, journal.poison)
				require.NotEmpty(t, journal.Evidence().pending, "error retains attempted payload/event evidence")
				calls := len(fault.calls)
				_, err = journal.Append(t.Context(), event)
				require.Error(t, err)
				require.Len(t, fault.calls, calls)
				key, err := journal.key(manifest)
				require.NoError(t, err)
				prefix, readErr := journal.Read(t.Context(), key, manifest.context)
				require.False(t, prefix.complete)
				if failure >= 8 {
					// Ack may exist despite a failed sync/return. Historical read
					// cannot authenticate whether a barrier actually completed.
					require.NotEmpty(t, prefix.manifest.document.canonical)
				}

				if failure >= 2 && failure <= 7 {
					require.Error(t, readErr, "payload/event orphan is not complete recovery")
				}
			}
		})
	}
}

func TestQualificationJournalEveryAppendBarrierFailure(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	for failure := 1; failure <= 6; failure++ {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			journal, _, _ := qualificationLocalTestJournal(t, manifest)
			_, err := journal.Claim(t.Context(), manifest)
			require.NoError(t, err)
			fault := &qualificationFaultIO{qualificationJournalIO: qualificationOSJournalIO{}, fail: failure}
			journal.config.IO = fault
			event := qualificationTestEvent(t, manifest, 1, manifest.document.sha256(), "run_admission", nil)
			_, err = journal.Append(t.Context(), event)
			require.Error(t, err)
			require.NotNil(t, journal.poison)
			require.NotEmpty(t, journal.Evidence().pending)
			_, err = journal.Append(t.Context(), event)
			require.Error(t, err)
			require.Len(t, fault.calls, failure)
		})
	}
}

func TestQualificationJournalAppendCASCloseAndCancellation(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	journal, _, _ := qualificationLocalTestJournal(t, manifest)
	_, err := journal.Claim(t.Context(), manifest)
	require.NoError(t, err)
	event := qualificationTestEvent(t, manifest, 1, manifest.document.sha256(), "run_admission", nil)
	qualificationTestAppend(t, journal, manifest, event)
	_, err = journal.Append(t.Context(), event)
	require.Error(t, err, "stale exact predecessor cannot append")
	_, err = qualificationOpenFreshnessSession(t.Context(), journal, qualificationFreshnessConfig{}, nil)
	require.Error(t, err)
	require.NoError(t, journal.Close())
	_, err = journal.Append(t.Context(), event)
	require.Error(t, err)
	_, err = journal.CommitTerminal(t.Context(), qualificationTerminal{}, event)
	require.Error(t, err)
	_, err = journal.Claim(t.Context(), manifest)
	require.Error(t, err)
	key, err := journal.key(manifest)
	require.NoError(t, err)
	_, err = journal.Read(t.Context(), key, manifest.context)
	require.Error(t, err)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	fresh, _, _ := qualificationLocalTestJournal(t, manifest)
	_, err = fresh.Claim(canceled, manifest)
	require.ErrorIs(t, err, context.Canceled)
}

func TestQualificationRegistrySubprocessLock(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("local process lock unsupported")
	}
	if directory := os.Getenv("QUALIFICATION_LOCK_CHILD"); directory != "" {
		root, err := os.OpenRoot(directory)
		require.NoError(t, err)
		defer func() { require.NoError(t, root.Close()) }()
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		lock, err := (qualificationOSJournalIO{}).Lock(ctx, root)
		if os.Getenv("QUALIFICATION_EXPECT_BLOCKED") == "1" {
			require.ErrorIs(t, err, context.DeadlineExceeded)
		} else {
			require.NoError(t, err)
			require.NoError(t, lock.Unlock())
		}
		return
	}
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	lock, err := (qualificationOSJournalIO{}).Lock(t.Context(), root)
	require.NoError(t, err)
	run := func(blocked bool) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQualificationRegistrySubprocessLock$", "-test.timeout=5s")
		cmd.Env = append(os.Environ(), "QUALIFICATION_LOCK_CHILD="+directory)
		if blocked {
			cmd.Env = append(cmd.Env, "QUALIFICATION_EXPECT_BLOCKED=1")
		}
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
	}
	run(true)
	require.NoError(t, lock.Unlock())
	run(false)
}

func TestQualificationRecoveryDoesNotMutateOrRepair(t *testing.T) {
	manifest, _ := qualificationProtocolFixture(t)
	journal, event, terminal := qualificationTestStartedJournal(t, manifest)
	key, err := journal.key(manifest)
	require.NoError(t, err)
	name, err := qualificationReservationName(key)
	require.NoError(t, err)
	child, err := journal.config.Root.OpenRoot(name)
	require.NoError(t, err)
	defer func() { require.NoError(t, child.Close()) }()
	require.NoError(t, child.WriteFile("job_terminal_payload-0000.json", terminal.document.bytes(), 0o600))
	prefix, err := journal.Read(t.Context(), key, manifest.context)
	require.Error(t, err)
	require.False(t, prefix.complete)
	require.NotEmpty(t, prefix.pending)
	before, err := child.ReadFile("job_terminal_payload-0000.json")
	require.NoError(t, err)
	_, err = journal.Read(t.Context(), key, qualificationAdmittedInputContext{})
	require.Error(t, err)
	after, err := child.ReadFile("job_terminal_payload-0000.json")
	require.NoError(t, err)
	require.True(t, slices.Equal(before, after))
	_, err = journal.Append(t.Context(), event)
	require.Error(t, err)
}
