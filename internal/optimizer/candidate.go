package optimizer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Candidate limits keep proposals reviewable and bound materialization cost.
const (
	MaxCandidateFiles     = 64
	MaxCandidateFileBytes = 256 << 10
	MaxCandidateBytes     = 1 << 20
	maxCandidatePathLen   = 200
	maxCandidatePathDepth = 8
)

// Candidate origins.
const (
	OriginSeed    = "seed"
	OriginAdapter = "adapter"
)

// FileContent is a candidate file carried in adapter requests and responses.
// Content must be valid UTF-8 text.
type FileContent struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// FileDigest identifies one immutable candidate file in reports.
type FileDigest struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

// Candidate is immutable, content-addressed lineage for one proposal.
type Candidate struct {
	ID             string       `json:"id"`
	ParentID       string       `json:"parent_id,omitempty"`
	Origin         string       `json:"origin"`
	Adapter        string       `json:"adapter"`
	AdapterVersion string       `json:"adapter_version"`
	Iteration      int          `json:"iteration"`
	Target         Target       `json:"target"`
	Files          []FileDigest `json:"files"`
	Rationale      string       `json:"rationale,omitempty"`
	// SavedPath is relative to the report directory.
	SavedPath string `json:"saved_path,omitempty"`
}

var (
	segmentPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	windowsReserved = regexp.MustCompile(`^(?i)(con|prn|aux|nul|com[0-9]|lpt[0-9])$`)
	skillEntrypoint = "SKILL.md"
)

// ValidateCandidatePath enforces the portable relative path policy.
func ValidateCandidatePath(p string) error {
	if p == "" || len(p) > maxCandidatePathLen {
		return newError(CategoryUnsafeCandidatePath, "candidate path %q must be 1-%d bytes", p, maxCandidatePathLen)
	}
	if path.Clean(p) != p || strings.HasPrefix(p, "/") {
		return newError(CategoryUnsafeCandidatePath, "candidate path %q must be a clean relative slash path", p)
	}
	segments := strings.Split(p, "/")
	if len(segments) > maxCandidatePathDepth {
		return newError(CategoryUnsafeCandidatePath, "candidate path %q exceeds depth %d", p, maxCandidatePathDepth)
	}
	for _, seg := range segments {
		if !segmentPattern.MatchString(seg) || strings.HasSuffix(seg, ".") {
			return newError(CategoryUnsafeCandidatePath,
				"candidate path %q segment %q must use [A-Za-z0-9._-], start alphanumeric and not end with '.'", p, seg)
		}
		base, _, _ := strings.Cut(seg, ".")
		if windowsReserved.MatchString(base) {
			return newError(CategoryUnsafeCandidatePath, "candidate path %q uses reserved name %q", p, seg)
		}
		if strings.EqualFold(seg, ".git") {
			return newError(CategoryUnsafeCandidatePath, "candidate path %q may not contain .git", p)
		}
	}
	return nil
}

// validateFiles checks paths, sizes, encoding and collisions and returns the
// files sorted by path.
func validateFiles(files []FileContent, target Target) ([]FileContent, error) {
	if len(files) == 0 {
		return nil, newError(CategoryMalformedProposal, "candidate has no files")
	}
	if len(files) > MaxCandidateFiles {
		return nil, newError(CategoryMalformedProposal, "candidate has %d files; limit is %d", len(files), MaxCandidateFiles)
	}
	sorted := make([]FileContent, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	total := 0
	folded := map[string]string{}
	for _, f := range sorted {
		if err := ValidateCandidatePath(f.Path); err != nil {
			return nil, err
		}
		if len(f.Content) > MaxCandidateFileBytes {
			return nil, newError(CategoryMalformedProposal, "candidate file %q is %d bytes; limit is %d", f.Path, len(f.Content), MaxCandidateFileBytes)
		}
		if !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) {
			return nil, newError(CategoryMalformedProposal, "candidate file %q must be UTF-8 text without NUL", f.Path)
		}
		total += len(f.Content)
		key := strings.ToLower(f.Path)
		if prev, ok := folded[key]; ok {
			return nil, newError(CategoryUnsafeCandidatePath, "candidate paths %q and %q collide", prev, f.Path)
		}
		folded[key] = f.Path
	}
	if total > MaxCandidateBytes {
		return nil, newError(CategoryMalformedProposal, "candidate is %d bytes; limit is %d", total, MaxCandidateBytes)
	}
	// A file may not also be used as a directory of another file.
	for _, f := range sorted {
		dir := path.Dir(f.Path)
		for dir != "." {
			if prev, ok := folded[strings.ToLower(dir)]; ok {
				return nil, newError(CategoryUnsafeCandidatePath, "candidate path %q conflicts with file %q", f.Path, prev)
			}
			dir = path.Dir(dir)
		}
	}
	if target == TargetSkillDirectory {
		if _, ok := folded[strings.ToLower(skillEntrypoint)]; !ok || folded[strings.ToLower(skillEntrypoint)] != skillEntrypoint {
			return nil, newError(CategoryMalformedProposal, "skill-directory candidates must include %s", skillEntrypoint)
		}
	}
	return sorted, nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// candidateIdentity returns the content-addressed ID and file digests.
// Lineage (parent, iteration, adapter) is deliberately excluded so identical
// content always has the same identity.
func candidateIdentity(target Target, files []FileContent) (string, []FileDigest) {
	digests := make([]FileDigest, len(files))
	for i, f := range files {
		digests[i] = FileDigest{Path: f.Path, SHA256: sha256Hex([]byte(f.Content)), Size: len(f.Content)}
	}
	type entry struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	entries := make([]entry, len(digests))
	for i, d := range digests {
		entries[i] = entry{d.Path, d.SHA256}
	}
	canonical, _ := json.Marshal(struct {
		Contract string  `json:"contract"`
		Target   Target  `json:"target"`
		Files    []entry `json:"files"`
	}{ContractVersion, target, entries})
	return "sha256:" + sha256Hex(canonical), digests
}

func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "...(truncated)"
}

