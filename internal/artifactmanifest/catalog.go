package artifactmanifest

import (
	"context"
	"errors"
	"slices"
	"strings"
)

const MaxCatalogEntries = 1000

// Catalog lists persisted declaration pointers, not verified result summaries.
// Truncated is explicit; callers cannot treat discovery as fleet completeness.
type Catalog struct {
	ManifestSHA256 []string     `json:"manifest_sha256"`
	Truncated      bool         `json:"truncated"`
	Verification   string       `json:"verification"`
	Diagnostics    []Diagnostic `json:"diagnostics"`
}

type manifestLister interface {
	ListManifests(context.Context) ([]string, bool, error)
}

func (s *Store) Catalog(ctx context.Context) (Catalog, error) {
	result := Catalog{ManifestSHA256: []string{}, Verification: "unverified-pointers-only", Diagnostics: []Diagnostic{}}
	if s == nil || s.backend == nil {
		err := errors.New("immutable backend is unavailable")
		result.Diagnostics = append(result.Diagnostics, diagnostic(err))
		return result, err
	}
	lister, ok := s.backend.(manifestLister)
	if !ok {
		err := errors.New("immutable backend does not support manifest discovery")
		result.Diagnostics = append(result.Diagnostics, diagnostic(err))
		return result, err
	}
	keys, truncated, err := lister.ListManifests(ctx)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, diagnostic(err))
		return result, err
	}
	for _, key := range keys {
		if strings.HasPrefix(key, "manifest-") && validKey(key) {
			result.ManifestSHA256 = append(result.ManifestSHA256, strings.TrimPrefix(key, "manifest-"))
		}
	}
	slices.Sort(result.ManifestSHA256)
	result.Truncated = truncated
	return result, nil
}
