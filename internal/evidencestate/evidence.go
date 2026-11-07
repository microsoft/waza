// Package evidencestate defines derived evidence declarations, not execution
// attestation. Parsing declarations is separate from verifying source bytes.
package evidencestate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Version          = "1.0"
	MaxDocumentBytes = artifactmanifest.MaxManifestBytes
)

var ErrInvalid = errors.New("invalid execution evidence declaration")

type Category string

const (
	Conversation Category = "conversation"
	Tool         Category = "tool"
	Grader       Category = "grader"
	Reliability  Category = "reliability"
	Usage        Category = "usage"
	Provenance   Category = "provenance"
)

type State string

const (
	Present     State = "present"
	Unavailable State = "unavailable"
	Incomplete  State = "incomplete"
	Redacted    State = "redacted"
	Conflicting State = "conflicting"
)

type Source string

const (
	Authored Source = "authored"
	Mock     Source = "mock"
	Cached   Source = "cached"
	Live     Source = "live"
	Unknown  Source = "unknown"
)

type Context string

const (
	Declared       Context = "declared"
	Observed       Context = "observed"
	UnknownContext Context = "unknown"
)

type Reason string

const (
	NotRecorded        Reason = "not_recorded"
	PartialCapture     Reason = "partial_capture"
	Privacy            Reason = "privacy"
	SourceConflict     Reason = "source_conflict"
	ArtifactCountLimit Reason = "artifact_count_limit"
	ObjectSizeLimit    Reason = "object_size_limit"
	TotalSizeLimit     Reason = "total_size_limit"
)

// Limit records a measured lower bound. Total is absent unless the exact total
// is known; neither value licenses truncation or weakens manifest admission.
type Limit struct {
	Observed int64  `json:"observed"`
	Total    *int64 `json:"total,omitempty"`
}

// Record contains no captured payload or free-text diagnostic. Source, context,
// counts and completeness are declarations requiring category-specific producer
// evidence; a matching byte digest alone does not verify these assertions.
type Record struct {
	Category      Category `json:"category"`
	State         State    `json:"state"`
	Source        Source   `json:"source"`
	Context       Context  `json:"context"`
	Artifacts     []string `json:"artifacts"`
	ObservedCount *int64   `json:"observed_count,omitempty"`
	Reason        Reason   `json:"reason,omitempty"`
	Limit         *Limit   `json:"limit,omitempty"`
}

type Relationship string

const (
	Retry       Relationship = "retry"
	Resume      Relationship = "resume"
	Cache       Relationship = "cache"
	Aggregation Relationship = "aggregation"
)

// Lineage references source manifests, not unique executions. Legacy binding
// attempt ordinals do not establish unique execution or duplicate decisions.
type Lineage struct {
	Manifest     string       `json:"manifest"`
	Relationship Relationship `json:"relationship"`
}

// Document is published separately after its origin manifest is finalized.
// It must never be added to, or mutate, that already finalized origin.
type Document struct {
	Kind           string    `json:"kind"`
	SchemaVersion  string    `json:"schema_version"`
	Publication    string    `json:"publication"`
	OriginManifest string    `json:"origin_manifest"`
	Categories     []Record  `json:"categories"`
	Lineage        []Lineage `json:"lineage"`
}

var evidenceSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.UseLoader(schemaloader.Offline{})
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemas.ExecutionEvidenceSchemaJSON))
	if err != nil {
		return nil, err
	}
	if err := c.AddResource("execution-evidence.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("execution-evidence.json")
})

func Parse(data []byte) (*Document, error) {
	if len(data) > MaxDocumentBytes {
		return nil, fmt.Errorf("%w: exceeds %d bytes", ErrInvalid, MaxDocumentBytes)
	}
	value, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	schema, err := evidenceSchema()
	if err != nil {
		return nil, fmt.Errorf("compiling evidence schema: %w", err)
	}
	if err := schema.Validate(value); err != nil {
		return nil, fmt.Errorf("%w: schema: %w", ErrInvalid, err)
	}
	var d Document
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("%w: decoding: %w", ErrInvalid, err)
	}
	seen := make(map[Category]bool, len(d.Categories))
	for _, r := range d.Categories {
		if seen[r.Category] {
			return nil, fmt.Errorf("%w: duplicate category %q", ErrInvalid, r.Category)
		}
		seen[r.Category] = true
		if r.State == Present {
			if (*r.ObservedCount == 0) != (len(r.Artifacts) == 0) {
				return nil, fmt.Errorf("%w: present-empty and artifact presence disagree for %s", ErrInvalid, r.Category)
			}
		}
		if r.Limit != nil {
			bound := int64(artifactmanifest.MaxTotalBytes)
			switch r.Reason {
			case ArtifactCountLimit:
				bound = 128 // The v1 manifest schema's fixed artifact count.
			case ObjectSizeLimit:
				bound = artifactmanifest.MaxArtifactBytes
			}
			if r.Limit.Observed <= bound || (r.Limit.Total != nil && *r.Limit.Total < r.Limit.Observed) {
				return nil, fmt.Errorf("%w: limit must record an exceeded bound and a consistent total", ErrInvalid)
			}
		}
	}
	links := make(map[Lineage]bool, len(d.Lineage))
	for _, link := range d.Lineage {
		if link.Manifest == d.OriginManifest || links[link] {
			return nil, fmt.Errorf("%w: self-reference or duplicate lineage", ErrInvalid)
		}
		links[link] = true
	}
	return &d, nil
}

