package assurance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qualificationTestClock struct{ value time.Time }

func (clock *qualificationTestClock) Now() time.Time { return clock.value }

type qualificationTestChallenges struct {
	value  byte
	repeat bool
}

func (source *qualificationTestChallenges) Next(ctx context.Context) ([32]byte, error) {
	var result [32]byte
	if !source.repeat {
		source.value++
	}
	result[0] = source.value
	return result, ctx.Err()
}

type qualificationActualTestSourceReader struct {
	t       *testing.T
	fixture qualificationSourceFixture
	input   qualificationInputs
	fail    bool
}

func (reader *qualificationActualTestSourceReader) Read(ctx context.Context, _ qualificationDocument) (qualificationDocument, error) {
	if reader.fail {
		return qualificationDocument{}, errors.New("source reader failed")
	}
	sources, err := qualificationAcquireSources(ctx, reader.fixture.reads)
	if err != nil {
		return qualificationDocument{}, err
	}
	now, err := qualificationAdmissionTime(reader.input)
	if err != nil {
		return qualificationDocument{}, err
	}
	association := marshalReferenceTest(reader.t, reader.input.Association)
	arm := marshalReferenceTest(reader.t, reader.input.ArmInput)
	policy := marshalReferenceTest(reader.t, reader.input.CurrentnessProfile)
	backend := marshalReferenceTest(reader.t, reader.input.BackendProfile)
	return qualificationBuildInputs(sources, association, arm, reader.fixture.request.Acceptance, now, policy, backend)
}

type qualificationProofTestAttestor struct {
	clock  *qualificationTestClock
	key    []byte
	mutate func(*qualificationCurrentnessAcknowledgment)
	err    error
}

