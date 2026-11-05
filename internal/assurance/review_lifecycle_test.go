package assurance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func syntheticLifecycle(t *testing.T, state ReviewState) (*ReviewLifecycle, ReviewSourceAcceptance, time.Time) {
	t.Helper()
	subject, supplied, acceptance, now := syntheticReviewInputs()
	lifecycle, err := NewReviewLifecycle(subject, supplied.SourceID, now)
	require.NoError(t, err)
	if state == ReviewUnreviewed {
		return lifecycle, acceptance, now
	}
	now = now.Add(time.Hour)
	require.NoError(t, lifecycle.Submit(now))
	if state == ReviewSubmitted {
		return lifecycle, acceptance, now
	}
	now = now.Add(time.Hour)
	supplied.Decision.State, supplied.Decision.ReviewedAt = state, now
	if state == ReviewRevoked {
		supplied.Decision.State = ReviewReviewed
	}
	require.NoError(t, lifecycle.Apply(supplied, acceptance, now))
	if state == ReviewRevoked {
		now = now.Add(time.Hour)
		supplied.Decision.State, supplied.Decision.ReviewedAt = ReviewRevoked, now
		require.NoError(t, lifecycle.Apply(supplied, acceptance, now))
	}
	return lifecycle, acceptance, now
}

func syntheticLifecycleDecision(t *testing.T, lifecycle *ReviewLifecycle, state ReviewState, at time.Time) SuppliedReview {
	t.Helper()
	supplied, err := lifecycle.Current()
	require.NoError(t, err)
	supplied.Decision.State = state
	supplied.Decision.Reviewer = "synthetic-review-declaration"
	supplied.Decision.ReviewedAt = at
	return supplied
}

func TestReviewLifecycleTransitions(t *testing.T) {
	states := []ReviewState{ReviewUnreviewed, ReviewSubmitted, ReviewReviewed, ReviewRejected, ReviewRevoked}
	for _, from := range states {
		for _, to := range states {
			t.Run(string(from)+"/"+string(to), func(t *testing.T) {
				lifecycle, acceptance, now := syntheticLifecycle(t, from)
				before, err := lifecycle.History()
				require.NoError(t, err)
				now = now.Add(time.Hour)
				allowed := (to == ReviewSubmitted && (from == ReviewUnreviewed || from == ReviewRejected || from == ReviewRevoked)) ||
					(from == ReviewSubmitted && (to == ReviewReviewed || to == ReviewRejected)) ||
					(from == ReviewReviewed && to == ReviewRevoked)
				if to == ReviewSubmitted {
					err = lifecycle.Submit(now)
				} else {
					err = lifecycle.Apply(syntheticLifecycleDecision(t, lifecycle, to, now), acceptance, now)
				}
				after, historyErr := lifecycle.History()
				require.NoError(t, historyErr)
				if !allowed {
					require.Error(t, err)
					require.Equal(t, before, after)
					return
				}
				require.NoError(t, err)
				require.Len(t, after, len(before)+1)
				require.Equal(t, before, after[:len(before)])
				require.Equal(t, to, after[len(after)-1].Decision.State)
				if to == ReviewSubmitted {
					require.Empty(t, after[len(after)-1].Decision.Reviewer)
					require.True(t, after[len(after)-1].Decision.ReviewedAt.IsZero())
				}
			})
		}
	}
}