// LoadSeed reads a seed candidate from regular files under dir. Symlinks and
// other special files are rejected rather than followed.
func LoadSeed(dir string, target Target) ([]FileContent, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, wrapError(CategoryInvalidConfig, err, "reading seed directory %q", dir)
	}
	if !info.IsDir() {
		return nil, newError(CategoryInvalidConfig, "seed %q must be a directory", dir)
	}
	var files []FileContent
	total := 0
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !d.Type().IsRegular() {
			return newError(CategoryUnsafeCandidatePath, "seed entry %q is not a regular file", rel)
		}
		if len(files) >= MaxCandidateFiles {
			return newError(CategoryInvalidConfig, "seed has more than %d files", MaxCandidateFiles)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		total += len(data)
		if total > MaxCandidateBytes {
			return newError(CategoryInvalidConfig, "seed exceeds %d bytes", MaxCandidateBytes)
		}
		files = append(files, FileContent{Path: rel, Content: string(data)})
		return nil
	})
	if err != nil {
		if CategoryOf(err) != "" {
			return nil, err
		}
		return nil, wrapError(CategoryInvalidConfig, err, "reading seed directory %q", dir)
	}
	return validateFiles(files, target)
}

// materialize writes files beneath dir through an os.Root so no path can
// escape the isolated candidate directory.
func materialize(dir string, files []FileContent) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, f := range files {
		if err := ValidateCandidatePath(f.Path); err != nil {
			return err
		}
		native := filepath.FromSlash(f.Path)
		if parent := filepath.Dir(native); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return err
			}
		}
		if err := root.WriteFile(native, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("writing candidate file %q: %w", f.Path, err)
		}
	}
	return nil
}

// copyTree copies regular files and directories from src into dst. Symlinks
// and special files fail closed so isolation cannot be bypassed.
func copyTree(src, dst string) error {
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		switch {
		case d.IsDir():
			return root.MkdirAll(rel, 0o755)
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return root.WriteFile(rel, data, 0o644)
		default:
			return fmt.Errorf("context entry %q is not a regular file or directory", filepath.ToSlash(rel))
		}
	})
}

// treeDigest hashes every entry under dir in sorted order. skip receives
// absolute paths of directories to exclude.
func treeDigest(dir string, skip func(string) bool) (string, error) {
	const maxEntries = 50000
	h := sha256.New()
	count := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && p != dir && (d.Name() == ".git" || (skip != nil && skip(p))) {
			return filepath.SkipDir
		}
		count++
		if count > maxEntries {
			return fmt.Errorf("more than %d entries under %s", maxEntries, dir)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			_, _ = fmt.Fprintf(h, "D %s\n", rel)
		case d.Type()&fs.ModeSymlink != 0:
			// Link text alone would not pin the target's content.
			return fmt.Errorf("symlink %s is not supported in a pinned directory", rel)
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(h, "F %s %s\n", rel, sha256Hex(data))
		default:
			_, _ = fmt.Fprintf(h, "S %s\n", rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
