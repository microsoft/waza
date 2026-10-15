package nativesnapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/microsoft/waza/internal/rootedfile"
)

const (
	maxSourceBytes     = 16 << 20
	maxExecutableBytes = 128 << 20
	maxTotalBytes      = 256 << 20
	maxProjectionBytes = 16 << 20
	maxTasks           = 1024
	maxEntries         = 16384
)

type source struct {
	Path   string
	Roles  []string
	Bytes  []byte
	Digest models.EvidenceDigest
}

// Captured files are evaluator-only. Metadata is retained for native fixture
// traversal; only files actually consumed enter the unique source inventory.
type capturedFiles struct {
	ctx     context.Context
	root    *os.Root
	base    string
	data    map[string][]byte
	info    map[string]os.FileInfo
	dirs    map[string][]fs.DirEntry
	roles   map[string]map[string]bool
	total   *int
	entries int
	frozen  bool
	sticky  error
	role    string
}

type discoveryFiles struct{ *capturedFiles }

func (f discoveryFiles) Stat(path string) (os.FileInfo, error) {
	return f.capturedFiles.Stat(filepath.Join(f.base, path))
}

func (f *capturedFiles) SetInputRole(role string) { f.role = role }

func canonical(path string, allowDot bool) error {
	if path == "." && allowDot {
		return nil
	}
	if path == "." || !filepath.IsLocal(path) || filepath.ToSlash(filepath.Clean(path)) != path || strings.Contains(path, "\\") {
		return fmt.Errorf("unsupported noncanonical rooted path %q", path)
	}
	return nil
}

func (f *capturedFiles) relative(path string) (string, error) {
	if err := f.ctx.Err(); err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("captured input requires a canonical absolute associated path")
	}
	rel, err := filepath.Rel(f.base, path)
	if err != nil {
		return "", err
	}
	if err := canonical(rel, true); err != nil {
		return "", err
	}
	return rel, nil
}

func (f *capturedFiles) check(path string) (os.FileInfo, error) {
	if err := canonical(path, true); err != nil {
		return nil, err
	}
	parts := strings.Split(path, "/")
	var info os.FileInfo
	for i := range parts {
		if err := f.ctx.Err(); err != nil {
			return nil, err
		}
		var err error
		info, err = f.root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return nil, fmt.Errorf("unsupported symlink or special input %q", path)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("input parent is not a directory")
		}
	}
	return info, nil
}

func same(before, after os.FileInfo) bool {
	return os.SameFile(before, after) && before.Mode() == after.Mode() &&
		before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func (f *capturedFiles) ReadFile(path string) (data []byte, err error) {
	defer func() {
		if err != nil && f.sticky == nil {
			f.sticky = err
		}
	}()
	rel, err := f.relative(path)
	if err != nil {
		return nil, err
	}
	if data, ok := f.data[rel]; ok {
		f.addRole(rel)
		return bytes.Clone(data), nil
	}
	if f.frozen {
		return nil, fmt.Errorf("uncaptured input %q", rel)
	}
	before, err := f.check(rel)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("input %q is not a regular file", rel)
	}
	for prior := range f.data {
		if prior != rel && os.SameFile(before, f.info[prior]) {
			return nil, fmt.Errorf("unsupported physical source path alias %q", rel)
		}
	}
	limit := maxSourceBytes
	if f.role == "executable" {
		limit = maxExecutableBytes
	}
	if before.Size() > int64(limit) {
		return nil, fmt.Errorf("input %q exceeds source limit", rel)
	}
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	file, err := rootedfile.Open(f.root, rel)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("checking opened input: %w", err)
	}
	if !opened.Mode().IsRegular() || !same(before, opened) {
		return nil, fmt.Errorf("input changed before read")
	}
	var output bytes.Buffer
	buf := make([]byte, 64<<10)
	for {
		if err := f.ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := file.Read(buf)
		output.Write(buf[:n])
		if output.Len() > limit || *f.total+output.Len() > maxTotalBytes {
			return nil, fmt.Errorf("captured source byte limit exceeded")
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	descriptorAfter, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("checking opened input after read: %w", err)
	}
	if !descriptorAfter.Mode().IsRegular() || !same(opened, descriptorAfter) ||
		int64(output.Len()) != descriptorAfter.Size() {
		return nil, fmt.Errorf("input changed during read")
	}
	after, err := f.check(rel)
	if err != nil {
		return nil, fmt.Errorf("checking input after read: %w", err)
	}
	if !same(before, after) || int64(output.Len()) != before.Size() {
		return nil, fmt.Errorf("input changed during read")
	}
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	f.data[rel] = bytes.Clone(output.Bytes())
	f.info[rel] = before
	*f.total += output.Len()
	f.addRole(rel)
	return bytes.Clone(output.Bytes()), nil
}

