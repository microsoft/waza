// Package adversarial provides offline, deterministic fault-injection /
// adversarial test packs that ship inside the waza binary.
//
// Packs are simple bundles of YAML task files plus on-disk fixtures, embedded
// via go:embed. The `waza adversarial` command extracts the selected packs to
// a temporary directory, synthesizes an EvalSpec, and reuses the normal
// orchestration pipeline so adversarial runs share the exact same engine
// adapters, graders, snapshots, gate semantics, and JSON output schema as
// every other eval.
//
// Each pack ships at least one task and every task is marked golden so that
// `waza gate` treats unsafe outcomes as hard failures (exit code 2).
package adversarial

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// PackFS is the embedded filesystem holding every built-in pack under
// data/<pack-name>/.
//
//go:embed all:data
var PackFS embed.FS

// Manifest is the on-disk shape of a pack's pack.yaml.
type Manifest struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Version     int      `yaml:"version"`
	Tags        []string `yaml:"tags,omitempty"`
	Tasks       []string `yaml:"tasks"`
}

// Pack is a loaded adversarial pack: its parsed manifest plus the embedded
// path it lives at. Pack content is read lazily through Extract.
type Pack struct {
	Manifest Manifest
	// Root is the path inside PackFS where this pack lives, e.g.
	// "data/prompt-injection".
	Root string
}

// ErrUnknownPack is returned when a caller asks for a pack that does not
// exist among the built-in packs.
var ErrUnknownPack = errors.New("unknown adversarial pack")

const embedRoot = "data"

// ListPacks returns the sorted names of every built-in pack.
func ListPacks() []string {
	entries, err := fs.ReadDir(PackFS, embedRoot)
	if err != nil {
		// PackFS is embedded at build time; an error here means the package
		// itself is misbuilt.
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Skip entries that don't have a pack.yaml so we never expose
		// half-built packs.
		if _, err := fs.Stat(PackFS, filepath.ToSlash(filepath.Join(embedRoot, e.Name(), "pack.yaml"))); err != nil {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// LoadPack parses the manifest of the named pack and returns it. It does not
// extract any fixtures or task files to disk; use Extract for that.
func LoadPack(name string) (*Pack, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is empty", ErrUnknownPack)
	}
	root := filepath.ToSlash(filepath.Join(embedRoot, name))
	manifestPath := filepath.ToSlash(filepath.Join(root, "pack.yaml"))
	raw, err := fs.ReadFile(PackFS, manifestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %q (known packs: %s)", ErrUnknownPack, name, strings.Join(ListPacks(), ", "))
		}
		return nil, fmt.Errorf("read pack %q manifest: %w", name, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse pack %q manifest: %w", name, err)
	}
	if m.Name == "" {
		return nil, fmt.Errorf("pack %q: manifest is missing name", name)
	}
	if m.Name != name {
		return nil, fmt.Errorf("pack %q: manifest name %q does not match directory", name, m.Name)
	}
	if len(m.Tasks) == 0 {
		return nil, fmt.Errorf("pack %q: manifest lists no tasks", name)
	}
	return &Pack{Manifest: m, Root: root}, nil
}

// Extract copies the pack's task files and fixtures into dst. The layout
// inside dst is:
//
//	dst/<pack-name>/pack.yaml
//	dst/<pack-name>/tasks/...
//	dst/<pack-name>/fixtures/...
//
// Returns the absolute pack root under dst.
func (p *Pack) Extract(dst string) (string, error) {
	if p == nil {
		return "", errors.New("nil pack")
	}
	if dst == "" {
		return "", errors.New("destination path is empty")
	}
	absDst, err := filepath.Abs(dst)
	if err != nil {
		return "", fmt.Errorf("resolve destination: %w", err)
	}
	packRoot := filepath.Join(absDst, p.Manifest.Name)
	if err := os.MkdirAll(packRoot, 0o755); err != nil {
		return "", fmt.Errorf("create pack root: %w", err)
	}

	walkErr := fs.WalkDir(PackFS, p.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(path, p.Root)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			return nil
		}
		target := filepath.Join(packRoot, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(PackFS, path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create parent for %s: %w", target, err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	return packRoot, nil
}

// TaskRelPaths returns the manifest's task-file paths joined to the
// extracted pack root. The returned paths use OS separators.
func (p *Pack) TaskRelPaths(packRoot string) []string {
	out := make([]string, 0, len(p.Manifest.Tasks))
	for _, t := range p.Manifest.Tasks {
		out = append(out, filepath.Join(packRoot, filepath.FromSlash(t)))
	}
	return out
}
