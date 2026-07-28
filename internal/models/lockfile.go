package models

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

const LockfileName = "waza.lock"

// Lockfile pins remote grader refs to immutable source and content digests.
type Lockfile struct {
	SchemaVersion int              `yaml:"schema_version"`
	Graders       []LockfileGrader `yaml:"graders"`
	byRef         map[string]int   `yaml:"-"`
}

type LockfileGrader struct {
	Ref    string `yaml:"ref"`
	Commit string `yaml:"commit"`
	Digest string `yaml:"digest"`
	URL    string `yaml:"url"`
}

func NewLockfile() *Lockfile {
	return &Lockfile{SchemaVersion: 1}
}

func LoadLockfile(path string) (*Lockfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lock Lockfile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&lock); err != nil {
		return nil, fmt.Errorf("parsing lockfile %s: %w", path, err)
	}
	if lock.SchemaVersion == 0 {
		lock.SchemaVersion = 1
	}
	if err := lock.Validate(); err != nil {
		return nil, fmt.Errorf("validating lockfile %s: %w", path, err)
	}
	return &lock, nil
}

func WriteLockfile(path string, lock *Lockfile) error {
	if lock == nil {
		lock = NewLockfile()
	}
	if lock.SchemaVersion == 0 {
		lock.SchemaVersion = 1
	}
	if err := lock.Validate(); err != nil {
		return err
	}
	sort.SliceStable(lock.Graders, func(i, j int) bool {
		return lock.Graders[i].Ref < lock.Graders[j].Ref
	})
	data, err := yaml.Marshal(lock)
	if err != nil {
		return fmt.Errorf("encoding lockfile: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func (l *Lockfile) Validate() error {
	if l == nil {
		return fmt.Errorf("lockfile is nil")
	}
	if l.SchemaVersion != 1 {
		return fmt.Errorf("unsupported lockfile schema_version %d", l.SchemaVersion)
	}
	seen := make(map[string]bool, len(l.Graders))
	for i, g := range l.Graders {
		if g.Ref == "" {
			return fmt.Errorf("graders[%d].ref is required", i)
		}
		if g.Commit == "" {
			return fmt.Errorf("graders[%d].commit is required", i)
		}
		if g.Digest == "" {
			return fmt.Errorf("graders[%d].digest is required", i)
		}
		if g.URL == "" {
			return fmt.Errorf("graders[%d].url is required", i)
		}
		if seen[g.Ref] {
			return fmt.Errorf("duplicate lock entry for ref %q", g.Ref)
		}
		seen[g.Ref] = true
	}
	l.rebuildIndex()
	return nil
}

func (l *Lockfile) UpsertGrader(entry LockfileGrader) {
	if l.SchemaVersion == 0 {
		l.SchemaVersion = 1
	}
	l.rebuildIndex()
	if i, ok := l.byRef[entry.Ref]; ok {
		l.Graders[i] = entry
		return
	}
	l.Graders = append(l.Graders, entry)
	l.byRef[entry.Ref] = len(l.Graders) - 1
}

func (l *Lockfile) Grader(ref string) (LockfileGrader, bool) {
	l.rebuildIndex()
	i, ok := l.byRef[ref]
	if !ok {
		return LockfileGrader{}, false
	}
	return l.Graders[i], true
}

func (l *Lockfile) rebuildIndex() {
	l.byRef = make(map[string]int, len(l.Graders))
	for i, g := range l.Graders {
		l.byRef[g.Ref] = i
	}
}
