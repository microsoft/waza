package nativetask

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
)

// CapturedSource carries immutable byte values, not roots or supplied seals.
// Bytes is a string solely to avoid shared mutable byte storage; it is not text.
// Values supplied directly to MatchSnapshot are not resolver authenticity.
type CapturedSource struct {
	Executable bool
	Path       string
	Bytes      string
	Digest     models.EvidenceDigest
}

// VerifiedCollectionSources is an immutable descriptor inventory whose byte
// identities were checked when it was constructed. It is intended for a
// CollectionCapture that separately rechecks its rooted sources before each
// attempt; callers with arbitrary inventories must use PrepareCaptured.
type VerifiedCollectionSources struct {
	sources []CapturedSource
}

// MatchSnapshot only compares retained inputs. It performs no source reads,
// preparation, admission, execution, policy allocation or producer acceptance.
func MatchSnapshot(ctx context.Context, admitted *Admitted, expectedKey releasepolicy.AttemptKey, req *execution.ExecutionRequest, sources []CapturedSource) error {
	return matchSnapshot(ctx, admitted, expectedKey, req, sources, false, false)
}

// MatchCollectionSnapshot admits the separately selected 2.0 collection
// inventory budget, including the retained evaluator executable.
func MatchCollectionSnapshot(ctx context.Context, admitted *Admitted, expectedKey releasepolicy.AttemptKey, req *execution.ExecutionRequest, sources []CapturedSource) error {
	verified, err := VerifyCollectionSources(ctx, sources)
	if err != nil {
		return err
	}
	return MatchVerifiedCollectionSnapshot(ctx, admitted, expectedKey, req, verified)
}

// MatchVerifiedCollectionSnapshot compares a previously verified immutable
// descriptor inventory. Its CollectionCapture caller still rechecks current
// rooted source bytes before every attempted execution.
func MatchVerifiedCollectionSnapshot(ctx context.Context, admitted *Admitted, expectedKey releasepolicy.AttemptKey, req *execution.ExecutionRequest, verified VerifiedCollectionSources) error {
	return matchSnapshot(ctx, admitted, expectedKey, req, verified.sources, true, true)
}

func matchSnapshot(ctx context.Context, admitted *Admitted, expectedKey releasepolicy.AttemptKey, req *execution.ExecutionRequest, sources []CapturedSource, collection, verified bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	binding, err := admitted.Binding()
	if err != nil {
		return err
	}
	data, err := admitted.prepared.view()
	if err != nil {
		return err
	}
	limit, encodedLimit := assurance.MaxLabelBytes, assurance.MaxLabelBytes
	if collection {
		if data.CollectionVersion != "2.0" {
			return fmt.Errorf("native collection requires separately selected preparation")
		}
		limit, encodedLimit = collectionSourceBytes, collectionEncodedBytes
	} else if data.CollectionVersion != "" {
		return fmt.Errorf("primitive snapshot does not admit collection preparation")
	}
	if (expectedKey.Arm != releasepolicy.Baseline && expectedKey.Arm != releasepolicy.Candidate) ||
		expectedKey.EvalID == "" || expectedKey.TaskID == "" || expectedKey.ClusterID == "" ||
		expectedKey.Trial < 1 || expectedKey.Attempt < 1 || binding.Key != expectedKey ||
		data.Arm != expectedKey.Arm || data.TaskID != expectedKey.TaskID ||
		binding.RequestDigest != admitted.prepared.requestDigest {
		return fmt.Errorf("native snapshot key or request binding differs from admission")
	}
	intent, total, err := projectRequest(req)
	if err != nil {
		return err
	}
	digest, err := evidence.JSONDigest(intent)
	if err != nil {
		return err
	}
	if digest == nil || *digest != binding.RequestDigest {
		return fmt.Errorf("native snapshot request digest differs from admission")
	}
	want, err := canonicalIntent(data.Request)
	if err != nil {
		return err
	}
	got, err := canonicalIntent(intent)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return fmt.Errorf("native snapshot canonical request differs from preparation")
	}
	if len(sources) == 0 || len(sources) != len(data.Sources) {
		return fmt.Errorf("native snapshot requires the complete prepared source inventory")
	}
	last := ""
	for i, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if source.Path <= last || source.Path == "." || !filepath.IsLocal(source.Path) ||
			strings.Contains(source.Path, "\\") || filepath.ToSlash(filepath.Clean(source.Path)) != source.Path {
			return fmt.Errorf("native snapshot sources require canonical unique ordered paths")
		}
		sourceLimit := limit
		if collection {
			sourceLimit = 16 << 20
			if source.Executable {
				sourceLimit = CollectionExecutableBytes
			}
		}
		if len(source.Bytes) > sourceLimit || total > limit-len(source.Bytes) {
			return fmt.Errorf("native snapshot bridge exceeds primitive bounded inventory")
		}
		prepared := data.Sources[i]
		actualDigest := source.Digest
		if !verified {
			actualDigest = releasepolicy.SourceDigest([]byte(source.Bytes))
		}
		if source.Path != prepared.Path || source.Digest != actualDigest || prepared.Digest != actualDigest ||
			(!collection && !bytes.Equal([]byte(source.Bytes), prepared.Bytes)) ||
			(collection && len(prepared.Bytes) != 0) {
			return fmt.Errorf("native snapshot source differs from preparation")
		}
		total += len(source.Bytes)
		last = source.Path
	}
	payload, err := json.Marshal(preparedData{Arm: data.Arm, TaskID: data.TaskID, Request: intent, Sources: data.Sources, CollectionVersion: data.CollectionVersion})
	if err != nil {
		return fmt.Errorf("encoding matched native inputs: %w", err)
	}
	if len(payload) > encodedLimit || len(admitted.prepared.data) > encodedLimit {
		return fmt.Errorf("native snapshot bridge exceeds primitive encoded inventory")
	}
	return ctx.Err()
}

// Use the same number-preserving parser and normalization as JSONDigest,
// retaining canonical bytes for equality rather than relying only on a hash.
func canonicalIntent(intent requestIntent) ([]byte, error) {
	raw, err := json.Marshal(intent)
	if err != nil {
		return nil, fmt.Errorf("encoding native request intent: %w", err)
	}
	value, err := jsonutil.Parse(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
