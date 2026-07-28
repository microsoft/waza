// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package registry

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// LockFileName is the conventional file name of the reproducibility
// lockfile (design §8).
const LockFileName = "waza.lock"

// LockSchemaVersion is the current lockfile schema version.
const LockSchemaVersion = 1

// LockEntry is a single resolved module recorded in waza.lock.
type LockEntry struct {
	Ref          string   `yaml:"ref" json:"ref"`
	Module       string   `yaml:"module" json:"module"`
	Version      string   `yaml:"version,omitempty" json:"version,omitempty"`
	Commit       string   `yaml:"commit,omitempty" json:"commit,omitempty"`
	Digest       string   `yaml:"digest,omitempty" json:"digest,omitempty"`
	Trusted      bool     `yaml:"trusted,omitempty" json:"trusted,omitempty"`
	ResolvedAt   string   `yaml:"resolved_at,omitempty" json:"resolved_at,omitempty"`
	Dependencies []string `yaml:"dependencies,omitempty" json:"dependencies,omitempty"`
}

// LockFile is the on-disk shape of waza.lock.
type LockFile struct {
	SchemaVersion int         `yaml:"schema_version" json:"schema_version"`
	Modules       []LockEntry `yaml:"modules" json:"modules"`
}

// LoadLockFile reads a lockfile from path. A missing file is reported as
// an empty LockFile with the current schema version so callers can
// unconditionally Upsert then Save.
func LoadLockFile(path string) (*LockFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &LockFile{SchemaVersion: LockSchemaVersion}, nil
		}
		return nil, fmt.Errorf("reading lock file %s: %w", path, err)
	}
	var lf LockFile
	if err := yaml.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("parsing lock file %s: %w", path, err)
	}
	if lf.SchemaVersion == 0 {
		lf.SchemaVersion = LockSchemaVersion
	}
	return &lf, nil
}

// Save writes the lockfile back to disk with stable field ordering.
func (lf *LockFile) Save(path string) error {
	if lf.SchemaVersion == 0 {
		lf.SchemaVersion = LockSchemaVersion
	}
	data, err := yaml.Marshal(lf)
	if err != nil {
		return fmt.Errorf("marshaling lock file: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing lock file %s: %w", path, err)
	}
	return nil
}

// Upsert inserts or replaces a lock entry keyed by Ref. It returns true
// if the entry was newly added.
func (lf *LockFile) Upsert(entry LockEntry) bool {
	if entry.ResolvedAt == "" {
		entry.ResolvedAt = time.Now().UTC().Format(time.RFC3339)
	}
	for i, e := range lf.Modules {
		if e.Ref == entry.Ref {
			lf.Modules[i] = entry
			return false
		}
	}
	lf.Modules = append(lf.Modules, entry)
	return true
}

// EntryFromResolution converts a resolver output into a LockEntry.
func EntryFromResolution(r Resolution) LockEntry {
	return LockEntry{
		Ref:        r.Ref.String(),
		Module:     r.Module,
		Version:    r.Version,
		Commit:     r.Commit,
		Digest:     r.Digest,
		Trusted:    r.Trusted,
		ResolvedAt: time.Now().UTC().Format(time.RFC3339),
	}
}
