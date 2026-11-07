package artifactmanifest

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/microsoft/waza/internal/runnerterminal"
)

// Backend must atomically make a complete object visible only when absent.
// Already-present objects are returned through Get and reverified by Store.
// There is deliberately no overwrite or delete operation.
type Backend interface {
	Create(context.Context, string, []byte) (bool, error)
	Get(context.Context, string, int64) ([]byte, error)
}

type Store struct{ backend Backend }

func NewStore(backend Backend) *Store { return &Store{backend: backend} }

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ArtifactState struct {
	Artifact
	Integrity string `json:"integrity"`
}

type Receipt struct {
	ManifestSHA256 string          `json:"manifest_sha256"`
	Attempt        string          `json:"attempt,omitempty"`
	Publication    string          `json:"publication"`
	Integrity      string          `json:"integrity"`
	Verification   string          `json:"verification"`
	Artifacts      []ArtifactState `json:"artifacts"`
	Diagnostics    []Diagnostic    `json:"diagnostics"`
}

func emptyReceipt(digest string) Receipt {
	return Receipt{ManifestSHA256: digest, Publication: "unfinalized", Integrity: "unverified",
		Verification: "content-and-declared-context-only", Artifacts: []ArtifactState{}, Diagnostics: []Diagnostic{}}
}

