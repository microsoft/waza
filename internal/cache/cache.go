package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/hooks"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/models"
)

// Cache provides caching for evaluation results
type Cache struct {
	dir string
	mu  sync.Mutex
}

// New creates a new cache instance with the specified directory
func New(dir string) *Cache {
	return &Cache{dir: dir}
}

// CacheKey generates a unique cache key for a test case run
// The key is based on:
// - spec content (name, config, graders)
// - task content (test case definition)
// - model ID
// - fixture file hashes
// - the task's effective MCP mocks, with fixture contents resolved
// - lifecycle hooks and eval inputs when hooks are configured
// - consumed skill, agent, instruction and context bytes (CacheKeyWithInputs)
// MCP fixtures use fixtureDir as their base; use CacheKeyWithSpecDir when the
// eval and task resource directories differ.
func CacheKey(spec *models.EvalSpec, task *models.TestCase, fixtureDir string) (string, error) {
	return CacheKeyWithSpecDir(spec, task, fixtureDir, fixtureDir)
}

// CacheKeyWithSpecDir resolves MCP fixtures relative to the eval directory,
// independently of the task resource directory.
func CacheKeyWithSpecDir(spec *models.EvalSpec, task *models.TestCase, fixtureDir, specDir string) (string, error) {
	return CacheKeyWithInputs(spec, task, fixtureDir, specDir, nil)
}

// ConsumedInputs are the bytes request construction reads beyond the
// declarations hashed by CacheKeyWithSpecDir: context fixtures, instruction
// files, the selected skill or agent definition, and skill directory trees.
type ConsumedInputs struct {
	// Files are fingerprinted in order, since order can change the request.
	Files []ConsumedFile
	// SkillDirs are hashed recursively, in precedence order.
	SkillDirs []string
}

// ConsumedFile is one file's content as consumed by request construction.
type ConsumedFile struct {
	Role    string
	Path    string
	Content []byte
}

func (in *ConsumedInputs) empty() bool {
	return in == nil || (len(in.Files) == 0 && len(in.SkillDirs) == 0)
}

// skippedSkillTreeDirs are not fingerprinted inside skill directories: they
// hold VCS metadata or vendored packages that skills do not read as content.
var skippedSkillTreeDirs = map[string]bool{".git": true, "node_modules": true}