// Encode sorts detached slices, emits compact Go JSON with no trailing newline,
// and applies exactly the same wire/invariant checks as Parse.
func Encode(d Document) ([]byte, error) {
	if err := encodingBounds(d); err != nil {
		return nil, err
	}
	d.Categories = slices.Clone(d.Categories)
	d.Lineage = slices.Clone(d.Lineage)
	for i := range d.Categories {
		d.Categories[i].Artifacts = slices.Clone(d.Categories[i].Artifacts)
		slices.Sort(d.Categories[i].Artifacts)
	}
	slices.SortFunc(d.Categories, func(a, b Record) int { return strings.Compare(string(a.Category), string(b.Category)) })
	slices.SortFunc(d.Lineage, func(a, b Lineage) int {
		if order := strings.Compare(a.Manifest, b.Manifest); order != 0 {
			return order
		}
		return strings.Compare(string(a.Relationship), string(b.Relationship))
	})
	data, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding: %w", ErrInvalid, err)
	}
	if _, err := Parse(data); err != nil {
		return nil, err
	}
	return data, nil
}

func encodingBounds(d Document) error {
	if len(d.Categories) != 6 || len(d.Lineage) > 128 {
		return fmt.Errorf("%w: category or lineage count exceeds profile bounds", ErrInvalid)
	}
	values := []string{d.Kind, d.SchemaVersion, d.Publication, d.OriginManifest}
	for _, r := range d.Categories {
		if len(r.Artifacts) > 128 {
			return fmt.Errorf("%w: artifact reference count exceeds profile bounds", ErrInvalid)
		}
		values = append(values, string(r.Category), string(r.State), string(r.Source), string(r.Context), string(r.Reason))
		values = append(values, r.Artifacts...)
	}
	for _, link := range d.Lineage {
		values = append(values, link.Manifest, string(link.Relationship))
	}
	for _, value := range values {
		if len(value) > 128 || !utf8.ValidString(value) {
			return fmt.Errorf("%w: metadata exceeds byte bounds or is not UTF-8", ErrInvalid)
		}
	}
	return nil
}

func Digest(d Document) (string, error) {
	data, err := Encode(d)
	if err != nil {
		return "", err
	}
	return artifactmanifest.Hash(data), nil
}

// VerifySources checks canonical source manifests, the bound terminal envelope,
// and bytes for every referenced origin artifact. Keys are manifest digests and
// artifact names, not paths. It performs no I/O and returns no acceptance verdict.
// It cannot prove finalization, category semantics, privacy policy, execution,
// or lineage relationships without the actual publisher/producer contracts.
func VerifySources(d Document, manifests, artifacts map[string][]byte) error {
	if _, err := Encode(d); err != nil {
		return err
	}
	origin, err := verifyManifest(d.OriginManifest, manifests)
	if err != nil {
		return fmt.Errorf("origin manifest: %w", err)
	}
	for _, link := range d.Lineage {
		if _, err := verifyManifest(link.Manifest, manifests); err != nil {
			return fmt.Errorf("lineage manifest: %w", err)
		}
	}
	required := map[string]bool{origin.TerminalArtifact: true}
	for _, r := range d.Categories {
		for _, name := range r.Artifacts {
			required[name] = true
		}
	}
	declared := make(map[string]artifactmanifest.Artifact, len(origin.Artifacts))
	for _, a := range origin.Artifacts {
		declared[a.Name] = a
	}
	for name := range required {
		a, ok := declared[name]
		if !ok {
			return fmt.Errorf("%w: reference is not declared in origin manifest", artifactmanifest.ErrConflict)
		}
		data, ok := artifacts[name]
		if !ok {
			return fmt.Errorf("%w: referenced artifact", artifactmanifest.ErrMissing)
		}
		if int64(len(data)) != a.SizeBytes || artifactmanifest.Hash(data) != a.SHA256 {
			return fmt.Errorf("%w: referenced artifact bytes", artifactmanifest.ErrCorrupt)
		}
	}
	if _, err := runnerterminal.Validate(artifacts[origin.TerminalArtifact], origin.Binding.TerminalBinding()); err != nil {
		return fmt.Errorf("origin terminal: %w", err)
	}
	return nil
}

func verifyManifest(digest string, sources map[string][]byte) (*artifactmanifest.Manifest, error) {
	data, ok := sources[digest]
	if !ok {
		return nil, artifactmanifest.ErrMissing
	}
	if artifactmanifest.Hash(data) != digest {
		return nil, artifactmanifest.ErrCorrupt
	}
	m, err := artifactmanifest.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", artifactmanifest.ErrCorrupt, err)
	}
	canonical, err := artifactmanifest.Encode(*m)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(data, canonical) {
		return nil, fmt.Errorf("%w: noncanonical source manifest", artifactmanifest.ErrCorrupt)
	}
	return m, nil
}
