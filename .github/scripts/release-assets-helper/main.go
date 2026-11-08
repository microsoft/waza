// release-assets-helper verifies release dashboard handoffs. It does not build
// the dashboard, admit a release, or prove native-platform execution.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/rootedfile"
)

const (
	maxBytes    = 100 << 20
	maxManifest = 1 << 20
	maxFiles    = 4096
)

type identity struct {
	Repository string `json:"repository"`
	SourceSHA  string `json:"source_sha"`
	RunID      string `json:"run_id"`
	RunAttempt string `json:"run_attempt"`
}

type entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Version int      `json:"version"`
	Root    string   `json:"root"`
	Source  identity `json:"source"`
	Files   []entry  `json:"files"`
}

var (
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	numberPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
	bundlePattern = regexp.MustCompile(`^assets/[A-Za-z0-9_.-]+-[A-Za-z0-9_-]{8,}\.(js|css)$`)
	referenceRE   = regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

func (id identity) validate(checkout string) error {
	if id.Repository != "microsoft/waza" || !commitPattern.MatchString(id.SourceSHA) ||
		!numberPattern.MatchString(id.RunID) || !numberPattern.MatchString(id.RunAttempt) {
		return errors.New("require microsoft/waza, full lowercase source SHA, and positive run ID and attempt")
	}
	if checkout != id.SourceSHA {
		return errors.New("checkout SHA must match the expected full source SHA")
	}
	return nil
}

func safePath(name string) bool {
	if name == "" || name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "\\:\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || strings.TrimSpace(part) != part || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

// Fault hooks are confined to the actual file lifecycle, not a second test
// implementation. Production uses nil hooks and ordinary descriptor operations.
type hooks struct {
	beforeOpen func(string) error
	beforeRoot func(string) error
	beforeCopy func() error
	discover   func(*os.File) ([]fs.DirEntry, error)
	read       func(*os.File) ([]byte, error)
	write      func(*os.File, []byte) error
	close      func(*os.File) error
	closeRoot  func(*os.Root) error
}

type session struct {
	roots       []*os.Root
	files       []*os.File
	directories map[string]*os.Root
	hooks       hooks
	observed    []observation
	rootEntries map[*os.Root]observation
	rootCache   map[rootedName]*os.Root
	fileCache   map[rootedName]*os.File
	filesystem  *os.Root
}

type rootedName struct {
	parent *os.Root
	name   string
}

type observation struct {
	parent *os.Root
	name   string
	info   fs.FileInfo
}

func (s *session) close() error {
	var errs []error
	for _, f := range s.files {
		errs = append(errs, s.closeFile(f))
	}
	for i := len(s.roots) - 1; i >= 0; i-- {
		r := s.roots[i]
		if s.hooks.closeRoot != nil {
			errs = append(errs, s.hooks.closeRoot(r))
		} else {
			errs = append(errs, r.Close())
		}
	}
	return errors.Join(errs...)
}

func (s *session) closeFile(f *os.File) error {
	if s.hooks.close != nil {
		return s.hooks.close(f)
	}
	return f.Close()
}

// Every path component is opened relative to a retained directory handle.
// Identity checks reject replacements observed during admission.
func (s *session) child(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect directory %q: %w", name, err)
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("directory %q must not be a symlink or non-directory", name)
	}
	if s.hooks.beforeRoot != nil {
		if err := s.hooks.beforeRoot(name); err != nil {
			return nil, err
		}
	}
	key := rootedName{parent, name}
	r := s.rootCache[key]
	if r == nil {
		r, err = parent.OpenRoot(name)
		if err != nil {
			return nil, fmt.Errorf("open directory %q: %w", name, err)
		}
		s.roots = append(s.roots, r)
	}
	opened, err := r.Stat(".")
	if err != nil {
		return nil, err
	}
	after, err := parent.Lstat(name)
	if err != nil || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return nil, fmt.Errorf("directory %q changed during admission: %w", name, errors.Join(err, errors.New("identity mismatch")))
	}
	s.observed = append(s.observed, observation{parent, name, opened})
	if s.rootEntries != nil {
		s.rootEntries[r] = observation{parent, name, opened}
	}
	if s.rootCache == nil {
		s.rootCache = make(map[rootedName]*os.Root)
	}
	s.rootCache[key] = r
	return r, nil
}