func (s *Store) create(ctx context.Context, key string, data []byte) error {
	if s == nil || s.backend == nil {
		return errors.New("immutable store backend is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.backend.Create(ctx, key, data); err != nil {
		return fmt.Errorf("creating immutable object: %w", err)
	}
	existing, err := s.backend.Get(ctx, key, int64(len(data)))
	if err != nil {
		return fmt.Errorf("verifying created immutable object: %w", err)
	}
	if err := verifyBytes(existing, Hash(data), int64(len(data))); err != nil {
		return err
	}
	if !bytes.Equal(existing, data) {
		return ErrConflict
	}
	return nil
}

// Put rejects incomplete or conflicting uploads before committing any object.
func (s *Store) Put(ctx context.Context, artifact Artifact, data []byte) error {
	if !validDigest(artifact.SHA256) || artifact.SizeBytes < 0 || artifact.SizeBytes > MaxArtifactBytes {
		return ErrInvalid
	}
	if err := verifyBytes(data, artifact.SHA256, artifact.SizeBytes); err != nil {
		return fmt.Errorf("upload size or digest mismatch: %w", err)
	}
	return s.create(ctx, "blob-"+artifact.SHA256, data)
}

// Create publishes the immutable declaration, not a completed result.
func (s *Store) Create(ctx context.Context, original []byte) (string, error) {
	m, err := Parse(original)
	if err != nil {
		return "", err
	}
	data, err := Encode(*m)
	if err != nil {
		return "", err
	}
	digest := Hash(data)
	if err := s.create(ctx, "manifest-"+digest, data); err != nil {
		return "", err
	}
	return digest, nil
}

func (s *Store) load(ctx context.Context, digest string) (*Manifest, []byte, error) {
	if !validDigest(digest) {
		return nil, nil, ErrInvalid
	}
	if s == nil || s.backend == nil {
		return nil, nil, errors.New("immutable store backend is unavailable")
	}
	data, err := s.backend.Get(ctx, "manifest-"+digest, MaxManifestBytes)
	if err != nil {
		return nil, nil, err
	}
	m, err := canonicalStored(data, digest)
	return m, data, err
}

func diagnostic(err error) Diagnostic {
	switch {
	case errors.Is(err, ErrMissing):
		return Diagnostic{"missing_object", "Upload the declared object and retry; no result has been verified."}
	case errors.Is(err, ErrCorrupt):
		return Diagnostic{"corrupt_object", "Stored bytes or metadata do not match the declaration. Preserve evidence and use a clean store; do not overwrite."}
	case errors.Is(err, ErrConflict):
		return Diagnostic{"publication_conflict", "The finalization record names different content. Preserve the record and inspect the storage conflict; never overwrite."}
	case errors.Is(err, ErrInvalid):
		return Diagnostic{"invalid_declaration", "Provide a valid v1 manifest and full lowercase SHA-256 digest."}
	default:
		return Diagnostic{"storage_error", "Storage could not be read or committed. Check operator diagnostics and retry without replacing evidence."}
	}
}

// Inspect always rereads current bytes. Error receipts carry only logical names
// and typed diagnostics; filesystem paths and provider URLs are not serialized.
func (s *Store) Inspect(ctx context.Context, digest string) (Receipt, error) {
	r, _, err := s.inspect(ctx, digest, false, nil)
	return r, err
}

func (s *Store) inspect(ctx context.Context, digest string, capture bool, names []string) (Receipt, *CompletedPublication, error) {
	r := emptyReceipt(digest)
	m, wire, err := s.load(ctx, digest)
	if err != nil {
		r.Diagnostics = append(r.Diagnostics, diagnostic(err))
		return r, nil, err
	}
	r.Attempt = m.Binding.Identity.Attempt
	selected := make(map[string]bool)
	var captured map[string][]byte
	if capture {
		if len(names) == 0 {
			names = []string{m.TerminalArtifact}
		}
		admitted := make(map[string]bool, len(m.Artifacts))
		for _, a := range m.Artifacts {
			admitted[a.Name] = true
		}
		for _, name := range names {
			if !admitted[name] || selected[name] {
				err := fmt.Errorf("%w: requested artifact is unknown or duplicated", ErrInvalid)
				r.Diagnostics = append(r.Diagnostics, diagnostic(err))
				return r, nil, err
			}
			selected[name] = true
		}
		captured = make(map[string][]byte, len(selected))
	}
	var failures []error
	var terminal []byte
	var terminalRead bool
	// Providers may read one extra byte per object to detect excess content.
	readBudget := int64(MaxTotalBytes) + int64(len(m.Artifacts))
	selectedBudget := int64(MaxTotalBytes)
	for _, a := range m.Artifacts {
		var data []byte
		var err error
		if a.SizeBytes+1 > readBudget {
			err = fmt.Errorf("%w: artifact read budget exceeded", ErrCorrupt)
		} else {
			readBudget -= a.SizeBytes + 1
			data, err = s.backend.Get(ctx, "blob-"+a.SHA256, a.SizeBytes)
		}
		if err == nil {
			err = verifyBytes(data, a.SHA256, a.SizeBytes)
		}
		if err == nil && selected[a.Name] {
			if int64(len(data)) > selectedBudget {
				err = fmt.Errorf("%w: selected artifact byte budget exceeded", ErrCorrupt)
			} else {
				selectedBudget -= int64(len(data))
				data = bytes.Clone(data)
				captured[a.Name] = data
			}
		}
		state := ArtifactState{Artifact: a, Integrity: "verified"}
		if err != nil {
			state.Integrity = "unverified"
			if errors.Is(err, ErrMissing) {
				state.Integrity = "missing"
			} else if errors.Is(err, ErrCorrupt) {
				state.Integrity = "corrupt"
			}
			d := diagnostic(err)
			d.Message = a.Name + ": " + d.Message
			r.Diagnostics = append(r.Diagnostics, d)
			failures = append(failures, err)
		} else if a.Name == m.TerminalArtifact {
			terminal = data
			terminalRead = true
		}
		r.Artifacts = append(r.Artifacts, state)
	}
	if terminalRead {
		if _, err := runnerterminal.Validate(terminal, m.Binding.TerminalBinding()); err != nil {
			failures = append(failures, fmt.Errorf("%w: terminal declared-context mismatch: %w", ErrCorrupt, err))
			r.Diagnostics = append(r.Diagnostics, Diagnostic{"terminal_mismatch", "Terminal bytes do not match the declared context. No execution attestation is provided."})
		}
	}
	marker, err := s.backend.Get(ctx, "final-"+digest, 64)
	switch {
	case errors.Is(err, ErrMissing):
	case errors.Is(err, ErrConflict):
		r.Publication = "conflict"
		failures = append(failures, err)
		r.Diagnostics = append(r.Diagnostics, diagnostic(err))
	case err != nil:
		failures = append(failures, err)
		r.Diagnostics = append(r.Diagnostics, diagnostic(err))
	case !validDigest(string(marker)):
		failures = append(failures, ErrCorrupt)
		r.Diagnostics = append(r.Diagnostics, diagnostic(ErrCorrupt))
	case string(marker) != digest:
		r.Publication = "conflict"
		failures = append(failures, ErrConflict)
		r.Diagnostics = append(r.Diagnostics, diagnostic(ErrConflict))
	default:
		r.Publication = "finalized"
	}
	if len(failures) == 0 {
		r.Integrity = "verified"
	} else {
		r.Integrity = "failed"
	}
	err = errors.Join(failures...)
	if !capture || err != nil || r.Publication != "finalized" || r.Integrity != "verified" {
		return r, nil, err
	}
	return r, &CompletedPublication{
		Manifest: *m, ManifestBytes: bytes.Clone(wire), ArtifactBytes: captured, Receipt: r,
	}, nil
}

// Finalize conditionally completes this manifest after content verification.
// Attempt ownership and duplicate decisions belong to the attempt ledger, not
// this content-addressed store; distinct attempts may share declared identities.
// Neither stored metadata nor a prior successful receipt bypasses rereading.
func (s *Store) Finalize(ctx context.Context, digest string) (Receipt, error) {
	r, err := s.Inspect(ctx, digest)
	if err != nil {
		return r, err
	}
	key := "final-" + digest
	if _, err := s.backend.Create(ctx, key, []byte(digest)); err != nil {
		r.Diagnostics = append(r.Diagnostics, diagnostic(err))
		r.Integrity = "unverified"
		return r, fmt.Errorf("committing finalization: %w", err)
	}
	return s.Inspect(ctx, digest)
}
