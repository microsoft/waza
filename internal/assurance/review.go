package assurance

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ReviewState string

const (
	ReviewUnreviewed ReviewState = "unreviewed"
	ReviewSubmitted  ReviewState = "submitted"
	ReviewReviewed   ReviewState = "reviewed"
	ReviewRejected   ReviewState = "rejected"
	ReviewRevoked    ReviewState = "revoked"
)

// ReviewSubject identifies label bytes, not an evidence manifest or report.
type ReviewSubject struct {
	ID      string
	Version string
	Labels  []byte
}

type ReviewDecision struct {
	SubjectID      string
	SubjectVersion string
	LabelsDigest   [sha256.Size]byte
	State          ReviewState
	Reviewer       string
	ReviewedAt     time.Time
}

type SuppliedReview struct {
	SourceID string
	Decision *ReviewDecision
}

// ReviewSourceAcceptance is evaluator-owned, never inferred from label metadata.
// AcceptCurrentDecision declares that this source supplies its current decision.
// The caller must not accept a source whose current state cannot be established.
type ReviewSourceAcceptance struct {
	SourceID              string
	AcceptCurrentDecision bool
}

type ReviewReason string

const (
	ReviewSourceNotAccepted ReviewReason = "source_not_accepted"
	ReviewDecisionMissing   ReviewReason = "decision_missing"
	ReviewNotApproved       ReviewReason = "not_reviewed"
	ReviewIdentityMismatch  ReviewReason = "subject_identity_mismatch"
	ReviewVersionMismatch   ReviewReason = "subject_version_mismatch"
	ReviewDigestMismatch    ReviewReason = "label_digest_mismatch"
	ReviewInvalid           ReviewReason = "invalid_review"
)

// ReviewEligibility does not establish human identity, independent review,
// grader agreement, evidence completeness, or calibration. Its zero value fails.
type ReviewEligibility struct {
	Eligible bool
	State    ReviewState
	Reason   ReviewReason
}

// CheckAuthorReview validates a supplied current declaration and explicit source
// acceptance. It cannot discover withheld revocations or authenticate freshness.
// It hashes exact label bytes without normalization and performs no I/O.
func CheckAuthorReview(subject ReviewSubject, supplied SuppliedReview, acceptance ReviewSourceAcceptance, now time.Time) (ReviewEligibility, error) {
	invalid := func(err error) (ReviewEligibility, error) {
		return ReviewEligibility{Reason: ReviewInvalid}, err
	}
	for _, identifier := range []struct{ name, value string }{
		{"label subject ID", subject.ID},
		{"label subject version", subject.Version},
	} {
		if err := validateReviewIdentifier(identifier.name, identifier.value); err != nil {
			return invalid(err)
		}
	}
	if len(subject.Labels) == 0 {
		return invalid(errors.New("review subject requires nonempty label bytes"))
	}
	if !acceptance.AcceptCurrentDecision || acceptance.SourceID != supplied.SourceID {
		return ReviewEligibility{Reason: ReviewSourceNotAccepted}, nil
	}
	if err := validateReviewIdentifier("accepted review source ID", acceptance.SourceID); err != nil {
		return invalid(err)
	}
	if supplied.Decision == nil {
		return ReviewEligibility{State: ReviewUnreviewed, Reason: ReviewDecisionMissing}, nil
	}
	decision := supplied.Decision
	result := ReviewEligibility{State: decision.State}
	switch decision.State {
	case ReviewUnreviewed, ReviewSubmitted, ReviewReviewed, ReviewRejected, ReviewRevoked:
	default:
		return invalid(fmt.Errorf("unknown author review state %q", decision.State))
	}
	for _, identifier := range []struct{ name, value string }{
		{"reviewed subject ID", decision.SubjectID},
		{"reviewed subject version", decision.SubjectVersion},
	} {
		if err := validateReviewIdentifier(identifier.name, identifier.value); err != nil {
			return invalid(err)
		}
	}
	switch {
	case decision.SubjectID != subject.ID:
		result.Reason = ReviewIdentityMismatch
	case decision.SubjectVersion != subject.Version:
		result.Reason = ReviewVersionMismatch
	case decision.LabelsDigest != sha256.Sum256(subject.Labels):
		result.Reason = ReviewDigestMismatch
	case decision.State != ReviewReviewed:
		result.Reason = ReviewNotApproved
	default:
		if err := validateReviewIdentifier("reviewer identity", decision.Reviewer); err != nil {
			return invalid(err)
		}
		if now.IsZero() {
			return invalid(errors.New("checking reviewed labels requires an explicit current time"))
		}
		if decision.ReviewedAt.IsZero() || decision.ReviewedAt.After(now) {
			return invalid(errors.New("reviewed labels require a nonzero review time no later than the current time"))
		}
		result.Eligible = true
	}
	return result, nil
}

func validateReviewIdentifier(name, value string) error {
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be nonempty and unpadded", name)
	}
	return nil
}
