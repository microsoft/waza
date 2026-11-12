package evidencestate

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

// Inspection reports current byte verification separately from declared
// completeness. It is not an assurance verdict or a ledger reservation.
type Inspection struct {
	EvidenceSHA256 string                        `json:"evidence_sha256"`
	Publication    string                        `json:"publication"`
	Integrity      string                        `json:"integrity"`
	Verification   string                        `json:"verification"`
	Document       *Document                     `json:"document,omitempty"`
	Sources        []artifactmanifest.Receipt    `json:"sources"`
	Diagnostics    []artifactmanifest.Diagnostic `json:"diagnostics"`
}

func ValidateDigest(digest string) error {
	if len(digest) != 64 || strings.ToLower(digest) != digest {
		return fmt.Errorf("%w: provide a full lowercase SHA-256 digest", ErrInvalid)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("%w: provide a full lowercase SHA-256 digest", ErrInvalid)
	}
	return nil
}

func newInspection(digest string) Inspection {
	return Inspection{
		EvidenceSHA256: digest, Publication: "derived", Integrity: "unverified",
		Verification: "content-and-declared-context-only",
		Sources:      []artifactmanifest.Receipt{}, Diagnostics: []artifactmanifest.Diagnostic{},
	}
}

func inspectionFailure(r Inspection, err error) (Inspection, error) {
	code, message := "storage_error", "Evidence inspection did not complete. Check operator diagnostics; no acceptance was established."
	switch {
	case errors.Is(err, artifactmanifest.ErrCorrupt):
		code, message = "corrupt_source", "Current evidence or source bytes do not match their immutable declaration."
	case errors.Is(err, artifactmanifest.ErrConflict):
		code, message = "conflicting_source", "Source publication, reference, or declared context conflicts."
	case errors.Is(err, ErrInvalid), errors.Is(err, artifactmanifest.ErrInvalid):
		code, message = "invalid_declaration", "Provide a valid evidence declaration and full lowercase digest."
	case errors.Is(err, artifactmanifest.ErrMissing):
		code, message = "missing_source", "Required evidence or source bytes are unavailable."
	}
	r.Document = nil
	r.Integrity = "failed"
	r.Diagnostics = append(r.Diagnostics, artifactmanifest.Diagnostic{Code: code, Message: message})
	return r, err
}

// Publish stores canonical evidence as its own blob only after actual Store
// inspection establishes finalized, verified source manifests. It never changes
// an origin manifest or creates a final marker for a non-manifest object.
func Publish(ctx context.Context, backend artifactmanifest.Backend, d Document) (Inspection, error) {
	r := newInspection("")
	data, err := Encode(d)
	if err != nil {
		return inspectionFailure(r, err)
	}
	r.EvidenceSHA256 = artifactmanifest.Hash(data)
	snapshot, err := Parse(data)
	if err != nil {
		return inspectionFailure(r, err)
	}
	admitted, _, err := inspectDocument(ctx, backend, *snapshot, r)
	if err != nil {
		return admitted, err
	}
	a := artifactmanifest.Artifact{
		Name: "execution-evidence", SHA256: r.EvidenceSHA256, SizeBytes: int64(len(data)), MediaType: "application/json",
	}
	if err := artifactmanifest.NewStore(backend).Put(ctx, a, data); err != nil {
		return inspectionFailure(admitted, fmt.Errorf("publishing derived evidence: %w", err))
	}
	persisted, err := backend.Get(ctx, "blob-"+r.EvidenceSHA256, MaxDocumentBytes)
	if err != nil {
		return inspectionFailure(admitted, fmt.Errorf("reading published evidence: %w", err))
	}
	if !bytes.Equal(persisted, data) {
		return inspectionFailure(admitted, artifactmanifest.ErrCorrupt)
	}
	return admitted, nil
}

// Inspect rereads the derived blob and all referenced source publications.
// A previously successful receipt is never reused as current evidence.
func Inspect(ctx context.Context, backend artifactmanifest.Backend, digest string) (Inspection, error) {
	r, _, err := inspectEvidence(ctx, backend, digest)
	return r, err
}