// CacheKeyWithInputs extends CacheKeyWithSpecDir with the content of consumed
// inputs, so editing a skill, instruction file or context fixture in place
// does not reuse an outcome produced from the previous bytes. Any unreadable
// input returns an error, which callers treat as "do not cache".
func CacheKeyWithInputs(spec *models.EvalSpec, task *models.TestCase, fixtureDir, specDir string, inputs *ConsumedInputs) (string, error) {
	h := sha256.New()

	// Include spec identity
	if err := writeString(h, spec.Name); err != nil {
		return "", err
	}
	if err := writeString(h, spec.SkillName); err != nil {
		return "", err
	}

	// Include config (model, engine, timeout, runs)
	if err := writeString(h, spec.Config.ModelID); err != nil {
		return "", err
	}
	if err := writeString(h, spec.Config.ReasoningEffort); err != nil {
		return "", err
	}
	if err := writeString(h, spec.Config.JudgeModel); err != nil {
		return "", err
	}
	if err := writeString(h, spec.Config.JudgeReasoningEffort); err != nil {
		return "", err
	}
	if err := writeString(h, spec.Config.EngineType); err != nil {
		return "", err
	}
	mode := spec.Config.WorkspaceCapture
	switch mode {
	case "", "full":
		mode = "full"
	case "none":
	default:
		return "", fmt.Errorf("invalid workspace capture mode %q", mode)
	}
	// Older keys did not distinguish capture modes and may contain no-capture
	// results, so neither mode can safely reuse them.
	if err := writeString(h, "workspace-capture-v1"); err != nil {
		return "", err
	}
	if err := writeString(h, mode); err != nil {
		return "", err
	}
	if err := writeInt(h, spec.Config.TimeoutSec); err != nil {
		return "", err
	}
	if err := writeInt(h, spec.Config.TrialsPerTask); err != nil {
		return "", err
	}
	if err := writeInt(h, spec.Config.MaxAttempts); err != nil {
		return "", err
	}
	if err := writeBool(h, spec.Config.TriggerSkillRouting); err != nil {
		return "", err
	}
	// Written only when enabled so existing cache keys stay unchanged.
	if spec.Config.NativeUserInput {
		if err := writeString(h, "native_user_input"); err != nil {
			return "", err
		}
	}

	// Include skill paths (critical for baseline A/B: with-skills vs without-skills
	// must produce different cache keys)
	skillsJSON, err := json.Marshal(spec.Config.SkillPaths)
	if err != nil {
		return "", fmt.Errorf("marshaling skill paths: %w", err)
	}
	if _, err := h.Write(skillsJSON); err != nil {
		return "", err
	}

	// Include graders configuration, including exact frozen-evidence fixture
	// declarations. Task-level declarations are covered by taskJSON below.
	gradersJSON, err := json.Marshal(spec.Graders)
	if err != nil {
		return "", fmt.Errorf("marshaling graders: %w", err)
	}
	if _, err := h.Write(gradersJSON); err != nil {
		return "", err
	}

	if len(spec.Hooks.BeforeRun)+len(spec.Hooks.AfterRun)+len(spec.Hooks.BeforeTask)+len(spec.Hooks.AfterTask) > 0 {
		if err := writeString(h, "hook-inputs-v1"); err != nil {
			return "", err
		}
		hookContextJSON, err := json.Marshal(struct {
			Hooks  hooks.HooksConfig `json:"hooks"`
			Inputs map[string]string `json:"inputs"`
		}{
			Hooks:  spec.Hooks,
			Inputs: spec.Inputs,
		})
		if err != nil {
			return "", fmt.Errorf("marshaling hook cache context: %w", err)
		}
		if _, err := h.Write(hookContextJSON); err != nil {
			return "", err
		}
	}

	// Include task definition
	taskJSON, err := json.Marshal(task)
	if err != nil {
		return "", fmt.Errorf("marshaling task: %w", err)
	}
	if _, err := h.Write(taskJSON); err != nil {
		return "", err
	}

	// Include fixture files from resources
	var fixtures []string
	for _, res := range task.Stimulus.Resources {
		if res.Location != "" {
			fixtures = append(fixtures, res.Location)
		}
	}
	if err := hashFixtures(h, fixtureDir, fixtures); err != nil {
		return "", fmt.Errorf("hashing fixtures: %w", err)
	}

	// Hash each task's effective mocks (task overrides applied), so an edited
	// task or inherited eval mock does not reuse an outcome from different responses.
	if effective := models.EffectiveMCPMocks(spec, task); len(effective) > 0 {
		resolved := make(map[string]*mcpmock.Config, len(effective))
		for _, mock := range effective {
			cfg, err := mcpmock.FromEvalConfigOfflineWithVersion(mock, specDir, spec.SchemaVersion)
			if err != nil {
				return "", fmt.Errorf("resolving MCP mock %q for cache: %w", mock.Name, err)
			}
			resolved[cfg.Name] = cfg
		}
		// Earlier keys omitted MCP mocks, so salt only MCP-backed keys to
		// invalidate entries that could not reflect their mock configuration.
		if err := writeString(h, "mcp-mock-attempt-v1"); err != nil {
			return "", err
		}
		mocksJSON, err := json.Marshal(resolved)
		if err != nil {
			return "", fmt.Errorf("marshaling MCP mocks for cache: %w", err)
		}
		if _, err := h.Write(mocksJSON); err != nil {
			return "", err
		}
	}

	// Salted only when inputs are consumed, so keys for tasks without them are
	// unchanged while earlier keys that ignored these bytes are not reused.
	if !inputs.empty() {
		if err := hashConsumedInputs(h, inputs); err != nil {
			return "", fmt.Errorf("hashing consumed inputs: %w", err)
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashConsumedInputs(h io.Writer, inputs *ConsumedInputs) error {
	if err := writeString(h, "consumed-inputs-v1"); err != nil {
		return err
	}
	for _, file := range inputs.Files {
		sum := sha256.Sum256(file.Content)
		for _, s := range []string{"file", file.Role, file.Path, hex.EncodeToString(sum[:])} {
			if err := writeString(h, s); err != nil {
				return err
			}
		}
	}
	for i, dir := range inputs.SkillDirs {
		if err := writeString(h, "skill-dir"); err != nil {
			return err
		}
		if err := writeInt(h, i); err != nil {
			return err
		}
		if err := hashSkillTree(h, dir); err != nil {
			return fmt.Errorf("skill directory %s: %w", dir, err)
		}
	}
	return nil
}

// hashSkillTree writes each file's slash-separated relative path and content
// digest in lexical walk order. Symlinked directories record their target
// rather than being followed, which avoids cycles.
func hashSkillTree(h io.Writer, root string) error {
	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return writeString(h, "missing")
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if err := writeString(h, "file"); err != nil {
			return err
		}
		return hashFileDigest(h, root)
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && skippedSkillTreeDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if err := writeString(h, filepath.ToSlash(rel)); err != nil {
			return err
		}
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := filepath.EvalSymlinks(path)
			if os.IsNotExist(err) {
				return writeString(h, "dangling-symlink")
			}
			if err != nil {
				return err
			}
			targetInfo, err := os.Stat(target)
			if err != nil {
				return err
			}
			if targetInfo.IsDir() {
				return writeString(h, "symlink-dir:"+target)
			}
			return hashFileDigest(h, target)
		case entry.Type().IsRegular():
			return hashFileDigest(h, path)
		default:
			return writeString(h, "irregular")
		}
	})
}

