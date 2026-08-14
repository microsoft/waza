package registry

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"gopkg.in/yaml.v3"
)

type GitURLFunc func(Ref) string

type Resolver struct {
	cacheRoot string
	gitURL    GitURLFunc
}

type ResolverOption func(*Resolver)

func WithCacheRoot(path string) ResolverOption {
	return func(r *Resolver) {
		r.cacheRoot = path
	}
}

func WithGitURLFunc(fn GitURLFunc) ResolverOption {
	return func(r *Resolver) {
		r.gitURL = fn
	}
}

func NewResolver(opts ...ResolverOption) (*Resolver, error) {
	cacheRoot, err := DefaultCacheRoot()
	if err != nil {
		return nil, err
	}
	r := &Resolver{
		cacheRoot: cacheRoot,
		gitURL: func(ref Ref) string {
			return "https://" + ref.ModulePath() + ".git"
		},
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.cacheRoot == "" {
		return nil, fmt.Errorf("module cache root is empty")
	}
	cacheRoot, err = filepath.Abs(r.cacheRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving module cache root: %w", err)
	}
	r.cacheRoot = cacheRoot
	return r, nil
}

func DefaultCacheRoot() (string, error) {
	if dir := os.Getenv("WAZA_MODULE_CACHE"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".waza", "cache"), nil
}

func CollectGraderRefs(spec *models.EvalSpec) []string {
	seen := map[string]bool{}
	var refs []string
	for _, grader := range spec.Graders {
		if grader.Ref == "" || seen[grader.Ref] {
			continue
		}
		seen[grader.Ref] = true
		refs = append(refs, grader.Ref)
	}
	return refs
}

func (r *Resolver) ResolveRefs(ctx context.Context, refs []string) ([]models.LockfileGrader, error) {
	entries := make([]models.LockfileGrader, 0, len(refs))
	for _, raw := range refs {
		entry, err := r.ResolveRef(ctx, raw)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (r *Resolver) ResolveRef(ctx context.Context, raw string) (models.LockfileGrader, error) {
	ref, err := ParseRef(raw)
	if err != nil {
		return models.LockfileGrader{}, err
	}
	url := r.gitURL(ref)
	commit, cacheDir, err := r.ensureCached(ctx, ref, url)
	if err != nil {
		return models.LockfileGrader{}, fmt.Errorf("resolving %s: %w", raw, err)
	}
	digest, err := DigestDirectory(cacheDir)
	if err != nil {
		return models.LockfileGrader{}, fmt.Errorf("digesting %s: %w", cacheDir, err)
	}
	entry := models.LockfileGrader{
		Ref:    raw,
		Commit: commit,
		Digest: digest,
		URL:    url,
	}
	if _, err := r.loadGraderPreset(ref, cacheDir); err != nil {
		return models.LockfileGrader{}, fmt.Errorf("loading %s: %w", raw, err)
	}
	return entry, nil
}

func (r *Resolver) ResolveEvalLock(ctx context.Context, evalPath string) (*models.Lockfile, []models.LockfileGrader, error) {
	spec, err := models.LoadEvalSpec(evalPath)
	if err != nil {
		return nil, nil, err
	}
	refs := CollectGraderRefs(spec)
	if len(refs) == 0 {
		return nil, nil, fmt.Errorf("no remote grader refs found in %s", evalPath)
	}
	entries, err := r.ResolveRefs(ctx, refs)
	if err != nil {
		return nil, nil, err
	}
	lock := models.NewLockfile()
	for _, entry := range entries {
		lock.UpsertGrader(entry)
	}
	return lock, entries, nil
}

func (r *Resolver) ExpandLockedGraders(ctx context.Context, spec *models.EvalSpec, evalPath string) error {
	refs := CollectGraderRefs(spec)
	if len(refs) == 0 {
		return nil
	}
	lockPath := filepath.Join(filepath.Dir(evalPath), models.LockfileName)
	lock, err := models.LoadLockfile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("eval contains remote grader refs but %s is missing; run `waza get %s` first", lockPath, evalPath)
		}
		return err
	}
	for i, grader := range spec.Graders {
		if grader.Ref == "" {
			continue
		}
		entry, ok := lock.Grader(grader.Ref)
		if !ok {
			return fmt.Errorf("remote grader ref %q is not present in %s; run `waza get %s`", grader.Ref, lockPath, evalPath)
		}
		ref, err := ParseRef(grader.Ref)
		if err != nil {
			return err
		}
		preset, err := r.LoadLockedGrader(ctx, ref, entry)
		if err != nil {
			return err
		}
		merged, err := MergeGraderConfig(preset, grader)
		if err != nil {
			return fmt.Errorf("merging remote grader %q: %w", grader.Ref, err)
		}
		spec.Graders[i] = merged
	}
	return nil
}

func (r *Resolver) LoadLockedGrader(ctx context.Context, ref Ref, entry models.LockfileGrader) (models.GraderConfig, error) {
	_ = ctx
	cacheDir, err := r.cacheDir(ref, entry.Commit)
	if err != nil {
		return models.GraderConfig{}, err
	}
	if ok, err := cacheDirectoryExists(cacheDir); err != nil {
		return models.GraderConfig{}, err
	} else if !ok {
		return models.GraderConfig{}, fmt.Errorf("module not available offline for ref %q at %s; run `waza get` while online", entry.Ref, cacheDir)
	}
	digest, err := DigestDirectory(cacheDir)
	if err != nil {
		return models.GraderConfig{}, err
	}
	if digest != entry.Digest {
		return models.GraderConfig{}, fmt.Errorf("digest mismatch for ref %q: lock has %s, cache has %s", entry.Ref, entry.Digest, digest)
	}
	return r.loadGraderPreset(ref, cacheDir)
}

func (r *Resolver) ensureCached(ctx context.Context, ref Ref, url string) (string, string, error) {
	if err := os.MkdirAll(r.cacheRoot, 0o755); err != nil {
		return "", "", err
	}
	tmp, err := os.MkdirTemp(r.cacheRoot, ".mirror-*")
	if err != nil {
		return "", "", err
	}
	defer func() {
		if err := os.RemoveAll(tmp); err != nil {
			slog.Warn("failed to remove temporary module mirror", "path", tmp, "error", err)
		}
	}()

	mirror := filepath.Join(tmp, "repo.git")
	if err := runGit(ctx, "", "clone", "--quiet", "--mirror", url, mirror); err != nil {
		return "", "", err
	}
	commitBytes, err := gitOutput(ctx, "", "--git-dir", mirror, "rev-parse", "--verify", ref.Version+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("resolving version %q: %w", ref.Version, err)
	}
	commit := strings.TrimSpace(string(commitBytes))
	cacheDir, err := r.cacheDir(ref, commit)
	if err != nil {
		return "", "", err
	}
	if ok, err := cacheDirectoryExists(cacheDir); err != nil {
		return "", "", err
	} else if ok {
		return commit, cacheDir, nil
	}
	parent := filepath.Dir(cacheDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", "", err
	}
	extractDir, err := os.MkdirTemp(parent, ".extract-*")
	if err != nil {
		return "", "", err
	}
	defer func() {
		if err := os.RemoveAll(extractDir); err != nil {
			slog.Warn("failed to remove temporary module extract", "path", extractDir, "error", err)
		}
	}()

	if err := extractGitArchive(ctx, mirror, commit, extractDir); err != nil {
		return "", "", fmt.Errorf("archiving commit %s: %w", commit, err)
	}
	if err := os.Rename(extractDir, cacheDir); err != nil {
		if ok, statErr := cacheDirectoryExists(cacheDir); statErr != nil {
			return "", "", statErr
		} else if ok {
			return commit, cacheDir, nil
		}
		return "", "", err
	}
	return commit, cacheDir, nil
}

func (r *Resolver) cacheDir(ref Ref, commit string) (string, error) {
	if err := models.ValidateLockCommit(commit); err != nil {
		return "", err
	}
	segments := []struct {
		name  string
		value string
	}{
		{name: "host", value: ref.Host},
		{name: "owner", value: ref.Owner},
		{name: "repo", value: ref.Repo},
	}
	for _, segment := range segments {
		if err := validateModulePathSegment(segment.value); err != nil {
			return "", fmt.Errorf("invalid ref %s %q: %w", segment.name, segment.value, err)
		}
	}
	return pathWithin(r.cacheRoot, filepath.Join(r.cacheRoot, ref.Host, ref.Owner, ref.Repo, commit))
}

func cacheDirectoryExists(cacheDir string) (bool, error) {
	info, err := os.Stat(cacheDir)
	if err == nil {
		if !info.IsDir() {
			return false, fmt.Errorf("cache path %s exists and is not a directory", cacheDir)
		}
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func pathWithin(base string, target string) (string, error) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %s escapes %s", absTarget, absBase)
	}
	return absTarget, nil
}

type manifest struct {
	SchemaVersion int `yaml:"schema_version"`
	Exports       struct {
		Graders map[string]manifestExport `yaml:"graders"`
	} `yaml:"exports"`
}

type manifestExport struct {
	Path        string `yaml:"path"`
	Description string `yaml:"description,omitempty"`
}

func (r *Resolver) loadGraderPreset(ref Ref, moduleDir string) (models.GraderConfig, error) {
	if ref.Export != "" {
		manifestDir := moduleDir
		if ref.Path != "" {
			var err error
			manifestDir, err = safeJoinModulePath(moduleDir, ref.Path)
			if err != nil {
				return models.GraderConfig{}, err
			}
		}
		m, err := loadManifest(manifestDir)
		if err != nil {
			return models.GraderConfig{}, err
		}
		exp, ok := m.Exports.Graders[ref.Export]
		if !ok {
			return models.GraderConfig{}, fmt.Errorf("manifest does not export grader %q", ref.Export)
		}
		graderPath, err := safeJoinModulePath(manifestDir, exp.Path)
		if err != nil {
			return models.GraderConfig{}, err
		}
		return loadGraderFile(graderPath, ref.Export)
	}
	if ref.Path != "" {
		graderPath, err := safeJoinModulePath(moduleDir, ref.Path)
		if err != nil {
			return models.GraderConfig{}, err
		}
		return loadGraderFile(graderPath, filepath.Base(ref.Path))
	}
	m, err := loadManifest(moduleDir)
	if err != nil {
		return models.GraderConfig{}, err
	}
	if len(m.Exports.Graders) != 1 {
		return models.GraderConfig{}, fmt.Errorf("ref %q must select one grader with #export or /path", ref.Raw)
	}
	for name, exp := range m.Exports.Graders {
		graderPath, err := safeJoinModulePath(moduleDir, exp.Path)
		if err != nil {
			return models.GraderConfig{}, err
		}
		return loadGraderFile(graderPath, name)
	}
	return models.GraderConfig{}, fmt.Errorf("manifest has no grader exports")
}

func safeJoinModulePath(base string, slashPath string) (string, error) {
	clean, err := cleanRelativeSlashPath(slashPath)
	if err != nil || clean == "." {
		return "", fmt.Errorf("invalid module path %q", slashPath)
	}
	return pathWithin(base, filepath.Join(base, filepath.FromSlash(clean)))
}

func cleanRelativeSlashPath(slashPath string) (string, error) {
	if strings.TrimSpace(slashPath) == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.Contains(slashPath, `\`) {
		return "", fmt.Errorf("path must use forward slashes")
	}
	if path.IsAbs(slashPath) || hasParentPathSegment(slashPath) {
		return "", fmt.Errorf("path escapes module root")
	}
	clean := path.Clean(slashPath)
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", fmt.Errorf("path escapes module root")
	}
	return clean, nil
}

func loadManifest(dir string) (*manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "waza.registry.yaml"))
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	var m manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}
	if len(m.Exports.Graders) == 0 {
		return nil, fmt.Errorf("manifest has no grader exports")
	}
	return &m, nil
}

func loadGraderFile(graderPath string, fallbackName string) (models.GraderConfig, error) {
	resolved, err := resolveYAMLPath(graderPath)
	if err != nil {
		return models.GraderConfig{}, err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return models.GraderConfig{}, err
	}
	var grader models.GraderConfig
	if err := yaml.Unmarshal(data, &grader); err != nil {
		return models.GraderConfig{}, err
	}
	if grader.Kind == "" {
		return models.GraderConfig{}, fmt.Errorf("remote grader %s must declare type", resolved)
	}
	if grader.Kind == models.GraderKindProgram {
		return models.GraderConfig{}, fmt.Errorf("remote program graders are not supported without explicit trust")
	}
	if grader.Identifier == "" {
		grader.Identifier = strings.TrimSuffix(fallbackName, filepath.Ext(fallbackName))
	}
	return grader, nil
}

func resolveYAMLPath(graderPath string) (string, error) {
	candidates := []string{graderPath}
	if filepath.Ext(graderPath) == "" {
		candidates = append(candidates, graderPath+".yaml", graderPath+".yml")
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("grader file not found: %s", graderPath)
}

func MergeGraderConfig(preset models.GraderConfig, override models.GraderConfig) (models.GraderConfig, error) {
	if override.Kind != "" && override.Kind != preset.Kind {
		return models.GraderConfig{}, fmt.Errorf("local type %q does not match remote type %q", override.Kind, preset.Kind)
	}
	mergedConfig, err := configMap(preset.Parameters)
	if err != nil {
		return models.GraderConfig{}, err
	}
	localConfig, err := configMap(override.Parameters)
	if err != nil {
		return models.GraderConfig{}, err
	}
	deepMerge(mergedConfig, localConfig)

	raw := map[string]any{
		"type":   string(preset.Kind),
		"name":   preset.Identifier,
		"config": mergedConfig,
	}
	if preset.ScriptPath != "" {
		raw["script"] = preset.ScriptPath
	}
	if preset.Rubric != "" {
		raw["rubric"] = preset.Rubric
	}
	if preset.ModelID != "" {
		raw["model"] = preset.ModelID
	}
	if preset.Weight != 0 {
		raw["weight"] = preset.Weight
	}
	if override.Identifier != "" {
		raw["name"] = override.Identifier
	}
	if override.ScriptPath != "" {
		raw["script"] = override.ScriptPath
	}
	if override.Rubric != "" {
		raw["rubric"] = override.Rubric
	}
	if override.ModelID != "" {
		raw["model"] = override.ModelID
	}
	if override.Weight != 0 {
		raw["weight"] = override.Weight
	}

	data, err := yaml.Marshal(raw)
	if err != nil {
		return models.GraderConfig{}, err
	}
	var merged models.GraderConfig
	if err := yaml.Unmarshal(data, &merged); err != nil {
		return models.GraderConfig{}, err
	}
	merged.Ref = override.Ref
	return merged, nil
}

func configMap(params models.GraderParameters) (map[string]any, error) {
	if params == nil {
		return map[string]any{}, nil
	}
	data, err := yaml.Marshal(params)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func deepMerge(dst, src map[string]any) {
	for key, srcValue := range src {
		srcMap, srcOK := srcValue.(map[string]any)
		dstMap, dstOK := dst[key].(map[string]any)
		if srcOK && dstOK {
			deepMerge(dstMap, srcMap)
			continue
		}
		dst[key] = srcValue
	}
}

func DigestDirectory(dir string) (string, error) {
	var files []string
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot digest non-regular file %s", path)
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, file := range files {
		rel, err := filepath.Rel(dir, file)
		if err != nil {
			return "", err
		}
		h.Write([]byte(filepath.ToSlash(rel)))
		h.Write([]byte{0})
		data, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		h.Write(data)
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func extractTar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		cleanName, err := cleanRelativeSlashPath(header.Name)
		if err != nil {
			return fmt.Errorf("archive contains unsafe path %q", header.Name)
		}
		if cleanName == "." {
			continue
		}
		target, err := pathWithin(dest, filepath.Join(dest, filepath.FromSlash(cleanName)))
		if err != nil {
			return fmt.Errorf("archive contains unsafe path %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, tr)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("archive contains unsupported entry %q", header.Name)
		}
	}
}

func extractGitArchive(ctx context.Context, mirror string, commit string, dest string) error {
	cmd := exec.CommandContext(ctx, "git", "--git-dir", mirror, "archive", "--format=tar", commit)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	extractErr := extractTar(stdout, dest)
	if extractErr == nil {
		_, extractErr = io.Copy(io.Discard, stdout)
	}
	if extractErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if extractErr != nil {
		return extractErr
	}
	if waitErr != nil {
		return fmt.Errorf("git archive failed: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := gitOutput(ctx, dir, args...)
	return err
}

func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