func TestReviewLifecycleRejectedInputsAreAtomic(t *testing.T) {
	for _, candidate := range []struct {
		name   string
		change func(*SuppliedReview, *ReviewSourceAcceptance, *time.Time)
	}{
		{"unaccepted-source", func(_ *SuppliedReview, a *ReviewSourceAcceptance, _ *time.Time) { a.AcceptCurrentDecision = false }},
		{"wrong-source", func(s *SuppliedReview, a *ReviewSourceAcceptance, _ *time.Time) {
			s.SourceID, a.SourceID = "other", "other"
		}},
		{"wrong-acceptance", func(_ *SuppliedReview, a *ReviewSourceAcceptance, _ *time.Time) { a.SourceID = "other" }},
		{"missing-decision", func(s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) { s.Decision = nil }},
		{"wrong-id", func(s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) { s.Decision.SubjectID = "other" }},
		{"wrong-version", func(s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) {
			s.Decision.SubjectVersion = "test-v2"
		}},
		{"wrong-digest", func(s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) { s.Decision.LabelsDigest[0] ^= 1 }},
		{"unknown-state", func(s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) { s.Decision.State = "unknown" }},
		{"empty-reviewer", func(s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) { s.Decision.Reviewer = "" }},
		{"zero-review-time", func(s *SuppliedReview, _ *ReviewSourceAcceptance, _ *time.Time) { s.Decision.ReviewedAt = time.Time{} }},
		{"future-review-time", func(s *SuppliedReview, _ *ReviewSourceAcceptance, n *time.Time) {
			s.Decision.ReviewedAt = n.Add(time.Second)
		}},
		{"review-before-submission", func(s *SuppliedReview, _ *ReviewSourceAcceptance, n *time.Time) {
			s.Decision.ReviewedAt = n.Add(-2 * time.Hour)
		}},
		{"zero-application-time", func(_ *SuppliedReview, _ *ReviewSourceAcceptance, n *time.Time) { *n = time.Time{} }},
		{"backward-application-time", func(_ *SuppliedReview, _ *ReviewSourceAcceptance, n *time.Time) { *n = n.Add(-2 * time.Hour) }},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			lifecycle, acceptance, now := syntheticLifecycle(t, ReviewSubmitted)
			subjectBefore, err := lifecycle.Subject()
			require.NoError(t, err)
			currentBefore, err := lifecycle.Current()
			require.NoError(t, err)
			historyBefore, err := lifecycle.History()
			require.NoError(t, err)
			now = now.Add(time.Hour)
			supplied := syntheticLifecycleDecision(t, lifecycle, ReviewReviewed, now)
			candidate.change(&supplied, &acceptance, &now)
			require.Error(t, lifecycle.Apply(supplied, acceptance, now))
			subjectAfter, err := lifecycle.Subject()
			require.NoError(t, err)
			currentAfter, err := lifecycle.Current()
			require.NoError(t, err)
			historyAfter, err := lifecycle.History()
			require.NoError(t, err)
			require.Equal(t, subjectBefore, subjectAfter)
			require.Equal(t, currentBefore, currentAfter)
			require.Equal(t, historyBefore, historyAfter)
		})
	}
}

func TestReviewLifecycleRejectsReplayAcrossSubmissionRounds(t *testing.T) {
	for _, state := range []ReviewState{ReviewReviewed, ReviewRejected} {
		t.Run(string(state), func(t *testing.T) {
			lifecycle, acceptance, now := syntheticLifecycle(t, ReviewSubmitted)
			supplied := syntheticLifecycleDecision(t, lifecycle, state, now)
			require.NoError(t, lifecycle.Apply(supplied, acceptance, now))
			if state == ReviewReviewed {
				require.NoError(t, lifecycle.Apply(syntheticLifecycleDecision(t, lifecycle, ReviewRevoked, now), acceptance, now))
			}
			require.NoError(t, lifecycle.Submit(now))
			before, err := lifecycle.History()
			require.NoError(t, err)
			supplied.Decision.ReviewedAt = supplied.Decision.ReviewedAt.In(time.FixedZone("synthetic-offset", 3600))
			require.ErrorContains(t, lifecycle.Apply(supplied, acceptance, now), "already been applied")
			after, err := lifecycle.History()
			require.NoError(t, err)
			require.Equal(t, before, after)
			now = now.Add(time.Second)
			supplied.Decision.ReviewedAt = now
			require.NoError(t, lifecycle.Apply(supplied, acceptance, now))
		})
	}
}

