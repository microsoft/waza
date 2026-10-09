package assurance

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func syntheticReviewInputs() (ReviewSubject, SuppliedReview, ReviewSourceAcceptance, time.Time) {
	subject := ReviewSubject{ID: "synthetic-labels", Version: "test-v1", Labels: []byte("synthetic labels\n")}
	now := time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)
	supplied := SuppliedReview{SourceID: "synthetic-review-source", Decision: &ReviewDecision{
		SubjectID: subject.ID, SubjectVersion: subject.Version,
		LabelsDigest: sha256.Sum256(subject.Labels), State: ReviewReviewed,
		Reviewer: "synthetic-review-declaration", ReviewedAt: now.Add(-time.Hour),
	}}
	acceptance := ReviewSourceAcceptance{SourceID: supplied.SourceID, AcceptCurrentDecision: true}
	return subject, supplied, acceptance, now
}

func TestAuthorReviewExplicitSourceAcceptance(t *testing.T) {
	for _, candidate := range []struct {
		name   string
		change func(*SuppliedReview, *ReviewSourceAcceptance)
		want   ReviewEligibility
	}{
		{"accepted-synthetic-declaration", func(*SuppliedReview, *ReviewSourceAcceptance) {}, ReviewEligibility{Eligible: true, State: ReviewReviewed}},
		{"not-accepted", func(_ *SuppliedReview, a *ReviewSourceAcceptance) { a.AcceptCurrentDecision = false }, ReviewEligibility{Reason: ReviewSourceNotAccepted}},
		{"different-source", func(_ *SuppliedReview, a *ReviewSourceAcceptance) { a.SourceID = "different-source" }, ReviewEligibility{Reason: ReviewSourceNotAccepted}},
		{"missing-decision", func(s *SuppliedReview, _ *ReviewSourceAcceptance) { s.Decision = nil }, ReviewEligibility{State: ReviewUnreviewed, Reason: ReviewDecisionMissing}},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			subject, supplied, acceptance, now := syntheticReviewInputs()
			candidate.change(&supplied, &acceptance)
			result, err := CheckAuthorReview(subject, supplied, acceptance, now)
			require.NoError(t, err)
			require.Equal(t, candidate.want, result)
		})
	}
	require.False(t, ReviewEligibility{}.Eligible)
}

func TestAuthorReviewNonReviewedStates(t *testing.T) {
	for _, state := range []ReviewState{ReviewUnreviewed, ReviewSubmitted, ReviewRejected, ReviewRevoked} {
		t.Run(string(state), func(t *testing.T) {
			subject, supplied, acceptance, now := syntheticReviewInputs()
			supplied.Decision.State = state
			supplied.Decision.Reviewer, supplied.Decision.ReviewedAt = "", time.Time{}
			result, err := CheckAuthorReview(subject, supplied, acceptance, now)
			require.NoError(t, err)
			require.Equal(t, ReviewEligibility{State: state, Reason: ReviewNotApproved}, result)
		})
	}
}

func TestAuthorReviewSubjectBinding(t *testing.T) {
	for _, candidate := range []struct {
		name   string
		change func(*ReviewSubject)
		reason ReviewReason
	}{
		{"different-id-same-bytes", func(s *ReviewSubject) { s.ID = "different-labels" }, ReviewIdentityMismatch},
		{"different-version-same-bytes", func(s *ReviewSubject) { s.Version = "test-v2" }, ReviewVersionMismatch},
		{"one-byte-change", func(s *ReviewSubject) { s.Labels[0] = 'X' }, ReviewDigestMismatch},
		{"whitespace-change", func(s *ReviewSubject) { s.Labels = append(s.Labels, ' ') }, ReviewDigestMismatch},
		{"newline-change", func(s *ReviewSubject) { s.Labels = []byte("synthetic labels\r\n") }, ReviewDigestMismatch},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			subject, supplied, acceptance, now := syntheticReviewInputs()
			candidate.change(&subject)
			result, err := CheckAuthorReview(subject, supplied, acceptance, now)
			require.NoError(t, err)
			require.False(t, result.Eligible)
			require.Equal(t, candidate.reason, result.Reason)
		})
	}
}

