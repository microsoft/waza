package nativesnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
)

// Joined is opaque, detached, inspection-only linkage. No root handles,
// lifecycle, policy allocation or execution capability are retained or granted.
type Joined struct {
	canonical []byte
	sources   map[releasepolicy.Arm][]source
	binding   nativetask.Admission
	snapshot  models.EvidenceDigest
	seal      models.EvidenceDigest
}

// joinSeal is private identity material, not a serialized public artifact.
type joinSeal struct {
	Snapshot  models.EvidenceDigest
	Admission nativetask.Admission
}

// CheckPrimitiveJoin compares one provided primitive slot to an actual retained
// resolver request and its complete arm inventory. It does not call Recheck:
// source currentness, root authenticity and policy order remain outside this join.
func CheckPrimitiveJoin(ctx context.Context, prepared *Prepared, admitted *nativetask.Admitted, expectedKey releasepolicy.AttemptKey) (*Joined, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prepared == nil {
		return nil, fmt.Errorf("missing native preparation")
	}
	canonical := bytes.Clone(prepared.canonical)
	snapshot, err := evidence.JSONDigest(json.RawMessage(canonical))
	if err != nil || snapshot == nil || *snapshot != prepared.seal {
		return nil, fmt.Errorf("invalid detached native preparation")
	}
	sources, err := detachedSources(ctx, canonical, prepared.sources)
	if err != nil {
		return nil, err
	}
	// Request decodes the actual retained CLIENT fields with UseNumber. No
	// caller-supplied request, replacement source inventory or seal is accepted.
	detached := &Prepared{canonical: canonical, seal: *snapshot}
	request, err := detached.Request(expectedKey.Arm, expectedKey.TaskID)
	if err != nil {
		return nil, err
	}
	captured := make([]nativetask.CapturedSource, 0, len(sources[expectedKey.Arm]))
	for _, source := range sources[expectedKey.Arm] {
		captured = append(captured, nativetask.CapturedSource{
			Path: source.Path, Bytes: string(source.Bytes), Digest: source.Digest,
		})
	}
	if err := nativetask.MatchSnapshot(ctx, admitted, expectedKey, request, captured); err != nil {
		return nil, err
	}
	binding, err := admitted.Binding()
	if err != nil {
		return nil, err
	}
	if binding.Key != expectedKey {
		return nil, fmt.Errorf("native join admission changed")
	}
	seal, err := evidence.JSONDigest(joinSeal{*snapshot, binding})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Joined{canonical: canonical, sources: sources, binding: binding, snapshot: *snapshot, seal: *seal}, nil
}

// Binding is inspection-only. It verifies retained identity, not producer
// acceptance or current source state, and grants no downstream capability.
func (j *Joined) Binding() (nativetask.Admission, error) {
	if j == nil {
		return nativetask.Admission{}, fmt.Errorf("missing native join")
	}
	snapshot, err := evidence.JSONDigest(json.RawMessage(j.canonical))
	if err != nil || snapshot == nil || *snapshot != j.snapshot {
		return nativetask.Admission{}, fmt.Errorf("native join snapshot seal changed")
	}
	if _, err := detachedSources(context.Background(), j.canonical, j.sources); err != nil {
		return nativetask.Admission{}, err
	}
	seal, err := evidence.JSONDigest(joinSeal{j.snapshot, j.binding})
	if err != nil || seal == nil || *seal != j.seal {
		return nativetask.Admission{}, fmt.Errorf("native join seal changed")
	}
	return j.binding, nil
}

// detachedSources validates every arm's complete raw inventory against the
// existing snapshot identities; the canonical snapshot independently binds all
// CLIENT fields, declarations, discoveries, roles and path associations.
func detachedSources(ctx context.Context, snapshotJSON []byte, retained map[releasepolicy.Arm][]source) (map[releasepolicy.Arm][]source, error) {
	var snapshots map[releasepolicy.Arm]armSnapshot
	if err := json.Unmarshal(snapshotJSON, &snapshots); err != nil {
		return nil, err
	}
	if len(snapshots) != 2 || len(retained) != 2 {
		return nil, fmt.Errorf("native join requires both complete arm snapshots")
	}
	result := make(map[releasepolicy.Arm][]source, 2)
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		snapshot, exists := snapshots[arm]
		inventory, captured := retained[arm]
		if !exists || !captured || len(inventory) == 0 || len(inventory) != len(snapshot.Sources) {
			return nil, fmt.Errorf("native join source inventory is incomplete")
		}
		last := ""
		result[arm] = make([]source, 0, len(inventory))
		for i, entry := range inventory {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := canonical(entry.Path, false); err != nil {
				return nil, err
			}
			identity := snapshot.Sources[i]
			if entry.Path <= last || entry.Path != identity.Path || len(entry.Bytes) != identity.ByteLength ||
				entry.Digest != identity.Digest || entry.Digest != releasepolicy.SourceDigest(entry.Bytes) ||
				!slices.Equal(entry.Roles, identity.Roles) {
				return nil, fmt.Errorf("native join retained source identity changed")
			}
			result[arm] = append(result[arm], source{
				Path: entry.Path, Roles: slices.Clone(entry.Roles), Bytes: bytes.Clone(entry.Bytes), Digest: entry.Digest,
			})
			last = entry.Path
		}
	}
	return result, nil
}