func TestReviewLifecycleOwnsImmutableInputsAndSnapshots(t *testing.T) {
	subject, supplied, acceptance, now := syntheticReviewInputs()
	lifecycle, err := NewReviewLifecycle(subject, supplied.SourceID, now)
	require.NoError(t, err)
	subject.Labels[0] = 'X'
	snapshot, err := lifecycle.Subject()
	require.NoError(t, err)
	require.Equal(t, "synthetic labels\n", string(snapshot.Labels))
	snapshot.Labels[0] = 'Y'
	current, err := lifecycle.Current()
	require.NoError(t, err)
	current.Decision.State = ReviewReviewed
	history, err := lifecycle.History()
	require.NoError(t, err)
	history[0].Decision.SubjectID = "other"
	require.NoError(t, lifecycle.Submit(now))
	supplied.Decision.ReviewedAt = now
	require.NoError(t, lifecycle.Apply(supplied, acceptance, now))
	supplied.Decision.Reviewer = "mutated"
	snapshot, err = lifecycle.Subject()
	require.NoError(t, err)
	require.Equal(t, "synthetic labels\n", string(snapshot.Labels))
	current, err = lifecycle.Current()
	require.NoError(t, err)
	require.Equal(t, "synthetic-review-declaration", current.Decision.Reviewer)
	history, err = lifecycle.History()
	require.NoError(t, err)
	require.Len(t, history, 3)
	require.Equal(t, ReviewUnreviewed, history[0].Decision.State)
	require.Equal(t, "synthetic-labels", history[0].Decision.SubjectID)
}

func TestReviewLifecycleUninitializedAndInvalidCreation(t *testing.T) {
	_, _, _, clock := syntheticReviewInputs()
	for _, lifecycle := range []*ReviewLifecycle{nil, {}} {
		require.Error(t, lifecycle.Submit(clock))
		require.Error(t, lifecycle.Apply(SuppliedReview{}, ReviewSourceAcceptance{}, clock))
		_, err := lifecycle.Current()
		require.Error(t, err)
		_, err = lifecycle.Subject()
		require.Error(t, err)
		_, err = lifecycle.History()
		require.Error(t, err)
	}
	subject, supplied, _, now := syntheticReviewInputs()
	for _, candidate := range []struct {
		subject ReviewSubject
		source  string
		at      time.Time
	}{
		{ReviewSubject{}, supplied.SourceID, now},
		{subject, "", now},
		{subject, supplied.SourceID, time.Time{}},
	} {
		lifecycle, err := NewReviewLifecycle(candidate.subject, candidate.source, candidate.at)
		require.Error(t, err)
		require.Nil(t, lifecycle)
	}
	lifecycle, _, now := syntheticLifecycle(t, ReviewUnreviewed)
	require.Error(t, lifecycle.Submit(time.Time{}))
	require.Error(t, lifecycle.Submit(now.Add(-time.Second)))
}

func TestReviewLifecycleNonApprovalDecisionsRequireReviewerAndTime(t *testing.T) {
	for _, state := range []ReviewState{ReviewRejected, ReviewRevoked} {
		for _, malformed := range []string{"reviewer", "zero-time", "future-time", "old-time"} {
			t.Run(string(state)+"/"+malformed, func(t *testing.T) {
				from := ReviewSubmitted
				if state == ReviewRevoked {
					from = ReviewReviewed
				}
				lifecycle, acceptance, now := syntheticLifecycle(t, from)
				before, err := lifecycle.History()
				require.NoError(t, err)
				now = now.Add(time.Hour)
				supplied := syntheticLifecycleDecision(t, lifecycle, state, now)
				switch malformed {
				case "reviewer":
					supplied.Decision.Reviewer = ""
				case "zero-time":
					supplied.Decision.ReviewedAt = time.Time{}
				case "future-time":
					supplied.Decision.ReviewedAt = now.Add(time.Second)
				case "old-time":
					supplied.Decision.ReviewedAt = now.Add(-2 * time.Hour)
				}
				require.Error(t, lifecycle.Apply(supplied, acceptance, now))
				after, err := lifecycle.History()
				require.NoError(t, err)
				require.Equal(t, before, after)
			})
		}
	}
}
