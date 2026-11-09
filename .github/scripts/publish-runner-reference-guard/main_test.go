package main

import (
	"strings"
	"testing"
)

const candidateSHA = "0123456789abcdef0123456789abcdef01234567"

func validCandidateContext() candidateContext {
	return candidateContext{
		EventName:    "workflow_dispatch",
		Repository:   "microsoft/waza",
		Ref:          "refs/heads/main",
		EventSHA:     candidateSHA,
		CandidateSHA: candidateSHA,
		CheckoutSHA:  candidateSHA,
		MainSHA:      candidateSHA,
	}
}

func TestValidateCandidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*candidateContext)
		want   string
	}{
		{name: "accepted current main candidate"},
		{
			name: "push event",
			mutate: func(candidate *candidateContext) {
				candidate.EventName = "push"
			},
			want: "not an allowed manual dispatch",
		},
		{
			name: "pull request event",
			mutate: func(candidate *candidateContext) {
				candidate.EventName = "pull_request"
			},
			want: "not an allowed manual dispatch",
		},
		{
			name: "wrong repository",
			mutate: func(candidate *candidateContext) {
				candidate.Repository = "attacker/waza"
			},
			want: "not the publication source",
		},
		{
			name: "non-main ref",
			mutate: func(candidate *candidateContext) {
				candidate.Ref = "refs/heads/feature"
			},
			want: "not the protected main branch",
		},
		{
			name: "short SHA",
			mutate: func(candidate *candidateContext) {
				candidate.CandidateSHA = candidateSHA[:12]
			},
			want: "full lowercase 40-character",
		},
		{
			name: "non-hex SHA",
			mutate: func(candidate *candidateContext) {
				candidate.CandidateSHA = strings.Repeat("z", 40)
			},
			want: "full lowercase 40-character",
		},
		{
			name: "event SHA mismatch",
			mutate: func(candidate *candidateContext) {
				candidate.EventSHA = strings.Repeat("a", 40)
			},
			want: "workflow event SHA",
		},
		{
			name: "checkout mismatch",
			mutate: func(candidate *candidateContext) {
				candidate.CheckoutSHA = strings.Repeat("b", 40)
			},
			want: "checkout SHA",
		},
		{
			name: "main advanced",
			mutate: func(candidate *candidateContext) {
				candidate.MainSHA = strings.Repeat("c", 40)
			},
			want: "current main SHA",
		},
		{
			name: "malformed main SHA",
			mutate: func(candidate *candidateContext) {
				candidate.MainSHA = candidateSHA[:39]
			},
			want: "current main SHA is not a full commit SHA",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validCandidateContext()
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			err := validateCandidate(candidate)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateCandidate() error = %v; want substring %q", err, test.want)
			}
		})
	}
}
