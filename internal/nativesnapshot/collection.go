package nativesnapshot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/microsoft/waza/internal/controlledprojection"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/nativetask"
	"github.com/microsoft/waza/internal/releasepolicy"
)

// CollectionCapture retains the source resolver's complete associated capture.
// It is not execution permission; the collector owns policy and retry admission.
type CollectionCapture struct {
	capture     *projectionCapture
	projections []byte
	sources     map[releasepolicy.Arm][]nativetask.CapturedSource
	verified    map[releasepolicy.Arm]nativetask.VerifiedCollectionSources
}

func CaptureCollection(ctx context.Context, locations map[releasepolicy.Arm]ArmLocation) (*CollectionCapture, error) {
	capture, err := prepareProjectionCapture(collectionContext(ctx), locations)
	if err != nil {
		return nil, err
	}
	result := &CollectionCapture{capture: capture}
	if err := result.initialize(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *CollectionCapture) Projections(ctx context.Context) (map[releasepolicy.Arm]controlledprojection.SourceProjection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.projections == nil {
		return nil, fmt.Errorf("missing collection capture")
	}
	var projections map[releasepolicy.Arm]controlledprojection.SourceProjection
	if err := json.Unmarshal(c.projections, &projections); err != nil {
		return nil, err
	}
	return projections, nil
}

// Validate the complete retained inventory once at construction. The public
// facade never exposes mutable capture storage; per-attempt joins retain only
// bounded request/source descriptors rather than expanding executable bytes.
func (c *CollectionCapture) initialize(ctx context.Context) error {
	ctx = collectionContext(ctx)
	input, err := projectionInput(ctx, c.capture)
	if err != nil {
		return err
	}
	projections, err := reconstructProjection(ctx, input)
	if err != nil {
		return err
	}
	c.projections, err = json.Marshal(projections)
	if err != nil {
		return err
	}
	c.sources = map[releasepolicy.Arm][]nativetask.CapturedSource{}
	c.verified = map[releasepolicy.Arm]nativetask.VerifiedCollectionSources{}
	blobs := map[string]string{}
	for arm, sources := range c.capture.prepared.sources {
		for _, entry := range sources {
			content, exists := blobs[entry.Digest.SHA256]
			if !exists {
				content = string(entry.Bytes)
				blobs[entry.Digest.SHA256] = content
			}
			c.sources[arm] = append(c.sources[arm], nativetask.CapturedSource{Path: entry.Path, Bytes: content, Digest: entry.Digest, Executable: slices.Contains(entry.Roles, "executable")})
		}
		c.verified[arm], err = nativetask.VerifyCollectionSources(ctx, c.sources[arm])
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *CollectionCapture) Recheck(ctx context.Context) error {
	if c == nil || c.capture == nil {
		return fmt.Errorf("missing collection capture")
	}
	_, err := Recheck(collectionContext(ctx), c.capture.prepared)
	return err
}

func (c *CollectionCapture) Request(ctx context.Context, arm releasepolicy.Arm, task string) (*execution.ExecutionRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.capture == nil {
		return nil, fmt.Errorf("missing collection capture")
	}
	return c.capture.prepared.Request(arm, task)
}

func (c *CollectionCapture) Task(ctx context.Context, arm releasepolicy.Arm, id string) (*models.EvalSpec, *models.TestCase, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if c == nil || c.projections == nil {
		return nil, nil, fmt.Errorf("missing collection capture")
	}
	var arms map[releasepolicy.Arm]armSnapshot
	if err := json.Unmarshal(c.capture.prepared.canonical, &arms); err != nil {
		return nil, nil, err
	}
	snapshot := arms[arm]
	read := func(absolute string) ([]byte, error) {
		label, err := filepath.Rel(snapshot.RootPath, absolute)
		if err != nil {
			return nil, err
		}
		for _, source := range c.capture.prepared.sources[arm] {
			if source.Path == label {
				return bytes.Clone(source.Bytes), nil
			}
		}
		return nil, fmt.Errorf("source absent from retained capture: %s", label)
	}
	evalPath := filepath.Join(snapshot.RootPath, snapshot.EvalPath)
	raw, err := read(evalPath)
	if err != nil {
		return nil, nil, err
	}
	spec, err := models.ParseCapturedEvalSpecOffline(raw, evalPath)
	if err != nil {
		return nil, nil, err
	}
	for _, task := range arms[arm].Tasks {
		if task.ID == id && task.Enabled {
			taskPath := filepath.Join(snapshot.RootPath, task.Path)
			raw, err := read(taskPath)
			if err != nil {
				return nil, nil, err
			}
			tc, err := models.ParseCapturedTestCaseOffline(raw, taskPath, read)
			return spec, tc, err
		}
	}
	return nil, nil, fmt.Errorf("task absent from retained native declaration")
}

func (c *CollectionCapture) Primitive(ctx context.Context, arm releasepolicy.Arm, task string) (*nativetask.Prepared, error) {
	request, err := c.Request(ctx, arm, task)
	if err != nil {
		return nil, err
	}
	verified, ok := c.verified[arm]
	if !ok {
		return nil, fmt.Errorf("missing verified collection source inventory")
	}
	return nativetask.PrepareVerifiedCaptured(ctx, arm, task, request, verified)
}

func (c *CollectionCapture) Join(ctx context.Context, admitted *nativetask.Admitted, key releasepolicy.AttemptKey) error {
	if c == nil || c.projections == nil {
		return fmt.Errorf("missing collection capture")
	}
	request, err := c.Request(ctx, key.Arm, key.TaskID)
	if err != nil {
		return err
	}
	verified, ok := c.verified[key.Arm]
	if !ok {
		return fmt.Errorf("missing verified collection source inventory")
	}
	return nativetask.MatchVerifiedCollectionSnapshot(ctx, admitted, key, request, verified)
}

type collectionCaptureWire struct {
	Kind         string                         `json:"kind"`
	Version      string                         `json:"version"`
	Snapshot     json.RawMessage                `json:"snapshot"`
	Associations json.RawMessage                `json:"associations"`
	Sources      map[releasepolicy.Arm][]source `json:"sources"`
	Blobs        map[string][]byte              `json:"blobs"`
}

func (c *CollectionCapture) Marshal(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.projections == nil {
		return nil, fmt.Errorf("missing collection capture")
	}
	wire := collectionCaptureWire{
		Kind: "waza.release-native-capture", Version: "1.0",
		Snapshot: c.capture.prepared.canonical, Associations: c.capture.associations.canonical,
		Sources: map[releasepolicy.Arm][]source{}, Blobs: map[string][]byte{},
	}
	for arm, sources := range c.capture.prepared.sources {
		wire.Sources[arm] = make([]source, 0, len(sources))
		for _, entry := range sources {
			wire.Blobs[entry.Digest.SHA256] = entry.Bytes
			wire.Sources[arm] = append(wire.Sources[arm], source{Path: entry.Path, Roles: entry.Roles, Digest: entry.Digest})
		}
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	if err := collectionWireSize(len(raw)); err != nil {
		return nil, err
	}
	return raw, nil
}

func collectionWireSize(size int) error {
	if size > CollectionWireBytes {
		return fmt.Errorf("native capture exceeds retained byte limit")
	}
	return nil
}

// ReadCollectionCapture reconstructs only retained bytes. It never accesses the
// source paths encoded in the capture, and cannot re-establish currentness.
func ReadCollectionCapture(ctx context.Context, raw []byte) (*CollectionCapture, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := collectionWireSize(len(raw)); err != nil {
		return nil, err
	}
	blobsStart, blobsEnd, found, err := jsonutil.ObjectMemberRange(raw, "blobs")
	if err != nil {
		return nil, err
	}
	strictRaw := raw
	if found {
		strictRaw = make([]byte, 0, len(raw)-(blobsEnd-blobsStart)+2)
		strictRaw = append(strictRaw, raw[:blobsStart]...)
		strictRaw = append(strictRaw, '{', '}')
		strictRaw = append(strictRaw, raw[blobsEnd:]...)
	}
	if err := models.ValidateNativeJSONKeys(strictRaw, collectionCaptureWire{}); err != nil {
		return nil, err
	}
	decode := func(data []byte) (collectionCaptureWire, error) {
		var wire collectionCaptureWire
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&wire)
		return wire, err
	}
	wire, decodeErr := decode(strictRaw)
	if decodeErr == nil && found {
		wire.Blobs, decodeErr = decodeCollectionBlobs(raw[blobsStart:blobsEnd])
	}
	if decodeErr != nil {
		// Preserve the original decoder's first error and byte offsets when
		// separate envelope/blob decoding encounters invalid field values.
		if _, err := decode(raw); err != nil {
			return nil, err
		}
		return nil, decodeErr
	}
	if wire.Kind != "waza.release-native-capture" || wire.Version != "1.0" {
		return nil, fmt.Errorf("unsupported native capture kind/version")
	}
	used := map[string]bool{}
	total := 0
	for arm, sources := range wire.Sources {
		for i, entry := range sources {
			content, exists := wire.Blobs[entry.Digest.SHA256]
			if !exists || entry.Bytes != nil || releasepolicy.SourceDigest(content) != entry.Digest {
				return nil, fmt.Errorf("native retained source blob is missing or contradictory")
			}
			if !used[entry.Digest.SHA256] {
				total += len(content)
				if total > CollectionSourceBytes {
					return nil, fmt.Errorf("native retained unique source blobs exceed 512 MiB")
				}
			}
			used[entry.Digest.SHA256] = true
			wire.Sources[arm][i].Bytes = content
		}
	}
	if len(used) != len(wire.Blobs) {
		return nil, fmt.Errorf("native capture contains unreferenced source blobs")
	}
	snapshot, err := evidence.JSONDigest(wire.Snapshot)
	if err != nil {
		return nil, err
	}
	associated, err := evidence.JSONDigest(wire.Associations)
	if err != nil {
		return nil, err
	}
	c := &CollectionCapture{capture: &projectionCapture{
		prepared:     &Prepared{canonical: bytes.Clone(wire.Snapshot), seal: *snapshot, sources: wire.Sources},
		associations: retainedAssociations{canonical: bytes.Clone(wire.Associations), seal: *associated},
	}}
	if err := c.initialize(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// ObjectMemberRange has already validated grammar, Unicode, depth and duplicate
// keys. Decode the writer's compact string map without another full JSON scan;
// other representations and all errors retain encoding/json's exact behavior.
func decodeCollectionBlobs(raw []byte) (map[string][]byte, error) {
	fallback := func() (map[string][]byte, error) {
		var blobs map[string][]byte
		err := json.Unmarshal(raw, &blobs)
		return blobs, err
	}
	if len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return fallback()
	}
	blobs := map[string][]byte{}
	remaining := raw[1 : len(raw)-1]
	for len(remaining) > 0 {
		if len(remaining) < 69 || remaining[0] != '"' || remaining[65] != '"' ||
			remaining[66] != ':' || remaining[67] != '"' ||
			bytes.ContainsAny(remaining[1:65], "\\\"") {
			return fallback()
		}
		end := bytes.IndexByte(remaining[68:], '"')
		if end < 0 {
			return fallback()
		}
		encoded := remaining[68 : 68+end]
		content := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
		n, err := base64.StdEncoding.Decode(content, encoded)
		if err != nil {
			return fallback()
		}
		blobs[string(remaining[1:65])] = content[:n]
		remaining = remaining[69+end:]
		if len(remaining) > 0 {
			if remaining[0] != ',' {
				return fallback()
			}
			remaining = remaining[1:]
		}
	}
	return blobs, nil
}