func hashFileDigest(h io.Writer, path string) error {
	digest := sha256.New()
	if err := hashFile(digest, path); err != nil {
		return err
	}
	return writeString(h, hex.EncodeToString(digest.Sum(nil)))
}

// RecordedMCPMocksMatch reports whether a cached outcome is safe to reuse for
// the given effective MCP mocks. Keys written before MCP mocks were hashed
// ignored eval-level mocks, so a mocked outcome can sit under the key a task
// without mocks now computes. An outcome whose runs record a mocked MCP
// dependency that is not currently mocked must be re-run. Runs that predate
// recorded dependency modes cannot be checked.
func RecordedMCPMocksMatch(outcome *models.TestOutcome, effective []models.MCPMockConfig) bool {
	if outcome == nil {
		return true
	}
	mocked := make(map[string]bool, len(effective))
	for _, mock := range effective {
		mocked[strings.TrimSpace(mock.Name)] = true
	}
	for _, run := range outcome.Runs {
		for _, mode := range run.DependencyModes {
			if mode.Kind == "mcp" && mode.Mode == "mocked" && !mocked[mode.Name] {
				return false
			}
		}
	}
	return true
}

// Get retrieves a cached test outcome if it exists
func (c *Cache) Get(key string) (*models.TestOutcome, bool) {
	if c.dir == "" {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	path := c.cachePath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		// Cache miss
		return nil, false
	}

	var outcome models.TestOutcome
	if err := json.Unmarshal(data, &outcome); err != nil {
		// Invalid cache entry, treat as miss
		return nil, false
	}

	return &outcome, true
}

// Put stores a test outcome in the cache
func (c *Cache) Put(key string, outcome *models.TestOutcome) error {
	if c.dir == "" {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Ensure cache directory exists
	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}

	data, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling outcome: %w", err)
	}

	path := c.cachePath(key)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing cache file: %w", err)
	}

	return nil
}

// Clear removes all cached results
func (c *Cache) Clear() error {
	if c.dir == "" {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if directory exists
	if _, err := os.Stat(c.dir); os.IsNotExist(err) {
		return nil
	}

	// Safety check: verify this is a waza cache directory before removing
	// Check for presence of at least one .json cache file or empty directory
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return fmt.Errorf("reading cache directory: %w", err)
	}

	// If directory is not empty, verify it contains only cache files
	if len(entries) > 0 {
		hasValidCache := false
		for _, entry := range entries {
			if entry.IsDir() {
				return fmt.Errorf("cache directory contains subdirectories - refusing to delete for safety")
			}
			if filepath.Ext(entry.Name()) == ".json" {
				hasValidCache = true
			} else {
				return fmt.Errorf("cache directory contains non-cache files - refusing to delete for safety")
			}
		}
		if !hasValidCache {
			return fmt.Errorf("no valid cache files found in directory - refusing to delete for safety")
		}
	}

	return os.RemoveAll(c.dir)
}

// cachePath returns the file path for a cache key
func (c *Cache) cachePath(key string) string {
	return filepath.Join(c.dir, key+".json")
}

// HasNonDeterministicGraders checks if any graders are non-deterministic
// Non-deterministic graders include: behavior and prompt
func HasNonDeterministicGraders(spec *models.EvalSpec) bool {
	for _, g := range spec.Graders {
		if g.Kind == models.GraderKindBehavior || g.Kind == models.GraderKindPrompt {
			return true
		}
	}
	return false
}

// Helper functions

func writeString(w io.Writer, s string) error {
	// Write string with null byte delimiter to prevent hash collisions
	_, err := w.Write([]byte(s + "\x00"))
	return err
}

func writeInt(w io.Writer, i int) error {
	// Write int with null byte delimiter to prevent hash collisions
	_, err := fmt.Fprintf(w, "%d\x00", i)
	return err
}

func writeBool(w io.Writer, value bool) error {
	_, err := fmt.Fprintf(w, "%t\x00", value)
	return err
}

func hashFixtures(h io.Writer, fixtureDir string, fixtures []string) error {
	if len(fixtures) == 0 {
		return nil
	}

	// Sort fixtures for deterministic hashing
	sortedFixtures := make([]string, len(fixtures))
	copy(sortedFixtures, fixtures)
	sort.Strings(sortedFixtures)

	for _, fixture := range sortedFixtures {
		// Resolve fixture path
		fixturePath := fixture
		if !filepath.IsAbs(fixturePath) && fixtureDir != "" {
			fixturePath = filepath.Join(fixtureDir, fixture)
		}

		// Hash the file content
		if err := hashFile(h, fixturePath); err != nil {
			// If file doesn't exist, include the path in hash anyway
			// This ensures cache invalidation if fixtures are added/removed
			if os.IsNotExist(err) {
				if err := writeString(h, fixture); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("hashing fixture %s: %w", fixture, err)
		}
	}

	return nil
}

func hashFile(h io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck

	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	return nil
}