func inspectionContentFailure(r Inspection, err error) (Inspection, *artifactmanifest.CompletedPublication, error) {
	r, err = inspectionFailure(r, err)
	return r, nil, err
}

func inspectEvidence(ctx context.Context, backend artifactmanifest.Backend, digest string) (Inspection, *artifactmanifest.CompletedPublication, error) {
	r := newInspection(digest)
	if err := ValidateDigest(digest); err != nil {
		return inspectionContentFailure(r, err)
	}
	if backend == nil {
		return inspectionContentFailure(r, errors.New("evidence backend is unavailable"))
	}
	data, err := backend.Get(ctx, "blob-"+digest, MaxDocumentBytes)
	if err != nil {
		return inspectionContentFailure(r, fmt.Errorf("reading derived evidence: %w", err))
	}
	if len(data) > MaxDocumentBytes || artifactmanifest.Hash(data) != digest {
		return inspectionContentFailure(r, artifactmanifest.ErrCorrupt)
	}
	d, err := Parse(data)
	if err != nil {
		return inspectionContentFailure(r, fmt.Errorf("%w: %w", artifactmanifest.ErrCorrupt, err))
	}
	canonical, err := Encode(*d)
	if err != nil {
		return inspectionContentFailure(r, err)
	}
	if !bytes.Equal(canonical, data) {
		return inspectionContentFailure(r, fmt.Errorf("%w: noncanonical evidence bytes", artifactmanifest.ErrCorrupt))
	}
	return inspectDocument(ctx, backend, *d, r)
}

func inspectDocument(ctx context.Context, backend artifactmanifest.Backend, d Document, r Inspection) (Inspection, *artifactmanifest.CompletedPublication, error) {
	if backend == nil {
		return inspectionContentFailure(r, errors.New("evidence backend is unavailable"))
	}
	if err := ctx.Err(); err != nil {
		return inspectionContentFailure(r, err)
	}
	store := artifactmanifest.NewStore(backend)
	loaded := make(map[string]bool, len(d.Lineage)+1)
	required := make(map[string]bool)
	if d.ExecutionSource != nil {
		required[d.AttemptArtifact] = true
	}
	for _, record := range d.Categories {
		for _, name := range record.Artifacts {
			required[name] = true
		}
	}
	names := make([]string, 0, len(required))
	for name := range required {
		names = append(names, name)
	}
	slices.Sort(names)
	var origin *artifactmanifest.CompletedPublication
	digests := []string{d.OriginManifest}
	for _, link := range d.Lineage {
		digests = append(digests, link.Manifest)
	}
	for _, digest := range digests {
		if loaded[digest] {
			continue
		}
		var selected []string
		if digest == d.OriginManifest {
			selected = names
		}
		completed, err := store.ReadCompleted(ctx, digest, selected...)
		if err != nil {
			if errors.Is(err, artifactmanifest.ErrInvalid) && !errors.Is(err, artifactmanifest.ErrCorrupt) {
				err = fmt.Errorf("%w: source artifact references: %w", artifactmanifest.ErrConflict, err)
			}
			return inspectionContentFailure(r, fmt.Errorf("reading completed evidence source: %w", err))
		}
		receipt := completed.Receipt
		// Do not export names or diagnostic text from unreferenced/restricted
		// source objects. The sidecar contains only its admitted references.
		receipt.Artifacts = []artifactmanifest.ArtifactState{}
		receipt.Diagnostics = []artifactmanifest.Diagnostic{}
		r.Sources = append(r.Sources, receipt)
		loaded[digest] = true
		if digest == d.OriginManifest {
			origin = completed
		}
	}
	if err := verifyReferencedArtifacts(d, &origin.Manifest, origin.ArtifactBytes); err != nil {
		return inspectionContentFailure(r, err)
	}
	r.Document = &d
	r.Integrity = "verified"
	return r, origin, nil
}
