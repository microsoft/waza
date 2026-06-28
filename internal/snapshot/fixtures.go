package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// HashFixtures walks root and returns one FixtureDigest per regular file
// found, with paths recorded relative to root. Symlinks are followed only
// when they resolve to a regular file. Dot-prefixed directories are
// skipped (matches the runner's loadInstructions behavior for `.git`,
// `.vscode`, etc.).
//
// The returned slice is sorted by Path for deterministic snapshots.
func HashFixtures(root string) ([]FixtureDigest, error) {
	return HashFixturesExcluding(root, nil)
}

// HashFixturesExcluding is HashFixtures but also skips any directories
// (and their contents) whose absolute path matches an entry in skipDirs.
// This is used by the orchestrator to exclude the configured snapshot
// output directory so freshly-written snapshots do not perturb the fixture
// hash on subsequent runs.
func HashFixturesExcluding(root string, skipDirs []string) ([]FixtureDigest, error) {
	if root == "" {
		return nil, nil
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat fixtures root %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("fixtures root %s: not a directory", root)
	}

	skip := normaliseSkipDirs(skipDirs)

	var digests []FixtureDigest
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if absPath, absErr := filepath.Abs(path); absErr == nil {
				for _, s := range skip {
					if absPath == s {
						return filepath.SkipDir
					}
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// Try to follow a symlink if it resolves to a regular file.
			if d.Type()&fs.ModeSymlink == 0 {
				return nil
			}
			si, err := os.Stat(path)
			if err != nil || !si.Mode().IsRegular() {
				return nil
			}
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum, size, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("hash %s: %w", rel, err)
		}
		digests = append(digests, FixtureDigest{
			Path:   filepath.ToSlash(rel),
			Size:   size,
			SHA256: sum,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(digests, func(i, j int) bool {
		return digests[i].Path < digests[j].Path
	})
	return digests, nil
}

func normaliseSkipDirs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		abs, err := filepath.Abs(s)
		if err != nil {
			abs = s
		}
		out = append(out, abs)
	}
	return out
}

// hashFile streams the file at path and returns its sha256 hex digest and
// size in bytes. Using a streamed hash keeps memory bounded for large
// binaries (e.g., golden screenshots).
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	n, err := copyTo(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
