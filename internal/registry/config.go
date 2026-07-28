// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

// Source describes a configured registry index that the CLI can query for
// discovery. Multiple sources are consulted in priority order by
// `waza registry search` (see design §12).
type Source struct {
	// Name is a short human identifier (e.g. "public", "company").
	Name string `yaml:"name" json:"name"`
	// URL is the base URL of the index. For the public default
	// this is the waza-evals GitHub org page; index format is
	// documented in docs/research/waza-eval-registry-design.md §12.
	URL string `yaml:"url" json:"url"`
	// Priority ranks sources during federated search. Lower numbers
	// are consulted first. Zero is treated as the default (100).
	Priority int `yaml:"priority,omitempty" json:"priority,omitempty"`
}

// Config holds the set of registry sources known to the waza CLI. The
// list is loaded from `waza.registry.yaml` (design §7) but the file
// itself is not required — a default source is always injected so that
// `waza registry search` works out of the box.
type Config struct {
	Sources []Source `yaml:"registries" json:"registries"`
}

// DefaultPublicSource is the built-in public registry pointing at the
// waza-evals GitHub org. It is always available unless the caller
// explicitly overrides the source list with a config file that omits it.
var DefaultPublicSource = Source{
	Name:     "public",
	URL:      "https://github.com/waza-evals",
	Priority: 100,
}

// DefaultConfig returns a Config seeded with the public waza-evals org.
// This is what the CLI uses when no user-supplied config is found.
func DefaultConfig() Config {
	return Config{Sources: []Source{DefaultPublicSource}}
}

// FindSource returns the configured source with the given name, or the
// zero value and false if not found.
func (c Config) FindSource(name string) (Source, bool) {
	for _, s := range c.Sources {
		if s.Name == name {
			return s, true
		}
	}
	return Source{}, false
}
