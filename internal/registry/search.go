// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

// Kind identifies a registry artifact type as described in design §4.
// Values are stringly-typed so that unknown kinds surfaced by remote
// indexes flow through unchanged.
type Kind string

const (
	KindGrader  Kind = "grader"
	KindEval    Kind = "eval"
	KindDataset Kind = "dataset"
	// KindProgramGrader is the executable-grader flavor of KindGrader.
	// Consumers use it to gate on --allow-exec (design §14).
	KindProgramGrader Kind = "program-grader"
)

// SearchResult is a single row returned from an index search. The shape
// mirrors the table columns documented in design §13.
type SearchResult struct {
	Ref         string `json:"ref"`
	Kind        Kind   `json:"kind"`
	Description string `json:"description,omitempty"`
	Stars       int    `json:"stars,omitempty"`
	Source      string `json:"source,omitempty"`
}

// SearchOptions carries the flag-level input for a search request.
type SearchOptions struct {
	// Query is the free-form user query.
	Query string
	// Kind, when non-empty, restricts results to that artifact kind.
	Kind Kind
	// Registry, when non-empty, restricts the search to a single
	// configured Source by Name.
	Registry string
}

// Searcher searches configured registry sources. Real implementations
// will fan out to each Source and merge/dedupe results (design §12).
type Searcher interface {
	Search(opts SearchOptions) ([]SearchResult, error)
}

// stubSearcher returns a hard-coded set of well-known refs so the CLI
// has meaningful output before the real index API in issue #15 lands.
type stubSearcher struct {
	cfg Config
}

// NewSearcher returns a Searcher that uses the given config. Today it is
// a stub; once issue #15 lands, this will build a federated HTTP client.
//
// TODO(#15): swap this stub for a real index-backed implementation.
func NewSearcher(cfg Config) Searcher {
	return &stubSearcher{cfg: cfg}
}

// canned known-ref catalog. Keep this list small — it is only meant to
// prove the CLI plumbing works. Real content will come from a proper
// index API.
var stubCatalog = []SearchResult{
	{
		Ref:         "github.com/waza-evals/fact#factuality@v1.0.0",
		Kind:        KindGrader,
		Description: "Prompt grader for factual grounding",
		Stars:       12,
	},
	{
		Ref:         "github.com/waza-evals/fact#closedqa@v1.0.0",
		Kind:        KindGrader,
		Description: "Closed-question answer evaluator",
		Stars:       9,
	},
	{
		Ref:         "github.com/waza-evals/agent-basics#repo-maintainer@v1.0.0",
		Kind:        KindEval,
		Description: "Baseline eval for repository maintenance agents",
		Stars:       7,
	},
	{
		Ref:         "github.com/waza-evals/datasets#humaneval@v0.1.0",
		Kind:        KindDataset,
		Description: "HumanEval-style dataset packaged for waza",
		Stars:       4,
	},
}

func (s *stubSearcher) Search(opts SearchOptions) ([]SearchResult, error) {
	// TODO(#15/#67): call registry index API. For now, filter the
	// in-memory stub catalog so CLI wiring, table formatting, and
	// JSON output can be exercised end-to-end.
	q := opts.Query
	var out []SearchResult
	for _, r := range stubCatalog {
		if opts.Kind != "" && r.Kind != opts.Kind {
			continue
		}
		if q != "" && !containsFold(r.Ref, q) && !containsFold(r.Description, q) {
			continue
		}
		// Tag every result with the primary configured source so users
		// can see which registry it came from once federation lands.
		if len(s.cfg.Sources) > 0 {
			r.Source = s.cfg.Sources[0].Name
		}
		if opts.Registry != "" && r.Source != opts.Registry {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// containsFold is a small case-insensitive substring helper. Kept
// private so we don't pull in strings.EqualFold semantics for every
// caller that only needs substring matching.
func containsFold(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	// Manual case fold to avoid an extra strings import elsewhere.
	h := make([]byte, len(haystack))
	for i := 0; i < len(haystack); i++ {
		c := haystack[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		h[i] = c
	}
	n := make([]byte, len(needle))
	for i := 0; i < len(needle); i++ {
		c := needle[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		n[i] = c
	}
	return bytesContains(h, n)
}

func bytesContains(h, n []byte) bool {
	if len(n) == 0 {
		return true
	}
	if len(n) > len(h) {
		return false
	}
outer:
	for i := 0; i <= len(h)-len(n); i++ {
		for j := 0; j < len(n); j++ {
			if h[i+j] != n[j] {
				continue outer
			}
		}
		return true
	}
	return false
}