func qualificationTestProof(key []byte, request qualificationDocument) string {
	proof := hmac.New(sha256.New, key)
	proof.Write(request.bytes())
	return base64.StdEncoding.EncodeToString(proof.Sum(nil))
}
func (attestor *qualificationProofTestAttestor) Check(ctx context.Context, request qualificationDocument) (qualificationDocument, error) {
	if attestor.err != nil {
		return qualificationDocument{}, attestor.err
	}
	value, err := qualificationDecode[qualificationCurrentnessRequest](request.bytes(), qualificationDocumentLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	now := attestor.clock.Now().UTC()
	ack := qualificationCurrentnessAcknowledgment{Kind: "waza.qualification-currentness-acknowledgment", Version: qualificationVersion,
		RequestSHA256: request.sha256(), CurrentnessProfileSHA256: value.CurrentnessProfileSHA256, Stage: value.Stage,
		Current: true, AssociationValid: true, CheckedAt: now.Format(time.RFC3339Nano),
		ValidUntil: now.Add(time.Minute).Format(time.RFC3339Nano), AuthorityID: "synthetic-authority", ReceiptToken: "test-owned-proof",
		AttestationEvidenceBase64: qualificationTestProof(attestor.key, request)}
	if attestor.mutate != nil {
		attestor.mutate(&ack)
	}
	document, err := qualificationSeal(ack)
	return document, errors.Join(err, ctx.Err())
}

type qualificationProofTestVerifier struct {
	reject bool
	calls  int
}

func (verifier *qualificationProofTestVerifier) Verify(ctx context.Context, material qualificationAuthorityMaterial, request, ack qualificationDocument) error {
	verifier.calls++
	value, err := qualificationDecode[qualificationCurrentnessAcknowledgment](ack.bytes(), qualificationDocumentLimit)
	if err != nil {
		return err
	}
	received, err := base64.StdEncoding.Strict().DecodeString(value.AttestationEvidenceBase64)
	if err != nil {
		return err
	}
	expected, err := base64.StdEncoding.Strict().DecodeString(qualificationTestProof(material.OriginalVerificationMaterial, request))
	if err != nil {
		return err
	}
	if verifier.reject || !hmac.Equal(received, expected) {
		return errors.New("test source-specific proof verification rejected")
	}
	return ctx.Err()
}
func qualificationFreshnessFixture(t *testing.T) (*qualificationFreshnessSession, *qualificationLocalJournal, qualificationSourceFixture, *qualificationTestClock, *qualificationProofTestAttestor, *qualificationProofTestVerifier) {
	return qualificationFreshnessFixtureSetup(t, true)
}

func qualificationFreshnessFixtureSetup(t *testing.T, admitRun bool) (*qualificationFreshnessSession, *qualificationLocalJournal, qualificationSourceFixture, *qualificationTestClock, *qualificationProofTestAttestor, *qualificationProofTestVerifier) {
	t.Helper()
	manifest, fixture := qualificationProtocolFixture(t)
	journal, _, _ := qualificationLocalTestJournal(t, manifest)
	_, err := journal.Claim(t.Context(), manifest)
	require.NoError(t, err)
	if admitRun {
		event := qualificationTestEvent(t, manifest, 1, manifest.document.sha256(), "run_admission", nil)
		qualificationTestAppend(t, journal, manifest, event)
	}
	m, err := qualificationManifestValue(manifest)
	require.NoError(t, err)
	policy, err := qualificationSeal(m.Inputs.CurrentnessProfile)
	require.NoError(t, err)
	clock := &qualificationTestClock{fixture.request.Now.UTC()}
	key := []byte("test-only source-specific proof material; no production authority")
	attestor := &qualificationProofTestAttestor{clock: clock, key: key}
	verifier := &qualificationProofTestVerifier{}
	config := qualificationFreshnessConfig{Policy: policy, Clock: clock,
		SourceReader: &qualificationActualTestSourceReader{t: t, fixture: fixture, input: m.Inputs}, Attestor: attestor, Verifier: verifier,
		AuthorityMaterial: []qualificationAuthorityMaterial{{AuthorityID: "synthetic-authority",
			VerificationBindingSHA256: m.Inputs.CurrentnessProfile.Authorities[0].VerificationBindingSHA256,
			VerifierBindingSHA256:     m.Inputs.CurrentnessProfile.VerifierBindingSHA256, OriginalVerificationMaterial: key}}}
	session, err := qualificationOpenFreshnessSession(t.Context(), journal, config, &qualificationTestChallenges{})
	require.NoError(t, err)
	// Caller mutation of accepted material after constructor cannot alter custody.
	config.AuthorityMaterial[0].OriginalVerificationMaterial[0] = '!'
	attestor.key = append([]byte(nil), session.config.AuthorityMaterial[0].OriginalVerificationMaterial...)
	return session, journal, fixture, clock, attestor, verifier
}

func TestQualificationFreshnessCurrentSourcesProofAndOneShot(t *testing.T) {
	session, journal, _, clock, _, verifier := qualificationFreshnessFixture(t)
	lease, err := session.Check(t.Context(), "before_job", new(uint64(0)))
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.GreaterOrEqual(t, verifier.calls, 2)
	require.Len(t, journal.Evidence().records, 2, "receipt event durably acknowledged")
	require.NoError(t, lease.Revalidate(t.Context()))
	clock.value = clock.value.Add(time.Second)
	require.NoError(t, lease.Revalidate(t.Context()))
	_, err = qualificationOpenFreshnessSession(t.Context(), journal, session.config, &qualificationTestChallenges{})
	require.Error(t, err)
	_, err = session.Check(t.Context(), "before_job", new(uint64(0)))
	require.Error(t, err, "same job admission receipt cannot replay")
	require.Error(t, lease.Revalidate(t.Context()), "one-shot failure stops all old leases")
}

func TestQualificationFreshnessNegativeProofTimeAndIdentityStop(t *testing.T) {
	for _, failure := range []string{"current false", "association false", "wrong request", "wrong profile", "wrong stage",
		"wrong authority", "expired", "future", "excessive validity", "proof", "source changed", "reader", "attestor",
		"wrong ordinal", "wrong stage ordinal", "canceled", "clock backward", "nil clock"} {
		t.Run(failure, func(t *testing.T) {
			session, journal, fixture, clock, attestor, verifier := qualificationFreshnessFixture(t)
			ctx := t.Context()
			stage, ordinal := "before_job", new(uint64(0))
			switch failure {
			case "current false":
				attestor.mutate = func(a *qualificationCurrentnessAcknowledgment) { a.Current = false }
			case "association false":
				attestor.mutate = func(a *qualificationCurrentnessAcknowledgment) { a.AssociationValid = false }
			case "wrong request":
				attestor.mutate = func(a *qualificationCurrentnessAcknowledgment) { a.RequestSHA256 = strings.Repeat("b", 64) }
			case "wrong profile":
				attestor.mutate = func(a *qualificationCurrentnessAcknowledgment) { a.CurrentnessProfileSHA256 = strings.Repeat("b", 64) }
			case "wrong stage":
				attestor.mutate = func(a *qualificationCurrentnessAcknowledgment) { a.Stage = "before_decision" }
			case "wrong authority":
				attestor.mutate = func(a *qualificationCurrentnessAcknowledgment) { a.AuthorityID = "unapproved" }
			case "expired":
				attestor.mutate = func(a *qualificationCurrentnessAcknowledgment) {
					a.CheckedAt = clock.value.Add(-time.Minute).Format(time.RFC3339Nano)
					a.ValidUntil = clock.value.Format(time.RFC3339Nano)
				}
			case "future":
				attestor.mutate = func(a *qualificationCurrentnessAcknowledgment) {
					a.CheckedAt = clock.value.Add(2 * time.Second).Format(time.RFC3339Nano)
					a.ValidUntil = clock.value.Add(time.Minute).Format(time.RFC3339Nano)
				}
			case "excessive validity":
				attestor.mutate = func(a *qualificationCurrentnessAcknowledgment) {
					a.ValidUntil = clock.value.Add(61 * time.Second).Format(time.RFC3339Nano)
				}
			case "proof":
				verifier.reject = true
			case "source changed":
				require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "rubric.md"), []byte("changed original"), 0o600))
			case "reader":
				reader, ok := session.config.SourceReader.(*qualificationActualTestSourceReader)
				require.True(t, ok)
				reader.fail = true
			case "attestor":
				attestor.err = errors.New("attestor unavailable")
			case "wrong ordinal":
				ordinal = new(uint64(4))
			case "wrong stage ordinal":
				stage = "before_decision"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "clock backward":
				clock.value = clock.value.Add(-time.Nanosecond)
			case "nil clock":
				clock.value = time.Time{}
			}
			lease, err := session.Check(ctx, stage, ordinal)
			require.Error(t, err)
			require.NotNil(t, journal.poison)
			if lease != nil {
				require.Error(t, lease.Revalidate(t.Context()))
			}
			_, err = session.Check(t.Context(), "before_job", new(uint64(1)))
			require.Error(t, err, "negative/failed proof cannot start new job")
		})
	}
}

