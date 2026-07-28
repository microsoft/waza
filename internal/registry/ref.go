// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

// Package registry implements Phase 1 of the Waza eval/grader registry: Go-module-style
// references, a resolver/cache, and a lockfile for reproducible remote grader presets.
// See docs/research/waza-eval-registry-design.md for the full design.
package registry

import (
	"fmt"
	"regexp"
	"strings"
)

// Ref is a parsed Waza module reference in Go-module style:
//
//	<host>/<owner>/<repo>[/path][#export]@<version>
//
// Examples:
//
//	github.com/waza-evals/fact#factuality@v1.0.0
//	github.com/waza-evals/fact/graders/factuality@v1.0.0
//	github.com/myorg/private-evals/security#secrets@v2.1.3
type Ref struct {
	// Raw is the original ref string as it appeared in YAML.
	Raw string
	// Host is the source host, e.g. "github.com". Phase 1 only supports github.com.
	Host string
	// Owner is the org or user, e.g. "waza-evals".
	Owner string
	// Repo is the repository name, e.g. "fact".
	Repo string
	// Path is the optional subpath inside the repo, e.g. "graders/factuality".
	// Empty when using the "#export" syntax.
	Path string
	// Export is the optional export name from waza.registry.yaml.
	// Empty when using the path syntax.
	Export string
	// Version is the version selector — a semver tag (v1.0.0) or full commit SHA.
	// Phase 1 rejects floating selectors (branches, ranges) in eval.yaml.
	Version string
}

// Module returns the canonical module identity ("host/owner/repo") without
// path/export/version qualifiers.
func (r Ref) Module() string {
	return r.Host + "/" + r.Owner + "/" + r.Repo
}

// String returns the canonical form of the ref.
func (r Ref) String() string {
	var b strings.Builder
	b.WriteString(r.Module())
	if r.Path != "" {
		b.WriteString("/")
		b.WriteString(r.Path)
	}
	if r.Export != "" {
		b.WriteString("#")
		b.WriteString(r.Export)
	}
	if r.Version != "" {
		b.WriteString("@")
		b.WriteString(r.Version)
	}
	return b.String()
}

// commitSHARE matches a 40-char lowercase hex commit SHA.
var commitSHARE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// semverTagRE matches semver-style tags: v1.2.3, v1.2.3-rc1, v1.2.3+build, etc.
var semverTagRE = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// ParseRef parses a canonical Waza ref. It rejects floating version selectors
// (branches, ranges) because Phase 1 only supports reproducible refs in eval.yaml.
func ParseRef(s string) (Ref, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Ref{}, fmt.Errorf("ref is empty")
	}

	// Split off @version.
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return Ref{}, fmt.Errorf("ref %q missing @version selector", raw)
	}
	head, version := raw[:at], raw[at+1:]
	if version == "" {
		return Ref{}, fmt.Errorf("ref %q has empty version", raw)
	}

	// Split off #export.
	var export string
	if hash := strings.Index(head, "#"); hash >= 0 {
		export = head[hash+1:]
		head = head[:hash]
		if export == "" {
			return Ref{}, fmt.Errorf("ref %q has empty export after '#'", raw)
		}
	}

	// Split host/owner/repo[/path].
	parts := strings.Split(head, "/")
	if len(parts) < 3 {
		return Ref{}, fmt.Errorf("ref %q must be host/owner/repo[/path][#export]@version", raw)
	}
	host, owner, repo := parts[0], parts[1], parts[2]
	if host == "" || owner == "" || repo == "" {
		return Ref{}, fmt.Errorf("ref %q has empty host/owner/repo segment", raw)
	}
	// Phase 1: only github.com is supported.
	if host != "github.com" {
		return Ref{}, fmt.Errorf("ref %q: only github.com is supported in Phase 1 (got %q)", raw, host)
	}

	path := strings.Join(parts[3:], "/")
	if path != "" && export != "" {
		return Ref{}, fmt.Errorf("ref %q cannot combine subpath and #export syntax", raw)
	}

	// Version must be an exact tag or full commit SHA for reproducibility.
	if !semverTagRE.MatchString(version) && !commitSHARE.MatchString(version) {
		return Ref{}, fmt.Errorf("ref %q: version %q must be a semver tag (vX.Y.Z) or 40-char commit SHA (floating selectors are not allowed in eval.yaml)", raw, version)
	}

	return Ref{
		Raw:     raw,
		Host:    host,
		Owner:   owner,
		Repo:    repo,
		Path:    path,
		Export:  export,
		Version: version,
	}, nil
}

// IsCommitSHA reports whether the ref version is already a pinned commit SHA
// (as opposed to a semver tag that still needs resolving).
func (r Ref) IsCommitSHA() bool {
	return commitSHARE.MatchString(r.Version)
}
