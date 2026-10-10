package assurance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"
)

type qualificationAuthorityMaterial struct {
	AuthorityID                  string
	VerificationBindingSHA256    string
	VerifierBindingSHA256        string
	OriginalVerificationMaterial []byte
}
type qualificationClock interface{ Now() time.Time }
type qualificationCurrentSourceReader interface {
	// Recompute the admitted Inputs identity from current original rooted source
	// bytes, keeping its admission-time/acceptance projection. No cached seal.
	Read(context.Context, qualificationDocument) (qualificationDocument, error)
}
type qualificationCurrentAttestor interface {
	Check(context.Context, qualificationDocument) (qualificationDocument, error)
}
type qualificationAuthorityEvidenceVerifier interface {
	Verify(context.Context, qualificationAuthorityMaterial, qualificationDocument, qualificationDocument) error
}
type qualificationFreshnessConfig struct {
	Policy            qualificationDocument
	Clock             qualificationClock
	SourceReader      qualificationCurrentSourceReader
	Attestor          qualificationCurrentAttestor
	Verifier          qualificationAuthorityEvidenceVerifier
	AuthorityMaterial []qualificationAuthorityMaterial
}
type qualificationChallengeSource interface {
	Next(context.Context) ([32]byte, error)
}
type qualificationRandomChallenge struct{}

func (qualificationRandomChallenge) Next(ctx context.Context) ([32]byte, error) {
	var challenge [32]byte
	if err := ctx.Err(); err != nil {
		return challenge, err
	}
	_, err := rand.Read(challenge[:])
	return challenge, errors.Join(err, ctx.Err())
}

type qualificationFreshnessSession struct {
	mu         sync.Mutex
	journal    *qualificationLocalJournal
	manifest   qualificationManifest
	config     qualificationFreshnessConfig
	challenges qualificationChallengeSource
	seen       map[string]bool
	checked    map[string]bool
	last       time.Time
	stopped    error
}
type qualificationReceiptLease struct {
	owner   *qualificationFreshnessSession
	request qualificationDocument
	ack     qualificationDocument
}