func TestQualificationFreshnessAcrossLeaseClockAndUniqueChallenge(t *testing.T) {
	session, journal, _, clock, _, _ := qualificationFreshnessFixture(t)
	session.challenges = &qualificationTestChallenges{repeat: true}
	first, err := session.Check(t.Context(), "before_job", new(uint64(0)))
	require.NoError(t, err)
	head, err := qualificationDecode[qualificationPrefixWire](journal.Evidence().document.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	event := qualificationTestEvent(t, session.manifest, head.LastSequence+1, head.LastEventSHA256, "job_admission", new(uint64(0)))
	qualificationTestAppend(t, journal, session.manifest, event)
	start := qualificationTestEvent(t, session.manifest, head.LastSequence+2, event.document.sha256(), "job_start", new(uint64(0)))
	qualificationTestAppend(t, journal, session.manifest, start)
	terminal := qualificationTestObservedTerminal(t, session.manifest, 0)
	end := qualificationTestTerminalEvent(t, session.manifest, head.LastSequence+3, start.document.sha256(), 0, terminal)
	_, err = journal.CommitTerminal(t.Context(), terminal, end)
	require.NoError(t, err)
	clock.value = clock.value.Add(time.Second)
	require.NoError(t, first.Revalidate(t.Context()))
	_, err = session.Check(t.Context(), "before_job", new(uint64(1)))
	require.ErrorContains(t, err, "challenge repeated")
	require.Error(t, first.Revalidate(t.Context()))
}

func TestQualificationFreshnessLateExpiryAndClose(t *testing.T) {
	for _, failure := range []string{"expiry", "rollback", "close"} {
		t.Run(failure, func(t *testing.T) {
			session, journal, _, clock, _, _ := qualificationFreshnessFixture(t)
			lease, err := session.Check(t.Context(), "before_job", new(uint64(0)))
			require.NoError(t, err)
			switch failure {
			case "expiry":
				clock.value = clock.value.Add(time.Minute)
			case "rollback":
				clock.value = clock.value.Add(-time.Nanosecond)
			case "close":
				require.NoError(t, journal.Close())
			}
			require.Error(t, lease.Revalidate(t.Context()))
			_, err = session.Check(t.Context(), "before_job", new(uint64(1)))
			require.Error(t, err)
			require.Len(t, journal.Evidence().records, 2, "historical receipt survives currentness failure")
		})
	}
}