func (s *session) openRoot(name string) (*os.Root, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, errors.New("rooted release asset helper supports Linux and macOS hosts only")
	}
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	r := s.filesystem
	if r == nil {
		r, err = os.OpenRoot(string(filepath.Separator))
		if err != nil {
			return nil, err
		}
		s.roots = append(s.roots, r)
		s.filesystem = r
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, string(filepath.Separator)), string(filepath.Separator)) {
		if part != "" {
			r, err = s.child(r, part)
			if err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

func (s *session) regular(r *os.Root, name string) (*os.File, error) {
	before, err := r.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect file %q: %w", name, err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("file %q must be regular, not a symlink or special file", name)
	}
	if s.hooks.beforeOpen != nil {
		if err := s.hooks.beforeOpen(name); err != nil {
			return nil, err
		}
	}
	key := rootedName{r, name}
	f := s.fileCache[key]
	if f == nil {
		f, err = rootedfile.Open(r, name)
		if err != nil {
			return nil, fmt.Errorf("open file %q: %w", name, err)
		}
		s.files = append(s.files, f)
	}
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	after, err := r.Lstat(name)
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return nil, fmt.Errorf("file %q changed during admission: %w", name, errors.Join(err, errors.New("identity mismatch")))
	}
	s.observed = append(s.observed, observation{r, name, opened})
	if s.fileCache == nil {
		s.fileCache = make(map[rootedName]*os.File)
	}
	s.fileCache[key] = f
	return f, nil
}

func (s *session) read(f *os.File, limit int64) ([]byte, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var data []byte
	var err error
	if s.hooks.read != nil {
		data, err = s.hooks.read(f)
	} else {
		data, err = io.ReadAll(io.LimitReader(f, limit+1))
	}
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", f.Name(), err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file %q exceeds %d-byte limit", f.Name(), limit)
	}
	return data, nil
}

type snapshot struct {
	files []entry
	data  map[string][]byte
	dirs  []string
}

func sum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (s *session) inventory(r *os.Root) (snapshot, error) {
	return s.inventoryWithDirectories(r, nil)
}

