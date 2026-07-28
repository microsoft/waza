// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import "testing"

func TestSearcherFiltersByKind(t *testing.T) {
	s := NewSearcher(DefaultConfig())
	got, err := s.Search(SearchOptions{Kind: KindGrader})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected results")
	}
	for _, r := range got {
		if r.Kind != KindGrader {
			t.Errorf("got kind %q, want grader", r.Kind)
		}
	}
}

func TestSearcherFiltersByQuery(t *testing.T) {
	s := NewSearcher(DefaultConfig())
	got, err := s.Search(SearchOptions{Query: "factuality"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected results")
	}
	for _, r := range got {
		if !containsFold(r.Ref, "factuality") && !containsFold(r.Description, "factuality") {
			t.Errorf("result %+v does not match query", r)
		}
	}
}

func TestSearcherRegistryFilter(t *testing.T) {
	s := NewSearcher(DefaultConfig())
	got, err := s.Search(SearchOptions{Registry: "public"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected some results from default public source")
	}
	empty, err := s.Search(SearchOptions{Registry: "nonexistent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("expected 0 results for unknown registry, got %d", len(empty))
	}
}

func TestDefaultConfigIncludesPublic(t *testing.T) {
	cfg := DefaultConfig()
	if _, ok := cfg.FindSource("public"); !ok {
		t.Error("default config missing public source")
	}
}