func TestAuthorReviewMalformedDeclarations(t *testing.T) {
	for _, candidate := range []struct {
		name   string
		change func(*ReviewSubject, *SuppliedReview, *ReviewSourceAcceptance, *time.Time)
	}{
		{"empty-id", func(s *ReviewSubject, _ *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) { s.ID = "" }},
		{"padded-id", func(s *ReviewSubject, _ *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) { s.ID += " " }},
		{"empty-version", func(s *ReviewSubject, _ *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) { s.Version = "" }},
		{"padded-version", func(s *ReviewSubject, _ *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) { s.Version += " " }},
		{"nil-labels", func(s *ReviewSubject, _ *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) { s.Labels = nil }},
		{"empty-source", func(_ *ReviewSubject, s *SuppliedReview, a *ReviewSourceAcceptance, _ *time.Time) {
			s.SourceID, a.SourceID = "", ""
		}},
		{"padded-source", func(_ *ReviewSubject, s *SuppliedReview, a *ReviewSourceAcceptance, _ *time.Time) {
			s.SourceID, a.SourceID = " source", " source"
		}},
		{"empty-state", func(_ *ReviewSubject, s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) {
			s.Decision.State = ""
		}},
		{"unknown-state", func(_ *ReviewSubject, s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) {
			s.Decision.State = "unknown"
		}},
		{"empty-reviewed-id", func(_ *ReviewSubject, s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) {
			s.Decision.SubjectID = ""
		}},
		{"padded-reviewed-id", func(_ *ReviewSubject, s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) {
			s.Decision.SubjectID += " "
		}},
		{"empty-reviewed-version", func(_ *ReviewSubject, s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) {
			s.Decision.SubjectVersion = ""
		}},
		{"padded-reviewed-version", func(_ *ReviewSubject, s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) {
			s.Decision.SubjectVersion += " "
		}},
		{"empty-reviewer", func(_ *ReviewSubject, s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) {
			s.Decision.Reviewer = ""
		}},
		{"padded-reviewer", func(_ *ReviewSubject, s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) {
			s.Decision.Reviewer += " "
		}},
		{"missing-review-time", func(_ *ReviewSubject, s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) {
			s.Decision.ReviewedAt = time.Time{}
		}},
		{"future-review-time", func(_ *ReviewSubject, s *SuppliedReview, _ *ReviewSourceAcceptance, n *time.Time) {
			s.Decision.ReviewedAt = n.Add(time.Second)
		}},
		{"missing-clock", func(_ *ReviewSubject, _ *SuppliedReview, _ *ReviewSourceAcceptance, n *time.Time) { *n = time.Time{} }},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			subject, supplied, acceptance, now := syntheticReviewInputs()
			candidate.change(&subject, &supplied, &acceptance, &now)
			result, err := CheckAuthorReview(subject, supplied, acceptance, now)
			require.Error(t, err)
			require.False(t, result.Eligible)
			require.Equal(t, ReviewInvalid, result.Reason)
		})
	}
}

func TestAuthorReviewRechecksBytesAndCurrentDecision(t *testing.T) {
	subject, supplied, acceptance, now := syntheticReviewInputs()
	result, err := CheckAuthorReview(subject, supplied, acceptance, now)
	require.NoError(t, err)
	require.True(t, result.Eligible)
	subject.Labels[0] = 'X'
	result, err = CheckAuthorReview(subject, supplied, acceptance, now)
	require.NoError(t, err)
	require.Equal(t, ReviewDigestMismatch, result.Reason)
	require.False(t, result.Eligible)
	subject.Labels[0] = 's'
	supplied.Decision.State = ReviewRevoked
	result, err = CheckAuthorReview(subject, supplied, acceptance, now)
	require.NoError(t, err)
	require.Equal(t, ReviewRevoked, result.State)
	require.False(t, result.Eligible)
}
