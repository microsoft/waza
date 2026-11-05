package main

import (
	"testing"
)

func validVerification() verificationContext {
	return verificationContext{
		EventName:      "workflow_dispatch",
		Repository:     "microsoft/waza",
		Ref:            "refs/heads/main",
		EventSHA:       "0123456789abcdef0123456789abcdef01234567",
		SourceSHA:      "0123456789abcdef0123456789abcdef01234567",
		ImageDigest:    "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		CheckoutSHA:    "0123456789abcdef0123456789abcdef01234567",
		CurrentMainSHA: "0123456789abcdef0123456789abcdef01234567",
		SourceOnMain:   true,
	}
}

func TestValidateVerification(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*verificationContext)
	}{
		{name: "accept exact public digest and main source"},
		{name: "reject non-dispatch", mutate: func(value *verificationContext) {
			value.EventName = "push"
		}},
		{name: "reject other repository", mutate: func(value *verificationContext) {
			value.Repository = "fork/waza"
		}},
		{name: "reject non-main ref", mutate: func(value *verificationContext) {
			value.Ref = "refs/heads/feature"
		}},
		{name: "reject abbreviated source SHA", mutate: func(value *verificationContext) {
			value.SourceSHA = "0123456"
		}},
		{name: "reject event and checkout mismatch", mutate: func(value *verificationContext) {
			value.CheckoutSHA = "1123456789abcdef0123456789abcdef01234567"
		}},
		{name: "reject stale workflow source", mutate: func(value *verificationContext) {
			value.CurrentMainSHA = "1123456789abcdef0123456789abcdef01234567"
		}},
		{name: "reject source outside current main", mutate: func(value *verificationContext) {
			value.SourceOnMain = false
		}},
		{name: "reject tagged reference instead of immutable digest", mutate: func(value *verificationContext) {
			value.ImageDigest = "candidate-latest"
		}},
		{name: "reject uppercase digest", mutate: func(value *verificationContext) {
			value.ImageDigest = "sha256:0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validVerification()
			if test.mutate != nil {
				test.mutate(&value)
			}
			err := validateVerification(value)
			if test.mutate == nil && err != nil {
				t.Fatalf("valid inputs rejected: %v", err)
			}
			if test.mutate != nil && err == nil {
				t.Fatal("invalid inputs were accepted")
			}
		})
	}
}
