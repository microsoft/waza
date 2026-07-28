// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

// Package registry provides ref parsing, configuration, and (in future
// issues) resolution logic for the Waza eval registry described in
// docs/research/waza-eval-registry-design.md.
//
// This file implements the ref syntax used by CLI subcommands introduced
// in issue #17 (`waza registry search`, `waza registry add`). Actual
// resolution of a Ref to a cached module tree is provided by the resolver
// added in issue #15; see resolver.go for the integration TODO.
package registry

import (
	"errors"
	"fmt"
	"strings"
)

// Ref is the canonical form of a registry reference:
//
//	<host>/<owner>/<repo>[/path][#export]@<version>
//
// Examples:
//
//	github.com/waza-evals/fact#factuality@v1.0.0
//	github.com/waza-evals/fact/graders/factuality@v1.0.0
//	github.com/myorg/private-evals/security#secrets@v2.1.3
type Ref struct {
	// Raw is the original string the caller supplied.
	Raw string
	// Host is the source host (e.g. "github.com").
	Host string
	// Owner is the org or user under the host (e.g. "waza-evals").
	Owner string
	// Repo is the repository name (e.g. "fact").
	Repo string
	// Path is the optional sub-path inside the repo (e.g. "graders/factuality").
	// Empty when omitted.
	Path string
	// Export is the optional artifact name after the "#" separator
	// (e.g. "factuality"). Empty when omitted.
	Export string
	// Version is the tag, semver, branch, or commit selector after "@"
	// (e.g. "v1.0.0", "main", "4f8c2d6a"). Empty when omitted.
	Version string
}

// Module returns the "<host>/<owner>/<repo>" portion of the ref, i.e. the
// module identity without any sub-path, export, or version suffix.
func (r Ref) Module() string {
	if r.Host == "" || r.Owner == "" || r.Repo == "" {
		return ""
	}
	return r.Host + "/" + r.Owner + "/" + r.Repo
}

// String reassembles a canonical string form of the ref. It is idempotent
// with ParseRef for valid inputs.
func (r Ref) String() string {
	var b strings.Builder
	b.WriteString(r.Module())
	if r.Path != "" {
		b.WriteByte('/')
		b.WriteString(r.Path)
	}
	if r.Export != "" {
		b.WriteByte('#')
		b.WriteString(r.Export)
	}
	if r.Version != "" {
		b.WriteByte('@')
		b.WriteString(r.Version)
	}
	return b.String()
}

// ErrInvalidRef is returned when a ref cannot be parsed.
var ErrInvalidRef = errors.New("invalid registry ref")

// ParseRef parses the canonical registry ref syntax.
//
// The grammar is intentionally forgiving: an empty version is allowed at
// parse time so that CLI callers can accept floating refs and later
// enforce their own version policy (see design §6). Callers that require
// a version should check Ref.Version explicitly.
func ParseRef(s string) (Ref, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return Ref{}, fmt.Errorf("%w: empty ref", ErrInvalidRef)
	}

	ref := Ref{Raw: trimmed}
	rest := trimmed

	if at := strings.LastIndex(rest, "@"); at >= 0 {
		ref.Version = rest[at+1:]
		rest = rest[:at]
		if ref.Version == "" {
			return Ref{}, fmt.Errorf("%w: version selector after '@' is empty", ErrInvalidRef)
		}
	}

	if hash := strings.LastIndex(rest, "#"); hash >= 0 {
		ref.Export = rest[hash+1:]
		rest = rest[:hash]
		if ref.Export == "" {
			return Ref{}, fmt.Errorf("%w: export after '#' is empty", ErrInvalidRef)
		}
	}

	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		return Ref{}, fmt.Errorf("%w: expected <host>/<owner>/<repo> got %q", ErrInvalidRef, rest)
	}
	ref.Host = parts[0]
	ref.Owner = parts[1]
	ref.Repo = parts[2]
	if ref.Host == "" || ref.Owner == "" || ref.Repo == "" {
		return Ref{}, fmt.Errorf("%w: host, owner, and repo are required", ErrInvalidRef)
	}
	if len(parts) > 3 {
		ref.Path = strings.Join(parts[3:], "/")
	}

	return ref, nil
}

// IsRemote reports whether s looks like a remote ref (host/owner/repo)
// rather than a local path or bare grader name.
func IsRemote(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") {
		return false
	}
	head := s
	if at := strings.Index(head, "@"); at >= 0 {
		head = head[:at]
	}
	if hash := strings.Index(head, "#"); hash >= 0 {
		head = head[:hash]
	}
	return strings.Count(head, "/") >= 2
}