func qualificationOpenFreshnessSession(ctx context.Context, journal *qualificationLocalJournal, config qualificationFreshnessConfig, challenges qualificationChallengeSource) (*qualificationFreshnessSession, error) {
	return qualificationOpenFreshnessSessionBounded(ctx, journal, config, challenges, MaxLabelBytes, qualificationTotalLimit, nil)
}
func qualificationOpenFreshnessSessionBounded(ctx context.Context, journal *qualificationLocalJournal, config qualificationFreshnessConfig, challenges qualificationChallengeSource, materialLimit, totalLimit uint64, materializing func()) (*qualificationFreshnessSession, error) {
	if journal == nil || config.Clock == nil || config.SourceReader == nil || config.Attestor == nil || config.Verifier == nil || challenges == nil {
		return nil, errors.New("qualification: explicit source-specific authority/clock/challenge owner required")
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if err := journal.live(); err != nil {
		return nil, err
	}
	if !journal.claimed || journal.freshnessTaken || journal.stopNew || journal.evidence.final != nil || journal.evidence.ack.canonical == "" {
		return nil, errors.New("qualification: uniquely fresh claimed journal freshness slot required")
	}
	journal.freshnessTaken = true
	fail := func(err error) (*qualificationFreshnessSession, error) {
		journal.poison = errors.Join(journal.poison, err)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	policy, err := qualificationParseCurrentnessPolicy(config.Policy.bytes())
	if err != nil {
		return fail(err)
	}
	m, err := qualificationManifestValue(journal.manifest)
	if err != nil {
		return fail(err)
	}
	expected, err := qualificationSeal(m.Inputs.CurrentnessProfile)
	if err != nil || expected.canonical != policy.canonical {
		return fail(errors.New("qualification: caller authority policy differs from admitted manifest"))
	}
	accepted := m.Inputs.CurrentnessProfile
	if len(config.AuthorityMaterial) != len(accepted.Authorities) {
		return fail(errors.New("qualification: accepted source-specific authority material missing"))
	}
	if materialLimit < 1 || materialLimit > MaxLabelBytes || totalLimit < 1 || totalLimit > qualificationTotalLimit {
		return fail(errors.New("qualification: fixed authority material ceilings required"))
	}
	total := uint64(0)
	for _, material := range config.AuthorityMaterial {
		size := uint64(len(material.OriginalVerificationMaterial))
		if size == 0 || size > materialLimit || size > totalLimit-total {
			return fail(errors.New("qualification: authority material aggregate/role limit"))
		}
		total += size
	}
	if materializing != nil {
		materializing()
	}
	materials := make([]qualificationAuthorityMaterial, len(config.AuthorityMaterial))
	seen := map[string]bool{}
	for i, original := range config.AuthorityMaterial {
		if seen[original.AuthorityID] || original.VerifierBindingSHA256 != accepted.VerifierBindingSHA256 ||
			len(original.OriginalVerificationMaterial) == 0 || len(original.OriginalVerificationMaterial) > MaxLabelBytes {
			return fail(errors.New("qualification: caller accepted bounded verification material required"))
		}
		match := false
		for _, authority := range accepted.Authorities {
			match = match || authority.AuthorityID == original.AuthorityID && authority.VerificationBindingSHA256 == original.VerificationBindingSHA256
		}
		if !match {
			return fail(errors.New("qualification: material is not accepted by caller authority policy"))
		}
		seen[original.AuthorityID] = true
		materials[i] = original
		materials[i].OriginalVerificationMaterial = slices.Clone(original.OriginalVerificationMaterial)
	}
	config.Policy, config.AuthorityMaterial = policy, materials
	session := &qualificationFreshnessSession{journal: journal, manifest: journal.manifest, config: config,
		challenges: challenges, seen: map[string]bool{}, checked: map[string]bool{}}
	now := config.Clock.Now().UTC()
	if now.IsZero() || ctx.Err() != nil {
		return fail(errors.Join(errors.New("qualification: valid caller clock required"), ctx.Err()))
	}
	session.last = now
	return session, nil
}
func (session *qualificationFreshnessSession) stop(err error) error {
	session.stopped = errors.Join(session.stopped, err)
	session.journal.mu.Lock()
	session.journal.poison = errors.Join(session.journal.poison, err)
	session.journal.mu.Unlock()
	return err
}
func (session *qualificationFreshnessSession) live(ctx context.Context) error {
	if session.stopped != nil {
		return session.stopped
	}
	if err := ctx.Err(); err != nil {
		return session.stop(err)
	}
	session.journal.mu.Lock()
	err := session.journal.live()
	session.journal.mu.Unlock()
	if err != nil {
		return session.stop(err)
	}
	return nil
}
func (session *qualificationFreshnessSession) now(ctx context.Context) (time.Time, error) {
	if err := session.live(ctx); err != nil {
		return time.Time{}, err
	}
	now := session.config.Clock.Now().UTC()
	if now.IsZero() || now.Before(session.last) {
		return now, session.stop(errors.New("qualification: invocation clock invalid/backward"))
	}
	session.last = now
	if err := ctx.Err(); err != nil {
		return now, session.stop(err)
	}
	return now, nil
}
func (session *qualificationFreshnessSession) Check(ctx context.Context, stage string, ordinal *uint64) (lease *qualificationReceiptLease, returnErr error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if ordinal != nil {
		ordinal = new(*ordinal)
	}
	if err := session.live(ctx); err != nil {
		return nil, err
	}
	lease = &qualificationReceiptLease{owner: session}
	defer func() {
		if returnErr != nil {
			session.journal.mu.Lock()
			if lease.request.canonical != "" {
				session.journal.evidence.pending = append(session.journal.evidence.pending, lease.request)
			}
			if lease.ack.canonical != "" {
				session.journal.evidence.pending = append(session.journal.evidence.pending, lease.ack)
			}
			session.journal.mu.Unlock()
			returnErr = session.stop(returnErr)
		}
	}()
	reservation, err := session.journal.reserveCurrentness(ctx, stage, ordinal)
	if err != nil {
		return lease, err
	}
	defer session.journal.releaseCurrentness(reservation)
	m, err := qualificationManifestValue(session.manifest)
	if err != nil {
		return lease, err
	}
	request := qualificationCurrentnessRequest{Kind: "waza.qualification-currentness-request", Version: qualificationVersion,
		InvocationID: m.InvocationID, ContractSHA256: m.Inputs.Association.ContractSHA256, CID: m.Inputs.Association.CID,
		ArmID: m.Inputs.Association.ArmID, ManifestSHA256: session.manifest.document.sha256(), InputsSHA256: m.InputsSHA256,
		CurrentnessProfileSHA256: session.config.Policy.sha256(), Stage: stage}
	var job qualificationJobWire
	switch stage {
	case "before_job":
		if ordinal == nil {
			return lease, errors.New("qualification: exact job ordinal required")
		}
		var digest qualificationDocument
		job, digest, err = qualificationJobAt(session.manifest, *ordinal)
		if err != nil {
			return lease, err
		}
		request.Ordinal, request.JobSHA256 = new(*ordinal), new(digest.sha256())
	case "after_cleanup", "before_decision":
		if ordinal != nil {
			return lease, errors.New("qualification: non-job currentness has null ordinal")
		}
	default:
		return lease, errors.New("qualification: explicit currentness stage required")
	}
	checkKey := stage
	if ordinal != nil {
		checkKey += ":" + *request.JobSHA256
	}
	if session.checked[checkKey] {
		return lease, errors.New("qualification: failed or consumed admission receipt cannot replay")
	}
	session.checked[checkKey] = true
	if _, err := session.now(ctx); err != nil {
		return lease, err
	}
	challenge, err := session.challenges.Next(ctx)
	if err != nil || ctx.Err() != nil {
		return lease, errors.Join(err, ctx.Err())
	}
	if _, err := session.now(ctx); err != nil {
		return lease, err
	}
	request.Challenge = hex.EncodeToString(challenge[:])
	if session.seen[request.Challenge] {
		return lease, errors.New("qualification: invocation challenge repeated")
	}
	session.seen[request.Challenge] = true
	lease.request, err = qualificationSeal(request)
	if err != nil {
		return lease, err
	}
	timeout := time.Duration(m.Inputs.CurrentnessProfile.CheckTimeoutSeconds) * time.Second
	callbackCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	current, err := session.config.SourceReader.Read(callbackCtx, lease.request)
	if err != nil || callbackCtx.Err() != nil || current.canonical != session.manifest.context.input.canonical {
		return lease, errors.Join(errors.New("qualification: original current source/review inventory changed or unavailable"), err, callbackCtx.Err())
	}
	if _, err := session.now(callbackCtx); err != nil {
		return lease, err
	}
	ack, err := session.config.Attestor.Check(callbackCtx, lease.request)
	lease.ack = ack
	if err != nil || callbackCtx.Err() != nil {
		return lease, errors.Join(err, callbackCtx.Err())
	}
	if _, err := session.now(callbackCtx); err != nil {
		return lease, err
	}
	if err := session.verify(callbackCtx, lease); err != nil {
		return lease, err
	}
	// The receipt event is synchronously acknowledged before lease success.
	prefix := session.journal.Evidence()
	head, err := qualificationDecode[qualificationPrefixWire](prefix.document.bytes(), qualificationDocumentLimit)
	if err != nil {
		return lease, err
	}
	ackWire, err := qualificationDecode[qualificationCurrentnessAcknowledgment](lease.ack.bytes(), qualificationDocumentLimit)
	if err != nil {
		return lease, err
	}
	event := qualificationEventWire{Kind: "waza.qualification-event", Version: qualificationVersion, InvocationID: m.InvocationID,
		ManifestSHA256: session.manifest.document.sha256(), ContractSHA256: request.ContractSHA256, CID: request.CID, ArmID: request.ArmID,
		Sequence: head.LastSequence + 1, PreviousSHA256: head.LastEventSHA256, Type: "currentness",
		CurrentnessRequest: &request, CurrentnessAcknowledgment: &ackWire}
	if ordinal != nil {
		event.Ordinal, event.JobSHA256, event.Selector = new(*ordinal), request.JobSHA256, new(job.Selector)
	}
	doc, err := qualificationSeal(event)
	if err != nil {
		return lease, err
	}
	parsed, err := qualificationParseEvent(doc.bytes(), session.manifest)
	if err != nil {
		return lease, err
	}
	appendCtx := context.WithValue(callbackCtx, qualificationStageReservationKey{}, reservation)
	if _, err := session.journal.Append(appendCtx, parsed); err != nil {
		return lease, err
	}
	return lease, session.verify(callbackCtx, lease)
}
func (session *qualificationFreshnessSession) verify(ctx context.Context, lease *qualificationReceiptLease) error {
	if err := session.live(ctx); err != nil {
		return err
	}
	if lease.owner != session || lease.request.canonical == "" || lease.ack.canonical == "" {
		return session.stop(errors.New("qualification: receipt lease ownership invalid"))
	}
	ack, err := qualificationParseCurrentnessAcknowledgment(lease.ack.bytes())
	if err != nil {
		return session.stop(err)
	}
	value, err := qualificationDecode[qualificationCurrentnessAcknowledgment](ack.bytes(), qualificationDocumentLimit)
	request, requestErr := qualificationDecode[qualificationCurrentnessRequest](lease.request.bytes(), qualificationDocumentLimit)
	if err != nil || requestErr != nil || value.RequestSHA256 != lease.request.sha256() ||
		value.CurrentnessProfileSHA256 != session.config.Policy.sha256() || value.Stage != request.Stage ||
		!value.Current || !value.AssociationValid {
		return session.stop(errors.New("qualification: current receipt exact identity or decision rejected"))
	}
	var material *qualificationAuthorityMaterial
	for _, candidate := range session.config.AuthorityMaterial {
		if candidate.AuthorityID == value.AuthorityID {
			copy := candidate
			copy.OriginalVerificationMaterial = slices.Clone(candidate.OriginalVerificationMaterial)
			material = &copy
		}
	}
	if material == nil {
		return session.stop(errors.New("qualification: issuer not caller accepted"))
	}
	if _, err := session.now(ctx); err != nil {
		return err
	}
	current, err := session.config.SourceReader.Read(ctx, lease.request)
	if err != nil || ctx.Err() != nil || current.canonical != session.manifest.context.input.canonical {
		return session.stop(errors.Join(errors.New("qualification: current original source inventory differs"), err, ctx.Err()))
	}
	if _, err := session.now(ctx); err != nil {
		return err
	}
	if err := session.config.Verifier.Verify(ctx, *material, lease.request, lease.ack); err != nil || ctx.Err() != nil {
		return session.stop(errors.Join(errors.New("qualification: source-specific authority proof rejected"), err, ctx.Err()))
	}
	now, err := session.now(ctx)
	if err != nil {
		return err
	}
	policy, err := qualificationDecode[qualificationCurrentnessPolicy](session.config.Policy.bytes(), qualificationDocumentLimit)
	if err != nil {
		return session.stop(err)
	}
	checked, checkedErr := time.Parse(time.RFC3339Nano, value.CheckedAt)
	until, untilErr := time.Parse(time.RFC3339Nano, value.ValidUntil)
	if checkedErr != nil || untilErr != nil || !now.Before(until) ||
		checked.After(now.Add(time.Duration(policy.MaxClockSkewSeconds)*time.Second)) ||
		now.Sub(checked) > time.Duration(policy.MaxReceiptAgeSeconds)*time.Second ||
		until.Sub(checked) > time.Duration(policy.MaxValiditySeconds)*time.Second {
		return session.stop(errors.New("qualification: receipt expired, future, stale or excessive validity"))
	}
	return nil
}
func (lease *qualificationReceiptLease) Revalidate(ctx context.Context) error {
	if lease == nil || lease.owner == nil {
		return errors.New("qualification: owned receipt lease required")
	}
	session := lease.owner
	session.mu.Lock()
	defer session.mu.Unlock()
	m, err := qualificationManifestValue(session.manifest)
	if err != nil {
		return session.stop(err)
	}
	callbackCtx, cancel := context.WithTimeout(ctx, time.Duration(m.Inputs.CurrentnessProfile.CheckTimeoutSeconds)*time.Second)
	defer cancel()
	return session.verify(callbackCtx, lease)
}
