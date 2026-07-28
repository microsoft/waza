// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"errors"
	"testing"
)

func TestParseRef(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Ref
		wantErr bool
	}{
		{
			name: "canonical with export and version",
			in:   "github.com/waza-evals/fact#factuality@v1.0.0",
			want: Ref{
				Raw:     "github.com/waza-evals/fact#factuality@v1.0.0",
				Host:    "github.com",
				Owner:   "waza-evals",
				Repo:    "fact",
				Export:  "factuality",
				Version: "v1.0.0",
			},
		},
		{
			name: "path form without export",
			in:   "github.com/waza-evals/fact/graders/factuality@v1.0.0",
			want: Ref{
				Raw:     "github.com/waza-evals/fact/graders/factuality@v1.0.0",
				Host:    "github.com",
				Owner:   "waza-evals",
				Repo:    "fact",
				Path:    "graders/factuality",
				Version: "v1.0.0",
			},
		},
		{
			name: "no version",
			in:   "github.com/waza-evals/fact#factuality",
			want: Ref{
				Raw:    "github.com/waza-evals/fact#factuality",
				Host:   "github.com",
				Owner:  "waza-evals",
				Repo:   "fact",
				Export: "factuality",
			},
		},
		{name: "empty", in: "", wantErr: true},
		{name: "too few parts", in: "github.com/foo", wantErr: true},
		{name: "empty version", in: "github.com/waza-evals/fact@", wantErr: true},
		{name: "empty export", in: "github.com/waza-evals/fact#@v1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRef(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				if !errors.Is(err, ErrInvalidRef) {
					t.Fatalf("expected ErrInvalidRef, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v want %+v", got, tt.want)
			}
			if got.String() != tt.in {
				t.Fatalf("round-trip: got %q want %q", got.String(), tt.in)
			}
			if got.Module() != tt.want.Host+"/"+tt.want.Owner+"/"+tt.want.Repo {
				t.Fatalf("module mismatch: %q", got.Module())
			}
		})
	}
}

func TestIsRemote(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"github.com/waza-evals/fact#factuality@v1.0.0", true},
		{"github.com/waza-evals/fact", true},
		{"./local.yaml", false},
		{"/abs/path", false},
		{"factuality", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsRemote(tt.in); got != tt.want {
			t.Errorf("IsRemote(%q) = %v want %v", tt.in, got, tt.want)
		}
	}
}