func (f *capturedFiles) addRole(path string) {
	if f.roles[path] == nil {
		f.roles[path] = map[string]bool{}
	}
	f.roles[path][f.role] = true
}

func (f *capturedFiles) Stat(path string) (os.FileInfo, error) {
	rel, err := f.relative(path)
	if err != nil {
		return nil, err
	}
	if info, ok := f.info[rel]; ok {
		return info, nil
	}
	if f.frozen {
		return nil, fmt.Errorf("uncaptured stat %q", rel)
	}
	info, err := f.check(rel)
	if err == nil {
		f.info[rel] = info
	}
	return info, err
}

func (f *capturedFiles) EvalSymlinks(path string) (string, error) {
	_, err := f.Stat(path)
	return path, err // capture has rejected every symlink component
}

// ReadDir is also used by fs.Glob, whose ordering matches filepath.Glob for the
// supported canonical slash patterns. Keep errors sticky: Glob ignores I/O errors.
func (f *capturedFiles) ReadDir(path string) (entries []fs.DirEntry, err error) {
	defer func() {
		if err != nil && f.sticky == nil {
			f.sticky = err
		}
	}()
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	if entries, ok := f.dirs[path]; ok {
		return append([]fs.DirEntry(nil), entries...), nil
	}
	if f.frozen {
		return nil, fmt.Errorf("uncaptured directory %q", path)
	}
	before, err := f.check(path)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("glob/fixture input is not a directory")
	}
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := rootedfile.Open(f.root, path)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil, fmt.Errorf("unsupported native directory discovery on Windows; prepare native snapshots on a supported Unix platform: %w", err)
		}
		return nil, err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	opened, err := dir.Stat()
	if err != nil {
		return nil, fmt.Errorf("checking opened directory: %w", err)
	}
	if !opened.IsDir() || !same(before, opened) {
		return nil, fmt.Errorf("directory changed before capture")
	}
	for {
		if err := f.ctx.Err(); err != nil {
			return nil, err
		}
		batch, readErr := dir.ReadDir(128)
		f.entries += len(batch)
		if f.entries > maxEntries {
			return nil, fmt.Errorf("native discovery exceeds bounded directory entries")
		}
		entries = append(entries, batch...)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	descriptorAfter, err := dir.Stat()
	if err != nil {
		return nil, fmt.Errorf("checking opened directory after capture: %w", err)
	}
	if !descriptorAfter.IsDir() || !same(opened, descriptorAfter) {
		return nil, fmt.Errorf("directory changed during capture")
	}
	after, err := f.check(path)
	if err != nil {
		return nil, fmt.Errorf("checking directory after capture: %w", err)
	}
	if !same(before, after) {
		return nil, fmt.Errorf("directory changed during capture")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	f.info[path] = before
	f.dirs[path] = append([]fs.DirEntry(nil), entries...)
	return entries, nil
}

func (f discoveryFiles) Open(string) (fs.File, error) {
	err := fmt.Errorf("native discovery requires bounded Stat/ReadDir, not raw Open")
	f.sticky = err
	return nil, err
}

func (f *capturedFiles) WalkDir(path string, fn fs.WalkDirFunc) error {
	rel, err := f.relative(path)
	if err != nil {
		return err
	}
	var walk func(string) error
	walk = func(current string) error {
		info, err := f.Stat(filepath.Join(f.base, current))
		if err != nil {
			return err
		}
		entry := fs.FileInfoToDirEntry(info)
		if err := fn(filepath.Join(f.base, current), entry, nil); err != nil {
			return err
		}
		if !info.IsDir() {
			return nil
		}
		entries, err := f.ReadDir(current)
		if err != nil {
			return err
		}
		for _, child := range entries {
			if err := walk(filepath.Join(current, child.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(rel)
}

func (f *capturedFiles) inventory() []source {
	result := make([]source, 0, len(f.data))
	for path, data := range f.data {
		roles := make([]string, 0, len(f.roles[path]))
		for role := range f.roles[path] {
			roles = append(roles, role)
		}

		sort.Strings(roles)
		result = append(result, source{path, roles, bytes.Clone(data), releasepolicy.SourceDigest(data)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func (f *capturedFiles) verifyStable() error {
	for path, before := range f.info {
		after, err := f.check(path)
		if err != nil {
			return err
		}
		if !same(before, after) {
			return fmt.Errorf("native input or discovery directory changed during capture")
		}
	}
	return nil
}
