// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/microsoft/waza/internal/models"
)

// ResolveSpec expands all `ref:` grader entries in spec against the lockfile
// beside evalPath. It returns the number of refs that were resolved and
// whether the lockfile was modified (so callers can decide to persist it).
//
// Behavior:
//   - If updateLock is false: every ref must already be in the lock, and the
//     cached content must match the recorded digest. Unlocked refs return
//     ErrRefNotInLock.
//   - If updateLock is true: missing entries are resolved and added to lock.
//
// Non-ref graders are left untouched.
func ResolveSpec(ctx context.Context, spec *models.EvalSpec, evalPath string, updateLock bool) (resolved int, lockChanged bool, err error) {
	if spec == nil {
		return 0, false, errors.New("resolve: spec is nil")
	}
	refCount := 0
	for _, g := range spec.Graders {
		if g.Ref != "" {
			refCount++
		}
	}
	if refCount == 0 {
		return 0, false, nil
	}

	lockPath := LockfilePath(evalPath)
	lock, err := LoadLockfile(lockPath)
	if err != nil {
		return 0, false, err
	}
	if lock == nil {
		lock = &Lockfile{SchemaVersion: LockfileSchemaVersion}
	}

	res, err := NewResolver()
	if err != nil {
		return 0, false, err
	}

	before := snapshotLock(lock)

	for i, g := range spec.Graders {
		if g.Ref == "" {
			continue
		}
		ref, err := ParseRef(g.Ref)
		if err != nil {
			return resolved, false, fmt.Errorf("grader[%d]: %w", i, err)
		}
		got, err := res.Resolve(ctx, ref, lock, updateLock)
		if err != nil {
			return resolved, false, fmt.Errorf("grader[%d] %s: %w", i, ref.Raw, err)
		}
		expanded, err := ExpandGraderConfig(g, got)
		if err != nil {
			return resolved, false, fmt.Errorf("grader[%d] %s: %w", i, ref.Raw, err)
		}
		spec.Graders[i] = expanded
		resolved++
	}

	lockChanged = !lockSnapshotEqual(before, snapshotLock(lock))
	if updateLock && lockChanged {
		if err := lock.Save(lockPath); err != nil {
			return resolved, lockChanged, err
		}
	}
	return resolved, lockChanged, nil
}

// snapshotLock captures a comparable representation of lock entries for
// change detection.
type lockSnap struct {
	ref, commit, digest string
}

func snapshotLock(l *Lockfile) []lockSnap {
	if l == nil {
		return nil
	}
	out := make([]lockSnap, 0, len(l.Modules))
	for _, m := range l.Modules {
		out = append(out, lockSnap{ref: m.Ref, commit: m.Commit, digest: m.Digest})
	}
	return out
}

func lockSnapshotEqual(a, b []lockSnap) bool {
	if len(a) != len(b) {
		return false
	}
	// Build map keyed by ref for order independence.
	m := make(map[string]lockSnap, len(a))
	for _, e := range a {
		m[e.ref] = e
	}
	for _, e := range b {
		if got, ok := m[e.ref]; !ok || got != e {
			return false
		}
	}
	return true
}
