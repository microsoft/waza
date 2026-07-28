// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"errors"
	"fmt"
)

// Resolution is the result of resolving a Ref to a concrete, cached
// module version. It is a lightweight stand-in for the richer resolver
// output that issue #15 will produce; it lets `waza registry add` write
// a plausible waza.lock entry today.
type Resolution struct {
	// Ref is the canonical ref as supplied by the user.
	Ref Ref
	// Module is "<host>/<owner>/<repo>".
	Module string
	// Version is the resolved version selector (may still be floating
	// for the stub).
	Version string
	// Commit is the resolved commit SHA. Empty from the stub.
	Commit string
	// Digest is the content digest. Empty from the stub.
	Digest string
	// Kind is the resolved artifact kind, when known.
	Kind Kind
	// Trusted indicates whether the caller granted trust to execute
	// program-graders from this ref. See design §14.
	Trusted bool
}

// Resolver resolves a Ref to a Resolution. The real implementation lives
// in issue #15's resolver package.
type Resolver interface {
	Resolve(ref Ref) (Resolution, error)
}

// ErrResolverNotImplemented is returned by the stub resolver in place of
// a real network round-trip. CLI commands surface this to the user with
// a clear message pointing at issue #15.
var ErrResolverNotImplemented = errors.New("registry resolver not yet implemented (see issue #15)")

// StubResolver returns partial resolution metadata sufficient for the
// Phase 1 CLI to update eval.yaml and produce a placeholder waza.lock
// entry without performing any network I/O.
type StubResolver struct{}

// Resolve returns a Resolution derived purely from the ref's own
// syntax. It never fails on well-formed input; callers that need real
// commit/digest values must wait for issue #15.
//
// TODO(#15): replace with a real resolver that reads waza.registry.yaml,
// authenticates against the source backend, downloads the module, and
// computes commit + digest.
func (StubResolver) Resolve(ref Ref) (Resolution, error) {
	if ref.Module() == "" {
		return Resolution{}, fmt.Errorf("cannot resolve empty ref")
	}
	return Resolution{
		Ref:     ref,
		Module:  ref.Module(),
		Version: ref.Version,
	}, nil
}
