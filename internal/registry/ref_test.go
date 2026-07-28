// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"strings"
	"testing"
)

func TestParseRef_Valid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Ref
	}{
		{
			name: "export syntax with tag",
			in:   "github.com/waza-evals/fact#factuality@v1.0.0",
			want: Ref{
				Host: "github.com", Owner: "waza-evals", Repo: "fact",
				Export: "factuality", Version: "v1.0.0",
			},
		},
		{
			name: "subpath syntax with tag",
			in:   "github.com/waza-evals/fact/graders/factuality@v1.2.3-rc1",
			want: Ref{
				Host: "github.com", Owner: "waza-evals", Repo: "fact",
				Path: "graders/factuality", Version: "v1.2.3-rc1",
			},
		},
		{
			name: "pinned commit SHA",
			in:   "github.com/o/r#g@" + strings.Repeat("a", 40),
			want: Ref{
				Host: "github.com", Owner: "o", Repo: "r",
				Export: "g", Version: strings.Repeat("a", 40),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRef(tc.in)
			if err != nil {
				t.Fatalf("ParseRef(%q): %v", tc.in, err)
			}
			tc.want.Raw = tc.in
			if got != tc.want {
				t.Fatalf("ParseRef(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseRef_Errors(t *testing.T) {
	cases := map[string]string{
		"empty":               "",
		"no version":          "github.com/o/r",
		"empty version":       "github.com/o/r@",
		"non-github host":     "gitlab.com/o/r#x@v1.0.0",
		"floating branch":     "github.com/o/r#x@main",
		"floating range":      "github.com/o/r#x@^v1.0.0",
		"short sha":           "github.com/o/r#x@abcdef1",
		"path + export mixed": "github.com/o/r/sub#x@v1.0.0",
		"missing owner/repo":  "github.com/o@v1.0.0",
		"empty export":        "github.com/o/r#@v1.0.0",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRef(in); err == nil {
				t.Fatalf("ParseRef(%q) expected error, got nil", in)
			}
		})
	}
}

func TestRef_IsCommitSHA(t *testing.T) {
	sha := strings.Repeat("f", 40)
	r, err := ParseRef("github.com/o/r#g@" + sha)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsCommitSHA() {
		t.Fatal("expected IsCommitSHA true")
	}
	r2, err := ParseRef("github.com/o/r#g@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if r2.IsCommitSHA() {
		t.Fatal("expected IsCommitSHA false for tag")
	}
}

func TestRef_StringRoundTrip(t *testing.T) {
	in := "github.com/o/r#g@v1.0.0"
	r, err := ParseRef(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.String(); got != in {
		t.Fatalf("String() = %q, want %q", got, in)
	}
}
