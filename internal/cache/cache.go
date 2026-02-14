package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/spboyer/waza/internal/models"
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
func CacheKey(spec *models.BenchmarkSpec, task *models.TestCase, fixtureDir string) (string, error) {
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
	if err := writeString(h, spec.Config.EngineType); err != nil {
		return "", err
	}
	if err := writeInt(h, spec.Config.TimeoutSec); err != nil {
		return "", err
	}
	if err := writeInt(h, spec.Config.RunsPerTest); err != nil {
		return "", err
	}

	// Include graders configuration
	gradersJSON, err := json.Marshal(spec.Graders)
	if err != nil {
		return "", fmt.Errorf("marshaling graders: %w", err)
	}
	if _, err := h.Write(gradersJSON); err != nil {
		return "", err
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

	return hex.EncodeToString(h.Sum(nil)), nil
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
func HasNonDeterministicGraders(spec *models.BenchmarkSpec) bool {
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
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	return nil
}
