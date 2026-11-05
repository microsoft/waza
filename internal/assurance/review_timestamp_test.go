package assurance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func reviewTimestampDocument(t *testing.T, timestamp json.RawMessage, state ReviewState) []byte {
	t.Helper()
	document := ReviewDocument{
		SchemaVersion: ReferenceVersion, Kind: ReviewKind, SourceID: "synthetic-source",
		SubjectID: "labels", SubjectVersion: "1", LabelsSHA256: strings.Repeat("a", 64),
		State: state, Reviewer: "synthetic-test-reviewer",
		ReviewedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
	}
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(marshalReferenceTest(t, document), &raw))
	if timestamp == nil {
		delete(raw, "reviewed_at")
	} else {
		raw["reviewed_at"] = timestamp
	}
	return marshalReferenceTest(t, raw)
}

func TestParseReviewRejectsMalformedTimestamp(t *testing.T) {
	for _, state := range []ReviewState{ReviewUnreviewed, ReviewSubmitted, ReviewReviewed, ReviewRejected, ReviewRevoked} {
		for _, timestamp := range []string{
			`null`,
			`"2026-10-09T12:00:00+24:00"`,
			`"2026-10-09T12:00:00-24:00"`,
			`"2026-10-09T12:00:00+00:60"`,
			`"2026-10-09T12:00:00-00:60"`,
			`"2026-10-09T2:00:00Z"`,
			`"2026-10-09T12:00:00,5Z"`,
			`"2026-02-30T12:00:00Z"`,
		} {
			t.Run(string(state)+"/"+timestamp, func(t *testing.T) {
				_, err := ParseReview(reviewTimestampDocument(t, json.RawMessage(timestamp), state))
				require.Error(t, err, "the original timestamp must satisfy its declared string/date-time contract")
			})
		}
	}
}

func TestParseReviewPreservesValidTimestampForms(t *testing.T) {
	for _, timestamp := range []string{
		`"2026-10-09T12:00:00Z"`,
		`"2026-10-09T12:00:00+00:00"`,
		`"2026-10-09T12:00:00-00:00"`,
		`"2026-10-09T12:00:00+13:45"`,
		`"2026-10-09T12:00:00-23:59"`,
		`"2026-10-09T12:00:00.123456789Z"`,
		`"2026-10-09T12:00:00.1234567890Z"`,
		`"2024-02-29T12:00:00Z"`,
	} {
		t.Run(timestamp, func(t *testing.T) {
			var expected time.Time
			require.NoError(t, json.Unmarshal([]byte(timestamp), &expected))
			review, err := ParseReview(reviewTimestampDocument(t, json.RawMessage(timestamp), ReviewReviewed))
			require.NoError(t, err)
			require.NotNil(t, review.Decision)
			require.True(t, expected.Equal(review.Decision.ReviewedAt))
		})
	}
	for _, state := range []ReviewState{ReviewUnreviewed, ReviewSubmitted} {
		t.Run(string(state)+"/absent", func(t *testing.T) {
			_, err := ParseReview(reviewTimestampDocument(t, nil, state))
			require.NoError(t, err)
		})
	}
}