func (s *session) inventoryWithDirectories(r *os.Root, intended map[string]bool) (snapshot, error) {
	result := snapshot{data: make(map[string][]byte)}
	var total int64
	var walk func(*os.Root, string) error
	walk = func(dir *os.Root, prefix string) error {
		if len(s.roots) > maxFiles || strings.Count(prefix, "/") > 64 {
			return errors.New("asset inventory exceeds directory or depth limit")
		}
		f, err := dir.Open(".")
		if err != nil {
			return err
		}
		var names []fs.DirEntry
		var readErr error
		if s.hooks.discover != nil {
			names, readErr = s.hooks.discover(f)
		} else {
			for {
				batch, err := f.ReadDir(128)
				names = append(names, batch...)
				if len(names) > 2*maxFiles {
					readErr = errors.New("asset directory exceeds entry limit")
					break
				}
				if err != nil {
					if err != io.EOF {
						readErr = err
					}
					break
				}
			}
		}
		if len(names) > 2*maxFiles {
			readErr = errors.Join(readErr, errors.New("asset directory exceeds entry limit"))
		}
		closeErr := s.closeFile(f)
		if err := errors.Join(readErr, closeErr); err != nil {
			return fmt.Errorf("discover %q: %w", prefix, err)
		}
		slices.SortFunc(names, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		for _, item := range names {
			name := path.Join(prefix, item.Name())
			if !safePath(name) {
				return fmt.Errorf("unsafe inventory path %q", name)
			}
			info, err := dir.Lstat(item.Name())
			if err != nil {
				return err
			}
			if info.IsDir() {
				if intended != nil && !intended[name] {
					return fmt.Errorf("unexpected output directory %q", name)
				}
				child, err := s.child(dir, item.Name())
				if err != nil {
					return err
				}
				result.dirs = append(result.dirs, name)
				if intended != nil && s.directories[name] == nil {
					s.directories[name] = child
				}
				count := len(result.files)
				if err := walk(child, name); err != nil {
					return err
				}
				if len(result.files) == count && intended == nil {
					return fmt.Errorf("empty directory %q is not represented by the file inventory", name)
				}
				continue
			}
			if len(result.files) >= maxFiles {
				return errors.New("asset inventory exceeds file limit")
			}
			file, err := s.regular(dir, item.Name())
			if err != nil {
				return err
			}
			data, err := s.read(file, maxBytes-total)
			if err != nil {
				return err
			}
			total += int64(len(data))
			result.files = append(result.files, entry{Path: name, Size: int64(len(data)), SHA256: sum(data)})
			result.data[name] = data
		}
		return nil
	}
	if err := walk(r, ""); err != nil {
		return snapshot{}, err
	}
	if err := s.checkObserved(); err != nil {
		return snapshot{}, fmt.Errorf("recheck inventory observations: %w", err)
	}
	slices.SortFunc(result.files, func(a, b entry) int { return strings.Compare(a.Path, b.Path) })
	slices.Sort(result.dirs)
	return result, nil
}

func (snap snapshot) validate() error {
	index := snap.data["index.html"]
	if len(index) == 0 || len(snap.data["favicon.svg"]) == 0 {
		return errors.New("dashboard requires nonempty index.html and favicon.svg")
	}
	var js, css bool
	for _, file := range snap.files {
		if bundlePattern.MatchString(file.Path) && file.Size > 0 {
			js = js || strings.HasSuffix(file.Path, ".js")
			css = css || strings.HasSuffix(file.Path, ".css")
		}
	}
	if !js || !css {
		return errors.New("dashboard requires nonempty hashed JS and CSS bundles")
	}
	var referencedJS, referencedCSS bool
	for _, match := range referenceRE.FindAllSubmatch(index, -1) {
		value := string(match[1])
		if value == "" {
			value = string(match[2])
		}
		ref, err := url.Parse(value)
		if err != nil || ref.IsAbs() || ref.Host != "" {
			return fmt.Errorf("index reference %q must be a local asset", value)
		}
		if ref.Path == "" {
			continue
		}
		name := strings.TrimPrefix(ref.Path, "/")
		if !safePath(name) || len(snap.data[name]) == 0 {
			return fmt.Errorf("index reference %q has no nonempty inventory file", value)
		}
		if bundlePattern.MatchString(name) {
			referencedJS = referencedJS || strings.HasSuffix(name, ".js")
			referencedCSS = referencedCSS || strings.HasSuffix(name, ".css")
		}
	}
	if !referencedJS || !referencedCSS {
		return errors.New("index must reference hashed JS and CSS inventory files")
	}
	return nil
}

func decodeManifest(data []byte, expected identity) (manifest, error) {
	var m manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		return m, fmt.Errorf("decode manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return m, errors.New("manifest must contain exactly one JSON value")
	}
	// Canonical bytes also reject duplicate fields and alternate wire types.
	canonical, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	if !bytes.Equal(data, canonical) {
		return m, errors.New("manifest must use canonical helper-generated JSON without duplicate or extra fields")
	}
	if m.Version != 1 || m.Root != "web/dist" || m.Source != expected {
		return m, errors.New("manifest identity mismatch; rerun all jobs including the producer, not only consumers")
	}
	if len(m.Files) == 0 || len(m.Files) > maxFiles {
		return m, errors.New("manifest inventory is empty or exceeds file limit")
	}
	var previous string
	var total int64
	for _, file := range m.Files {
		if !safePath(file.Path) || file.Path <= previous || !digestPattern.MatchString(file.SHA256) ||
			file.Size < 0 || file.Size > maxBytes-total {
			return m, fmt.Errorf("invalid, duplicate, unsorted or oversized manifest entry %q", file.Path)
		}
		total += file.Size
		previous = file.Path
	}
	return m, nil
}

func (s *session) verify(artifact *os.Root, expected identity, digest string) (snapshot, error) {
	if !digestPattern.MatchString(digest) {
		return snapshot{}, errors.New("expected manifest SHA256 must be 64 lowercase hexadecimal characters")
	}
	f, err := s.regular(artifact, "manifest.json")
	if err != nil {
		return snapshot{}, err
	}
	data, err := s.read(f, maxManifest)
	if err != nil {
		return snapshot{}, err
	}
	if sum(data) != digest {
		return snapshot{}, errors.New("manifest digest does not match trusted producer output")
	}
	m, err := decodeManifest(data, expected)
	if err != nil {
		return snapshot{}, err
	}
	full, err := s.inventory(artifact)
	if err != nil {
		return snapshot{}, err
	}
	observedManifest := full.data["manifest.json"]
	if !bytes.Equal(data, observedManifest) || sum(observedManifest) != digest {
		return snapshot{}, errors.New("manifest changed during inventory; observed bytes do not match trusted producer digest")
	}
	snap := snapshot{data: make(map[string][]byte)}
	for _, file := range full.files {
		if file.Path == "manifest.json" {
			continue
		}
		name, ok := strings.CutPrefix(file.Path, "web/dist/")
		if !ok {
			return snapshot{}, fmt.Errorf("unexpected artifact file %q", file.Path)
		}
		snap.data[name] = full.data[file.Path]
		file.Path = name
		snap.files = append(snap.files, file)
	}
	if !slices.Equal(m.Files, snap.files) {
		return snapshot{}, errors.New("artifact inventory, sizes or hashes do not match manifest")
	}
	return snap, snap.validate()
}

func (s *session) createDestination(name string) (*os.Root, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	parent, err := s.openRoot(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	leaf := filepath.Base(absolute)
	if err := parent.Mkdir(leaf, 0o700); err != nil {
		return nil, fmt.Errorf("exclusively create destination %q (must not already exist): %w", absolute, err)
	}
	root, err := s.child(parent, leaf)
	if err != nil {
		return nil, fmt.Errorf("admit newly created destination: %w; partial output may remain (requested destination %q; current pathname is not guaranteed after directory replacement); no paths were deleted", err, absolute)
	}
	return root, nil
}

func (s *session) write(r *os.Root, name string, data []byte) error {
	parts := strings.Split(name, "/")
	prefix := ""
	for _, part := range parts[:len(parts)-1] {
		prefix = path.Join(prefix, part)
		if existing := s.directories[prefix]; existing != nil {
			r = existing
			continue
		}
		if err := r.Mkdir(part, 0o700); err != nil {
			return fmt.Errorf("exclusively create output directory %q: %w", prefix, err)
		}
		var err error
		r, err = s.child(r, part)
		if err != nil {
			return err
		}
		s.directories[prefix] = r
	}
	file, err := r.OpenFile(parts[len(parts)-1], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("exclusively create %q: %w", name, err)
	}
	var writeErr error
	if s.hooks.write != nil {
		writeErr = s.hooks.write(file, data)
	} else {
		var n int
		n, writeErr = file.Write(data)
		if writeErr == nil && n != len(data) {
			writeErr = io.ErrShortWrite
		}
	}
	return errors.Join(writeErr, s.closeFile(file))
}

func equalSnapshot(a, b snapshot) bool { return slices.Equal(a.files, b.files) }

func (s *session) visible(root *os.Root, name string) error {
	visible, err := s.openRoot(name)
	if err != nil {
		return err
	}
	a, err := root.Stat(".")
	if err != nil {
		return err
	}
	b, err := visible.Stat(".")
	if err != nil || !os.SameFile(a, b) {
		return errors.Join(err, fmt.Errorf("directory %q was replaced", name))
	}
	return nil
}

type options struct {
	command, input, destination, digest, checkout, workspace string
	source                                                   identity
}

func execute(opts options, fault hooks) (digest string, err error) {
	if err := opts.source.validate(opts.checkout); err != nil {
		return "", err
	}
	if opts.command != "pack" && opts.command != "verify" && opts.command != "restore" &&
		opts.command != "install" && opts.command != "verify-installed" {
		return "", errors.New("command must be pack, verify, restore, install or verify-installed")
	}
	if opts.command == "install" || opts.command == "verify-installed" {
		return executeInstall(opts, fault)
	}
	s := session{hooks: fault, directories: make(map[string]*os.Root)}
	created := false
	defer func() {
		err = errors.Join(err, s.close())
		if err != nil && created {
			err = fmt.Errorf("%w; partial output may remain (requested destination %q; current pathname is not guaranteed after directory replacement); no paths were deleted; inspect invocation-owned output before cleanup", err, opts.destination)
		}
	}()
	input, err := s.openRoot(opts.input)
	if err != nil {
		return "", err
	}
	var snap snapshot
	var metadata []byte
	sourceRoot, sourcePath := input, opts.input
	if opts.command == "pack" {
		web, childErr := s.child(input, "web")
		if childErr != nil {
			return "", childErr
		}
		dist, childErr := s.child(web, "dist")
		if childErr != nil {
			return "", childErr
		}
		sourceRoot, sourcePath = dist, filepath.Join(opts.input, "web", "dist")
		snap, err = s.inventory(dist)
		if err == nil {
			err = snap.validate()
		}
		if err != nil {
			return "", err
		}
		metadata, err = json.Marshal(manifest{Version: 1, Root: "web/dist", Source: opts.source, Files: snap.files})
		if err != nil {
			return "", err
		}
		digest = sum(metadata)
	} else {
		snap, err = s.verify(input, opts.source, opts.digest)
		if err != nil {
			return "", err
		}
		digest = opts.digest
	}
	if opts.command == "verify" {
		return digest, s.visible(sourceRoot, sourcePath)
	}
	if s.hooks.beforeCopy != nil {
		if err := s.hooks.beforeCopy(); err != nil {
			return "", err
		}
	}
	output, err := s.createDestination(opts.destination)
	if err != nil {
		return "", err
	}
	created = true
	for _, file := range snap.files {
		name := file.Path
		if opts.command == "pack" {
			name = "web/dist/" + name
		}
		if err := s.write(output, name, snap.data[file.Path]); err != nil {
			return "", fmt.Errorf("write asset %q: %w", name, err)
		}
	}
	if opts.command == "pack" {
		if err := s.write(output, "manifest.json", metadata); err != nil {
			return "", err
		}
		if _, err := s.verify(output, opts.source, digest); err != nil {
			return "", fmt.Errorf("reverify packed output: %w", err)
		}
	} else {
		actual, err := s.inventory(output)
		if err != nil || !equalSnapshot(snap, actual) {
			return "", fmt.Errorf("reverify restored output: %w", errors.Join(err, errors.New("inventory mismatch")))
		}
	}
	if err := s.visible(output, opts.destination); err != nil {
		return "", err
	}
	if err := s.visible(sourceRoot, sourcePath); err != nil {
		return "", err
	}
	var current snapshot
	if opts.command == "pack" {
		current, err = s.inventory(sourceRoot)
	} else {
		current, err = s.verify(sourceRoot, opts.source, digest)
	}
	if err != nil || !equalSnapshot(snap, current) {
		return "", fmt.Errorf("source changed before handoff completed: %w", errors.Join(err, errors.New("inventory mismatch")))
	}
	return digest, nil
}

func run(args []string, stdout io.Writer, fault hooks) error {
	if len(args) == 0 {
		return errors.New("usage: release-assets-helper pack|verify|restore|install|verify-installed --input PATH [--destination NEW_PATH | --workspace CHECKOUT] --repository microsoft/waza --source-sha SHA --checkout-sha SHA --run-id ID --run-attempt ATTEMPT [--manifest-sha256 DIGEST]")
	}
	opts := options{command: args[0]}
	flags := flag.NewFlagSet("release-assets-helper", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.input, "input", "", "workspace for pack; artifact for verify/restore")
	flags.StringVar(&opts.destination, "destination", "", "new, absent artifact or dist directory")
	flags.StringVar(&opts.workspace, "workspace", "", "existing checkout for install/verify-installed")
	flags.StringVar(&opts.source.Repository, "repository", "", "expected repository")
	flags.StringVar(&opts.source.SourceSHA, "source-sha", "", "expected full source SHA")
	flags.StringVar(&opts.checkout, "checkout-sha", "", "independently observed checkout SHA")
	flags.StringVar(&opts.source.RunID, "run-id", "", "expected run ID")
	flags.StringVar(&opts.source.RunAttempt, "run-attempt", "", "expected run attempt")
	flags.StringVar(&opts.digest, "manifest-sha256", "", "trusted producer manifest digest")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	var destinationSet, workspaceSet bool
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "destination":
			destinationSet = true
		case "workspace":
			workspaceSet = true
		}
	})
	install := opts.command == "install" || opts.command == "verify-installed"
	if flags.NArg() != 0 || opts.input == "" || (install && (opts.workspace == "" || destinationSet)) ||
		(!install && (workspaceSet || (opts.command != "verify" && opts.destination == ""))) {
		return errors.New("require input and no positional arguments; install/verify-installed require nonempty workspace and forbid destination, original commands forbid workspace and pack/restore require an absent destination")
	}
	digest, err := execute(opts, fault)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "manifest_sha256=%s\n", digest); err != nil {
		return fmt.Errorf("write verified digest output: %w", err)
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, hooks{}); err != nil {
		if _, writeErr := fmt.Fprintf(os.Stderr, "release assets: %v\n", err); writeErr != nil {
			panic(writeErr)
		}
		os.Exit(1)
	}
}
