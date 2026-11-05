package assurance

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"
)

type ReviewEvent struct {
	// At records application time; Decision preserves the supplied declaration.
	At       time.Time
	Decision ReviewDecision
}

type reviewDecisionIdentity struct {
	state      ReviewState
	reviewer   string
	declaredAt time.Time
}

// ReviewLifecycle owns immutable label bytes and append-only supplied review
// transitions. Callers must serialize access; it does not authenticate reviewers
// or establish external source freshness.
type ReviewLifecycle struct {
	subject ReviewSubject
	source  string
	events  []ReviewEvent
	seen    map[reviewDecisionIdentity]struct{}
}

func NewReviewLifecycle(subject ReviewSubject, source string, createdAt time.Time) (*ReviewLifecycle, error) {
	if err := validateReviewSubject(subject); err != nil {
		return nil, fmt.Errorf("creating review lifecycle: %w", err)
	}
	if err := validateReviewIdentifier("review source ID", source); err != nil {
		return nil, fmt.Errorf("creating review lifecycle: %w", err)
	}
	if createdAt.IsZero() {
		return nil, errors.New("creating review lifecycle requires a nonzero creation time")
	}
	subject.Labels = bytes.Clone(subject.Labels)
	return &ReviewLifecycle{
		subject: subject, source: source,
		events: []ReviewEvent{{At: createdAt, Decision: ReviewDecision{
			SubjectID: subject.ID, SubjectVersion: subject.Version,
			LabelsDigest: sha256.Sum256(subject.Labels), State: ReviewUnreviewed,
		}}},
		seen: make(map[reviewDecisionIdentity]struct{}),
	}, nil
}

func (l *ReviewLifecycle) Subject() (ReviewSubject, error) {
	if err := l.checkInitialized(); err != nil {
		return ReviewSubject{}, err
	}
	subject := l.subject
	subject.Labels = bytes.Clone(subject.Labels)
	return subject, nil
}

func (l *ReviewLifecycle) Current() (SuppliedReview, error) {
	if err := l.checkInitialized(); err != nil {
		return SuppliedReview{}, err
	}
	decision := l.events[len(l.events)-1].Decision
	return SuppliedReview{SourceID: l.source, Decision: &decision}, nil
}

func (l *ReviewLifecycle) History() ([]ReviewEvent, error) {
	if err := l.checkInitialized(); err != nil {
		return nil, err
	}
	return slices.Clone(l.events), nil
}

func (l *ReviewLifecycle) Submit(now time.Time) error {
	if err := l.checkApplicationTime(now); err != nil {
		return err
	}
	decision := l.events[len(l.events)-1].Decision
	switch decision.State {
	case ReviewUnreviewed, ReviewRejected, ReviewRevoked:
	default:
		return fmt.Errorf("cannot submit labels from review state %q", decision.State)
	}
	decision.State, decision.Reviewer, decision.ReviewedAt = ReviewSubmitted, "", time.Time{}
	l.events = append(l.events, ReviewEvent{At: now, Decision: decision})
	return nil
}

func (l *ReviewLifecycle) Apply(supplied SuppliedReview, acceptance ReviewSourceAcceptance, now time.Time) error {
	if err := l.checkApplicationTime(now); err != nil {
		return err
	}
	if supplied.SourceID != l.source {
		return errors.New("review decision source does not match lifecycle source")
	}
	eligibility, err := CheckAuthorReview(l.subject, supplied, acceptance, now)
	if err != nil {
		return fmt.Errorf("applying review decision: %w", err)
	}
	if !eligibility.Eligible && eligibility.Reason != ReviewNotApproved {
		return fmt.Errorf("applying review decision: %s", eligibility.Reason)
	}
	decision := *supplied.Decision
	last := l.events[len(l.events)-1]
	allowed := (last.Decision.State == ReviewSubmitted && (decision.State == ReviewReviewed || decision.State == ReviewRejected)) ||
		(last.Decision.State == ReviewReviewed && decision.State == ReviewRevoked)
	if !allowed {
		return fmt.Errorf("review transition from %q to %q is not allowed", last.Decision.State, decision.State)
	}
	if err := validateReviewIdentifier("review decision reviewer", decision.Reviewer); err != nil {
		return err
	}
	if decision.ReviewedAt.IsZero() || decision.ReviewedAt.After(now) || decision.ReviewedAt.Before(last.At) {
		return errors.New("review decision time must be nonzero, no earlier than the previous event and no later than application time")
	}
	identity := reviewDecisionIdentity{
		state: decision.State, reviewer: decision.Reviewer,
		declaredAt: decision.ReviewedAt.Round(0).UTC(),
	}
	if _, used := l.seen[identity]; used {
		return errors.New("review decision has already been applied; resubmitted labels require a new explicit decision")
	}
	l.events = append(l.events, ReviewEvent{At: now, Decision: decision})
	l.seen[identity] = struct{}{}
	return nil
}

func (l *ReviewLifecycle) checkInitialized() error {
	if l == nil || len(l.events) == 0 || l.seen == nil {
		return errors.New("review lifecycle is not initialized")
	}
	return nil
}

func (l *ReviewLifecycle) checkApplicationTime(now time.Time) error {
	if err := l.checkInitialized(); err != nil {
		return err
	}
	if now.IsZero() || now.Before(l.events[len(l.events)-1].At) {
		return errors.New("review application time must be nonzero and no earlier than the previous event")
	}
	return nil
}
